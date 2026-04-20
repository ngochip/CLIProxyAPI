#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench để giữ toggle "OpenAI API Key" không bị tự tắt.
 *
 * Root cause: Cursor có 2 listener tự gọi setUseOpenAIKey(false) mỗi khi
 * auth/subscription state thay đổi (refresh token, membership poll, etc.):
 *   - loginChangedListener: tắt nếu membership là PRO/PRO_PLUS/ULTRA
 *   - subscriptionChangedListener: tắt nếu membership không phải FREE
 *
 * Do Cursor poll định kỳ → toggle "OpenAI Key" bị random switch off.
 *
 * Fix: Thay body 2 listener bằng no-op, giữ nguyên setter setUseOpenAIKey()
 * để user vẫn manual toggle on/off bình thường.
 *
 * Usage: node patch-cursor-openai-key-persistence.js [--restore]
 *
 * Tested on: Cursor 3.2.0+ (2026-04-17)
 * Xem CURSOR-ARCHITECTURE.md để hiểu chi tiết kiến trúc.
 */

const fs = require("fs");
const crypto = require("crypto");

const WORKBENCH_PATH =
  "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js";
const PRODUCT_PATH =
  "/Applications/Cursor.app/Contents/Resources/app/product.json";
const WORKBENCH_BACKUP = WORKBENCH_PATH + ".backup";
const PRODUCT_BACKUP = PRODUCT_PATH + ".backup";
const CHECKSUM_KEY = "vs/workbench/workbench.desktop.main.js";

const PATCH_MARKER = "__openaiKeyPersistencePatched:login";

function updateProductChecksum() {
  const fileData = fs.readFileSync(WORKBENCH_PATH);
  const newHash = crypto
    .createHash("sha256")
    .update(fileData)
    .digest("base64")
    .replace(/=+$/, "");

  const product = JSON.parse(fs.readFileSync(PRODUCT_PATH, "utf8"));
  if (product.checksums && product.checksums[CHECKSUM_KEY]) {
    product.checksums[CHECKSUM_KEY] = newHash;
    fs.writeFileSync(PRODUCT_PATH, JSON.stringify(product, null, "\t"));
    console.log("   Checksum updated:", newHash);
  }
}

// --- Restore mode ---
if (process.argv.includes("--restore")) {
  console.log("🔄 Restoring from backup...");

  if (!fs.existsSync(WORKBENCH_BACKUP)) {
    console.error("❌ Workbench backup not found:", WORKBENCH_BACKUP);
    console.error(
      "   (Backup may have been created by another patch script. Check manually.)"
    );
    process.exit(1);
  }

  fs.copyFileSync(WORKBENCH_BACKUP, WORKBENCH_PATH);
  console.log("✅ Workbench restored from backup");

  if (fs.existsSync(PRODUCT_BACKUP)) {
    fs.copyFileSync(PRODUCT_BACKUP, PRODUCT_PATH);
    console.log("✅ product.json restored from backup");
  }

  console.log("\n📋 Restart Cursor (Cmd+Q → reopen) to apply.");
  process.exit(0);
}

// --- Patch mode ---
console.log("🔍 Reading Cursor workbench...");
const data = fs.readFileSync(WORKBENCH_PATH, "utf8");

if (data.includes(PATCH_MARKER)) {
  console.log(
    "ℹ️  Already patched (OpenAI Key persistence). Use --restore to revert, then re-apply all patches."
  );
  process.exit(0);
}

// --- Detect loginChangedListener pattern ---
// Cursor minifies enum name (Ma, etc.) và param name (p, etc.)
// Pattern: this.loginChangedListener=<p>=>{(this.cursorAuthenticationService.membershipType()===<Ma>.PRO||...PRO_PLUS||...ULTRA)&&this.setUseOpenAIKey(!1)}
const LOGIN_LISTENER_RE =
  /this\.loginChangedListener=([\w$])=>\{\(this\.cursorAuthenticationService\.membershipType\(\)===([\w$]+)\.PRO\|\|this\.cursorAuthenticationService\.membershipType\(\)===\2\.PRO_PLUS\|\|this\.cursorAuthenticationService\.membershipType\(\)===\2\.ULTRA\)&&this\.setUseOpenAIKey\(!1\)\}/;

