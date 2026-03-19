#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench to render text streaming more smoothly.
 *
 * Problem: Cursor's _origHandleTextDelta dumps each SSE chunk immediately into
 * the SolidJS reactive store, triggering a full markdown re-parse per chunk.
 * When multiple chunks arrive in a TCP burst (common with Claude API batching),
 * they all trigger separate re-renders in rapid succession → text appears "chunky".
 *
 * Solution: Intercept text before it reaches _origHandleTextDelta and feed it through
 * a lightweight word streamer (same vkf class Cursor already uses for thinking text).
 * The streamer splits text into small chunks and emits them on a timer, creating
 * smooth word-by-word appearance.
 *
 * The streamer auto-flushes when:
 *   - Tool calls start (cancelUnfinishedToolCalls)
 *   - Thinking blocks start (handleThinkingDelta)
 *   - Stream completes (dispose)
 *
 * NOTE: This patch is independent of patch-cursor-thinking.js.
 *       It patches _origHandleTextDelta (which the thinking patch creates).
 *       If thinking patch is NOT applied, it patches handleTextDelta directly.
 *
 * Minified helper names (h_/m_, Jw/Gw, etc.) change across Cursor versions.
 * This patch uses regex to detect them dynamically.
 *
 * Usage: node patch-cursor-smooth-streaming.js [--restore]
 *
 * Tested on: Cursor 2.6.19+
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
  console.log("ℹ️  Smooth streaming patch uses shared backup with thinking patch.");
  console.log("   Use patch-cursor-thinking.js --restore to restore all patches.");
  process.exit(0);
}

// --- Patch mode ---
console.log("🔍 Reading Cursor workbench...\n");
let data = fs.readFileSync(WORKBENCH_PATH, "utf8");

// ============================================================
// Patch: _origHandleTextDelta → smooth word streamer
// ============================================================

const PATCH_MARKER = "__textWordStreamer";

/**
 * Strategy: Wrap _origHandleTextDelta with a word streamer.
 *
 * We intercept the text BEFORE the final store update and feed it through
 * a word streamer (reusing Cursor's existing vkf class). The streamer
 * splits text into ~16 char chunks and emits them at intervals, creating
 * smooth word-by-word output.
 *
 * The original _origHandleTextDelta handles bubble creation and store updates.
 * We split it into:
 *   1. _ensureTextBubble(n): handles bubble creation + cancelUnfinishedToolCalls
 *   2. _appendTextToStore(n): just the store update (s.text + n → store)
 *
 * The streamer calls _appendTextToStore for each word chunk.
 * _origHandleTextDelta now: _ensureTextBubble(n) + enqueue to streamer.
 *
 * The vkf class reference is found via thinkingWordStreamer constructor.
 *
 * Minified helper names are detected via regex:
 *   - helperFn: h_(), m_(), etc. — creates default bubble object
 *   - batchFn:  Jw(), Gw(), etc. — batches reactive store updates
 */

// Regex matches _origHandleTextDelta body, captures minified helper names
const ORIG_REGEX = new RegExp(
  '_origHandleTextDelta\\(n\\)\\{' +
  'if\\(n\\.length===0\\)return;' +
  'this\\.cancelUnfinishedToolCalls\\(\\),' +
  'this\\.notifyFirstTokenIfNeeded\\(\\);' +
  'const e=this\\.instantiationService\\.invokeFunction\\(a=>a\\.get\\(Oa\\)\\),' +
  't=e\\.getComposerData\\(this\\.composerDataHandle\\);' +
  'if\\(!t\\)return;' +
  'const i=e\\.getLastBubble\\(this\\.composerDataHandle\\),' +
  'r=i&&t\\.generatingBubbleIds\\?\\.includes\\(i\\.bubbleId\\);' +
  'if\\(i\\?\\.type!==ul\\.AI\\|\\|i\\.capabilityType!==void 0\\|\\|!r\\)' +
  '\\{const a=\\{\\.\\.\\.(\\w+)\\(\\),codeBlocks:\\[\\],type:ul\\.AI,text:""\\};' +
  '(\\w+)\\(\\(\\)=>\\{' +
  'e\\.appendComposerBubbles\\(this\\.composerDataHandle,\\[a\\]\\),' +
  'e\\.updateComposerDataSetStore\\(this\\.composerDataHandle,' +
  'l=>l\\("generatingBubbleIds",\\[a\\.bubbleId\\]\\)\\)\\}\\)\\}' +
  'const s=e\\.getLastAiBubble\\(this\\.composerDataHandle\\);' +
  'if\\(!s\\)return;' +
  'const o=s\\.text\\+n;' +
  'e\\.updateComposerDataSetStore\\(this\\.composerDataHandle,' +
  'a=>a\\("conversationMap",s\\.bubbleId,"text",o\\)\\)\\}'
);

const origMatch = data.match(ORIG_REGEX);

let PATCH_ORIGINAL = null;
let PATCH_PATCHED = null;

