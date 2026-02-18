# Cursor Thinking Block Patch

Patch Cursor IDE để hiển thị thinking content từ custom models (qua OpenAI API proxy) dưới dạng native thinking blocks (collapsible "Thought for Xs").

## Vấn đề

Custom models giao tiếp với Cursor qua OpenAI Chat Completions API. Cursor không có cách nào hiển thị thinking/reasoning content từ custom models vì:

1. **`reasoning_content` field**: 0 occurrences trong client code - Cursor server strip trường này
2. **`<think>` tags trong content**: Cursor strip hoặc hiển thị raw text, không render thành thinking UI
3. **Native thinking**: Chỉ hoạt động qua internal gRPC protobuf protocol riêng của Cursor

## Giải pháp

### Tổng quan

Patch gồm 2 phần:

1. **Proxy server** (Go): Stream thinking content qua `<think>...</think>` tags trong `delta.content` field của OpenAI SSE response
2. **Cursor client patch** (Node.js): Intercept `handleTextDelta` để detect `<think>` tags và redirect sang `handleThinkingDelta` - trigger native thinking UI

### Flow

```
Claude API ──thinking_delta──► Proxy Server ──<think>tags in content──► Cursor Server ──protobuf──► Cursor Client
                                                                                                        │
                                                                                              handleTextDelta (patched)
                                                                                                        │
                                                                                              detect <think> tag
                                                                                                        │
                                                                                              handleThinkingDelta
                                                                                                        │
                                                                                              Native Thinking UI ✅
```

## Sử dụng

```bash
# Apply patch
node cursor-thinking-solutions/patch-cursor-thinking.js

# Restore bản gốc
node cursor-thinking-solutions/patch-cursor-thinking.js --restore
```

Sau khi patch/restore, cần restart Cursor (Cmd+Q → reopen).

---

## Chi tiết kỹ thuật

### 1. Cursor Architecture cho Thinking

#### Protobuf Messages

Cursor dùng gRPC/protobuf cho communication giữa client ↔ server.

```protobuf
// Response stream từ server → client
message InferenceStreamResponse {
  oneof response {
    InferenceTextStreamPart text_part = 1;         // {text, is_final}
    InferenceToolCallStreamPart tool_call_part = 2; // {tool_call_id, tool_name, args, is_complete}
    InferenceUsageInfo usage = 3;
    InferenceResponseInfo response_info = 4;
    InferenceExtendedUsageInfo extended_usage = 5;
    InferenceProviderMetadataInfo provider_metadata = 6;
    InferenceInvocationIdInfo invocation_id = 7;
    InferenceStreamError error = 8;
    InferenceThinkingStreamPart thinking_part = 9;  // {text, signature?, is_final}
  }
}

// Thinking level trong request
// Field 49 của StreamUnifiedChatRequest
enum ThinkingLevel {
  THINKING_LEVEL_UNSPECIFIED = 0;
  THINKING_LEVEL_MEDIUM = 1;
  THINKING_LEVEL_HIGH = 2;
}
```

#### Internal Event Pipeline

```
Protobuf message  ──►  i3t() converter  ──►  Internal events  ──►  Handler methods
                                                                         │
textDelta         ──►  {type:"text-delta", text}        ──►  handleTextDelta()
thinkingDelta     ──►  {type:"thinking-delta", text}    ──►  handleThinkingDelta()
thinkingCompleted ──►  {type:"thinking-completed", ms}  ──►  handleThinkingCompleted()
```

#### Custom Model Flow

Client gửi `apiKeyCredentials` (apiKey + baseUrl) lên Cursor server qua gRPC:

```
Cursor Client ──gRPC(apiKeyCredentials)──► Cursor Server ──HTTP(OpenAI SSE)──► Custom Proxy
                                                │
                                    Server parse SSE response
                                    Convert to InferenceStreamResponse protobuf
                                    (KHÔNG có thinking_part cho custom models)
                                                │
                                          ◄──gRPC──
```

### 2. Files quan trọng trong Cursor App

| File | Kích thước | Vai trò |
|------|-----------|---------|
| `.../workbench.desktop.main.js` | ~50MB | Client-side workbench (UI, handlers) |
| `.../product.json` | ~5KB | Metadata + checksums |
| `.../sharedProcess/sharedProcessMain.js` | ~1MB | Shared process (checksum service) |
| `.../nls.messages.json` | ~500KB | Localization strings |

Paths:
```
/Applications/Cursor.app/Contents/Resources/app/
├── product.json                                    ← checksums stored here
└── out/
    └── vs/
        ├── workbench/
        │   └── workbench.desktop.main.js           ← PATCH TARGET
        └── code/
            └── electron-utility/
                └── sharedProcess/
                    └── sharedProcessMain.js         ← checksum verification service
```

### 3. Patch Target: `handleTextDelta`

#### Vị trí trong workbench.desktop.main.js

```javascript
// ORIGINAL (1 occurrence, unique string)
handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()
```

