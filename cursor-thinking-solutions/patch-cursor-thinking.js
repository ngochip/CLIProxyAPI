#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench.
 *
 * Patch A: handleTextDelta - detect thinking delimiters → redirect sang handleThinkingDelta
 *   v9 (2026-04-15) - Fix partial delimiter buffering:
 *     - Unified partial-open regex covers ALL prefixes: <, <!, <!-, <!--, ..., <!--thinking-start:NONCE--
 *     - Also covers legacy <think> prefixes: <t, <th, ..., <think
 *     - Closing tag partial buffer threshold lowered to length>=1 (was >=2)
 *     - Legacy </think> partial buffer includes single "<" at end
 *
 *   v8 (2026-04-13) - Cursor 3.2.0 compatibility:
 *     - Fixed partial delimiter buffering: regex-based partial match thay vì
 *       startsWith check. Handles SSE splits within nonce (e.g. "<!--thinking-start:c"
 *       + "4a7f9e1-->"). Old logic chỉ buffer khi tail < 19 chars (prefix length),
 *       fail khi split xảy ra trong nonce portion.
 *     - Strip <!--thinkId:xxx--> markers from text after closing delimiter.
 *
 *   v6 (nonce-based delimiters):
 *     Proxy gửi nonce-based delimiters thay vì <think>/<\/think>:
 *       <!--thinking-start:XXXXXXXX--> / <!--thinking-end:XXXXXXXX-->
 *     Backward compatible: vẫn detect legacy <think>/<\/think> format.
 *
 * Patch B: summarize() - thêm credentials vào model details khi summarize
 *   Cursor mới (3.0+) không gửi apiKey/openaiApiBaseUrl khi summarize,
 *   nên Cursor server không forward request qua proxy.
 *   Patch này thêm credentials (apiKey + proxy URL) giống normal chat,
 *   để summarize request được forward qua proxy.
 *
 * Usage: node patch-cursor-thinking.js [--restore] [--force]
 *   --restore: Khôi phục file gốc từ backup
 *   --force:   Apply patch ngay cả khi đã bị disable (xem DISABLED_PATCHES)
 *
 * Tested on: Cursor 2.6.11, 2.6.18, 2.6.19, 3.0.12, 3.2.0
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
// Cursor 3.0+ thay đổi: thêm preserveUnfinishedToolsOnNarration check trước cancelUnfinishedToolCalls
const PATCH_A_ORIGINAL_V2 =
  "handleTextDelta(n){if(n.length===0)return;this.options.preserveUnfinishedToolsOnNarration||this.cancelUnfinishedToolCalls()";
const PATCH_A_ORIGINAL_V1 =
  "handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()";
const PATCH_A_ORIGINAL = null; // auto-detect below

