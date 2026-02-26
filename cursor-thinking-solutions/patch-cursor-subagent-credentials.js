#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench để sub-agents kế thừa custom OpenAI base URL và API key
 * từ parent conversation.
 *
 * Root cause: _runSubagent tạo model details object thiếu apiKey và openaiApiBaseUrl,
 * khiến sub-agents luôn dùng Cursor default API thay vì custom proxy.
 *
 * Fix: Inject credentials từ parent model vào Xf constructor bằng spread operator.
 *
 * Usage: node patch-cursor-subagent-credentials.js [--restore]
 *   --restore: Khôi phục file gốc từ backup
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

const PATCH_MARKER = '_d=this._aiService.getModelDetails({specificModelField:"composer"})';

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
    "ℹ️  Already patched (subagent credentials). Use --restore to revert, then patch again."
  );
  process.exit(0);
}

// Search pattern: duy nhất 1 occurrence trong _runSubagent method
// Pattern này version-independent (không dùng minified class names)
const ORIGINAL = "modelName:e.modelId,maxMode:!1}";

const idx = data.indexOf(ORIGINAL);
if (idx === -1) {
  console.error(
    "❌ Target pattern not found. Cursor version may be incompatible."
  );
  console.error('   Pattern: "' + ORIGINAL + '"');
  console.error("");
  console.error("   Debug: tìm _runSubagent trong workbench để xác định pattern mới:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('_runSubagent');console.log(d.substring(i,i+800))\""
  );
  process.exit(1);
}

// Verify uniqueness
let count = 0;
let pos = 0;
while ((pos = data.indexOf(ORIGINAL, pos)) !== -1) {
  count++;
  pos += 10;
}
if (count !== 1) {
  console.error(
    `❌ Found ${count} occurrences of target pattern. Expected 1.`
  );
  process.exit(1);
}

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
 * ORIGINAL (trong _runSubagent):
 *   new Xf({modelName:e.modelId,maxMode:!1})
 *
 * PATCHED:
 *   new Xf({modelName:e.modelId,maxMode:!1,...(()=>{try{const _d=...getModelDetails...;
 *     return{apiKey:_d?.apiKey,openaiApiBaseUrl:_d?.openaiApiBaseUrl,
 *            azureState:_d?.azureState,bedrockState:_d?.bedrockState}}catch(_e){return{}}})()})
 *
 * Spread operator inject credentials từ parent model:
 * - this._aiService.getModelDetails({specificModelField:"composer"}) → full Xf với credentials
 * - Nếu không có custom API key → apiKey = undefined → không thay đổi behavior
 * - try/catch → graceful fallback nếu getModelDetails fail
 */
const PATCHED =
  'modelName:e.modelId,maxMode:!1,...(()=>{try{const _d=this._aiService.getModelDetails({specificModelField:"composer"});return{apiKey:_d?.apiKey,openaiApiBaseUrl:_d?.openaiApiBaseUrl,azureState:_d?.azureState,bedrockState:_d?.bedrockState}}catch(_e){return{}}})()}';

console.log("🔧 Patching _runSubagent credentials...");
const patched = data.replace(ORIGINAL, PATCHED);

if (patched === data || !patched.includes(PATCH_MARKER)) {
  console.error("❌ Patch failed - replacement did not take effect.");
  process.exit(1);
}

console.log("💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, patched);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log("\n✅ Patch successful! Sub-agents will now inherit custom OpenAI credentials.");
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-subagent-credentials.js --restore");
console.log("");
console.log("⚠️  Nếu đã apply thinking patch, cần re-apply sau restore:");
console.log("   node patch-cursor-thinking.js");
