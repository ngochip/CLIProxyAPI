# Cursor IDE Architecture Reference

Tài liệu reference cho AI agents khi cần debug, patch, hoặc extend Cursor IDE.
Cập nhật lần cuối: 2026-03-04, Cursor v2.6.11.

---

## 1. App File Structure

```
/Applications/Cursor.app/Contents/Resources/app/
├── product.json                                    ← metadata + checksums
└── out/
    └── vs/
        ├── workbench/
        │   └── workbench.desktop.main.js           ← MAIN PATCH TARGET (~50MB, minified)
        │   └── workbench.desktop.main.css
        ├── code/
        │   └── electron-sandbox/
        │       └── workbench/
        │           └── workbench.html
        │           └── workbench.js
        │   └── electron-utility/
        │       └── sharedProcess/
        │           └── sharedProcessMain.js         ← checksum verification service
        └── base/
            └── parts/
                └── sandbox/
                    └── electron-sandbox/
                        └── preload.js
```

### Key Files

| File | Size | Role |
|------|------|------|
| `workbench.desktop.main.js` | ~50MB | Client-side workbench (UI, handlers, services) |
| `product.json` | ~5KB | App metadata + SHA-256 checksums |
| `sharedProcessMain.js` | ~1MB | Shared process (checksum verification service) |

---

## 2. Client-Server Communication Protocol

Cursor dùng **gRPC/protobuf** cho communication giữa client ↔ Cursor AI server.

### Flow cho custom models (Override OpenAI Base URL)

```
Cursor Client                    Cursor Server                   Custom Proxy
     │                                │                               │
     │──gRPC(apiKeyCredentials)──────►│                               │
     │   {apiKey, baseUrl}            │──HTTP(OpenAI SSE)────────────►│
     │                                │   Authorization: Bearer <key> │
     │                                │◄────────SSE stream────────────│
     │                                │   data: {"choices":[...]}     │
     │◄──protobuf stream─────────────│                               │
     │   InferenceStreamResponse      │                               │
```

**QUAN TRỌNG**: Custom models KHÔNG giao tiếp trực tiếp với client. Tất cả đi qua Cursor server.
Client gửi `apiKeyCredentials` (apiKey + baseUrl) lên server qua gRPC → server dùng credentials đó
để gọi HTTP tới custom proxy → server convert SSE response → protobuf → gửi về client.

### InferenceStreamResponse (Protobuf)

Message chính mà Cursor server gửi cho client khi streaming:

```protobuf
message InferenceStreamResponse {
  oneof response {
    InferenceTextStreamPart text_part = 1;         // {text, is_final}
    InferenceToolCallStreamPart tool_call_part = 2; // {tool_call_id, tool_name, args, is_complete, tool_index}
    InferenceUsageInfo usage = 3;
    InferenceResponseInfo response_info = 4;
    InferenceExtendedUsageInfo extended_usage = 5;
    InferenceProviderMetadataInfo provider_metadata = 6;
    InferenceInvocationIdInfo invocation_id = 7;
    InferenceStreamError error = 8;
    InferenceThinkingStreamPart thinking_part = 9;  // {text, signature?, is_final}
  }
}
```

### ThinkingLevel (Request field 49)

```protobuf
enum ThinkingLevel {
  THINKING_LEVEL_UNSPECIFIED = 0;
  THINKING_LEVEL_MEDIUM = 1;
  THINKING_LEVEL_HIGH = 2;
}
```

### apiKeyCredentials

Client gửi custom model credentials qua gRPC:

```javascript
e.apiKey ? {
  case: "apiKeyCredentials",
  value: new iTn({apiKey: e.apiKey, baseUrl: e.openaiApiBaseUrl})
} : {case: void 0}
```

Nếu `apiKey` là undefined → `{case: void 0}` → server dùng default Cursor API.

---

## 3. Internal Event Pipeline

Protobuf messages từ server được convert thành internal events (tên converter function thay đổi mỗi version):

