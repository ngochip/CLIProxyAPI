# Claude 1M Context Configuration

## Overview

Anthropic's Claude API supports 1M token context window through the `context-1m-2025-08-07` beta flag. However, using this feature incurs **extra usage charges** from Anthropic.

This proxy provides a config option to control whether the beta flag is forwarded to Anthropic or stripped to avoid unexpected charges.

## Configuration

Add to your `config.yaml`:

```yaml
claude-header-defaults:
  filter-context-1m: false  # Default: false (forward beta flag as-is)
```

### Options

- **`false` (default)**: Forward `context-1m-2025-08-07` beta flag from client to Anthropic
  - Client explicitly sending the flag will get 1M context (with extra charges)
  - Suitable when you want to allow 1M context for clients that opt-in

- **`true`**: Strip `context-1m-2025-08-07` from all client requests
  - Prevents accidental 1M context usage and extra charges
  - Requests > 200K tokens will be rejected by Anthropic
  - Suitable for cost control in production environments

## Behavior Examples

### Scenario 1: `filter-context-1m: false` (default)

```bash
# Client sends 221K tokens WITH context-1m beta flag
curl http://localhost:8317/v1/messages \
  -H "Anthropic-Beta: context-1m-2025-08-07,oauth-2025-04-20" \
  -d '{"model":"claude-opus-4-6","max_tokens":100,"messages":[...]}'

# ✓ Success: 1M context enabled, extra charges apply
# Response: 221K input tokens
```

```bash
# Client sends 221K tokens WITHOUT context-1m beta flag  
curl http://localhost:8317/v1/messages \
  -H "Anthropic-Beta: oauth-2025-04-20" \
  -d '{"model":"claude-opus-4-6","max_tokens":100,"messages":[...]}'

# ✗ Failure: Anthropic rejects (prompt too long > 200K)
```

### Scenario 2: `filter-context-1m: true`

```bash
# Client sends 221K tokens WITH context-1m beta flag
curl http://localhost:8317/v1/messages \
  -H "Anthropic-Beta: context-1m-2025-08-07,oauth-2025-04-20" \
  -d '{"model":"claude-opus-4-6","max_tokens":100,"messages":[...]}'

# ✗ Failure: Proxy strips context-1m, Anthropic rejects (prompt too long > 200K)
# No extra charges incurred
```

## Cost Implications

According to Anthropic's pricing (as of 2026-02):

- **Standard context (≤200K tokens)**: Regular pricing
- **1M context (200K-1M tokens)**: **Additional charges per token**

Use `filter-context-1m: true` to:
- Prevent accidental 1M context usage in production
- Control costs when proxying requests from multiple clients
- Enforce 200K token limit for all requests

## Testing

Run the test script to verify behavior:

```bash
# Test with filter-context-1m = false (default)
python3 test_filter_context1m.py "YOUR_BEARER_TOKEN"
# Expected: Success with 221K tokens

# Update config.yaml:
# claude-header-defaults:
#   filter-context-1m: true

# Restart server and test again
python3 test_filter_context1m.py "YOUR_BEARER_TOKEN"  
# Expected: Failure "prompt too long > 200K"
```

## Implementation Details

- Config field: `config.ClaudeHeaderDefaults.FilterContext1M` (bool)
- Filter location: `internal/runtime/executor/claude_executor.go:filterExcludedBetas()`
- Applies to both `/v1/messages` and `/v1/chat/completions` endpoints
- Only filters `context-1m-2025-08-07` - other beta flags are unaffected

## Recommendations

- **Development**: Use `filter-context-1m: false` to allow testing 1M context
- **Production**: Use `filter-context-1m: true` to prevent unexpected charges
- **Hybrid**: Use separate proxy instances with different configs for different user groups