/**
 * Patch A strategy (v9 - improved partial delimiter buffering):
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
 *
 * v9 changes:
 *   - Unified partial-open regex covers ALL prefixes including <, <!, <!-
 *     (v8 missed these 3 positions, causing tag loss on unlucky TCP splits)
 *   - Closing tag partial buffer accepts length>=1 (v8 required >=2)
 *   - Legacy </think> partial buffer includes single "<" at end
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
// Thinking logic core (shared between V1 and V2)
function buildPatchA(cancelExpr) {
  // Unified partial opening tag regex - matches any trailing suffix that could be
  // the beginning of <!--thinking-start:NONCE--> or legacy <think>.
  // Covers ALL positions: <, <!, <!-, <!--, <!--t, ..., <!--thinking-start:NONCE--
  // Also covers legacy: <t, <th, <thi, <thin, <think
  const partialOpenRegex = '/(?:<(?:!(?:-(?:-(?:t(?:h(?:i(?:n(?:k(?:i(?:n(?:g(?:-(?:s(?:t(?:a(?:r(?:t(?::(?:[0-9a-f]{1,8}(?:-(?:-)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?)?|<(?:t(?:h(?:i(?:n(?:k)?)?)?)?)?)$/';

  return 'handleTextDelta(n){if(n.length===0)return;if(!this.__thinkTagState)this.__thinkTagState={active:false,startTime:0,buf:"",nonce:""};const _s=this.__thinkTagState;let _txt=_s.buf+n;_s.buf="";if(!_s.active){const _sm=_txt.match(/<!--thinking-start:([0-9a-f]{8})-->/);if(_sm){const _oi=_sm.index;const _before=_txt.substring(0,_oi);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.nonce=_sm[1];_s.startTime=Date.now();_txt=_txt.substring(_oi+_sm[0].length);if(_txt.length===0)return;}else{const _oi2=_txt.indexOf("<think>");if(_oi2!==-1){const _before=_txt.substring(0,_oi2);if(_before.length>0)this._origHandleTextDelta(_before);_s.active=true;_s.nonce="";_s.startTime=Date.now();_txt=_txt.substring(_oi2+7);if(_txt.length===0)return;}else{let _bs=-1;const _pm=_txt.match(' + partialOpenRegex + ');\nif(_pm){_bs=_pm.index;}if(_bs!==-1){_s.buf=_txt.substring(_bs);_txt=_txt.substring(0,_bs);}if(_txt.length>0)this._origHandleTextDelta(_txt);return;}}}if(_s.active){let _ci=-1;let _tl=0;if(_s.nonce){const _et="<!--thinking-end:"+_s.nonce+"-->";_ci=_txt.indexOf(_et);_tl=_et.length;}else{_ci=_txt.indexOf("</think>");_tl=8;}if(_ci!==-1){const _tp=_txt.substring(0,_ci);if(_tp.length>0)this.handleThinkingDelta(_tp);const _dur=Math.max(1000,Date.now()-_s.startTime);_s.active=false;_s.startTime=0;_s.nonce="";let _after=_txt.substring(_ci+_tl);_after=_after.replace(/<!--thinkId:[a-f0-9]+-->/g,"");this.handleThinkingCompleted({message:{case:"thinkingCompleted",value:{thinkingDurationMs:_dur}}});if(_after.length>0)this.handleTextDelta(_after);return;}if(_s.nonce){const _et="<!--thinking-end:"+_s.nonce+"-->";for(let _k=Math.max(0,_txt.length-_et.length+1);_k<_txt.length;_k++){if(_et.startsWith(_txt.substring(_k))&&_txt.substring(_k).length>=1){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}}else{for(let _k=Math.max(0,_txt.length-7);_k<_txt.length;_k++){if("</think>".startsWith(_txt.substring(_k))){_s.buf=_txt.substring(_k);_txt=_txt.substring(0,_k);break;}}}if(_txt.length>0)this.handleThinkingDelta(_txt);return;}this._origHandleTextDelta(_txt)}_origHandleTextDelta(n){if(n.length===0)return;' + cancelExpr;
}

// --- Auto-detect pattern version ---
let detectedOriginal = null;
let detectedVersion = null;

if (data.includes(PATCH_A_ORIGINAL_V2)) {
  detectedOriginal = PATCH_A_ORIGINAL_V2;
  detectedVersion = "V2 (Cursor 3.0+)";
} else if (data.includes(PATCH_A_ORIGINAL_V1)) {
  detectedOriginal = PATCH_A_ORIGINAL_V1;
  detectedVersion = "V1 (Cursor 2.6.x)";
}

// --- Check current state ---
const patchAApplied = data.includes(PATCH_A_MARKER);
const patchBAlreadyApplied = data.includes("__summarize_creds_patched");

if (patchAApplied && patchBAlreadyApplied) {
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
  `   Patch B (summarize no-creds): ${patchBAlreadyApplied ? "✅ applied" : "⏳ pending"}`
);
if (detectedVersion) {
  console.log(`   Detected pattern: ${detectedVersion}`);
}
console.log("");

// --- Backup ---
// Luôn refresh backup từ unpatched source khi Cursor update
// (backup cũ có thể từ version trước, restore sẽ gây lỗi)
const noPatchAppliedYet = !patchAApplied && !patchBAlreadyApplied;

if (!fs.existsSync(WORKBENCH_BACKUP)) {
  console.log("💾 Backing up workbench...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
} else if (noPatchAppliedYet) {
  console.log("💾 Refreshing workbench backup (Cursor may have been updated)...");
  fs.copyFileSync(WORKBENCH_PATH, WORKBENCH_BACKUP);
}

if (!fs.existsSync(PRODUCT_BACKUP)) {
  console.log("💾 Backing up product.json...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
} else if (noPatchAppliedYet) {
  console.log("💾 Refreshing product.json backup...");
  fs.copyFileSync(PRODUCT_PATH, PRODUCT_BACKUP);
}

// --- Apply Patch A ---
if (patchAApplied) {
  console.log("ℹ️  Patch A already applied, skipping.");
} else if (DISABLED_PATCHES.A && !forceApply) {
  console.log(
    `⏭️  Patch A skipped: ${DISABLED_PATCHES.A}\n   Dùng --force để apply.\n`
  );
} else {
  if (!detectedOriginal) {
    console.error(
      "❌ Patch A: handleTextDelta pattern not found. Cursor version may be incompatible."
    );
    console.error(
      "   Tried V2 (3.0+): ...preserveUnfinishedToolsOnNarration||this.cancelUnfinishedToolCalls()"
    );
    console.error(
      "   Tried V1 (2.6.x): ...this.cancelUnfinishedToolCalls()"
    );
    process.exit(1);
  }

  const countA = countOccurrences(data, detectedOriginal);
  if (countA !== 1) {
    console.error(
      `❌ Patch A: Found ${countA} occurrences of handleTextDelta. Expected 1.`
    );
    process.exit(1);
  }

  const cancelExpr = detectedOriginal === PATCH_A_ORIGINAL_V2
    ? "this.options.preserveUnfinishedToolsOnNarration||this.cancelUnfinishedToolCalls()"
    : "this.cancelUnfinishedToolCalls()";
  const PATCH_A_PATCHED = buildPatchA(cancelExpr);

  console.log(
    `🔧 Applying Patch A (${detectedVersion}): handleTextDelta → thinking delimiter detection...`
  );
  data = data.replace(detectedOriginal, PATCH_A_PATCHED);

  if (!data.includes(PATCH_A_MARKER)) {
    console.error("❌ Patch A failed.");
    process.exit(1);
  }
  console.log("   ✅ Patch A applied");
}

// ============================================================
// Patch B: summarize() - bỏ credentials, force built-in model
// ============================================================

const PATCH_B_MARKER = "__summarize_creds_patched";

// Hardcoded patterns cho backward compatibility
const PATCH_B_ORIGINAL_V2 =
  'f=new Yf({modelName:l.modelConfig?.modelName})';
const PATCH_B_ORIGINAL_V1 =
  'f=new jf({modelName:l.modelConfig?.modelName})';
// Cursor 3.2+ thêm maxMode param
const PATCH_B_ORIGINAL_V3 =
  'f=new ng({modelName:l.modelConfig?.modelName,maxMode:!1})';

// Auto-detect regex cho bất kỳ class name nào (vd: Yf, jf, Qg, ng...)
// Dùng [\w$] vì minified identifier có thể chứa $
// Pattern: <var>=new <Class>({modelName:<src>.modelConfig?.modelName}) hoặc thêm ,maxMode:!1
const PATCH_B_AUTO_REGEX = /([\w$])=new ([\w$]+)\(\{modelName:([\w$]+)\.modelConfig\?\.modelName(,maxMode:![01])?\}\)/;

// Thêm credentials vào constructor giống normal chat
// → convertModelDetailsToCredentials(f) sẽ trả về apiKeyCredentials với proxy baseUrl
// → Cursor server forward summarize request qua proxy
function buildPatchB(className, resultVar, sourceVar, maxModeSuffix) {
  resultVar = resultVar || 'f';
  sourceVar = sourceVar || 'l';
  maxModeSuffix = maxModeSuffix || '';
  return resultVar + '=new ' + className + '({modelName:' + sourceVar + '.modelConfig?.modelName' + maxModeSuffix + ',apiKey:this.cursorAuthenticationService.getApiKeyForModel(' + sourceVar + '.modelConfig?.modelName),openaiApiBaseUrl:this.reactiveStorageService.applicationUserPersistentStorage.openAIBaseUrl??void 0})/*' + PATCH_B_MARKER + '*/';
}

