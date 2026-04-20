# Cursor IDE Patches

Patches cho Cursor IDE để hỗ trợ custom models qua OpenAI API proxy.

**Cursor version tested:** 3.2.0+ (2026-04-16)
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

## Patch Status (Cursor 3.2.0+)

| Patch | Trạng thái | Ghi chú |
|-------|-----------|---------| 
| Thinking blocks (`<think>` tags) | **Active** — cần patch | Cursor chưa support natively |
| Smooth streaming (word-by-word) | **Active** — cần patch | Smooth text output, giảm chunky rendering |
| Summarize credentials | **Active** — cần patch | Custom OpenAI creds cho summarization |
| Subagent credentials | **Active** — cần patch | Custom OpenAI creds cho sub-agents |
| BugBot credentials (Agent Review) | **Experimental** | Custom OpenAI creds cho Agent Review |
| OpenAI Key persistence | **Active** — cần patch | Giữ toggle OpenAI Key không bị auto-off |

---

## Patches

### 1. Thinking Block Patch (`patch-cursor-thinking.js`)

**Vấn đề:** Custom models gửi thinking content qua `delta.content`, nhưng Cursor không render thành native thinking UI. `reasoning_content` field cũng không được Cursor support (0 occurrences trong client code).

**Giải pháp:** Patch `handleTextDelta` để detect thinking delimiters → redirect sang `handleThinkingDelta` → trigger native collapsible thinking blocks.

**v8 (2026-04-13) - Cursor 3.2.0 compatibility:**
- Fixed partial delimiter buffering: dùng regex-based partial match thay vì `startsWith` check.
  SSE có thể split `<!--thinking-start:NONCE-->` tại bất kỳ vị trí nào (kể cả giữa nonce).
  Old logic chỉ buffer khi tail ngắn hơn prefix (19 chars), fail khi split trong nonce portion.
- Strip `<!--thinkId:xxx-->` markers khỏi text sau closing delimiter.

**v7 (2026-04-04) - Cursor 3.0.12 compatibility:**
Cursor 3.0+ thay đổi `handleTextDelta` pattern:
- V1 (2.6.x): `this.cancelUnfinishedToolCalls()`
- V2 (3.0+): `this.options.preserveUnfinishedToolsOnNarration||this.cancelUnfinishedToolCalls()`

Patch tự auto-detect version và apply đúng pattern.

**v6 fixes (2026-04-02) - Nonce-based delimiters:**
Thinking content có thể chứa `</think>` literal (code blocks, XML discussion, etc.) → patch v5 detect premature closing tag → thinking bị cắt, phần còn lại tràn ra như text → "thought briefly".

Fix: Proxy gửi nonce-based delimiters thay vì `<think>/<\/think>`:
- Opening: `<!--thinking-start:XXXXXXXX-->` (XXXXXXXX = 8 hex chars random per block)
- Closing: `<!--thinking-end:XXXXXXXX-->` (same nonce)
- Patch chỉ match closing tag có ĐÚNG nonce → không bao giờ false-positive.
- Backward compatible: vẫn detect legacy `<think>/<\/think>`.

**Yêu cầu phía proxy (v6+):** Stream thinking qua nonce-based delimiters trong `delta.content`:
```
data: {"choices":[{"delta":{"content":"<!--thinking-start:a1b2c3d4-->\n"}}]}
data: {"choices":[{"delta":{"content":"thinking text (may contain </think>, code blocks, etc.)..."}}]}
data: {"choices":[{"delta":{"content":"\n<!--thinking-end:a1b2c3d4-->\n"}}]}
data: {"choices":[{"delta":{"content":"actual response..."}}]}
```

**Search pattern (auto-detect):**
```
V2 (3.0+): handleTextDelta(n){if(n.length===0)return;this.options.preserveUnfinishedToolsOnNarration||this.cancelUnfinishedToolCalls()
V1 (2.6.x): handleTextDelta(n){if(n.length===0)return;this.cancelUnfinishedToolCalls()
```

**Chi tiết kỹ thuật:** xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) mục 3 (Internal Event Pipeline).

### 2. Smooth Streaming Patch (`patch-cursor-smooth-streaming.js`)

**Vấn đề:** SSE chunks từ API arrive trong bursts → text hiển thị "chunky" (nhiều chars cùng lúc, rồi pause).