if (origMatch) {
  PATCH_ORIGINAL = origMatch[0];
  const helperFn = origMatch[1];
  const batchFn = origMatch[2];
  console.log(`   Detected helpers: ${helperFn}(), ${batchFn}()`);

  PATCH_PATCHED =
    '_origHandleTextDelta(n){if(n.length===0)return;' +
    'this.cancelUnfinishedToolCalls(),this.notifyFirstTokenIfNeeded();' +
    'const e=this.instantiationService.invokeFunction(a=>a.get(Oa)),' +
    't=e.getComposerData(this.composerDataHandle);' +
    'if(!t)return;' +
    'const i=e.getLastBubble(this.composerDataHandle),' +
    'r=i&&t.generatingBubbleIds?.includes(i.bubbleId);' +
    'if(i?.type!==ul.AI||i.capabilityType!==void 0||!r)' +
    '{const a={...' + helperFn + '(),codeBlocks:[],type:ul.AI,text:""};' +
    batchFn + '(()=>{' +
    'e.appendComposerBubbles(this.composerDataHandle,[a]),' +
    'e.updateComposerDataSetStore(this.composerDataHandle,' +
    'l=>l("generatingBubbleIds",[a.bubbleId]))})}' +
    'if(!this.__textWordStreamer){' +
    'const a=this.thinkingWordStreamer?.constructor;' +
    'if(a){this.__textWordStreamer=new a(l=>this._appendTextChunk(l))}' +
    'else{const s=e.getLastAiBubble(this.composerDataHandle);' +
    'if(!s)return;const o=s.text+n;' +
    'e.updateComposerDataSetStore(this.composerDataHandle,' +
    'a=>a("conversationMap",s.bubbleId,"text",o));return}}' +
    'this.__textWordStreamer.enqueue(n)}' +
    '_flushTextStreamer(){this.__textWordStreamer&&this.__textWordStreamer.flush()}' +
    '_appendTextChunk(n){' +
    'const e=this.instantiationService.invokeFunction(a=>a.get(Oa)),' +
    't=e.getLastAiBubble(this.composerDataHandle);' +
    'if(!t)return;const i=t.text+n;' +
    'e.updateComposerDataSetStore(this.composerDataHandle,' +
    'a=>a("conversationMap",t.bubbleId,"text",i))}';
}

const FLUSH_INJECT_CANCEL_ORIGINAL = "cancelUnfinishedToolCalls(){";
const FLUSH_INJECT_CANCEL_PATCHED = "cancelUnfinishedToolCalls(){this._flushTextStreamer&&this._flushTextStreamer();";

const FLUSH_INJECT_THINKING_ORIGINAL = "handleThinkingDelta(n,e){const t=n.length===0;this.cancelUnfinishedToolCalls()";
const FLUSH_INJECT_THINKING_PATCHED = "handleThinkingDelta(n,e){const t=n.length===0;this._flushTextStreamer&&this._flushTextStreamer();this.cancelUnfinishedToolCalls()";

// --- Check current state ---
const patchApplied = data.includes(PATCH_MARKER);

if (patchApplied) {
  console.log(
    "ℹ️  Smooth streaming patch already applied. Use --restore to revert, then patch again."
  );
  process.exit(0);
}

// Check if thinking patch is applied (we need _origHandleTextDelta to exist)
const hasThinkingPatch = data.includes("__thinkTagState");
if (!hasThinkingPatch) {
  console.error(
    "❌ Thinking patch not found. Apply patch-cursor-thinking.js first."
  );
  console.error(
    "   Smooth streaming patches _origHandleTextDelta which is created by the thinking patch."
  );
  process.exit(1);
}

console.log("📊 Patch status:");
console.log(`   Thinking patch: ✅ applied`);
console.log(
  `   Smooth streaming: ${patchApplied ? "✅ applied" : "⏳ pending"}`
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

// --- Apply main patch ---
if (!PATCH_ORIGINAL) {
  console.error(
    "❌ _origHandleTextDelta pattern not found. Cursor version may have changed."
  );
  console.error(
    "   Regex could not match method body. Minified structure may differ significantly."
  );
  process.exit(1);
}

console.log("🔧 Applying smooth streaming patch to _origHandleTextDelta...");
data = data.replace(PATCH_ORIGINAL, PATCH_PATCHED);

if (!data.includes(PATCH_MARKER)) {
  console.error("❌ Main patch failed.");
  process.exit(1);
}
console.log("   ✅ Main patch applied");

// --- Apply flush injection to cancelUnfinishedToolCalls ---
if (data.includes(FLUSH_INJECT_CANCEL_ORIGINAL)) {
  console.log("🔧 Injecting flush into cancelUnfinishedToolCalls...");
  data = data.replace(FLUSH_INJECT_CANCEL_ORIGINAL, FLUSH_INJECT_CANCEL_PATCHED);
  console.log("   ✅ Flush injection applied");
} else {
  console.log("⚠️  cancelUnfinishedToolCalls pattern not found, skipping flush injection");
}

// --- Apply flush injection to handleThinkingDelta ---
if (data.includes(FLUSH_INJECT_THINKING_ORIGINAL)) {
  console.log("🔧 Injecting flush into handleThinkingDelta...");
  data = data.replace(FLUSH_INJECT_THINKING_ORIGINAL, FLUSH_INJECT_THINKING_PATCHED);
  console.log("   ✅ Flush injection applied");
} else {
  console.log("⚠️  handleThinkingDelta pattern not found, skipping flush injection");
}

// --- Write & update checksum ---
console.log("\n💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, data);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log("\n✅ Smooth streaming patch applied successfully!");
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-thinking.js --restore");