```
Protobuf message  ──►  i3t() converter  ──►  Internal events  ──►  Handler methods
                                                                         │
textDelta         ──►  {type:"text-delta", text}        ──►  handleTextDelta()
thinkingDelta     ──►  {type:"thinking-delta", text}    ──►  handleThinkingDelta()
thinkingCompleted ──►  {type:"thinking-completed", ms}  ──►  handleThinkingCompleted()
toolCallDelta     ──►  {type:"tool-call-delta", ...}    ──►  handleToolCallDelta()
```

### handleThinkingDelta(text, thinkingStyle?)

Tạo thinking bubble (nếu chưa có) với `capabilityType: go.THINKING`, dùng `thinkingWordStreamer` để stream text.
Bubble được append **synchronously** qua `S_()` (SolidJS `batch()`).

```javascript
handleThinkingDelta(n, e) {
  const i = this.instantiationService.invokeFunction(u => u.get($a));
  const s = i.getLastBubble(this.composerDataHandle);
  const o = s?.capabilityType === go.THINKING;

  let a;
  if (o) {
    a = s.bubbleId;  // Reuse existing thinking bubble
  } else {
    this.thinkingWordStreamer && this.thinkingWordStreamer.flush();
    const u = {...PS(), type: ol.AI, text: "", capabilityType: go.THINKING};
    a = u.bubbleId;
    S_(() => {  // S_ = SolidJS batch() → SYNCHRONOUS
      i.appendComposerBubbles(this.composerDataHandle, [u]);
      i.updateComposerDataSetStore(this.composerDataHandle, d => d("generatingBubbleIds", [u.bubbleId]));
    });
  }

  this.currentThinkingBubbleId = a;
  // ...
  (this.thinkingWordStreamer || (this.thinkingWordStreamer = new PKp(u => this.appendThinkingText(u))),
   this.thinkingWordStreamer.enqueue(n));
}
```

### appendThinkingText(text)

Dùng `currentThinkingBubbleId` (đọc tại thời điểm callback) để update thinking text.

```javascript
appendThinkingText(n) {
  const e = this.currentThinkingBubbleId;
  if (!e) return;
  // Update conversationMap[bubbleId].thinking.text += n
  this.instantiationService.invokeFunction(i => i.get($a))
    .updateComposerDataSetStore(this.composerDataHandle,
      i => i("conversationMap", e, "thinking",
        r => ({text: (r?.text ?? "") + n, signature: r?.signature ?? ""})));
}
```

### handleThinkingCompleted({thinkingDurationMs})

Flush word streamer, set duration, remove from generating list.
**QUAN TRỌNG:** Check `getLastBubble().capabilityType === go.THINKING` — nếu fail thì return early, `thinkingDurationMs` KHÔNG được set!

```javascript
handleThinkingCompleted(n) {
  this.thinkingWordStreamer && this.thinkingWordStreamer.flush();
  const e = this.instantiationService.invokeFunction(s => s.get($a));
  const t = e.getLastBubble(this.composerDataHandle);
  if (t?.capabilityType !== go.THINKING) return;  // ← bail-out point!
  const i = t.bubbleId;
  const r = n.thinkingDurationMs;
  this.emitAfterModelThought(i, r);
  S_(() => {
    e.updateComposerDataSetStore(this.composerDataHandle, s => s("conversationMap", i, "thinkingDurationMs", r));
    e.updateComposerDataSetStore(this.composerDataHandle, s => s("generatingBubbleIds", []));
  });
}
```

### S_() = SolidJS batch() (SYNCHRONOUS)

```javascript
function S_(n) { return Pse(n, false); }
// Pse runs callback synchronously, batches reactive updates
```

Bubble append trong `handleThinkingDelta` xảy ra **ngay lập tức**. Không có race condition.

### PKp = Word Streamer (thinkingWordStreamer)

Timer-based word streamer, chia text thành chunks và stream từng chunk qua interval.

```javascript
class PKp {
  constructor(onChunk) { this.onChunk = onChunk; this.queue = []; this.timer = ...; }
  enqueue(n) { this.queue.push(n); this.startNextSummary(); }
  flush() { this.timer.cancel(); /* process all remaining synchronously */ }
  dispose() { this.isDisposed = true; this.timer.dispose(); }
}
```

**Quan trọng:** PKp hỗ trợ re-enqueue sau `flush()`. Không cần tạo instance mới cho thinking block tiếp theo.

