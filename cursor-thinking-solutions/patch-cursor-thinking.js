#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench to render <think>...</think> tags as native thinking blocks.
 *
 * Patch A: handleTextDelta - detect <think> tags → redirect sang handleThinkingDelta
 *
 * NOTE: Patch B (thinking render loading fix) không còn cần thiết từ Cursor 2.6.11+
 * Cursor đã fix natively: loading branch dùng N=hasContent trực tiếp,
 * content hiển thị khi loading mà không phụ thuộc durationMs.
 *
 * Cursor 2.6.18 thay đổi:
 *   - handleThinkingDelta(text, thinkingStyle): param thứ 2 optional, undefined → DEFAULT
 *   - thinkingWordStreamer: stream word-by-word, flush() khi completed
 *   - Markdown renderer strip HTML tags → <think> nội dung hiển thị plaintext nếu không patch
 *   - Utility functions m0a/p0a strip <think> blocks trong context/summarization (ko ảnh hưởng display)
 *
 * Usage: node patch-cursor-thinking.js [--restore] [--force]
 *   --restore: Khôi phục file gốc từ backup
 *   --force:   Apply patch ngay cả khi đã bị disable (xem DISABLED_PATCHES)
 *
 * Tested on: Cursor 2.6.11, 2.6.18
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

function countOccurrences(data, pattern) {
  let count = 0,
    idx = 0;
  while ((idx = data.indexOf(pattern, idx)) !== -1) {
    count++;
    idx += pattern.length;
  }
  return count;
}

// --- Restore mode ---
if (process.argv.includes("--restore")) {
  let restored = false;

  if (fs.existsSync(WORKBENCH_BACKUP)) {
    fs.copyFileSync(WORKBENCH_BACKUP, WORKBENCH_PATH);
    console.log("✅ Workbench restored from backup");
    restored = true;
  } else {
    console.error("❌ Workbench backup not found:", WORKBENCH_BACKUP);
  }

  if (fs.existsSync(PRODUCT_BACKUP)) {
    fs.copyFileSync(PRODUCT_BACKUP, PRODUCT_PATH);
    console.log("✅ product.json restored from backup");
    restored = true;
  } else {
    console.error("❌ product.json backup not found:", PRODUCT_BACKUP);
  }

  if (restored) {
    console.log("\n📋 Restart Cursor (Cmd+Q → reopen) to apply.");
  }
  process.exit(restored ? 0 : 1);
}

// --- Patch mode ---
const forceApply = process.argv.includes("--force");

// Patches tạm disable — dùng --force nếu vẫn muốn apply
const DISABLED_PATCHES = {
};

console.log("🔍 Reading Cursor workbench...\n");
let data = fs.readFileSync(WORKBENCH_PATH, "utf8");

// ============================================================
// Patch A: handleTextDelta - <think> tags → native thinking UI
// ============================================================

const PATCH_A_MARKER = "__thinkTagState";
const PATCH_A_ORIGINAL =
  "handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()";

/**
 * Patch A strategy (v5 - compatible with Cursor 2.6.11 → 2.6.18):
 *
 * State: __thinkTagState = {active, startTime, buf, bubbleId}
 *   - active: đang trong thinking block
 *   - startTime: thời điểm bắt đầu thinking (cho duration)
 *   - buf: buffer cho partial <think>/<\/think> tags bị split across SSE deltas
 *   - bubbleId: ID thinking bubble hiện tại (để append trực tiếp)
 *
 * Flow:
 *   1. Prepend buffer từ delta trước (xử lý tag split)
 *   2. Nếu KHÔNG active:
 *      - Tìm <think> → gửi text trước qua _origHandleTextDelta, activate thinking
 *      - Không tìm thấy → check partial tag (>= 2 chars) ở cuối text, buffer nếu có
 *   3. Nếu active (đang trong thinking):
 *      - Tìm </think> → gửi thinking content, handleThinkingCompleted + recursive cho _after
 *      - Không tìm thấy → check partial tag (>= 2 chars) ở cuối, buffer nếu có
 *
 *   Buffer chỉ giữ lại partial >= 2 chars (ví dụ "<t", "</t") để giảm false positive.
 *   Ký tự "<" đơn lẻ xuất hiện rất phổ biến (HTML, code, markdown) và gây giật render.
 *
 * IMPORTANT (Cursor 2.6.11+):
 *   handleThinkingCompleted(n) expects n.message.case === "thinkingCompleted"
 *   and reads duration from n.message.value.thinkingDurationMs.
 *
 * Smooth streaming (v5):
 *   Thay vì gọi handleThinkingDelta → thinkingWordStreamer (gây giật với chunk lớn),
 *   patch tạo thinking bubble 1 lần rồi append text trực tiếp vào bubble.thinking.text
 *   qua _appendThinkContent(). Giống cách handleTextDelta append text vào bubble.text.
 *   handleThinkingDelta chỉ được gọi 1 lần với empty string để init bubble + thinkingStyle.
 */