Tìm bằng: `data.indexOf('handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()')`

#### Patched version

```javascript
handleTextDelta(n){
  if(n.length===0) return;
  
  // Init state tracker per instance
  if(!this.__thinkTagState) this.__thinkTagState = {active:false, startTime:0};
  const _s = this.__thinkTagState;
  let _txt = n;
  
  // Phase 1: Not in thinking mode - check for <think> opening tag
  if(!_s.active){
    const _oi = _txt.indexOf("<think>");
    if(_oi !== -1){
      // Text before <think> goes to normal text handler
      const _before = _txt.substring(0, _oi);
      if(_before.length > 0) this._origHandleTextDelta(_before);
      
      // Switch to thinking mode
      _s.active = true;
      _s.startTime = Date.now();
      _txt = _txt.substring(_oi + 7); // skip "<think>"
      if(_txt.length === 0) return;
    }
  }
  
  // Phase 2: In thinking mode - redirect to thinking handler
  if(_s.active){
    const _ci = _txt.indexOf("</think>");
    if(_ci !== -1){
      // Remaining thinking content
      const _thinkPart = _txt.substring(0, _ci);
      if(_thinkPart.length > 0) this.handleThinkingDelta(_thinkPart);
      
      // Complete thinking with real duration (minimum 1s for UI to show expanded)
      const _dur = Math.max(1000, Date.now() - _s.startTime);
      this.handleThinkingCompleted({thinkingDurationMs: _dur});
      _s.active = false;
      _s.startTime = 0;
      
      // Text after </think> goes to normal text handler
      const _after = _txt.substring(_ci + 8); // skip "</think>"
      if(_after.length > 0) this._origHandleTextDelta(_after);
      return;
    }
    // Still thinking - send all to thinking handler
    this.handleThinkingDelta(_txt);
    return;
  }
  
  // Phase 3: Normal text - pass through to original handler
  this._origHandleTextDelta(_txt);
}

// Original method renamed
_origHandleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()...
```

### 4. Handler Methods (reference)

#### `handleThinkingDelta(text, thinkingStyle?)`

Tạo thinking bubble (nếu chưa có) với `capabilityType: mo.THINKING`, dùng `thinkingWordStreamer` để stream text.

```javascript
// Simplified from source
handleThinkingDelta(n, e) {
  // Create thinking bubble if not exists
  if (lastBubble?.capabilityType !== mo.THINKING) {
    const bubble = {...FS(), type: al.AI, text: "", capabilityType: mo.THINKING};
    appendComposerBubbles(handle, [bubble]);
  }
  // Stream thinking text
  this.thinkingWordStreamer.enqueue(n);
}
```

#### `handleThinkingCompleted({thinkingDurationMs})`

Flush word streamer, set duration, remove from generating list.

```javascript
handleThinkingCompleted(n) {
  this.thinkingWordStreamer?.flush();
  // Set duration for "Thought for Xs" display
  updateComposerDataSetStore(handle, s => s("conversationMap", bubbleId, "thinkingDurationMs", n.thinkingDurationMs));
  // Remove from generating list (stops loading animation)
  updateComposerDataSetStore(handle, s => s("generatingBubbleIds", []));
}
```

### 5. Thinking UI Collapse Logic

```javascript
// Thinking block render decision
function dnc({thinking, durationMs, loading}) {
  const seconds = durationMs ? Math.round(durationMs / 1000) : 0;
  const hasContent = !!(thinking && thinking.trim().length > 0);
  
  // f = true → collapsed, NOT expandable
  const f = (durationMs === undefined || durationMs <= 0) && seconds <= 0;
  
  // v = expandable (user can click to expand)
  const v = f ? false : hasContent;
  
  // When loading: open=v (expanded while streaming)
  // When done: defaultOpen=false (collapsed, but expandable if v=true)
  return loading
    ? <ThinkingBlock action="Thinking" expandable={v} open={v} loading={true} />
    : <ThinkingBlock action="Thought" details={durationText} expandable={v} defaultOpen={false} />;
}
```

**Quan trọng:** `thinkingDurationMs` PHẢI > 0 để thinking block expandable. Nếu = 0 → "Thought briefly" và KHÔNG expandable.

Duration text logic:
```javascript
function unc({seconds, durationMs, parseHeaders, headerTitle}) {
  if (parseHeaders && headerTitle.length > 0 && durationMs > 0)
    return `${(durationMs / 1000).toFixed(1)}s`;
  if (durationMs > 0 && seconds === 0)
    return `for ${(durationMs / 1000).toFixed(1)}s`;
  if (seconds > 0)
    return `for ${seconds}s`;
  return "briefly";
}
```

### 6. Integrity Check / Checksum

#### Checksum Service (sharedProcessMain.js)

