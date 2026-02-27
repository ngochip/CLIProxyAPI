#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench to render <think>...</think> tags as native thinking blocks.
 *
 * Patch A: handleTextDelta - detect <think> tags → redirect sang handleThinkingDelta
 * Patch B: thinking render function - hiển thị content khi loading (fix stuck loading)
 *
 * Usage: node patch-cursor-thinking.js [--restore]
 *   --restore: Khôi phục file gốc từ backup
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
console.log("🔍 Reading Cursor workbench...\n");
let data = fs.readFileSync(WORKBENCH_PATH, "utf8");

// ============================================================
// Patch A: handleTextDelta - <think> tags → native thinking UI
// ============================================================

const PATCH_A_MARKER = "__thinkTagState";
const PATCH_A_ORIGINAL =
  "handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()";

/**
 * Patch A strategy (v3 - fixes thinking inside Exploring groups):
 *
 * State: __thinkTagState = {active, startTime, buf}
 *   - active: đang trong thinking block
 *   - startTime: thời điểm bắt đầu thinking (cho duration)
 *   - buf: buffer cho partial <think>/<\/think> tags bị split across SSE deltas
 *
 * Flow:
 *   1. Prepend buffer từ delta trước (xử lý tag split)
 *   2. Nếu KHÔNG active:
 *      - Tìm <think> → gửi text trước qua _origHandleTextDelta, activate thinking
 *      - Không tìm thấy → check partial tag ở cuối text, buffer nếu có
 *   3. Nếu active (đang trong thinking):
 *      - Tìm </think> → gửi thinking content qua handleThinkingDelta
 *        → handleThinkingCompleted ĐỒNG BỘ + recursive handleTextDelta cho _after
 *      - Không tìm thấy → check partial tag ở cuối, buffer nếu có
 */
const PATCH_A_PATCHED = `handleTextDelta(n){if(n.length===0)return;if(!this.__thinkTagState)this.__thinkTagState={active:false,startTime:0,buf:""};const _s=this.__thinkTagState;let _txt=_s.buf+n;_s.buf="";if(!_s.active){const _oi=_txt.indexOf("<think>");if(_oi!==-1){const _before=_txt.substring(0,_oi);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.startTime=Date.now();_txt=_txt.substring(_oi+7);if(_txt.length===0)return;}else{for(let _k=Math.max(0,_txt.length-6);_k<_txt.length;_k++){if("<think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}if(_txt.length>0)this._origHandleTextDelta(_txt);return;}}if(_s.active){const _ci=_txt.indexOf("</think>");if(_ci!==-1){const _thinkPart=_txt.substring(0,_ci);if(_thinkPart.length>0)this.handleThinkingDelta(_thinkPart);const _dur=Math.max(1000,Date.now()-_s.startTime);_s.active=false;_s.startTime=0;const _after=_txt.substring(_ci+8);this.handleThinkingCompleted({thinkingDurationMs:_dur});if(_after.length>0)this.handleTextDelta(_after);return;}for(let _k=Math.max(0,_txt.length-7);_k<_txt.length;_k++){if("</think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}if(_txt.length>0)this.handleThinkingDelta(_txt);return;}this._origHandleTextDelta(_txt)}_origHandleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()`;

// ============================================================
// Patch B: Thinking render - hiển thị content khi loading
// ============================================================

/**
 * Patch B: Fix thinking block stuck loading (v4)
 *
 * Root cause: Hàm render thinking block tính expandable dựa trên durationMs.
 * Khi thinking đang chạy (loading=true), durationMs = undefined → f = true → v = false
 * → content KHÔNG được render dù text đã có trong store.
 * User thấy "Thinking" animation mà không thấy content.
 * Khi thinking kết thúc → durationMs > 0 → content hiện ra hết cùng lúc.
 *
 * Fix: Thêm !t (not loading) vào điều kiện f.
 * Khi loading=true: f luôn = false → v = s (true nếu có content) → content hiển thị.
 * Khi loading=false: giữ nguyên behavior cũ.
 *
 * Render function (minified):
 *   function Fnc({thinking:n, durationMs:e, loading:t=!1, parseHeaders:i=!1}) {
 *     ...
 *     f = !m && (e === void 0 || e <= 0) && r <= 0;   // ← BUG: f=true khi loading
 *     v = d || f ? !1 : s;                              // v=false → content ẩn
 *     return t
 *       ? <ThinkingBlock expandable={v} open={v} loading={true}>
 *           {v && n && <MarkdownRenderer isStreaming={true}>{n}</MarkdownRenderer>}
 *         </ThinkingBlock>
 *       : ...;
 *   }
 */
const PATCH_B_MARKER = "!t&&(e===void 0||e<=0)";
const PATCH_B_ORIGINAL = "f=!m&&(e===void 0||e<=0)&&r<=0";
const PATCH_B_PATCHED = "f=!m&&!t&&(e===void 0||e<=0)&&r<=0";

// --- Check current state ---
const patchAApplied = data.includes(PATCH_A_MARKER);
const patchBApplied = data.includes(PATCH_B_MARKER);

if (patchAApplied && patchBApplied) {
  console.log(
    "ℹ️  All patches already applied. Use --restore to revert, then patch again."
  );
  process.exit(0);
}

console.log("📊 Patch status:");
console.log(
  `   Patch A (handleTextDelta): ${patchAApplied ? "✅ applied" : "⏳ pending"}`
);
console.log(
  `   Patch B (thinking render): ${patchBApplied ? "✅ applied" : "⏳ pending"}`
);
console.log("");

// --- Backup ---
if (!fs.existsSync(WORKBENCH_BACKUP)) {
  console.log("💾 Backing up workbench...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
}

if (!fs.existsSync(PRODUCT_BACKUP)) {
  console.log("💾 Backing up product.json...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
}

let patchCount = 0;

// --- Apply Patch A ---
if (!patchAApplied) {
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

  console.log("🔧 Applying Patch A: handleTextDelta → <think> tag detection...");
  data = data.replace(PATCH_A_ORIGINAL, PATCH_A_PATCHED);

  if (!data.includes(PATCH_A_MARKER)) {
    console.error("❌ Patch A failed.");
    process.exit(1);
  }
  patchCount++;
  console.log("   ✅ Patch A applied");
}

// --- Apply Patch B ---
if (!patchBApplied) {
  const countB = countOccurrences(data, PATCH_B_ORIGINAL);
  if (countB === 0) {
    console.error(
      "❌ Patch B: Thinking render pattern not found. Cursor version may be incompatible."
    );
    process.exit(1);
  }
  if (countB !== 1) {
    console.error(
      `❌ Patch B: Found ${countB} occurrences of render pattern. Expected 1.`
    );
    process.exit(1);
  }

  console.log(
    "🔧 Applying Patch B: thinking render → show content during loading..."
  );
  data = data.replace(PATCH_B_ORIGINAL, PATCH_B_PATCHED);

  if (!data.includes(PATCH_B_MARKER)) {
    console.error("❌ Patch B failed.");
    process.exit(1);
  }
  patchCount++;
  console.log("   ✅ Patch B applied");
}

if (patchCount === 0) {
  console.log("\nℹ️  No patches to apply.");
  process.exit(0);
}

// --- Write & update checksum ---
console.log("\n💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, data);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log(`\n✅ ${patchCount} patch(es) applied successfully!`);
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-thinking.js --restore");