const patchBApplied = data.includes(PATCH_B_MARKER);

console.log(
  `   Patch B (summarize no-creds): ${patchBApplied ? "✅ applied" : "⏳ pending"}`
);

if (!patchBApplied) {
  if (DISABLED_PATCHES.B && !forceApply) {
    console.log(
      `⏭️  Patch B skipped: ${DISABLED_PATCHES.B}\n   Dùng --force để apply.\n`
    );
  } else {
    let detectedBOriginal = null;
    let detectedBVersion = null;
    let detectedClassName = null;
    let detectedResultVar = 'f';
    let detectedSourceVar = 'l';
    let detectedMaxMode = '';

    if (data.includes(PATCH_B_ORIGINAL_V3)) {
      detectedBOriginal = PATCH_B_ORIGINAL_V3;
      detectedBVersion = "V3 (ng with maxMode)";
      detectedClassName = "ng";
      detectedMaxMode = ",maxMode:!1";
    } else if (data.includes(PATCH_B_ORIGINAL_V2)) {
      detectedBOriginal = PATCH_B_ORIGINAL_V2;
      detectedBVersion = "V2 (Yf without creds)";
      detectedClassName = "Yf";
    } else if (data.includes(PATCH_B_ORIGINAL_V1)) {
      detectedBOriginal = PATCH_B_ORIGINAL_V1;
      detectedBVersion = "V1 (jf without creds)";
      detectedClassName = "jf";
    } else {
      // Auto-detect: tìm gần "summarizeAction" bằng regex
      const anchor = '"summarizeAction"';
      const anchorIdx = data.indexOf(anchor);
      if (anchorIdx !== -1) {
        const region = data.substring(anchorIdx, Math.min(data.length, anchorIdx + 1500));
        const autoMatch = region.match(PATCH_B_AUTO_REGEX);
        if (autoMatch) {
          detectedBOriginal = autoMatch[0];
          detectedResultVar = autoMatch[1];
          detectedClassName = autoMatch[2];
          detectedSourceVar = autoMatch[3];
          detectedMaxMode = autoMatch[4] || '';
          detectedBVersion = `auto-detected (${detectedClassName} without creds)`;
        }
      }
    }

    if (!detectedBOriginal) {
      console.error(
        "❌ Patch B: summarize model pattern not found. Cursor version may be incompatible."
      );
      console.error(
        "   Tried V2: f=new Yf({modelName:...})"
      );
      console.error(
        "   Tried V1: f=new jf({modelName:...})"
      );
      console.error(
        "   Tried auto-detect regex near summarizeAction"
      );
    } else {
      const countB = countOccurrences(data, detectedBOriginal);
      if (countB !== 1) {
        console.error(
          `❌ Patch B: Found ${countB} occurrences. Expected 1.`
        );
      } else {
        const PATCH_B_PATCHED = buildPatchB(detectedClassName, detectedResultVar, detectedSourceVar, detectedMaxMode);
        console.log(
          `🔧 Applying Patch B (${detectedBVersion}): summarize() → add credentials to ${detectedClassName}...`
        );
        data = data.replace(detectedBOriginal, PATCH_B_PATCHED);

        if (!data.includes(PATCH_B_MARKER)) {
          console.error("❌ Patch B failed.");
          process.exit(1);
        }
        console.log("   ✅ Patch B applied");
      }
    }
  }
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