```javascript
// Checksum service tính SHA-256, base64, STRIP trailing '='
async checksum(uri) {
  const stream = (await this.fileService.readFileStream(uri)).value;
  return new Promise((resolve, reject) => {
    const hash = createHash("sha256");
    ba(stream, {
      onData: chunk => hash.update(chunk.buffer),
      onError: err => reject(err),
      onEnd: () => resolve(hash.digest("base64").replace(/=+$/, ""))  // ← STRIP PADDING
    });
  });
}
```

**QUAN TRỌNG:** Checksum dùng `digest("base64").replace(/=+$/, "")` - strip trailing `=` padding.

#### Integrity Check Flow (workbench)

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
  
  async _resolve(file, expectedChecksum) {
    const uri = asFileUri(file);
    const actualChecksum = await this.checksumService.checksum(uri);
    return { uri, actual: actualChecksum, expected: expectedChecksum, isPure: actual === expected };
  }
  
  _showNotification() {
    // "Your {appName} installation appears to be corrupt. Please reinstall."
    // Options: "More Information" | "Don't Show Again"
  }
}
```

#### product.json checksums

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

Patch script phải update checksum SAU KHI modify workbench file, dùng cùng algorithm:
```javascript
crypto.createHash("sha256").update(fileData).digest("base64").replace(/=+$/, "")
```

### 7. Proxy Server Side (Go)

Thinking content stream qua `delta.content` với `<think>` tags:

#### content_block_start (thinking)
```go
// Gửi opening tag
template, _ = sjson.Set(template, "choices.0.delta.content", "<think>\n")
```

#### thinking_delta (handleContentBlockDelta)
```go
// Stream thinking text qua content field (cùng deltaPrefix với text_delta)
if p.deltaPrefix != "" {
    return []string{p.deltaPrefix + thinking.Raw + p.deltaSuffix}
}
```

#### content_block_stop (thinking)
```go
// Gửi closing tag + thinkId marker
closingContent := "\n</think>\n```plaintext:thinkId:" + thinkingID + "```\n"
template, _ = sjson.Set(template, "choices.0.delta.content", closingContent)
```

SSE output format:
```
data: {"choices":[{"delta":{"content":"<think>\n"}}]}
data: {"choices":[{"delta":{"content":"thinking text..."}}]}
data: {"choices":[{"delta":{"content":"more thinking..."}}]}
data: {"choices":[{"delta":{"content":"\n</think>\n```plaintext:thinkId:abc123```\n"}}]}
data: {"choices":[{"delta":{"content":"actual response text..."}}]}
```

---

## Troubleshooting: Khi Cursor Update

Khi Cursor auto-update, file workbench bị thay thế → patch mất. Cần re-apply:

```bash
# Xóa backup cũ (incompatible với version mới)
rm -f "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js.backup"
rm -f "/Applications/Cursor.app/Contents/Resources/app/product.json.backup"

# Re-apply patch
node cursor-thinking-solutions/patch-cursor-thinking.js
```

### Nếu patch fail sau update

Nguyên nhân có thể:
1. **`handleTextDelta` signature thay đổi** → Cần tìm lại signature mới
2. **`handleThinkingDelta`/`handleThinkingCompleted` thay đổi** → Cần verify API

#### Cách debug

```bash
# Tìm handleTextDelta signature mới
node -e "
const fs = require('fs');
const data = fs.readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js', 'utf8');
const matches = [];
let p = 0;
while (true) {
  p = data.indexOf('handleTextDelta', p);
  if (p === -1) break;
  matches.push(data.substring(p, p + 200));
  p += 10;
}
matches.forEach((m, i) => console.log(i + ':', m));
"

# Verify handleThinkingDelta vẫn tồn tại
node -e "
const fs = require('fs');
const data = fs.readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js', 'utf8');
console.log('handleThinkingDelta exists:', data.includes('handleThinkingDelta'));
console.log('handleThinkingCompleted exists:', data.includes('handleThinkingCompleted'));
console.log('thinkingDurationMs exists:', data.includes('thinkingDurationMs'));
"

# Verify checksum format
node -e "
const fs = require('fs');
const data = fs.readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/code/electron-utility/sharedProcess/sharedProcessMain.js', 'utf8');
const idx = data.indexOf('checksum(');
console.log(data.substring(Math.max(0,idx-50), Math.min(data.length, idx+300)));
"
```

## Các approach đã thử và fail

| Approach | Kết quả | Lý do |
|----------|---------|-------|
| `reasoning_content` field trong delta | Bị strip | Cursor server không parse field này cho custom models |
| `<think>` tags trong content (không patch) | Hiển thị raw text | Markdown renderer không xử lý HTML tags |
| `<thinking>` tags | Bị strip bởi `h1o()` regex | Cursor strip cả `<think>` và `<thinking>` ở subtitle |

## Tested on

- **Cursor version:** 2.5.17 (commit 7b98dcb824ea96c9c62362a5e80dbf0d1aae4770)
- **OS:** macOS (darwin)
- **Patch date:** 2026-02-18