**Giải pháp:** Intercept text trước khi đưa vào SolidJS store, feed qua word streamer (reuse Cursor's existing `vkf` class). Streamer chia text thành ~16 char chunks, emit qua timer → smooth word-by-word output.

Auto-flush khi: tool calls start, thinking blocks start, stream completes.

**v3 (2026-04-16) - Cursor 0.50+ compatibility:**
Cursor 0.50+ đổi store API cuối method `_origHandleTextDelta`:
- V2 (3.0 – 0.49): `e.updateComposerDataSetStore(handle, fn("conversationMap", bubbleId, "text", o))`
- V3 (0.50+): `e.updateComposerBubbleSetStore(handle, bubbleId, fn("text", o))`

Patch giờ thử 4 combinations (2 cancel prefix × 2 store API) và dùng đúng API cho cả patched `_appendTextChunk` method. Thêm debug output khi regex fail.

**v2 (2026-04-04):** Auto-detect minified names thay vì hardcode (`Oa`→`Ua`, `ul`→`Pc`, etc.). Support cả Cursor 2.6.x và 3.0+.

### 3. Summarize Credentials Patch (`patch-cursor-summarize-credentials.js`)

**Vấn đề:** Cursor summarize conversation tạo `ModelDetails` chỉ với `modelName`, thiếu `apiKey`/`openaiApiBaseUrl`. Request đi qua Cursor server thay vì custom proxy → lỗi "AI Model Not Found: Model name is not valid" khi model name là custom (VD: "Opus 4.6 Max Fast (GW)").

**Giải pháp:** Inject credentials từ `cursorAuthenticationService` và `reactiveStorageService` vào `ModelDetails` constructor.

**v2 (2026-04-04):** Auto-detect variable names (`f`→`g`, `u`→`l`) và class names (`Zf`→`Qg`) bằng regex. Support cả Cursor 2.6.x và 3.0+.

### 4. Sub-agent Credentials Patch (`patch-cursor-subagent-credentials.js`)

**Vấn đề:** Sub-agents tạo model config từ `_modelConfigService.getModelConfig("composer")` nhưng thiếu custom API credentials → request đi qua Cursor server thay vì proxy.

**Giải pháp:** Inject credentials từ `_aiService.getModelDetails` vào model config object.

**Lưu ý:** Cursor 3.0+ vẫn dùng pattern V2 (`getModelConfig spread`), patch vẫn tương thích.

### 5. BugBot Credentials Patch (`patch-cursor-bugbot-credentials.js`) — EXPERIMENTAL

**Vấn đề:** Agent Review (BugBot) không dùng custom OpenAI credentials.

**Lưu ý:** Server có thể ignore `model_details` cho BugBot — patch này là experimental.

### 6. OpenAI Key Persistence Patch (`patch-cursor-openai-key-persistence.js`)

**Vấn đề:** Toggle "OpenAI API Key" trong Settings thỉnh thoảng tự switch từ ON → OFF. Cursor có 2 listener (`loginChangedListener`, `subscriptionChangedListener`) tự gọi `setUseOpenAIKey(false)` mỗi khi refresh token hoặc poll membership. Tài khoản PRO/PRO_PLUS/ULTRA sẽ bị auto-off liên tục.

**Giải pháp:** Thay body 2 listener bằng no-op. User vẫn manual toggle on/off bình thường — chỉ chặn auto-off từ auth/subscription refresh.

**Search pattern (auto-detect):**
```
this.loginChangedListener=p=>{(this.cursorAuthenticationService.membershipType()===Ma.PRO||...PRO_PLUS||...ULTRA)&&this.setUseOpenAIKey(!1)}
this.subscriptionChangedListener=p=>{p!==Ma.FREE&&this.setUseOpenAIKey(!1)}
```

---

## Khi Cursor Update

Khi Cursor auto-update, workbench file bị thay thế → tất cả patches mất.

### Re-apply workflow

```bash
# 1. Xóa backup cũ (incompatible với version mới)
rm -f "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js.backup"
rm -f "/Applications/Cursor.app/Contents/Resources/app/product.json.backup"

# 2. Apply patches (tự detect version)
./cursor-thinking-solutions/apply-all-patches.sh

# 3. Check status
./cursor-thinking-solutions/apply-all-patches.sh --status

# 4. Restart Cursor (Cmd+Q → reopen)
```

### Version Compatibility

Tất cả patches tự auto-detect Cursor version:

| Pattern | Cursor 2.6.x | Cursor 3.0 – 0.49 | Cursor 0.50+ |
|---------|-------------|-------------|-------------|
| handleTextDelta cancel | `this.cancelUnfinishedToolCalls()` | `this.options.preserveUnfinishedToolsOnNarration\|\|this.cancelUnfinishedToolCalls()` | same as 3.0 |
| Store API (text update) | `updateComposerDataSetStore` | `updateComposerDataSetStore` | `updateComposerBubbleSetStore` |
| Minified service token | `Oa` | `Ua` | `al` (auto-detected) |
| Minified type enum | `ul` | `Pc` | `Rl` (auto-detected) |
| Summarize vars | `f=new Zf({modelName:u...})` | `g=new Qg({modelName:l...})` | auto-detected |
| Subagent pattern | V2 (getModelConfig spread) | V2 (same pattern) | V2 (same pattern) |

### Nếu pattern thay đổi

Xem [CURSOR-ARCHITECTURE.md](CURSOR-ARCHITECTURE.md) mục 9 (Checklist khi Cursor Update) để debug.

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
| `patch-cursor-smooth-streaming.js` | Patch smooth word-by-word text streaming |
| `patch-cursor-summarize-credentials.js` | Patch summarize dùng custom OpenAI creds |
| `patch-cursor-subagent-credentials.js` | Patch sub-agent dùng custom OpenAI creds |
| `patch-cursor-bugbot-credentials.js` | Patch Agent Review dùng custom OpenAI creds (experimental) |
| `patch-cursor-openai-key-persistence.js` | Patch giữ toggle OpenAI Key không bị auto-off |
| `cursor_thinking-solutions.md` | Transcript of original debugging session |
| `cursor_think_tag_loading_issue.md` | Transcript debugging loading issue |
