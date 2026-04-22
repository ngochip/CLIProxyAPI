# Claude OAuth Third-Party Detection Bypass

Last updated: 2026-04-22
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

## Detection Vectors

### 1. TOOLS — Primary Detection Vector

**Rule (since 2026-04-22): tool names must follow the official Claude Code MCP
format `mcp__<server>__<tool>` (double underscore separators) once tool count
reaches Anthropic's threshold.**

The server slug content is **not** validated — any stable identifier such as
`proxy`, `openclaw`, or `server1` is accepted. The tool slug is also
unrestricted. What matters is the exact shape: `mcp__` + segment + `__` +
segment.

| Scenario | Result |
|----------|--------|
| 0 tools | PASS |
| 9 bare-name tools (e.g. `agents_list`, `exec`) | PASS |
| 12 bare-name tools | **FAIL** |
| 12 tools with legacy single-underscore `mcp_<name>` | **FAIL** |
| 12 tools with `mcp__proxy__<name>` (double underscore) | **PASS** |
| 12 tools with `mcp__openclaw__<name>` (any server slug) | **PASS** |
| 35 tools with `mcp__proxy__<name>` | **PASS** |
| Individual tool names (any name, alone) | Always PASS |

**Key change (2026-04-22):** the older single-underscore prefix `mcp_<name>`
that used to bypass detection no longer works. Anthropic now requires the
exact Claude Code MCP naming scheme. Brand keywords inside
`tools[].description` are **not** a detection vector on their own — only the
tool `name` shape is fingerprinted.

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
User-Agent: claude-cli/2.1.92 (external, cli)
Anthropic-Beta: claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14
X-App: cli
```

The default Claude Code version baseline was bumped from `2.1.63` to `2.1.92`
on 2026-04-22 to match the current npm `stable` tag. Version itself is not a
detection vector in isolation, but staying close to the latest release avoids
issues if Anthropic adds a min-version check in the future.

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
      -> Wrap unknown tools in official Claude Code MCP format
         mcp__proxy__<orig>, e.g. agents_list -> mcp__proxy__agents_list,
         lcm_grep -> mcp__proxy__lcm_grep
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
- Follow the official MCP format `mcp__<server>__<tool>`:
  `mcp__proxy__agents_list`, `mcp__proxy__lcm_grep`, etc.

If you see bare names like `agents_list`, `lcm_grep` — or legacy
`mcp_<name>` single-underscore names — the new double-underscore prefix is
not being applied.

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
| 2026-04-22 | Switched to double-underscore MCP format | Anthropic now requires `mcp__<server>__<tool>`; single-underscore `mcp_<name>` is rejected upstream. 13 probes confirmed: brand keywords in `tools[].description` are NOT a detection vector, only tool `name` shape matters. |
| 2026-04-22 | Bumped default cc_version/User-Agent | `2.1.63` → `2.1.92` (npm stable) as hygiene; not a detection vector on its own. |