const loginMatch = data.match(LOGIN_LISTENER_RE);
if (!loginMatch) {
  console.error("❌ loginChangedListener pattern not found.");
  console.error(
    "   Expected: this.loginChangedListener=p=>{(this.cursorAuthenticationService.membershipType()===Ma.PRO||...PRO_PLUS||...ULTRA)&&this.setUseOpenAIKey(!1)}"
  );
  console.error("");
  console.error("   Debug: search for loginChangedListener in workbench:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('loginChangedListener=');if(i>-1)console.log(d.substring(i,i+500));else console.log('not found')\""
  );
  process.exit(1);
}

const loginOriginal = loginMatch[0];
const loginParam = loginMatch[1];
const loginEnumName = loginMatch[2];
console.log(
  `   loginChangedListener: param=${loginParam}, enum=${loginEnumName}`
);
console.log(`   Pattern: ${loginOriginal.substring(0, 80)}...`);

// Verify uniqueness
let loginCount = 0;
let pos = 0;
while ((pos = data.indexOf(loginOriginal, pos)) !== -1) {
  loginCount++;
  pos += 10;
}
if (loginCount !== 1) {
  console.error(
    `❌ Found ${loginCount} occurrences of loginChangedListener pattern. Expected 1.`
  );
  process.exit(1);
}
console.log("   loginChangedListener is unique ✓");

// --- Detect subscriptionChangedListener pattern ---
// Pattern: this.subscriptionChangedListener=<p>=>{<p>!==<Ma>.FREE&&this.setUseOpenAIKey(!1)}
const SUB_LISTENER_RE =
  /this\.subscriptionChangedListener=([\w$])=>\{\1!==([\w$]+)\.FREE&&this\.setUseOpenAIKey\(!1\)\}/;

const subMatch = data.match(SUB_LISTENER_RE);
if (!subMatch) {
  console.error("❌ subscriptionChangedListener pattern not found.");
  console.error(
    "   Expected: this.subscriptionChangedListener=p=>{p!==Ma.FREE&&this.setUseOpenAIKey(!1)}"
  );
  console.error("");
  console.error(
    "   Debug: search for subscriptionChangedListener in workbench:"
  );
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('subscriptionChangedListener=');if(i>-1)console.log(d.substring(i,i+300));else console.log('not found')\""
  );
  process.exit(1);
}

const subOriginal = subMatch[0];
const subParam = subMatch[1];
const subEnumName = subMatch[2];
console.log(
  `   subscriptionChangedListener: param=${subParam}, enum=${subEnumName}`
);
console.log(`   Pattern: ${subOriginal}`);

// Verify uniqueness
let subCount = 0;
pos = 0;
while ((pos = data.indexOf(subOriginal, pos)) !== -1) {
  subCount++;
  pos += 10;
}
if (subCount !== 1) {
  console.error(
    `❌ Found ${subCount} occurrences of subscriptionChangedListener pattern. Expected 1.`
  );
  process.exit(1);
}
console.log("   subscriptionChangedListener is unique ✓");

// --- Backup ---
if (!fs.existsSync(WORKBENCH_BACKUP)) {
  console.log("💾 Backing up workbench...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
}

if (!fs.existsSync(PRODUCT_BACKUP)) {
  console.log("💾 Backing up product.json...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
}

// --- Patch ---
const loginPatched = `this.loginChangedListener=${loginParam}=>{/*__openaiKeyPersistencePatched:login*/}`;
const subPatched = `this.subscriptionChangedListener=${subParam}=>{/*__openaiKeyPersistencePatched:sub*/}`;

console.log("🔧 Patching loginChangedListener → no-op...");
let patched = data.replace(loginOriginal, loginPatched);

if (patched === data || !patched.includes(PATCH_MARKER)) {
  console.error(
    "❌ Patch failed - loginChangedListener replacement did not take effect."
  );
  process.exit(1);
}

console.log("🔧 Patching subscriptionChangedListener → no-op...");
patched = patched.replace(subOriginal, subPatched);

if (!patched.includes("__openaiKeyPersistencePatched:sub")) {
  console.error(
    "❌ Patch failed - subscriptionChangedListener replacement did not take effect."
  );
  process.exit(1);
}

console.log("💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, patched);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log(
  "\n✅ Patch successful! OpenAI Key toggle will no longer be auto-disabled."
);
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-openai-key-persistence.js --restore");
