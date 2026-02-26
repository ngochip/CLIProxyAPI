# Cursor IDE Patches

Patches cho Cursor IDE để hỗ trợ custom models qua OpenAI API proxy.

**Cursor version tested:** 2.5.17 (2026-02-22)
**OS:** macOS (darwin)

---

## Quick Start

```bash
# Apply tất cả patches
node cursor-thinking-solutions/patch-cursor-thinking.js
node cursor-thinking-solutions/patch-cursor-subagent-credentials.js

# Restart Cursor (Cmd+Q → reopen)

# Restore tất cả patches
node cursor-thinking-solutions/patch-cursor-thinking.js --restore
node cursor-thinking-solutions/patch-cursor-subagent-credentials.js --restore
```

---

## Patches

### 1. Thinking Block Patch (`patch-cursor-thinking.js`)

**Vấn đề:** Custom models gửi thinking content qua `<think>` tags trong `delta.content`, nhưng Cursor không render thành native thinking UI. `reasoning_content` field cũng không được Cursor support (0 occurrences trong client code).

**Giải pháp:** Patch `handleTextDelta` để detect `<think>` tags → redirect sang `handleThinkingDelta` → trigger native collapsible thinking blocks.

**v2 fixes (2026-02-22):** Thinking block thứ 2/3 đôi khi chỉ hiển thị "Thinking" animation mà không hiển thị text content. Root causes và fixes:
- **Fix A - Tag boundary buffering:** `<think>` hoặc `</think>` có thể bị split across SSE deltas khi Cursor server re-chunk protobuf. Patch v2 buffer partial tags và ghép lại ở delta tiếp theo.
- **Fix B - Recursive _after handling:** Text sau `</think>` giờ route qua `handleTextDelta` (thay vì `_origHandleTextDelta`) để detect nested `<think>` tags trong cùng delta.

**v3 fixes (2026-02-26):** Thinking trong Exploring groups không hiển thị content, thẻ Exploring bị mất:
- **Fix C - Synchronous completion:** Bỏ `queueMicrotask`, gọi `handleThinkingCompleted` đồng bộ. Root cause: khi Cursor server batch nhiều protobuf events (text-delta + tool-call-started), microtask chạy SAU tool-call-started → `getLastBubble()` trả về tool call bubble thay vì thinking → bail out → `thinkingDurationMs` không set → UI không expandable. `S_()` (SolidJS batch) là synchronous nên không cần defer.

**Yêu cầu phía proxy:** Stream thinking qua `<think>...</think>` tags trong `delta.content`:
```
data: {"choices":[{"delta":{"content":"<think>\n"}}]}
data: {"choices":[{"delta":{"content":"thinking text..."}}]}
data: {"choices":[{"delta":{"content":"\n</think>\n"}}]}
data: {"choices":[{"delta":{"content":"actual response..."}}]}
```

**Search pattern (unique):**
```
handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()
```

**Chi tiết kỹ thuật:** xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) mục 3 (Internal Event Pipeline).

### 2. Sub-agent Credentials Patch (`patch-cursor-subagent-credentials.js`)

**Vấn đề:** Khi sub-agents (Task tool) được spawn, chúng KHÔNG kế thừa custom OpenAI base URL và API key từ parent conversation. Sub-agents luôn dùng Cursor default API.

**Root cause:** `_runSubagent` tạo model details object thiếu `apiKey` và `openaiApiBaseUrl`:
```javascript
// BUG: thiếu credentials
const u = new Xf({modelName: e.modelId, maxMode: !1});
```

Trong khi parent conversation dùng `getModelDetailsFromName()` trả về đầy đủ credentials.

**Giải pháp:** Inject credentials từ parent model vào Xf constructor bằng spread operator:
```javascript
new Xf({
  modelName: e.modelId, maxMode: !1,
  ...(()=>{
    try {
      const _d = this._aiService.getModelDetails({specificModelField:"composer"});
      return {
        apiKey: _d?.apiKey,
        openaiApiBaseUrl: _d?.openaiApiBaseUrl,
        azureState: _d?.azureState,
        bedrockState: _d?.bedrockState
      };
    } catch(_e) { return {}; }
  })()
})
```

**Search pattern (unique):**
```
modelName:e.modelId,maxMode:!1}
```

**Chi tiết kỹ thuật:** xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) mục 5 (Subagent Architecture).

---

## Khi Cursor Update

Khi Cursor auto-update, workbench file bị thay thế → tất cả patches mất.

### Re-apply workflow

```bash
# 1. Xóa backup cũ (incompatible với version mới)
rm -f "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js.backup"
rm -f "/Applications/Cursor.app/Contents/Resources/app/product.json.backup"

# 2. Verify patterns vẫn tồn tại
node -e "
const data = require('fs').readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js', 'utf8');
const patterns = [
  ['thinking', 'handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()'],
  ['subagent-creds', 'modelName:e.modelId,maxMode:!1}']
];
patterns.forEach(([name, p]) => {
  let c = 0, i = 0;
  while ((i = data.indexOf(p, i)) !== -1) { c++; i++; }
  console.log(name + ': ' + c + ' hit(s) ' + (c === 1 ? '✅' : '❌ CHANGED'));
});
"

# 3. Re-apply patches
node cursor-thinking-solutions/patch-cursor-thinking.js
node cursor-thinking-solutions/patch-cursor-subagent-credentials.js

# 4. Restart Cursor
```

### Nếu pattern thay đổi

Xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) mục 9 (Checklist khi Cursor Update) để debug:

```bash
# Tìm _runSubagent signature mới
node -e "
const d=require('fs').readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js','utf8');
const i=d.indexOf('_runSubagent');
if(i===-1) console.log('_runSubagent NOT FOUND - method may be renamed');
else console.log(d.substring(i, i+800));
"

# Tìm handleTextDelta signature mới
node -e "
const d=require('fs').readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js','utf8');
let p=0;
while(true){p=d.indexOf('handleTextDelta',p);if(p===-1)break;console.log(d.substring(p,p+200));p+=10;}
"
```

---

## Architecture Reference

Xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) cho tài liệu chi tiết về:

- Cursor app file structure
- Client-server gRPC/protobuf protocol
- Internal event pipeline & thinking UI
- Key services & classes (với cách tìm trong minified code)
- Subagent architecture & credentials flow
- Integrity check / checksum system
- Debugging & searching techniques (Node.js, NOT grep)
- Patching guide & template

---

## Files

| File | Mô tả |
|------|--------|
| `README.md` | Index doc (file này) |
| `CURSOR-ARCHITECTURE.md` | Cursor internals reference cho future agents |
| `patch-cursor-thinking.js` | Patch thinking block display |
| `patch-cursor-subagent-credentials.js` | Patch sub-agent custom API credentials |
| `cursor_thinking-solutions.md` | Transcript of original debugging session |