### Thinking UI Render Logic (function `iYc` - tên minified thay đổi)

Tìm bằng: `thinking:n,durationMs:e,loading:t=!1,parseHeaders:i=!1`

```javascript
function iYc({thinking: n, durationMs: e, loading: t = false, parseHeaders: i = false, copyText: r}) {
  const a = e !== void 0 ? Math.round(e / 1e3) : 0;  // seconds
  const l = !!(n && n.trim().length > 0);              // hasContent

  // Header parsing
  const {title: u, hasBodyContent: d, headerCount: m} = l ? NGo(n) : {...};
  const p = i && u.length > 0;   // parseHeaders && hasTitle
  const g = u.length > 0 && !d;  // titleOnly
  const f = g || p;              // isHeaderMode

  // C = collapsed khi completed (no duration)
  const C = !f && (e === void 0 || e <= 0) && a <= 0;
  // x = expandable (completed path only)
  const x = g || C ? false : l;

  // [B, R] = SolidJS signal, B starts as false (expanded)
  const [B, R] = mr(false);

  if (t) {
    // LOADING path (Cursor 2.6.11 native fix):
    // N = hasContent, M = hasContent && !collapsed
    const N = l, M = N && !B;
    return <ThinkingBlock expandable={N} open={M} onOpenChange={O => R(!O)} loading={true}>
      {M && n && <MarkdownRenderer isStreaming={true} shouldFade={true}>{n}</MarkdownRenderer>}
    </ThinkingBlock>;
  }

  // COMPLETED path
  return <ThinkingBlock expandable={x} defaultOpen={false}>
    {x && I && <MarkdownRenderer isStreaming={false}>{I}</MarkdownRenderer>}
  </ThinkingBlock>;
}
```

**QUAN TRỌNG:**
- `thinkingDurationMs` PHẢI > 0 để completed thinking block expandable
- Nếu `handleThinkingCompleted` bail-out → `thinkingDurationMs` KHÔNG set → "Thought briefly" nhưng KHÔNG expand
- **Cursor 2.6.11+ native fix:** Loading path dùng `N = hasContent` trực tiếp thay vì `x`.
  Content hiển thị khi loading mà không phụ thuộc `durationMs`.
  **Patch B (loading fix) KHÔNG CÒN CẦN THIẾT.**

### vLe = Event Handler Class

Handler được tạo **MỘT LẦN** cho toàn bộ agent loop (shared across turns). `__thinkTagState`, `thinkingWordStreamer`, `currentThinkingBubbleId` persist across turns.

```javascript
// Tạo handler → dùng cho tất cả turns → dispose khi agent loop kết thúc
const handler = new vLe(instantiationService, composerDataHandle, actionManager, generationUUID);
try { await agentClientService.run(..., handler, ...); }
finally { handler.dispose(); }
```

`dispose()` flushes word streamer nhưng KHÔNG gọi `handleThinkingCompleted`.

### Tag Stripping Functions (client-side)

`p1o` và `g1o` strip `<think>`/`<thinking>` tags — nhưng CHỈ dùng cho conversation history formatting (gửi lại cho model), KHÔNG ảnh hưởng incoming text deltas.

```javascript
function p1o(n) {
  return n.replace(/<think>[\s\S]*?<\/think>/gi, "")
    .replace(/<thinking>[\s\S]*?<\/thinking>/gi, "")
    .replace(/\n{3,}/g, "\n\n").trim();
}
```

---

## 4. Key Services & Classes

> **Lưu ý**: Tên class bị minify (Xf, b1o, ALe, etc.) thay đổi MỖI version.
> Property names và method names được giữ nguyên. Dùng method name để tìm class.

### 4.1 Model Details Object (class `Xf` - tên minified thay đổi)

Tìm bằng: `z(this,"modelName"),z(this,"apiKey"),z(this,"enableGhostMode")`

```javascript
class Xf extends Gn {
  constructor(e) {
    super();
    z(this, "modelName");
    z(this, "apiKey");
    z(this, "enableGhostMode");
    z(this, "azureState");
    z(this, "enableSlowPool");
    z(this, "openaiApiBaseUrl");
    z(this, "bedrockState");
    z(this, "maxMode");
    ge.util.initPartial(e, this);
  }
}
```

