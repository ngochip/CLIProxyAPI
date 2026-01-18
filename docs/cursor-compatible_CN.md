# Cursor 兼容分支

> **分支**: `feature/cursor-compatible`
> **目的**: 修改代码使 Claude 模型与 Cursor 的 OpenAI 兼容层兼容。

## 概述

此分支包含修改，以实现 Claude 模型与 Cursor IDE 通过 OpenAI 兼容 API 格式的无缝集成。该实现确保 Cursor 可以与 Claude 模型通信，同时保留扩展思考等高级功能。

## 主要功能

### 1. 请求/响应转换 (Cursor ↔ CLIProxyAPI)

**位置**: `internal/translator/claude/openai/chat-completions/`

转换器处理 Cursor 的 OpenAI 格式与 Claude 原生格式之间的双向转换：

| 方向 | 文件 |
|------|------|
| 请求: OpenAI → Claude | `claude_openai_request.go` |
| 响应: Claude → OpenAI | `claude_openai_response.go` |

**关键转换:**
- 将 OpenAI `messages` 格式转换为 Claude `messages` 格式
- 映射 `tool_calls` ↔ `tool_use` 块
- 处理流式 SSE 格式差异
- 在多轮对话中保留思考块

### 2. 模型别名支持

**配置**: `config.yaml` → `model-aliases`

允许定义 Cursor 可以使用的替代模型名称：

```yaml
model-aliases:
  # Claude 4.5 别名（替代命名格式）
  claude-4.5-sonnet: "claude-sonnet-4-5-20250929"
  claude-4.5-sonnet-thinking: "claude-sonnet-4-5-20250929(medium)"
  claude-4.5-opus: "claude-opus-4-5-20251101"
  claude-4.5-opus-high-thinking: "claude-opus-4-5-20251101(high)"
```

**实现**:
- `internal/util/thinking_suffix.go` → `SetModelAliases()`, `ResolveModelAlias()`
- `sdk/api/handlers/handlers.go` → `getRequestDetails()` 调用 `util.ResolveModelAlias()`

**流程**:
```
Cursor 请求: "claude-4.5-opus-high-thinking"
       ↓ ResolveModelAlias()
内部: "claude-opus-4-5-20251101(high)"
       ↓ ParseSuffix()
提供商查找: "claude-opus-4-5-20251101" + 思考级别 "high"
```

### 3. 通过 ThinkID 支持思考模式

**问题**: Claude 的扩展思考需要 `signature` 字段用于多轮对话。Cursor（OpenAI 格式）原生不支持此功能。

**解决方案**: 使用 `thinkId` 标记来缓存和恢复带有签名的思考块。

#### 流程图

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         响应流程                                         │
│                     (Claude → Cursor)                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Claude API 响应                                                         │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ {                                                         │           │
│  │   "type": "thinking",                                     │           │
│  │   "thinking": "让我分析一下...",                          │           │
│  │   "signature": "abc123..."                                │           │
│  │ }                                                         │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ 1. 生成 thinkingID = hash(thinkingText)                  │           │
│  │ 2. CacheThinking(thinkingID, thinkingText, signature)    │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  Cursor (OpenAI 格式)                                                    │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ <think>                                                   │           │
│  │ 让我分析一下...                                           │           │
│  │ </think>                                                  │           │
│  │ ```plaintext:thinkId:a1b2c3d4e5f6...```                  │           │
│  │                                                           │           │
│  │ 这是我的回复...                                           │           │
│  └──────────────────────────────────────────────────────────┘           │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                         请求流程                                         │
│                     (Cursor → Claude)                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Cursor 发送下一条消息（包含之前的助手消息）                              │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ <think>                                                   │           │
│  │ 让我分析一下...                                           │           │
│  │ </think>                                                  │           │
│  │ ```plaintext:thinkId:a1b2c3d4e5f6...```                  │           │
│  │                                                           │           │
│  │ 这是我的回复...                                           │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ 1. 从消息中提取 thinkId                                  │           │
│  │ 2. entry = GetCachedThinking(thinkingID)                 │           │
│  │ 3. 使用缓存的签名恢复思考块                              │           │
│  └──────────────────────────────────────────────────────────┘           │
│                              │                                           │
│                              ▼                                           │
│  Claude API 请求                                                         │
│  ┌──────────────────────────────────────────────────────────┐           │
│  │ {                                                         │           │
│  │   "type": "thinking",                                     │           │
│  │   "thinking": "让我分析一下...",                          │           │
│  │   "signature": "abc123..."  ← 从缓存恢复                  │           │
│  │ }                                                         │           │
│  └──────────────────────────────────────────────────────────┘           │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

#### 关键文件

| 文件 | 用途 |
|------|------|
| `internal/cache/signature_cache.go` | 思考 + 签名的缓存存储 |
| `internal/translator/claude/openai/chat-completions/claude_openai_response.go` | 将思考包装在 `<think>` 标签中，附加 thinkId |
| `internal/translator/claude/openai/chat-completions/claude_openai_request.go` | 提取 thinkId，查找缓存，恢复签名 |

#### 缓存函数

```go
// 从思考文本生成基于哈希的 ID
thinkingID := cache.GenerateThinkingID(thinkingText)

// 存储带签名的思考（TTL: 2小时）
cache.CacheThinking(thinkingID, thinkingText, signature)

// 检索缓存的思考
entry := cache.GetCachedThinking(thinkingID)
// entry.ThinkingText, entry.Signature
```

## 相关工具文件

从功能分支恢复的文件（cursor 兼容性所需）：

| 文件 | 用途 |
|------|------|
| `internal/util/thinking_suffix.go` | 模型别名解析，思考后缀解析 |
| `internal/util/claude_thinking.go` | Claude 特定的思考配置助手 |
| `internal/util/gemini_thinking.go` | Gemini 特定的思考配置助手 |
| `internal/util/thinking.go` | 通用思考工具 |
| `internal/util/thinking_text.go` | 思考文本处理 |

## 配置示例

```yaml
# config.yaml

# Cursor 兼容的模型别名
model-aliases:
  claude-4.5-sonnet: "claude-sonnet-4-5-20250929"
  claude-4.5-sonnet-thinking: "claude-sonnet-4-5-20250929(medium)"
  claude-4.5-opus: "claude-opus-4-5-20251101"
  claude-4.5-opus-high-thinking: "claude-opus-4-5-20251101(high)"

# 思考级别: none, low, medium, high, 或数字预算
# 示例: "claude-opus-4-5-20251101(16384)" 表示 16k token 预算
```

## 合并注意事项

将 `main` 合并到 `feature/cursor-compatible` 时，确保：

1. **保留工具文件**: 不要删除 `internal/util/` 中与思考相关的文件
2. **保留 ResolveModelAlias 调用**: 在 `getRequestDetails()` 中，确保调用 `util.ResolveModelAlias()`
3. **维护缓存逻辑**: `internal/cache/signature_cache.go` 中的思考缓存是必需的

## 测试

```bash
# 构建和测试
go build ./...
go test ./sdk/api/handlers/... -v
go test ./internal/... -v
go test ./test/... -v
```

## 故障排除

### 模型别名不起作用
- 检查 `getRequestDetails()` 中是否调用了 `util.ResolveModelAlias()`
- 验证配置加载时是否调用了 `SetModelAliases()`（在 `server.go` 和 `main.go` 中）

### 多轮对话中思考未保留
- 检查缓存 TTL（默认 2 小时）
- 验证 thinkId 标记格式: `` ```plaintext:thinkId:xxx``` ``
- 检查 `GetCachedThinking()` 是否返回有效条目

---

*最后更新: 2026-01-18*
