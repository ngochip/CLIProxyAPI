# Cursor IDE Patches

Patches cho Cursor IDE để hỗ trợ custom models qua OpenAI API proxy.

**Cursor version tested:** 2.6.19 (2026-03-14)
**OS:** macOS (darwin)

---

## Quick Start

```bash
# Apply tất cả patches (tự detect version, skip nếu đã fix natively)
./cursor-thinking-solutions/apply-all-patches.sh

# Check trạng thái
./cursor-thinking-solutions/apply-all-patches.sh --status

# Restore tất cả patches
./cursor-thinking-solutions/apply-all-patches.sh --restore

# Restart Cursor (Cmd+Q → reopen)
```

---

## Patch Status (Cursor 2.6.19)

| Patch | Trạng thái | Ghi chú |
|-------|-----------|---------|
| Thinking blocks (`<think>` tags) | **Active** — cần patch | Cursor chưa support natively |
| Summarize credentials | **Active** — cần patch | Custom OpenAI creds cho summarization |
| Subagent credentials | **Không cần** | Cursor ≥ 2.6.11 đã fix natively |
| Subagent maxMode (thinking) | **Không cần** | Cursor ≥ 2.6.19 đã fix natively |

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

### 2. Summarize Credentials Patch (`patch-cursor-summarize-credentials.js`)

**Vấn đề:** Cursor summarize conversation dùng hardcoded credentials (Cursor API), không dùng user's custom OpenAI credentials. Kết quả: summarization fails khi dùng custom proxy.

**Giải pháp:** Patch `ModelDetails` class để đọc custom OpenAI credentials từ `reactiveStorageService` và dùng cho summarize requests.

### 3. Sub-agent maxMode Patch (`patch-cursor-subagent-maxmode.js`) — DEPRECATED

**Trạng thái:** Không cần từ Cursor ≥ 2.6.19. Script tự detect và skip.

**Vấn đề gốc:** `_runSubagent` hardcode `maxMode:!1` → sub-agents LUÔN chạy không có thinking, dù parent conversation bật thinking mode.

**Cursor 2.6.19 đã fix:** Code mới đọc `modelConfig?.maxMode` từ parent composer:
```javascript
const m = d ? this._composerDataService.getComposerData(d)?.modelConfig?.maxMode ?? !1 : !1;
new Yf({modelName:e.modelId, maxMode:m, ...Qty(e.credentials)});
```

### 4. Sub-agent Credentials Patch (`patch-cursor-subagent-credentials.js`) — DEPRECATED

**Trạng thái:** Không cần từ Cursor ≥ 2.6.11. Giữ lại để reference.

**Vấn đề gốc:** Sub-agents không kế thừa custom OpenAI base URL và API key từ parent conversation.

**Cursor 2.6.11 đã fix:** Credentials giờ được truyền qua `e.credentials` + helper function.

---

## Khi Cursor Update

Khi Cursor auto-update, workbench file bị thay thế → tất cả patches mất.

### Re-apply workflow

```bash
# 1. Xóa backup cũ (incompatible với version mới)
rm -f "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js.backup"
rm -f "/Applications/Cursor.app/Contents/Resources/app/product.json.backup"

# 2. Apply patches (tự detect version, skip deprecated patches)
./cursor-thinking-solutions/apply-all-patches.sh

# 3. Check status
./cursor-thinking-solutions/apply-all-patches.sh --status

# 4. Restart Cursor (Cmd+Q → reopen)
```

### Nếu pattern thay đổi

Xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) mục 9 (Checklist khi Cursor Update) để debug:

```bash
# Tìm handleTextDelta signature mới
node -e "
const d=require('fs').readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js','utf8');
let p=0;
while(true){p=d.indexOf('handleTextDelta',p);if(p===-1)break;console.log(d.substring(p,p+200));p+=10;}
"

# Tìm _runSubagent signature mới
node -e "
const d=require('fs').readFileSync('/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js','utf8');
const i=d.indexOf('_runSubagent');
if(i===-1) console.log('_runSubagent NOT FOUND - method may be renamed');
else console.log(d.substring(i, i+800));
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
| `apply-all-patches.sh` | Script apply/restore/status tất cả patches |
| `check-patch-status.js` | Status checker (dùng bởi apply-all-patches.sh) |
| `patch-cursor-thinking.js` | Patch thinking block display |
| `patch-cursor-summarize-credentials.js` | Patch summarize dùng custom OpenAI creds |
| `patch-cursor-subagent-maxmode.js` | ~~Patch sub-agent maxMode~~ (deprecated, Cursor fixed natively) |
| `patch-cursor-subagent-credentials.js` | ~~Patch sub-agent credentials~~ (deprecated, Cursor fixed natively) |
| `cursor_thinking-solutions.md` | Transcript of original debugging session |
| `cursor_think_tag_loading_issue.md` | Transcript debugging loading issue |