Fields quan trọng:
- `modelName`: tên model (e.g., "claude-4-opus", "cursor-small")
- `apiKey`: custom API key (undefined nếu dùng Cursor default)
- `openaiApiBaseUrl`: custom OpenAI base URL
- `azureState`: Azure credentials
- `bedrockState`: AWS Bedrock credentials
- `maxMode`: boolean, thinking mode enabled

### 4.2 SubagentComposerService (class `b1o` - tên thay đổi)

Tìm bằng: `"SubagentCreationError"` hoặc `createOrResumeSubagent`

```javascript
class SubagentComposerService {
  constructor(
    _composerDataService,   // e - composer data
    _aiService,             // t - AI service (has getModelDetails)
    _modelConfigService,    // i - model config
    _instantiationService,  // r
    _structuredLogService,  // s
    _metricsService,        // o
    _workspaceContextService, // a
    _pathService            // l
  )
}
```

Methods quan trọng:
- `createOrResumeSubagent(e)` - Tạo/resume subagent composer
- `_runSubagent(e, t, i)` - **BUG LOCATION** cho credentials issue
- `runSubagentWithHandle(e, t)` - Public wrapper cho _runSubagent
- `runWithContinuation(e, t, i)` - Chạy subagent với continuation loop
- `cancelSubagentTree(e)` - Cancel subagent và children

### 4.3 AgentCompatService

Tìm bằng: `"[AgentCompatService]"` hoặc `runAgentLoop`

Methods quan trọng:
- `runAgentLoop(e, t)` - Main agent execution loop
- `buildRequestedModel(e, t)` - Build model request object
- `convertModelDetailsToCredentials(e)` - Convert model details → gRPC credentials
- `resume(e, t)` - Resume interrupted conversation

### 4.4 AI Service

Tìm bằng: `getModelDetailsFromName` hoặc `getModelDetails({specificModelField`

```javascript
getModelDetailsFromName(modelName, maxMode) {
  let apiKey = this._cursorAuthenticationService.getApiKeyForModel(modelName);
  const useApiKey = this._aiSettingsService.getUseApiKeyForModel(modelName);
  const azureState = this._reactiveStorageService.applicationUserPersistentStorage.azureState;
  const bedrockState = this._reactiveStorageService.applicationUserPersistentStorage.bedrockState;

  (!useApiKey || !apiKey) && (apiKey = undefined);

  const serverModelName = this._aiSettingsService.getServerModelName(modelName);
  return new Xf({
    apiKey,
    modelName: serverModelName,
    azureState,
    openaiApiBaseUrl: this._reactiveStorageService.applicationUserPersistentStorage.openAIBaseUrl ?? undefined,
    bedrockState,
    maxMode
  });
}

getModelDetails({specificModelField, composerId} = {}) {
  // Resolves model name from config, then calls getModelDetailsFromName
  // Returns full Xf object with all credentials
}
```

### 4.5 ModelConfigService

Tìm bằng: `getModelConfig(e){this._isMigrating`

```javascript
getModelConfig(feature) {
  // feature: "composer", "cmd-k", "background-composer", "plan-execution"
  let config = this.reactiveStorageService
    .applicationUserPersistentStorage.aiSettings.modelConfig[feature];
  if (!config) {
    config = {
      modelName: this.getDefaultModelForFeature(feature),
      maxMode: feature === "background-composer"
    };
  }
  return {...config};
}
```

### 4.6 Credential Resolution Chain

```javascript
getUseApiKeyForModel(modelName) {
  // Claude model? → check useClaudeKey setting
  // Google model? → check useGoogleKey setting
  // Otherwise → check getUseOpenAIKey() (global OpenAI toggle)
}

getApiKeyForModel(modelName) {
  // Returns stored API key from cursorAuthenticationService
}
```

---

## 5. Subagent Architecture

### 5.1 Subagent Creation Flow

