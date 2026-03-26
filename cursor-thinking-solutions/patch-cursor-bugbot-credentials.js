#!/usr/bin/env node

/**
 * Patch Cursor IDE workbench để Agent Review (BugBot) sử dụng custom OpenAI credentials.
 *
 * Root cause: gTt() tạo StreamBugBotRequest (Dpn) KHÔNG có model_details field.
 * Khi model_details undefined, Cursor server tự chọn model (default) thay vì dùng custom proxy.
 *
 * Fix: Inject model_details vào StreamBugBotRequest bằng cách:
 * 1. Auto-detect aiService service ID (Ti("aiService") → biến minified thay đổi theo version)
 * 2. Dùng n.instantiationService.invokeFunction để get aiService
 * 3. Gọi aiService.getModelDetails({specificModelField:"composer"}) để lấy credentials
 * 4. Tạo ModelDetails (Yf) object với credentials và inject vào Dpn
 *
 * Usage: node patch-cursor-bugbot-credentials.js [--restore]
 *
 * Tested on: Cursor 2.6.19+
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

const PATCH_MARKER = "_bb_ai=n.instantiationService.invokeFunction";

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
    "ℹ️  Already patched (bugbot credentials). Use --restore to revert, then re-apply all patches."
  );
  process.exit(0);
}

/**
 * Step 1: Auto-detect aiService service ID
 *
 * Pattern: <varName>=Ti("aiService")
 * Ti = createDecorator, tạo service identifier cho DI container.
 * Tên biến (Gv, Hv, ...) thay đổi theo từng bản Cursor → auto-detect bằng regex.
 */
const aiServiceIdRegex = /(\w+)=Ti\("aiService"\)/;
const aiServiceIdMatch = data.match(aiServiceIdRegex);

if (!aiServiceIdMatch) {
  console.error('❌ Cannot find aiService ID (Ti("aiService") pattern).');
  console.error("");
  console.error("   Debug: search for aiService registration:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('aiService');console.log(d.substring(Math.max(0,i-50),i+200))\""
  );
  process.exit(1);
}

const AI_SERVICE_ID = aiServiceIdMatch[1];
console.log(`   aiService ID: ${AI_SERVICE_ID}`);

/**
 * Step 2: Auto-detect ModelDetails class name (Yf, Zf, ...)
 *
 * Pattern: typeName:"aiserver.v1.ModelDetails" trong class definition
 */
const modelDetailsAnchor = '"aiserver.v1.ModelDetails"';
const mdIdx = data.indexOf(modelDetailsAnchor);
if (mdIdx === -1) {
  console.error("❌ Cannot find aiserver.v1.ModelDetails in workbench.");
  process.exit(1);
}

