#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench để summarization kế thừa custom OpenAI base URL và API key.
 *
 * Root cause: summarize() tạo model details (Zf) chỉ với modelName,
 * thiếu apiKey/openaiApiBaseUrl → request đi qua Cursor server thay vì custom proxy
 * → "Slow Pool Error: Claude 4.6 Opus is not currently enabled in the slow pool"
 *
 * Fix: Inject credentials từ cursorAuthenticationService và reactiveStorageService
 * vào Zf constructor.
 *
 * NOTE: Từ Cursor 2.6.11+, class chứa summarize không còn có aiService.
 * Dùng cursorAuthenticationService.getApiKeyForModel + reactiveStorageService thay thế.
 * Class name (Zf, Yf, ...) thay đổi theo bản Cursor → auto-detect bằng regex.
 *
 * Usage: node patch-cursor-summarize-credentials.js [--restore]
 *
 * Tested on: Cursor 2.6.11, 2.6.14+, 3.0.12
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

const PATCH_MARKER = "_creds_s=this.reactiveStorageService.applicationUserPersistentStorage";

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

// Thinking patch (Patch B) cũng patch summarize credentials với marker khác
if (data.includes("__summarize_creds_patched")) {
  console.log(
    "ℹ️  Already patched by patch-cursor-thinking.js (Patch B). Skipping."
  );
  process.exit(0);
}

// Verify summarizeAction exists (anchor point)
if (!data.includes('"summarizeAction"')) {
  console.error("❌ 'summarizeAction' not found. Cursor version may be incompatible.");
  process.exit(1);
}

/**
 * Search pattern (v2 - Cursor 2.6.11+):
 *
 *   f=new <ModelDetailsClass>({modelName:u.modelConfig?.modelName})
 *
 * Class name (Zf, Yf, ...) thay đổi theo từng bản Cursor.
 * Auto-detect bằng regex gần "summarizeAction" thay vì hardcode.
 */
const REGION_ANCHOR = '"summarizeAction"';
const regionStart = data.indexOf(REGION_ANCHOR);
if (regionStart === -1) {
  console.error("❌ Cannot find summarizeAction region.");
  process.exit(1);
}

const regionEnd = Math.min(data.length, regionStart + 1500);
const region = data.substring(regionStart, regionEnd);

// Auto-detect: <varName>=new <ClassName>({modelName:<sourceVar>.modelConfig?.modelName})
// Cursor 2.6.x: f=new Zf({modelName:u.modelConfig?.modelName})
// Cursor 3.0+:  g=new Qg({modelName:l.modelConfig?.modelName})
// Dùng [\w$] vì minified identifier có thể chứa $ (vd: $l, $e)
const modelDetailsRegex = /([\w$])=new ([\w$]+)\(\{modelName:([\w$]+)\.modelConfig\?\.modelName\}\)/;
const regionMatch = region.match(modelDetailsRegex);

if (!regionMatch) {
  console.error("❌ Target pattern not found near summarizeAction.");
  console.error("   Expected: <var>=new <ClassName>({modelName:<src>.modelConfig?.modelName})");
  console.error("");
  console.error("   Debug: check summarize method:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('summarizeAction');console.log(d.substring(i,i+1500))\""
  );
  process.exit(1);
}

const RESULT_VAR = regionMatch[1];
const MODEL_DETAILS_CLASS = regionMatch[2];
const SOURCE_VAR = regionMatch[3];
const ORIGINAL = `${RESULT_VAR}=new ${MODEL_DETAILS_CLASS}({modelName:${SOURCE_VAR}.modelConfig?.modelName})`;
console.log(`   ModelDetails class: ${MODEL_DETAILS_CLASS} (${RESULT_VAR}=new ${MODEL_DETAILS_CLASS}({modelName:${SOURCE_VAR}...}))`);

// Verify uniqueness: pattern phải xuất hiện đúng 1 lần trong TOÀN BỘ file
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
 * Patch strategy (v2 - Cursor 2.6.11+):
 *
 * ORIGINAL (trong summarize method):
 *   f=new <ModelDetailsClass>({modelName:u.modelConfig?.modelName})
 *
 * PATCHED:
 *   f=new <ModelDetailsClass>({modelName:u.modelConfig?.modelName,...(()=>{try{
 *     const _creds_s=this.reactiveStorageService.applicationUserPersistentStorage;
 *     return{
 *       apiKey:this.cursorAuthenticationService.getApiKeyForModel(u.modelConfig?.modelName),
 *       openaiApiBaseUrl:_creds_s.openAIBaseUrl??void 0,
 *       azureState:_creds_s.azureState,
 *       bedrockState:_creds_s.bedrockState
 *     }}catch(_e){return{}}})()})
 *
 * Services dùng (available trong class):
 * - this.cursorAuthenticationService.getApiKeyForModel(modelName) → API key
 * - this.reactiveStorageService.applicationUserPersistentStorage → storage chứa settings
 *
 * NOTE: Class name (Zf, Yf, ...) auto-detected ở trên.
 */
const PATCHED =
  `${RESULT_VAR}=new ${MODEL_DETAILS_CLASS}({modelName:${SOURCE_VAR}.modelConfig?.modelName,...(()=>{try{const _creds_s=this.reactiveStorageService.applicationUserPersistentStorage;return{apiKey:this.cursorAuthenticationService.getApiKeyForModel(${SOURCE_VAR}.modelConfig?.modelName),openaiApiBaseUrl:_creds_s.openAIBaseUrl??void 0,azureState:_creds_s.azureState,bedrockState:_creds_s.bedrockState}}catch(_e){return{}}})()})`;

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
console.log("⚠️  Nếu đã apply thinking patch, cần re-apply sau restore:");
console.log("   node patch-cursor-thinking.js");