```
Task tool invocation
    │
    ▼
createOrResumeSubagent(e)
    │  e.modelId = "fast" or specific model
    │  e.subagentType = "generalPurpose", "explore", "shell", etc.
    │  e.prompt = task description
    │
    ├── New subagent:
    │   const modelConfig = {...getModelConfig("composer"), modelName: e.modelId};
    │   const composerData = E6(modelConfig, composerId, mode);
    │   composerData.subagentInfo = {
    │     subagentType: TASK,
    │     parentComposerId: parent.composerId,
    │     subagentTypeName: e.subagentType,
    │     toolCallId: e.toolCallId
    │   };
    │   appendSubComposer(composerData);
    │
    └── Resume subagent:
        getComposerHandleById(e.resumeAgentId);
        appendPromptBubbles(handle, {prompt, toolCallId});
```

### 5.2 Subagent Execution Flow (_runSubagent) - **BUG LOCATION**

```javascript
async _runSubagent(e, t, i) {
  const [generationUUID, abortController] = this._aiService.registerNewGeneration({...});

  // ⚠️ BUG: Xf object thiếu apiKey, openaiApiBaseUrl, azureState, bedrockState
  const modelDetails = new Xf({modelName: e.modelId, maxMode: false});

  const conversationState = this._composerDataService.getComposerData(t)?.conversationState;

  // Gọi getAgentStreamResponse → streamFromAgentBackend → runAgentLoop
  // → buildRequestedModel → convertModelDetailsToCredentials
  // → credentials = {case: void 0} vì apiKey undefined!
  await getAgentStreamResponse({
    modelDetails,        // ← THIẾU credentials
    generationUUID,
    composerHandle: t,
    abortController,
    conversationState
  });
}
```

### 5.3 So sánh với Parent Conversation Flow (đúng)

```javascript
// ComposerChatService gọi getModelDetails → getModelDetailsFromName
// → returns Xf object với ĐẦY ĐỦ credentials
const modelDetails = await this.getModelDetails(composerHandle);
// modelDetails.apiKey = "sk-..."
// modelDetails.openaiApiBaseUrl = "https://proxy.example.com/v1"
```

### 5.4 Feature Flags liên quan đến Subagent

```javascript
{
  explore_subagent: {client: true, default: false},
  remove_subagent_soft_limit: {client: false, default: false},
  shell_subagent: {client: true, default: false},
  past_conversation_explorer_subagent: {client: false, default: false},
  debug_subagent: {client: false, default: false},
  enable_build_with_swarm: {client: true, default: false}
}
```

---

## 6. Integrity Check / Checksum System

### product.json checksums

```json
{
  "checksums": {
    "vs/base/parts/sandbox/electron-sandbox/preload.js": "...",
    "vs/workbench/workbench.desktop.main.js": "BdZ/J4Cwb7yEu1jaMrnhc+oQS4mQjvDpuJLqHLL2dbA",
    "vs/workbench/workbench.desktop.main.css": "...",
    "vs/workbench/api/node/extensionHostProcess.js": "...",
    "vs/code/electron-sandbox/workbench/workbench.html": "...",
    "vs/code/electron-sandbox/workbench/workbench.js": "..."
  }
}
```

### Checksum Algorithm

```javascript
crypto.createHash("sha256")
  .update(fileData)
  .digest("base64")
  .replace(/=+$/, "");   // ← STRIP trailing '=' padding
```

### Integrity Check Flow

```javascript
class IntegrityService {
  async _isPure() {
    const checksums = this.productService.checksums || {};  // từ product.json
    await this.lifecycleService.when(4);  // wait for restore phase
    const results = await Promise.all(
      Object.keys(checksums).map(file => this._resolve(file, checksums[file]))
    );
    return { isPure: results.every(r => r.isPure) };
  }

  _showNotification() {
    // "Your {appName} installation appears to be corrupt. Please reinstall."
    // Options: "More Information" | "Don't Show Again"
  }
}
```

**Patch script PHẢI update checksum** sau khi modify workbench file.

---

## 7. Debugging & Searching Techniques

### DO: Dùng Node.js để search file 51MB

