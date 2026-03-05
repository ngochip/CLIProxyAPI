#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench để sub-agents kế thừa maxMode (thinking) từ parent.
 *
 * Root cause: _runSubagent tạo Zf object với maxMode hardcoded thành false:
 *   new Zf({modelName:e.modelId, maxMode:!1, ...hty(e.credentials)})
 * → sub-agent LUÔN chạy không có thinking mode, dù parent có bật maxMode.
 *
 * Fix: Đọc maxMode từ parent's composer config qua _modelConfigService.
 *
 * NOTE: Cursor 2.6.11 đã fix credentials (qua e.credentials + hty()),
 * nhưng maxMode vẫn bị hardcode false.
 *
 * Usage: node patch-cursor-subagent-maxmode.js [--restore]
 *
 * Tested on: Cursor 2.6.11
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

const PATCH_MARKER = 'maxMode:this._modelConfigService.getModelConfig("composer").maxMode';

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
    "ℹ️  Already patched (subagent maxMode). Use --restore to revert, then re-apply all patches."
  );
  process.exit(0);
}

/**
 * Search pattern (version-independent):
 *   modelName:e.modelId,maxMode:!1
 *
 * Không include tên hàm minified (hty/gty/...) vì thay đổi mỗi version.
 * "modelName:", "modelId", "maxMode:" là stable identifiers.
 * Pattern unique vì chỉ có 1 chỗ trong _runSubagent tạo Zf với e.modelId.
 */
const ORIGINAL = "modelName:e.modelId,maxMode:!1";

let count = 0;
let pos = 0;
while ((pos = data.indexOf(ORIGINAL, pos)) !== -1) {
  count++;
  pos += 10;
}

if (count === 0) {
  console.error("❌ Target pattern not found. Cursor version may be incompatible.");
  console.error('   Pattern: "' + ORIGINAL + '"');
  console.error("");
  console.error("   Debug: tìm _runSubagent trong workbench:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('async _runSubagent');console.log(d.substring(i,i+800))\""
  );
  process.exit(1);
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
 * ORIGINAL (trong _runSubagent):
 *   new Zf({modelName:e.modelId, maxMode:!1, ...})
 *
 * PATCHED:
 *   new Zf({modelName:e.modelId, maxMode:this._modelConfigService.getModelConfig("composer").maxMode??!1, ...})
 *
 * - this._modelConfigService.getModelConfig("composer") → parent's composer config
 * - .maxMode → true nếu user bật thinking mode, false/undefined nếu không
 * - ?? !1 → fallback false nếu maxMode undefined
 * - Chỉ replace "modelName:e.modelId,maxMode:!1" → phần sau (...spread) giữ nguyên
 */
const PATCHED =
  'modelName:e.modelId,maxMode:this._modelConfigService.getModelConfig("composer").maxMode??!1';

console.log("🔧 Patching subagent maxMode...");
const patched = data.replace(ORIGINAL, PATCHED);

if (patched === data || !patched.includes(PATCH_MARKER)) {
  console.error("❌ Patch failed - replacement did not take effect.");
  process.exit(1);
}

console.log("💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, patched);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log("\n✅ Patch successful! Sub-agents will now inherit maxMode (thinking) from parent.");
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-subagent-maxmode.js --restore");
console.log("");
console.log("⚠️  Nếu đã apply các patch khác, cần re-apply sau restore:");
console.log("   ./apply-all-patches.sh");
