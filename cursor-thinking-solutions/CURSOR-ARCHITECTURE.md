# Cursor IDE Architecture Reference

Tài liệu reference cho AI agents khi cần debug, patch, hoặc extend Cursor IDE.
Cập nhật lần cuối: 2026-04-04, Cursor v3.0.12.

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

### 4.1 Model Details Object (protobuf `aiserver.v1.ModelDetails`)

Tìm bằng: `typeName="aiserver.v1.ModelDetails"` hoặc `model_name.*api_key.*enable_ghost_mode`

```javascript
// Cursor 3.0.12: class Qg (tên minified thay đổi mỗi version)
Qg=class extends re {
  constructor(e) {
    super();
    b.util.initPartial(e, this);
  }
  static typeName = "aiserver.v1.ModelDetails";
  static fields = [
    {no:1, name:"model_name", kind:"scalar", T:9, opt:true},  // modelName
    {no:2, name:"api_key", kind:"scalar", T:9, opt:true},      // apiKey
    {no:3, name:"enable_ghost_mode", kind:"scalar", T:8, opt:true},
    {no:4, name:"azure_state", kind:"message", opt:true},
    {no:5, name:"enable_slow_pool", kind:"scalar", T:8, opt:true},
    {no:6, name:"openai_api_base_url", kind:"scalar", T:9, opt:true},
    {no:7, name:"bedrock_state", kind:"message", opt:true},
    {no:8, name:"max_mode", kind:"scalar", T:8, opt:true},
  ];
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
- `shouldUseRequestedModel({selectedModels, isSubagent})` - Chọn new vs old model path
- `buildRequestedModel(e, t)` - Build `RequestedModel` object (new path, Cursor 3.0+)
- `convertModelDetailsToCredentials(e)` - Convert model details → gRPC credentials
- `resume(e, t)` - Resume interrupted conversation

**Cursor 3.0 Model Selection Flow:**
```javascript
// shouldUseRequestedModel returns true when:
//   useModelParameters === true && selectedModels.length > 0
// Khi true → dùng RequestedModel (new path), model_details = undefined
// Khi false → dùng ModelDetails (old path, E_e)

const ye = this.shouldUseRequestedModel({selectedModels, isSubagent});
const be = ye ? void 0 : new E_e({modelId, maxMode, credentials});  // old path
const Ne = ye ? this.buildRequestedModel(modelDetails, handle) : void 0;  // new path
```

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

Tìm bằng: `getModelConfig` hoặc `getServerModelName`

```javascript
getModelConfig(feature) {
  // feature: "composer", "cmd-k", "background-composer", "plan-execution", "spec", "deep-search", "quick-agent"
  // Returns {modelName, maxMode, selectedModels, ...}
}

getServerModelName(modelName) {
  // Map display name → server name via model catalog
  // "default" → "default"
  // Known model → serverModelName from catalog
  // Unknown model → passthrough (modelName as-is)
}

getAvailableDefaultModels() {
  // Returns model catalog from reactiveStorageService.availableDefaultModels2
  // Fallback: [{name:"default", ...}]
}
```

### 4.6 Model Name Resolution (Cursor 3.0+)

```javascript
// AKy: claude model check
function AKy(n) { return n.startsWith("claude-"); }
// yKy: google model check
function yKy(n) { return n.startsWith("gemini-") && !OHf.has(n); }

// FHf: determine API key provider
function FHf(modelName, storage) {
  return AKy(modelName) ? storage.useClaudeKey ? "anthropic" : void 0
       : yKy(modelName) ? storage.useGoogleKey ? "google" : void 0
       : storage.useOpenAIKey ? "openai" : void 0;
}