const PATCH_A_PATCHED = `handleTextDelta(n){if(n.length===0)return;if(!this.__thinkTagState)this.__thinkTagState={active:false,startTime:0,buf:""};const _s=this.__thinkTagState;let _txt=_s.buf+n;_s.buf="";if(!_s.active){const _oi=_txt.indexOf("<think>");if(_oi!==-1){const _before=_txt.substring(0,_oi);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.startTime=Date.now();_txt=_txt.substring(_oi+7);if(_txt.length===0)return;}else{for(let _k=Math.max(0,_txt.length-6);_k<_txt.length-1;_k++){if("<think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}if(_txt.length>0)this._origHandleTextDelta(_txt);return;}}if(_s.active){const _ci=_txt.indexOf("</think>");if(_ci!==-1){const _thinkPart=_txt.substring(0,_ci);if(_thinkPart.length>0)this.handleThinkingDelta(_thinkPart);const _dur=Math.max(1000,Date.now()-_s.startTime);_s.active=false;_s.startTime=0;const _after=_txt.substring(_ci+8);this.handleThinkingCompleted({message:{case:"thinkingCompleted",value:{thinkingDurationMs:_dur}}});if(_after.length>0)this.handleTextDelta(_after);return;}for(let _k=Math.max(0,_txt.length-7);_k<_txt.length-1;_k++){if("</think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}if(_txt.length>0)this.handleThinkingDelta(_txt);return;}this._origHandleTextDelta(_txt)}_origHandleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()`;

// --- Check current state ---
const patchAApplied = data.includes(PATCH_A_MARKER);

if (patchAApplied) {
  console.log(
    "ℹ️  Patch already applied. Use --restore to revert, then patch again."
  );
  process.exit(0);
}

console.log("📊 Patch status:");
console.log(
  `   Patch A (handleTextDelta): ${patchAApplied ? "✅ applied" : "⏳ pending"}`
);
console.log("");

// --- Backup ---
// Luôn refresh backup từ unpatched source khi Cursor update
// (backup cũ có thể từ version trước, restore sẽ gây lỗi)
if (!fs.existsSync(WORKBENCH_BACKUP)) {
  console.log("💾 Backing up workbench...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
} else if (!patchAApplied) {
  console.log("💾 Refreshing workbench backup (Cursor may have been updated)...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
}

if (!fs.existsSync(PRODUCT_BACKUP)) {
  console.log("💾 Backing up product.json...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
} else if (!patchAApplied) {
  console.log("💾 Refreshing product.json backup...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
}

// --- Apply Patch A ---
if (DISABLED_PATCHES.A && !forceApply) {
  console.log(
    `⏭️  Patch A skipped: ${DISABLED_PATCHES.A}\n   Dùng --force để apply.\n`
  );
} else {
  const countA = countOccurrences(data, PATCH_A_ORIGINAL);
  if (countA === 0) {
    console.error(
      "❌ Patch A: handleTextDelta pattern not found. Cursor version may be incompatible."
    );
    process.exit(1);
  }
  if (countA !== 1) {
    console.error(
      `❌ Patch A: Found ${countA} occurrences of handleTextDelta. Expected 1.`
    );
    process.exit(1);
  }

  console.log(
    "🔧 Applying Patch A: handleTextDelta → <think> tag detection..."
  );
  data = data.replace(PATCH_A_ORIGINAL, PATCH_A_PATCHED);

  if (!data.includes(PATCH_A_MARKER)) {
    console.error("❌ Patch A failed.");
    process.exit(1);
  }
  console.log("   ✅ Patch A applied");
}

// --- Check if any patch was actually applied ---
const anyPatchApplied = data !== fs.readFileSync(WORKBENCH_PATH, "utf8");

if (!anyPatchApplied) {
  console.log("ℹ️  No patches applied. Nothing to write.");
  process.exit(0);
}

// --- Write & update checksum ---
console.log("\n💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, data);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log("\n✅ Patch applied successfully!");
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-thinking.js --restore");
