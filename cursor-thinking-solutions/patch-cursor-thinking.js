#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench to render <think>...</think> tags as native thinking blocks.
 *
 * Cursor sử dụng internal gRPC protocol cho thinking (handleThinkingDelta).
 * Custom models qua OpenAI API không có path nào trigger native thinking UI.
 * Script này patch handleTextDelta để detect <think> tags trong content stream
 * và redirect sang handleThinkingDelta, cho phép custom models hiển thị thinking blocks.
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
console.log("🔍 Reading Cursor workbench...");
const data = fs.readFileSync(WORKBENCH_PATH, "utf8");

if (data.includes("__thinkTagState")) {
  console.log(
    "ℹ️  Already patched. Use --restore to revert, then patch again."
  );
  process.exit(0);
}

// Backup workbench
if (!fs.existsSync(WORKBENCH_BACKUP)) {
  console.log("💾 Backing up workbench...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
}

// Backup product.json
if (!fs.existsSync(PRODUCT_BACKUP)) {
  console.log("💾 Backing up product.json...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
}

const ORIGINAL =
  "handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()";

const idx = data.indexOf(ORIGINAL);
if (idx === -1) {
  console.error(
    "❌ handleTextDelta not found. Cursor version may be incompatible."
  );
  process.exit(1);
}

let count = 0;
let pos = 0;
while ((pos = data.indexOf(ORIGINAL, pos)) !== -1) {
  count++;
  pos += 10;
}
if (count !== 1) {
  console.error(
    `❌ Found ${count} occurrences of handleTextDelta. Expected 1.`
  );
  process.exit(1);
}

/**
 * Patch strategy (v3 - fixes thinking inside Exploring groups):
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
 *
 * Fixes so với v1:
 *   A. Tag boundary buffering: <think>/<\/think> bị split across deltas
 *   B. Recursive _after: text sau </think> qua handleTextDelta (detect nested tags)
 *
 * Fixes so với v2:
 *   C. (v3) Bỏ queueMicrotask → gọi handleThinkingCompleted ĐỒNG BỘ.
 *      Root cause v2 bug: khi Cursor server batch nhiều protobuf events
 *      (text-delta + tool-call-started), queueMicrotask chạy SAU tool-call-started
 *      → getLastBubble() trả về tool call bubble thay vì thinking → bail out
 *      → thinkingDurationMs không set → "Thinking" không có content.
 *      S_() (SolidJS batch) là synchronous nên không cần defer.
 */
const PATCHED = `handleTextDelta(n){if(n.length===0)return;if(!this.__thinkTagState)this.__thinkTagState={active:false,startTime:0,buf:""};const _s=this.__thinkTagState;let _txt=_s.buf+n;_s.buf="";if(!_s.active){const _oi=_txt.indexOf("<think>");if(_oi!==-1){const _before=_txt.substring(0,_oi);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.startTime=Date.now();_txt=_txt.substring(_oi+7);if(_txt.length===0)return;}else{for(let _k=Math.max(0,_txt.length-6);_k<_txt.length;_k++){if("<think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}if(_txt.length>0)this._origHandleTextDelta(_txt);return;}}if(_s.active){const _ci=_txt.indexOf("</think>");if(_ci!==-1){const _thinkPart=_txt.substring(0,_ci);if(_thinkPart.length>0)this.handleThinkingDelta(_thinkPart);const _dur=Math.max(1000,Date.now()-_s.startTime);_s.active=false;_s.startTime=0;const _after=_txt.substring(_ci+8);this.handleThinkingCompleted({thinkingDurationMs:_dur});if(_after.length>0)this.handleTextDelta(_after);return;}for(let _k=Math.max(0,_txt.length-7);_k<_txt.length;_k++){if("</think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}if(_txt.length>0)this.handleThinkingDelta(_txt);return;}this._origHandleTextDelta(_txt)}_origHandleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()`;

console.log("🔧 Patching handleTextDelta...");
const patched = data.replace(ORIGINAL, PATCHED);

if (patched === data || !patched.includes("__thinkTagState")) {
  console.error("❌ Patch failed.");
  process.exit(1);
}

console.log("💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, patched);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log("\n✅ Patch successful!");
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-thinking.js --restore");