// getApiKeyForModel: return stored API key
getApiKeyForModel(modelName) {
  const provider = FHf(modelName, storage);
  switch (provider) {
    case "anthropic": return this.claudeKey();
    case "google": return this.googleKey();
    case "openai": return this.openAIKey();
    default: return provider;  // undefined → no custom key
  }
}
```

**QUAN TRỌNG:** Custom model names (VD: "Opus 4.6 Max Fast (GW)") không startsWith "claude-" hay "gemini-"
→ FHf fallback tới `useOpenAIKey` check → returns "openai" nếu OpenAI key toggle enabled → dùng `openAIKey()`.

### 4.7 Error Classes (Cursor 3.0+)

```javascript
yWe               // Base error class
├── sBi            // Non-retryable error (isRetryable === false)
│   └── XXf        // Unsupported Model error
├── rae            // Connection/stream error (retryable)
├── elt            // Action-required error (login, upgrade, payment, config)
└── NZ             // Cancelled/aborted error
```

Lỗi "AI Model Not Found" đến từ **Cursor backend server** (gRPC):
- Server trả error với `isRetryable: false` + `title: "AI Model Not Found"`
- Client map thành `sBi` (non-retryable error)
- Xảy ra khi model name gửi lên server không nằm trong server's model catalog
- Fix: Đảm bảo request có `apiKeyCredentials` → server forward tới proxy thay vì tự validate

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

2. **Thử apply patches** (scripts tự auto-detect version):
   ```bash
   cd cursor-thinking-solutions && ./apply-all-patches.sh
   ```

3. **Nếu patch fail**, tìm pattern mới:
   - Tìm method name (stable): `handleTextDelta`, `summarizeAction`, `_runSubagent`
   - Xem code xung quanh để tìm pattern mới
   - Verify uniqueness
   - Update patch script regex

4. **Check status**:
   ```bash
   ./apply-all-patches.sh --status
   ```

5. **Restart Cursor** (Cmd+Q → reopen)

### Pattern Evolution Reference

| Component | 2.6.x | 3.0.12 |
|-----------|-------|--------|
| handleTextDelta cancel | `this.cancelUnfinishedToolCalls()` | `this.options.preserveUnfinishedToolsOnNarration\|\|this.cancelUnfinishedToolCalls()` |
| Composer service token | `Oa` | `Ua` |
| Bubble type enum | `ul` | `Pc` |
| Helper fn (default bubble) | `h_()` | `jw()` |
| Batch fn (SolidJS batch) | `Jw()` | `Hy()` |
| ModelDetails class | `Zf` | `Qg` |
| Summarize pattern | `f=new Zf({modelName:u...})` | `g=new Qg({modelName:l...})` |
| RequestedModel class | N/A | `Mj` (agent.v1.RequestedModel) |
| ModelDetails proto (agent) | N/A | `E_e` (agent.v1.ModelDetails) |

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
| Patch v5: `<think>`/`</think>` tags (v3 logic) | **PARTIAL** | Thinking content chứa `</think>` literal (code blocks, XML) → premature closing → "thought briefly" |
| Patch v6: Nonce-based delimiters `<!--thinking-start/end:NONCE-->` | **WORKS** | Proxy gửi unique nonce per block → closing tag match exact nonce → no false positives |
| Sub-agent inherit parent credentials | Patch riêng (≤2.5.x) | `_runSubagent` tạo Xf object thiếu credentials. **Cursor 2.6.11+ đã fix** |
| Summarize inherit parent credentials (v1) | Patch riêng (≤2.5.x) | `summarize()` tạo Zf object thiếu credentials. Dùng `this.aiService` |
| Summarize inherit parent credentials (v2) | **WORKS** (2.6.11+) | Dùng `cursorAuthenticationService` + `reactiveStorageService` thay `aiService` |

### Chi tiết Patch v6 - Nonce-based delimiters

**Root cause (v5 bug):** Proxy wrap thinking content bằng `<think>...</think>` tags. Patch dùng `indexOf("</think>")` để detect closing tag. Nhưng Claude thinking content có thể chứa `</think>` literal (code blocks, XML discussion, etc.) → premature closing → thinking bị cắt, phần còn lại tràn ra như text → "thought briefly".

**Fix:** Proxy gửi nonce-based delimiters:
- Opening: `<!--thinking-start:XXXXXXXX-->` (XXXXXXXX = 8 hex chars random)
- Closing: `<!--thinking-end:XXXXXXXX-->` (same nonce)
- Mỗi thinking block có nonce RIÊNG, tạo bởi `crypto/rand`
- Patch chỉ match closing tag có ĐÚNG nonce → không false-positive

**Backward compatibility:** Patch vẫn detect legacy `<think>/<\/think>` format (nonce = '' → naive indexOf match).

**Proxy changes (`claude_openai_response.go`):**
- `ThinkingAccumulator` có thêm field `Nonce string`
- `content_block_start` (thinking): gửi `<!--thinking-start:NONCE-->\n`
- `content_block_stop` (thinking): gửi `\n<!--thinking-end:NONCE-->\n<!--thinkId:ID-->\n`
- `generateThinkingNonce()`: 4 random bytes → 8 hex chars

**Patch changes (`patch-cursor-thinking.js`):**
- State: `__thinkTagState = {active, startTime, buf, nonce}`
- Opening: regex `/<!--thinking-start:([0-9a-f]{8})-->/` → capture nonce
- Closing: `indexOf("<!--thinking-end:" + nonce + "-->")` → exact match
- Buffer: partial delimiter ở cuối text (max 30 chars cho opening, `endTag.length` cho closing)

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

**Root cause:** `summarize()` method tạo model details (Qg/Zf) chỉ với `modelName`, thiếu `apiKey`/`openaiApiBaseUrl`. Khi Cursor cố summarize chat content, request đi qua Cursor server thay vì custom proxy.

**Error messages:**
- Cursor 2.6.x: `"Slow Pool Error: Claude 4.6 Opus is not currently enabled in the slow pool"`
- Cursor 3.0+: `"AI Model Not Found: Model name is not valid: <custom model name>"`

**Cursor 3.0.12 flow:**
```javascript
// summarize() method:
g=new Qg({modelName:l.modelConfig?.modelName})  // ← chỉ có modelName
f=this.shouldUseRequestedModel({selectedModels:l.modelConfig?.selectedModels,isSubagent:!1})
v=f ? void 0 : new E_e({modelId:..., credentials:this.convertModelDetailsToCredentials(g)})
// → g thiếu apiKey → credentials = {case: void 0} → Cursor server validate model name → FAIL

// FIX (inject credentials):
g=new Qg({
  modelName:l.modelConfig?.modelName,
  ...(()=>{
    try {
      const _creds_s = this.reactiveStorageService.applicationUserPersistentStorage;
      return {
        apiKey: this.cursorAuthenticationService.getApiKeyForModel(l.modelConfig?.modelName),
        openaiApiBaseUrl: _creds_s.openAIBaseUrl ?? void 0,
        azureState: _creds_s.azureState,
        bedrockState: _creds_s.bedrockState
      };
    } catch(_e) { return {}; }
  })()
})
```

**Auto-detect (patch script dùng regex):**
- Pattern: `<var>=new <ClassName>({modelName:<src>.modelConfig?.modelName})`
- Cursor 2.6.x: `f=new Zf({modelName:u.modelConfig?.modelName})`
- Cursor 3.0.12: `g=new Qg({modelName:l.modelConfig?.modelName})`

**Services dùng (available trong class):**
- `this.cursorAuthenticationService.getApiKeyForModel(modelName)` → API key
- `this.reactiveStorageService.applicationUserPersistentStorage` → storage chứa settings

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
