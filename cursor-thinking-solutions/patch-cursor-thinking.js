#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench to render thinking delimiters as native thinking blocks.
 *
 * Patch A: handleTextDelta - detect thinking delimiters → redirect sang handleThinkingDelta
 *
 * v6 (nonce-based delimiters):
 *   Proxy gửi nonce-based delimiters thay vì <think>/<\/think>:
 *     <!--thinking-start:XXXXXXXX--> (opening, XXXXXXXX = 8 hex chars random)
 *     <!--thinking-end:XXXXXXXX-->   (closing, cùng nonce)
 *   Nonce đảm bảo không bao giờ false-positive khi thinking content chứa
 *   </think>, code blocks, XML tags, etc.
 *   Backward compatible: vẫn detect legacy <think>/<\/think> format.
 *
 * NOTE: Patch B (thinking render loading fix) không còn cần thiết từ Cursor 2.6.11+
 *
 * Usage: node patch-cursor-thinking.js [--restore] [--force]
 *   --restore: Khôi phục file gốc từ backup
 *   --force:   Apply patch ngay cả khi đã bị disable (xem DISABLED_PATCHES)
 *
 * Tested on: Cursor 2.6.11, 2.6.18, 2.6.19
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
// Patch A: handleTextDelta - thinking delimiters → native UI
// ============================================================

const PATCH_A_MARKER = "__thinkTagState";
const PATCH_A_ORIGINAL =
  "handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()";

/**
 * Patch A strategy (v6 - nonce-based delimiters, compatible with Cursor 2.6.11+):
 *
 * State: __thinkTagState = {active, startTime, buf, nonce}
 *   - active: đang trong thinking block
 *   - startTime: thời điểm bắt đầu thinking (cho duration)
 *   - buf: buffer cho partial delimiters bị split across SSE deltas
 *   - nonce: 8 hex chars unique per thinking block ('' = legacy <think> mode)
 *
 * Delimiter format (proxy v6+):
 *   Opening: <!--thinking-start:XXXXXXXX-->  (XXXXXXXX = random nonce)
 *   Closing: <!--thinking-end:XXXXXXXX-->    (same nonce)
 *   Nonce match đảm bảo closing tag KHÔNG bao giờ false-positive,
 *   kể cả khi thinking content chứa </think>, code blocks, XML tags, etc.
 *
 * Backward compatibility:
 *   Vẫn detect legacy <think>/<\/think> format (nonce = '' → naive indexOf match).
 *
 * Flow:
 *   1. Prepend buffer từ delta trước (xử lý delimiter split)
 *   2. Nếu KHÔNG active:
 *      - Tìm <!--thinking-start:NONCE--> → activate thinking với nonce
 *      - Fallback: tìm <think> → activate thinking (legacy, nonce='')
 *      - Không tìm thấy → buffer partial delimiter ở cuối text
 *   3. Nếu active (đang trong thinking):
 *      - Nonce mode: tìm <!--thinking-end:NONCE--> (exact nonce match)
 *      - Legacy mode: tìm </think> (naive indexOf)
 *      - Khi tìm thấy → handleThinkingDelta + handleThinkingCompleted + recursive _after
 *      - Không tìm thấy → buffer partial closing delimiter
 */
const PATCH_A_PATCHED = 'handleTextDelta(n){if(n.length===0)return;if(!this.__thinkTagState)this.__thinkTagState={active:false,startTime:0,buf:"",nonce:""};const _s=this.__thinkTagState;let _txt=_s.buf+n;_s.buf="";if(!_s.active){const _sm=_txt.match(/<!--thinking-start:([0-9a-f]{8})-->/);if(_sm){const _oi=_sm.index;const _before=_txt.substring(0,_oi);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.nonce=_sm[1];_s.startTime=Date.now();_txt=_txt.substring(_oi+_sm[0].length);if(_txt.length===0)return;}else{const _oi2=_txt.indexOf("<think>");if(_oi2!==-1){const _before=_txt.substring(0,_oi2);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.nonce="";_s.startTime=Date.now();_txt=_txt.substring(_oi2+7);if(_txt.length===0)return;}else{let _bs=-1;for(let _k=Math.max(0,_txt.length-30);_k<_txt.length;_k++){const _tail=_txt.substring(_k);if("<!--thinking-start:".startsWith(_tail)||"<think>".startsWith(_tail)){if(_tail.length>=2){_bs=_k;break;}}}if(_bs!==-1){_s.buf=_txt.substring(_bs);_txt=_txt.substring(0,_bs);}if(_txt.length>0)this._origHandleTextDelta(_txt);return;}}}if(_s.active){let _ci=-1;let _tl=0;if(_s.nonce){const _et="<!--thinking-end:"+_s.nonce+"-->";_ci=_txt.indexOf(_et);_tl=_et.length;}else{_ci=_txt.indexOf("</think>");_tl=8;}if(_ci!==-1){const _tp=_txt.substring(0,_ci);if(_tp.length>0)this.handleThinkingDelta(_tp);const _dur=Math.max(1000,Date.now()-_s.startTime);_s.active=false;_s.startTime=0;_s.nonce="";const _after=_txt.substring(_ci+_tl);this.handleThinkingCompleted({message:{case:"thinkingCompleted",value:{thinkingDurationMs:_dur}}});if(_after.length>0)this.handleTextDelta(_after);return;}if(_s.nonce){const _et="<!--thinking-end:"+_s.nonce+"-->";for(let _k=Math.max(0,_txt.length-_et.length+1);_k<_txt.length;_k++){if(_et.startsWith(_txt.substring(_k))&&_txt.substring(_k).length>=2){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}}else{for(let _k=Math.max(0,_txt.length-7);_k<_txt.length-1;_k++){if("</think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}}if(_txt.length>0)this.handleThinkingDelta(_txt);return;}this._origHandleTextDelta(_txt)}_origHandleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()';

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
    "🔧 Applying Patch A: handleTextDelta → thinking delimiter detection..."
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