const mdRegion = data.substring(Math.max(0, mdIdx - 500), mdIdx);
const mdClassRegex = /,(\w+)=class \w+ extends \w+\{constructor\(\w+\)\{super\(\),\w+\.util\.initPartial\(\w+,this\)\}/;
const mdClassMatch = mdRegion.match(mdClassRegex);

if (!mdClassMatch) {
  console.error("❌ Cannot find ModelDetails class name.");
  console.error("");
  console.error("   Debug:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('aiserver.v1.ModelDetails');console.log(d.substring(Math.max(0,i-500),i+200))\""
  );
  process.exit(1);
}

const MODEL_DETAILS_CLASS = mdClassMatch[1];
console.log(`   ModelDetails class: ${MODEL_DETAILS_CLASS}`);

/**
 * Step 3: Find target pattern
 *
 * Pattern trong gTt (runEditorBugbot):
 *   he=new Dpn({gitDiff:W,contextFiles:z,...<userInstructions>...,
 *     inBackgroundSubsidized:e?.inBackgroundSubsidized===!0,deepReview:le})
 *
 * Stable pattern (không dùng minified names):
 *   inBackgroundSubsidized:e?.inBackgroundSubsidized===!0,deepReview:le})
 *
 * Biến he, le, ne thay đổi theo version → dùng regex
 */
const REGION_ANCHOR = "[runEditorBugbot]";
const regionStart = data.indexOf(REGION_ANCHOR);
if (regionStart === -1) {
  console.error("❌ Cannot find [runEditorBugbot] anchor.");
  process.exit(1);
}

const bugbotRegion = data.substring(
  Math.max(0, regionStart - 5000),
  regionStart
);

const targetRegex =
  /(\w+)=new (\w+)\(\{gitDiff:\w+,contextFiles:\w+,\.\.\.(\w+)!==void 0\?\{userInstructions:\3\}:\{\},inBackgroundSubsidized:e\?\.inBackgroundSubsidized===!0,deepReview:(\w+)\}\)/;
const targetMatch = bugbotRegion.match(targetRegex);

if (!targetMatch) {
  console.error("❌ Target pattern not found near [runEditorBugbot].");
  console.error("   Expected: <var>=new <Class>({gitDiff:...,deepReview:...})");
  console.error("");
  console.error("   Debug: check gTt function:");
  console.error(
    '   node -e "const d=require(\'fs\').readFileSync(\'' +
      WORKBENCH_PATH +
      "','utf8');const i=d.indexOf('[runEditorBugbot]');console.log(d.substring(Math.max(0,i-3000),i))\""
  );
  process.exit(1);
}

const ORIGINAL = targetMatch[0];
const REQUEST_VAR = targetMatch[1];
const REQUEST_CLASS = targetMatch[2];
const INSTRUCTIONS_VAR = targetMatch[3];
const DEEP_REVIEW_VAR = targetMatch[4];

console.log(`   Request class: ${REQUEST_CLASS}`);
console.log(`   Request var: ${REQUEST_VAR}`);

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
 * ORIGINAL:
 *   he=new Dpn({gitDiff:W,contextFiles:z,...ne!==void 0?{userInstructions:ne}:{},
 *     inBackgroundSubsidized:e?.inBackgroundSubsidized===!0,deepReview:le})
 *
 * PATCHED:
 *   he=new Dpn({gitDiff:W,contextFiles:z,...ne!==void 0?{userInstructions:ne}:{},
 *     inBackgroundSubsidized:e?.inBackgroundSubsidized===!0,deepReview:le,
 *     modelDetails:(()=>{try{
 *       const _bb_ai=n.instantiationService.invokeFunction(_f=>_f.get(<aiServiceId>));
 *       const _bb_d=_bb_ai.getModelDetails({specificModelField:"composer"});
 *       return new <ModelDetailsClass>({
 *         modelName:_bb_d?.modelName,
 *         apiKey:_bb_d?.apiKey,
 *         openaiApiBaseUrl:_bb_d?.openaiApiBaseUrl,
 *         azureState:_bb_d?.azureState,
 *         bedrockState:_bb_d?.bedrockState
 *       });
 *     }catch(_e){return void 0}})()
 *   })
 *
 * n = context object trong gTt, có n.instantiationService
 * Gv (auto-detected) = Ti("aiService") = service ID cho aiService
 * aiService.getModelDetails() trả về Yf object với đầy đủ credentials
 */
const PATCHED = `${REQUEST_VAR}=new ${REQUEST_CLASS}({gitDiff:W,contextFiles:z,...${INSTRUCTIONS_VAR}!==void 0?{userInstructions:${INSTRUCTIONS_VAR}}:{},inBackgroundSubsidized:e?.inBackgroundSubsidized===!0,deepReview:${DEEP_REVIEW_VAR},modelDetails:(()=>{try{const _bb_ai=n.instantiationService.invokeFunction(_f=>_f.get(${AI_SERVICE_ID}));const _bb_d=_bb_ai.getModelDetails({specificModelField:"composer"});return new ${MODEL_DETAILS_CLASS}({modelName:_bb_d?.modelName,apiKey:_bb_d?.apiKey,openaiApiBaseUrl:_bb_d?.openaiApiBaseUrl,azureState:_bb_d?.azureState,bedrockState:_bb_d?.bedrockState})}catch(_e){return void 0}})()})`;

console.log("🔧 Patching bugbot credentials...");
const patched = data.replace(ORIGINAL, PATCHED);

if (patched === data || !patched.includes(PATCH_MARKER)) {
  console.error("❌ Patch failed - replacement did not take effect.");
  process.exit(1);
}

console.log("💾 Writing patched workbench...");
fs.writeFileSync(WORKBENCH_PATH, patched);

console.log("🔑 Updating checksum in product.json...");
updateProductChecksum();

console.log(
  "\n✅ Patch successful! Agent Review (BugBot) will now use custom OpenAI credentials."
);
console.log("");
console.log("📋 Restart Cursor to apply:");
console.log("   1. Quit Cursor completely (Cmd+Q)");
console.log("   2. Reopen Cursor");
console.log("");
console.log("🔄 To restore:");
console.log("   node patch-cursor-bugbot-credentials.js --restore");
console.log("");
console.log("⚠️  Nếu đã apply other patches, cần re-apply sau restore:");
console.log("   ./apply-all-patches.sh");