```bash
# Đếm occurrences
node -e "
const fs = require('fs');
const data = fs.readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js', 'utf8');
const terms = ['apiKey', 'baseUrl', 'subagent', 'handleTextDelta'];
for (const t of terms) {
  console.log(t + ': ' + (data.split(t).length - 1));
}
"

# Tìm context xung quanh một pattern
node -e "
const fs = require('fs');
const data = fs.readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js', 'utf8');
const pattern = 'targetPattern';
let idx = 0;
while (true) {
  idx = data.indexOf(pattern, idx);
  if (idx === -1) break;
  console.log('pos ' + idx + ':');
  console.log(data.substring(Math.max(0, idx-200), Math.min(data.length, idx+500)));
  console.log('---');
  idx += pattern.length;
}
"

# Verify uniqueness trước khi patch
node -e "
const data = require('fs').readFileSync('...path...', 'utf8');
const pattern = 'your_search_pattern';
let count = 0, idx = 0;
while ((idx = data.indexOf(pattern, idx)) !== -1) { count++; idx++; }
console.log('Occurrences: ' + count);  // MUST be 1 for patching
"
```

### DON'T: grep/rg trên file 51MB

`grep` và `rg` cực kỳ chậm trên file 51MB (>100s). Luôn dùng Node.js.

### Tips tìm code trong minified workbench

1. **Method names được giữ nguyên**: `handleTextDelta`, `_runSubagent`, `getModelDetails`, `convertModelDetailsToCredentials`
2. **Property names được giữ nguyên**: `modelName`, `apiKey`, `openaiApiBaseUrl`, `subagentTypeName`
3. **String literals ổn định**: `"[AgentCompatService]"`, `"SubagentCreationError"`, `"composer"`
4. **Feature flag names ổn định**: `"explore_subagent"`, `"nal_task_tool"`
5. **Protobuf type names ổn định**: `"agent.v1.ThinkingMessage"`, `"aiserver.v1.InferenceThinkingStreamPart"`
6. **Class/variable names THAY ĐỔI mỗi version**: `Xf`, `b1o`, `ALe`, `iTn`, `sTn`, `rTn` → KHÔNG dùng trong search patterns

---

## 8. Patching Guide

### Nguyên tắc tạo patch

1. **Tìm unique pattern**: Pattern PHẢI có đúng 1 occurrence trong file
2. **Dùng stable identifiers**: method names, property names, string literals
3. **KHÔNG dùng minified names**: class names, variable names thay đổi mỗi version
4. **Test replacement**: verify output chứa marker string
5. **Update checksum**: LUÔN update product.json sau khi modify workbench
6. **Backup**: Tạo .backup file trước khi patch
7. **Idempotent**: Check if already patched trước khi apply

### Template patch script

```javascript
const fs = require("fs");
const crypto = require("crypto");

const WORKBENCH_PATH = "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js";
const PRODUCT_PATH = "/Applications/Cursor.app/Contents/Resources/app/product.json";
const WORKBENCH_BACKUP = WORKBENCH_PATH + ".backup";
const PRODUCT_BACKUP = PRODUCT_PATH + ".backup";
const CHECKSUM_KEY = "vs/workbench/workbench.desktop.main.js";

function updateProductChecksum() {
  const fileData = fs.readFileSync(WORKBENCH_PATH);
  const newHash = crypto.createHash("sha256").update(fileData).digest("base64").replace(/=+$/, "");
  const product = JSON.parse(fs.readFileSync(PRODUCT_PATH, "utf8"));
  if (product.checksums?.[CHECKSUM_KEY]) {
    product.checksums[CHECKSUM_KEY] = newHash;
    fs.writeFileSync(PRODUCT_PATH, JSON.stringify(product, null, "\t"));
  }
}

// 1. Check already patched (use unique marker)
// 2. Backup files
// 3. Find unique pattern (verify count === 1)
// 4. Replace with patched code
// 5. Write file
// 6. Update checksum
// 7. Print instructions
```

### Composing multiple patches

- Mỗi patch target vị trí khác nhau trong workbench → không conflict
- Checksum update áp dụng cho trạng thái CUỐI CÙNG sau tất cả patches
- Thứ tự apply không quan trọng (các patches independent)
- Nếu chỉ restore 1 patch → cần re-calculate checksum

---

## 9. Checklist khi Cursor Update

Khi Cursor auto-update, workbench file bị thay thế → tất cả patches mất. Cần:

1. **Xóa backup cũ** (incompatible với version mới):
   ```bash
   rm -f "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js.backup"
   rm -f "/Applications/Cursor.app/Contents/Resources/app/product.json.backup"
   ```

2. **Verify patterns vẫn tồn tại** trong version mới:
   ```bash
   node -e "
   const data = require('fs').readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js', 'utf8');
   const patterns = [
     {name: 'Thinking (handleTextDelta)', p: 'handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()'},
     {name: 'Summarize (Zf constructor)', p: 'f=new Zf({modelName:u.modelConfig?.modelName})'},
   ];
   patterns.forEach(({name, p}) => {
     let count = 0, idx = 0;
     while ((idx = data.indexOf(p, idx)) !== -1) { count++; idx++; }
     console.log(name + ': ' + count + ' occurrence(s) ' + (count === 1 ? 'OK' : 'CHANGED!'));
   });
   "
   ```

3. **Nếu pattern thay đổi**, cần tìm lại:
   - Tìm method name (stable): `handleTextDelta`, `summarizeAction`
   - Xem code xung quanh để tìm pattern mới
   - Verify uniqueness
   - Update patch script

4. **Re-apply patches**:
   ```bash
   cd cursor-thinking-solutions && ./apply-all-patches.sh
   ```

5. **Restart Cursor** (Cmd+Q → reopen)

---

## 10. Các Approach Đã Thử và Kết Quả

| Approach | Kết quả | Lý do |
|----------|---------|-------|
| `reasoning_content` field trong delta | Bị bỏ qua | Cursor server không parse field này cho custom models (0 occurrences trong client) |
| `<think>` tags trong content (không patch) | Hiển thị raw text | Cursor không render `<think>` tags thành UI, chỉ strip ở subtitle |
| `<thinking>` tags | Bị strip bởi `p1o()`/`g1o()` regex | Cursor strip cả `<think>` và `<thinking>` tags (cho conversation history, không ảnh hưởng incoming deltas) |
| Patch v1: `handleTextDelta` detect `<think>` → redirect `handleThinkingDelta` | **WORKS** (partially) | Thinking block 1 work, block 2/3 đôi khi chỉ show "Thinking" animation mà không có text |
| Patch v2: Thêm tag boundary buffering + recursive _after + queueMicrotask | **PARTIAL** | Fix tag split, nested tags, nhưng queueMicrotask gây bail-out khi events batched |
| Patch v3 (Patch A): Bỏ queueMicrotask → synchronous completion | **WORKS** | Fix thinking trong Exploring groups, thinkingDurationMs luôn được set |
| Patch v4 (Patch B): Fix thinking render → show content during loading | **WORKS** (≤2.5.x) | Fix stuck loading. **Cursor 2.6.11+ đã fix natively** → patch không cần |
| Sub-agent inherit parent credentials | Patch riêng (≤2.5.x) | `_runSubagent` tạo Xf object thiếu credentials. **Cursor 2.6.11+ đã fix** |
| Summarize inherit parent credentials (v1) | Patch riêng (≤2.5.x) | `summarize()` tạo Zf object thiếu credentials. Dùng `this.aiService` |
| Summarize inherit parent credentials (v2) | **WORKS** (2.6.11+) | Dùng `cursorAuthenticationService` + `reactiveStorageService` thay `aiService` |

### Chi tiết Patch v2 Fixes

**Fix A - Tag boundary buffering:**
`<think>` hoặc `</think>` có thể bị split across SSE deltas khi Cursor server re-chunk data. Ví dụ: `</thi` ở delta 1 và `nk>` ở delta 2. Patch v2 dùng `__thinkTagState.buf` để buffer partial tags và ghép lại ở delta tiếp theo.

**Fix B - Recursive _after handling:**
Text sau `</think>` giờ route qua `handleTextDelta` (recursive) thay vì `_origHandleTextDelta` (bypass). Cho phép detect multiple `<think>...</think>` blocks trong cùng 1 delta.

**Fix C (v2 → v3) - Synchronous handleThinkingCompleted:**
v2 dùng `queueMicrotask` cho `handleThinkingCompleted`. BUG: khi Cursor server batch nhiều protobuf events
(text-delta với `</think>` + tool-call-started), microtask chạy SAU tool-call-started đã tạo tool call bubble.
`getLastBubble()` trả về tool call bubble → bail out → `thinkingDurationMs` KHÔNG được set → UI hiển thị
"Thinking" mà không có content, thinking block không expandable.

