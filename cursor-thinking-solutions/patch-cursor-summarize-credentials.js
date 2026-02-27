#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench để summarization kế thừa custom OpenAI base URL và API key.
 *
 * Root cause: summarize() tạo model details (Zf) chỉ với modelName,
 * thiếu apiKey/openaiApiBaseUrl → request đi qua Cursor server thay vì custom proxy
 * → "Slow Pool Error: Claude 4.6 Opus is not currently enabled in the slow pool"
 *
 * Fix: Inject credentials từ aiService.getModelDetails vào constructor.
 * Dùng regex để handle minified class/variable names (thay đổi mỗi version).
 *
 * Usage: node patch-cursor-summarize-credentials.js [--restore]
 *
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

const PATCH_MARKER = '_d=this.aiService.getModelDetails({specificModelField:"composer"})';

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
    "ℹ️  Already patched (summarize credentials). Use --restore to revert, then re-apply all patches."
  );
  process.exit(0);
}

// Verify summarizeAction exists (anchor point)
if (!data.includes('"summarizeAction"')) {
  console.error("❌ 'summarizeAction' not found. Cursor version may be incompatible.");
  process.exit(1);
}

/**
 * Search pattern (regex):
 *   credentials:this.convertModelDetailsToCredentials(new <ClassName>({modelName:<varName>.modelConfig?.modelName}))
 *
 * - <ClassName> và <varName> là minified names, thay đổi mỗi version → dùng regex capture
 * - "credentials:", "convertModelDetailsToCredentials", "modelName:", "modelConfig" là stable identifiers
 *
 * Chỉ match pattern gần "summarizeAction" để tránh false positives.
 */
const REGION_ANCHOR = '"summarizeAction"';
const regionStart = data.indexOf(REGION_ANCHOR);
if (regionStart === -1) {
  console.error("❌ Cannot find summarizeAction region.");
  process.exit(1);
}

const SEARCH_REGEX =
  /credentials:this\.convertModelDetailsToCredentials\(new (\w+)\(\{modelName:(\w+)\.modelConfig\?\.modelName\}\)\)/;

// Tìm trong region 1000 chars sau summarizeAction
const regionEnd = Math.min(data.length, regionStart + 1000);
const region = data.substring(regionStart, regionEnd);
const match = SEARCH_REGEX.exec(region);

if (!match) {
  console.error("❌ Target pattern not found near summarizeAction.");
  console.error("   Expected: credentials:this.convertModelDetailsToCredentials(new <Class>({modelName:<var>.modelConfig?.modelName}))");
  console.error("");
  console.error("   Debug: check summarize method:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('summarizeAction');console.log(d.substring(i,i+500))\""
  );
  process.exit(1);
}

const className = match[1];
const varName = match[2];
console.log(`   Detected: class=${className}, var=${varName}`);

// Verify uniqueness: pattern phải xuất hiện đúng 1 lần trong TOÀN BỘ file
const ORIGINAL = match[0];
let count = 0;
let pos = 0;
while ((pos = data.indexOf(ORIGINAL, pos)) !== -1) {
  count++;
  pos += 10;
}
if (count !== 1) {
  console.error(`❌ Found ${count} occurrences of target pattern. Expected 1.`);
  process.exit(1);
}

console.log("   Pattern is unique ✓");

// Backup
if (!fs.existsSync(WORKBENCH_BACKUP)) {
  console.log("💾 Backing up workbench...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
}

if (!fs.existsSync(PRODUCT_BACKUP)) {
  console.log("💾 Backing up product.json...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
}

/**
 * Patch strategy:
 *
 * ORIGINAL (trong summarize method):
 *   new Zf({modelName:u.modelConfig?.modelName})
 *
 * PATCHED:
 *   new Zf({modelName:u.modelConfig?.modelName,...(()=>{try{const _d=this.aiService.getModelDetails(...)
 *     return{apiKey:_d?.apiKey,openaiApiBaseUrl:_d?.openaiApiBaseUrl,
 *            azureState:_d?.azureState,bedrockState:_d?.bedrockState}}catch(_e){return{}}})()})
 *
 * - this.aiService.getModelDetails({specificModelField:"composer"}) → full model details với credentials
 * - Nếu không có custom API key → apiKey = undefined → không thay đổi behavior
 * - try/catch → graceful fallback nếu getModelDetails fail
 */
const PATCHED =
  `credentials:this.convertModelDetailsToCredentials(new ${className}({modelName:${varName}.modelConfig?.modelName,...(()=>{try{const _d=this.aiService.getModelDetails({specificModelField:"composer"});return{apiKey:_d?.apiKey,openaiApiBaseUrl:_d?.openaiApiBaseUrl,azureState:_d?.azureState,bedrockState:_d?.bedrockState}}catch(_e){return{}}})()}))`;

console.log("🔧 Patching summarize credentials...");
const patched = data.replace(ORIGINAL, PATCHED);

if (patched === data || !patched.includes(PATCH_MARKER)) {
  console.error("❌ Patch failed - replacement did not take effect.");
  process.exit(1);
}

console.log("💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, patched);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log("\n✅ Patch successful! Summarization will now use custom OpenAI credentials.");
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-summarize-credentials.js --restore");
console.log("");
console.log("⚠️  Nếu đã apply thinking patch hoặc subagent patch, cần re-apply sau restore:");
console.log("   node patch-cursor-thinking.js");
console.log("   node patch-cursor-subagent-credentials.js");
