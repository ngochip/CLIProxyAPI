# Cursor Compatible Branch

> **Branch**: `feature/cursor-compatible`
> **Purpose**: Modify code to make Claude models compatible with Cursor's OpenAI-compatibility layer.

## Overview

This branch contains modifications to enable seamless integration between Claude models and Cursor IDE through the OpenAI-compatible API format. The implementation ensures that Cursor can communicate with Claude models while preserving advanced features like extended thinking.

## Key Features

### 1. Request/Response Translation (Cursor ↔ CLIProxyAPI)

**Location**: `internal/translator/claude/openai/chat-completions/`

The translator handles bidirectional conversion between Cursor's OpenAI format and Claude's native format:

| Direction | Files |
|-----------|-------|
| Request: OpenAI → Claude | `claude_openai_request.go` |
| Response: Claude → OpenAI | `claude_openai_response.go` |

**Key transformations:**
- Convert OpenAI `messages` format to Claude `messages` format
- Map `tool_calls` ↔ `tool_use` blocks
- Handle streaming SSE format differences
- Preserve thinking blocks in multi-turn conversations

### 2. Model Alias Support

**Configuration**: `config.yaml` → `model-aliases`

Allows defining alternative model names that Cursor can use:

```yaml
model-aliases:
  # Claude 4.5 aliases (alternative naming format)
  claude-4.5-sonnet: "claude-sonnet-4-5-20250929"
  claude-4.5-sonnet-thinking: "claude-sonnet-4-5-20250929(medium)"
  claude-4.5-opus: "claude-opus-4-5-20251101"
  claude-4.5-opus-high-thinking: "claude-opus-4-5-20251101(high)"
```

**Implementation**:
- `internal/util/thinking_suffix.go` → `SetModelAliases()`, `ResolveModelAlias()`
- `sdk/api/handlers/handlers.go` → `getRequestDetails()` calls `util.ResolveModelAlias()`

**Flow**:
```
Cursor request: "claude-4.5-opus-high-thinking"
       ↓ ResolveModelAlias()
Internal: "claude-opus-4-5-20251101(high)"
       ↓ ParseSuffix()
Provider lookup: "claude-opus-4-5-20251101" + thinking level "high"
```

### 3. Thinking Mode Support via ThinkID

**Problem**: Claude's extended thinking requires a `signature` field for multi-turn conversations. Cursor (OpenAI format) doesn't support this natively.

**Solution**: Use `thinkId` markers to cache and restore thinking blocks with their signatures.

#### Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         RESPONSE FLOW                                    │
│                     (Claude → Cursor)                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Claude API Response                                                     │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ {                                                         │           │
│  │   "type": "thinking",                                     │           │
│  │   "thinking": "Let me analyze this...",                   │           │
│  │   "signature": "abc123..."                                │           │
│  │ }                                                         │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ 1. Generate thinkingID = hash(thinkingText)              │           │
│  │ 2. CacheThinking(thinkingID, thinkingText, signature)    │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  Cursor (OpenAI format)                                                  │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ <think>                                                   │           │
│  │ Let me analyze this...                                    │           │
│  │ </think>                                                  │           │
│  │ ```plaintext:thinkId:a1b2c3d4e5f6...```                  │           │
│  │                                                           │           │
│  │ Here is my response...                                    │           │
│  └──────────────────────────────────────────────────────────┘           │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                         REQUEST FLOW                                     │
│                     (Cursor → Claude)                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Cursor sends next message (includes previous assistant message)         │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ <think>                                                   │           │
│  │ Let me analyze this...                                    │           │
│  │ </think>                                                  │           │
│  │ ```plaintext:thinkId:a1b2c3d4e5f6...```                  │           │
│  │                                                           │           │
│  │ Here is my response...                                    │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ 1. Extract thinkId from message                          │           │
│  │ 2. entry = GetCachedThinking(thinkingID)                 │           │
│  │ 3. Restore thinking block with cached signature          │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  Claude API Request                                                      │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ {                                                         │           │
│  │   "type": "thinking",                                     │           │
│  │   "thinking": "Let me analyze this...",                   │           │
│  │   "signature": "abc123..."  ← restored from cache         │           │
│  │ }                                                         │           │
│  └──────────────────────────────────────────────────────────┘           │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

#### Key Files

| File | Purpose |
|------|---------|
| `internal/cache/signature_cache.go` | Cache storage for thinking + signature |
| `internal/translator/claude/openai/chat-completions/claude_openai_response.go` | Wrap thinking in `<think>` tags, append thinkId |
| `internal/translator/claude/openai/chat-completions/claude_openai_request.go` | Extract thinkId, lookup cache, restore signature |

#### Cache Functions

```go
// Generate hash-based ID from thinking text
thinkingID := cache.GenerateThinkingID(thinkingText)

// Store thinking with signature (TTL: 2 hours)
cache.CacheThinking(thinkingID, thinkingText, signature)

// Retrieve cached thinking
entry := cache.GetCachedThinking(thinkingID)
// entry.ThinkingText, entry.Signature
```

## Related Utility Files

Files restored from feature branch (required for cursor compatibility):

| File | Purpose |
|------|---------|
| `internal/util/thinking_suffix.go` | Model alias resolution, thinking suffix parsing |
| `internal/util/claude_thinking.go` | Claude-specific thinking config helpers |
| `internal/util/gemini_thinking.go` | Gemini-specific thinking config helpers |
| `internal/util/thinking.go` | Common thinking utilities |
| `internal/util/thinking_text.go` | Thinking text processing |

## Configuration Example

```yaml
# config.yaml

# Model aliases for Cursor compatibility
model-aliases:
  claude-4.5-sonnet: "claude-sonnet-4-5-20250929"
  claude-4.5-sonnet-thinking: "claude-sonnet-4-5-20250929(medium)"
  claude-4.5-opus: "claude-opus-4-5-20251101"
  claude-4.5-opus-high-thinking: "claude-opus-4-5-20251101(high)"

# Thinking levels: none, low, medium, high, or numeric budget
# Example: "claude-opus-4-5-20251101(16384)" for 16k token budget
```

## Merge Notes

When merging `main` into `feature/cursor-compatible`, ensure:

1. **Keep utility files**: Do NOT delete files in `internal/util/` related to thinking
2. **Preserve ResolveModelAlias call**: In `getRequestDetails()`, ensure `util.ResolveModelAlias()` is called
3. **Maintain cache logic**: The thinking cache in `internal/cache/signature_cache.go` is essential

## Testing

```bash
# Build and test
go build ./...
go test ./sdk/api/handlers/... -v
go test ./internal/... -v
go test ./test/... -v
```

## Troubleshooting

### Model aliases not working
- Check if `util.ResolveModelAlias()` is called in `getRequestDetails()`
- Verify `SetModelAliases()` is called on config load (in `server.go` and `main.go`)

### Thinking not preserved in multi-turn
- Check cache TTL (default 2 hours)
- Verify thinkId marker format: `` ```plaintext:thinkId:xxx``` ``
- Check `GetCachedThinking()` returns valid entry

---

*Last updated: 2026-01-18*
