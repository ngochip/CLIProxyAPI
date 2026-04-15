# Claude OAuth Third-Party Detection Bypass

Last updated: 2026-04-15
Tested against: Claude API (api.anthropic.com), Claude Sonnet 4.6

## Background

Anthropic blocks third-party clients from using Claude OAuth (Pro/Max subscription) tokens.
When detected, requests return HTTP 400 with:

```
"Third-party apps now draw from your extra usage, not your plan limits."
```

or the older variant:

```
"You're out of extra usage. Add more at claude.ai/settings/usage and keep going."
```

The proxy must disguise requests to appear as Claude Code (Anthropic's official CLI).

## Detection Vectors (tested 2026-04-15, 77 API calls)

### 1. TOOLS — Primary Detection Vector

**Rule: Requests with >= 10 tools that lack `mcp_` prefix are blocked.**

| Scenario | Result |
|----------|--------|
| 0 tools | PASS |
| 9 bare-name tools (e.g. `agents_list`, `exec`) | PASS |
| 10 bare-name tools | **FAIL** |
| 37 bare-name tools | **FAIL** |
| 50 generic tools (`tool_0` ... `tool_49`) | PASS |
| 37 tools with `mcp_` prefix (`mcp_AgentsList`) | PASS |
| 20 `mcp_` tools only | PASS |
| Claude Code tools (`Read`, `Write`, `Bash`) + `mcp_` custom | PASS |
| Individual tool names (any name, alone) | Always PASS |

**Key insight:** Generic tool names like `tool_0` pass even at 50 count.
The detection specifically targets tool names that **look like third-party MCP tools
but don't use Claude Code's `mcp_PascalCase` naming convention**.

### 2. System Prompt — NOT a Detection Vector

| Scenario | Result |
|----------|--------|
| Full OpenClaw system prompt (brands, URLs, identity) + 0 tools | PASS |
| Keywords: `OpenClaw`, `mcporter`, `lossless-claw`, `clawhub.ai` | PASS |
| Identity line: `"You are a personal assistant operating inside OpenClaw"` | PASS |
| File paths: `~/.openclaw/workspace/IDENTITY.md` | PASS |
| Generic identity: `"You are a coding assistant"` | PASS |

System prompt content does NOT trigger detection. However, sanitizing it is still
good practice as Anthropic may add prompt-based detection in the future.

### 3. Headers — Required but Already Handled

These headers must be present (proxy already sets them):

```
User-Agent: claude-cli/2.1.63 (external, cli)
Anthropic-Beta: claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14
X-App: cli
```

### 4. Billing Header — Required

The `system[0]` block must contain a valid billing header:

```
x-anthropic-billing-header: cc_version=2.1.87.XXX; cc_entrypoint=sdk-cli; cch=XXXXX;
```

CCH is computed as SHA-256 of the first user message text (first 5 hex chars).
Version suffix uses positions [4, 7, 20] of the message text with salt `59cf53e54c78`.

### 5. CCH Body Signing — Required for OAuth

After billing header injection, the entire body is signed with xxHash64 (seed `0x6E52736AC806831E`).
The `cch=` field in the billing header is replaced with the xxHash64 checksum (first 5 hex chars).

## Proxy Implementation

### Cloaking Pipeline

```
Request arrives
  -> applyCloaking()
      -> checkSystemInstructionsWithSigningMode()
          -> Replace system[] with [billing, identity, static prompt]
          -> Extract original system text
          -> if sanitizePrompt: sanitizeForwardedSystemPrompt()
              Stage 1: Remove identity paragraphs (prefix match)
              Stage 2: Remove anchor URL paragraphs
              Stage 3: Inline text replacements
              Stage 4: Brand keyword scrubbing (regex, case-insensitive)
          -> Prepend sanitized text to first user message as <system-reminder>
      -> injectFakeUserID()
      -> obfuscateSensitiveWords()
  -> applyClaudeToolPrefix() [empty prefix, no-op]
  -> remapOAuthToolNames()
      -> Rename known tools (oauthToolRenameMap): bash->Bash, read->Read, etc.
      -> NEW: Prefix unknown tools with mcp_ + PascalCase
         e.g. agents_list -> mcp_AgentsList, lcm_grep -> mcp_LcmGrep
  -> signAnthropicMessagesBody() [xxHash64 CCH signing]
  -> Send to api.anthropic.com/v1/messages?beta=true
```

### Key Files

| File | Purpose |
|------|---------|
| `internal/runtime/executor/claude_executor.go` | Main cloaking pipeline, tool remapping, system prompt sanitization |
| `internal/runtime/executor/claude_signing.go` | CCH body signing (xxHash64) |
| `internal/runtime/executor/helps/claude_system_prompt.go` | Static Claude Code system prompt sections |
| `internal/runtime/executor/helps/claude_system_sanitize.go` | Brand keyword patterns for prompt sanitization |
| `internal/config/config.go` | `CloakConfig.SanitizePrompt` toggle |

### Config

```yaml
claude-key:
  - api-key: "sk-ant-oat-..."
    cloak:
      mode: auto              # auto | always | never
      sanitize-prompt: true   # nil=auto (OAuth only), true, false
```

## Debugging Checklist

When Anthropic changes detection and requests start failing:

### Step 1: Check the error message

- `"Third-party apps..."` or `"out of extra usage"` = detection triggered
- `"rate limit"` or `"overloaded"` = normal rate limiting, not detection
- `"invalid_api_key"` = token expired/revoked, needs refresh

### Step 2: Enable request logging

Set `log-request: true` in config or use `--log-request` flag.
Check the `=== API REQUEST ===` section in logs for the exact body sent upstream.

### Step 3: Verify tool naming

```bash
# Extract tool names from log
grep -o '"name":"[^"]*"' logs/request-*.log | sort -u
```

All tool names should either:
- Be known Claude Code tools: `Read`, `Write`, `Edit`, `Bash`, `Glob`, `Grep`, etc.
- Have `mcp_` prefix: `mcp_AgentsList`, `mcp_LcmGrep`, etc.

If you see bare names like `agents_list`, `lcm_grep` — the mcp_ prefix is not being applied.

### Step 4: Verify headers

Check for:
- `User-Agent: claude-cli/X.Y.Z`
- `Anthropic-Beta` includes `claude-code-20250219` and `oauth-2025-04-20`
- `Authorization: Bearer sk-ant-oat...` (not x-api-key)

### Step 5: Verify system blocks

`system[0]` must be billing header starting with `x-anthropic-billing-header:`
`system[1]` must be Claude Code identity
`system[2]` must be Claude Code static prompt

### Step 6: Run the test script

```bash
ANTHROPIC_OAUTH_TOKEN=sk-ant-oat-... python3 test/oauth_bypass_test.py
```

This runs progressive tests to identify which vector is being detected.

### Step 7: Check Claude Code version

Anthropic may update the expected `cc_version`. Check the latest Claude Code release:

```bash
npm view @anthropic-ai/claude-code version
```

Update `helps.DefaultClaudeVersion()` and the `USER_AGENT` string if needed.

## Test Script

`test/oauth_bypass_test.py` — Phase 1 comprehensive test with 14 test cases.
Requires `requests` package and `ANTHROPIC_OAUTH_TOKEN` env var.

For more targeted debugging, create additional test scripts following the pattern
in the Phase 2-5 tests (deleted but documented above). Key approaches:

- **Phase 2**: Vary tools while keeping system prompt constant
- **Phase 3**: Test individual tool names in isolation
- **Phase 4**: Binary search for tool count thresholds
- **Phase 5**: Test `mcp_` prefix, Claude Code tool names, and SDK-style mixes

## Version History

| Date | Change | Detection Vector |
|------|--------|-----------------|
| 2026-04-15 | Initial investigation | Tools >= 10 without `mcp_` prefix |
| 2026-04-15 | Added mcp_ auto-prefix | `remapOAuthToolNames()` now prefixes unknown tools |
| 2026-04-15 | Added prompt sanitization | 4-stage system prompt scrubbing (defense in depth) |