v3 fix: gọi `handleThinkingCompleted` ĐỒNG BỘ ngay sau khi detect `</think>`. `S_()` (SolidJS batch)
là synchronous nên thinking bubble đã tồn tại khi `getLastBubble` check. Không cần defer.

### Chi tiết Patch v4 (Patch B) - Fix thinking render stuck loading

> **NOTE: Patch này KHÔNG CÒN CẦN THIẾT từ Cursor 2.6.11+**
> Cursor đã fix natively: loading branch dùng `N = l = hasContent` trực tiếp.

**Root cause (Cursor ≤2.5.x):** Hàm render thinking block tính `expandable` dựa trên `durationMs`.
Khi thinking đang chạy (`loading=true`), `durationMs` chưa được set (`undefined`):
- `f = !m && (e === void 0 || e <= 0) && r <= 0` → `f = true`
- `v = d || f ? false : s` → `v = false`
- ThinkingBlock render với `expandable={false}, open={false}` → content ẩn!

**Cursor 2.6.11 native fix:**
Loading branch giờ dùng code riêng, không phụ thuộc `durationMs`:
```javascript
if (t) {
  const N = l;        // N = hasContent (true nếu có thinking text)
  const M = N && !B;  // M = hasContent && !userCollapsed
  return <ThinkingBlock expandable={N} open={M} ...>
    {M && n && <MarkdownRenderer isStreaming={true}>{n}</MarkdownRenderer>}
  </ThinkingBlock>;
}
```
Content hiển thị ngay khi có, user có thể collapse/expand tự do.

### Summarize Credentials Bug (Patch C)

**Root cause:** `summarize()` method tạo model details (Zf) chỉ với `modelName`, thiếu `apiKey`/`openaiApiBaseUrl`. Khi Cursor cố summarize chat content, request đi qua Cursor server thay vì custom proxy → `"Slow Pool Error: Claude 4.6 Opus is not currently enabled in the slow pool"`.

**Cursor 2.6.11 changes:**
- Class chứa summarize không còn có `aiService`. Dùng `cursorAuthenticationService` và `reactiveStorageService` thay thế.
- Pattern tách riêng: `f=new Zf({...})` trước, rồi `convertModelDetailsToCredentials(f)` sau.

```javascript
// BUG (trong summarize method - Cursor 2.6.11):
f=new Zf({modelName:u.modelConfig?.modelName})
// → Zf object thiếu apiKey → convertModelDetailsToCredentials(f) trả về {case: void 0}
// → Request đi qua Cursor server → "Slow Pool Error"

// FIX (inject credentials từ available services):
f=new Zf({
  modelName:u.modelConfig?.modelName,
  ...(()=>{
    try {
      const _creds_s = this.reactiveStorageService.applicationUserPersistentStorage;
      return {
        apiKey: this.cursorAuthenticationService.getApiKeyForModel(u.modelConfig?.modelName),
        openaiApiBaseUrl: _creds_s.openAIBaseUrl ?? void 0,
        azureState: _creds_s.azureState,
        bedrockState: _creds_s.bedrockState
      };
    } catch(_e) { return {}; }
  })()
})
```

**Services dùng (available trong class):**
- `this.cursorAuthenticationService.getApiKeyForModel(modelName)` → API key
- `this.reactiveStorageService.applicationUserPersistentStorage` → storage chứa settings

**Lưu ý:** Từ Cursor 2.6.11, `aiService` không còn trong class này. Dùng `cursorAuthenticationService` + `reactiveStorageService` thay thế.

---

## Appendix: Protobuf Type Names (stable across versions)

```
agent.v1.ThinkingMessage
agent.v1.ThinkingDetails
agent.v1.ThinkingDeltaUpdate
agent.v1.ThinkingCompletedUpdate
aiserver.v1.ConversationMessage.Thinking
aiserver.v1.InferenceThinkingStreamPart
aiserver.v1.InferenceStreamResponse
aiserver.v1.StreamUnifiedChatRequest
```
