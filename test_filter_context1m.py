#!/usr/bin/env python3
"""
Test filter-context-1m config option.

Scenario 1: filter-context-1m = false (default)
  - Client gửi context-1m → forward lên Anthropic → 221K tokens OK
  
Scenario 2: filter-context-1m = true  
  - Client gửi context-1m → stripped by proxy → >200K rejected by Anthropic
"""

import json
import sys
import time
import urllib.request
import urllib.error

TARGET_CHARS = 1_250_000
SAMPLE_TEXT = (
    "The quick brown fox jumps over the lazy dog. "
    "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
    "Software engineering involves designing, developing, and maintaining systems. "
    "Artificial intelligence continues to transform how we build applications. "
)

def generate_large_text(target_chars: int) -> str:
    repeats = (target_chars // len(SAMPLE_TEXT)) + 1
    text = SAMPLE_TEXT * repeats
    return text[:target_chars]

def test_with_beta_header(token, label):
    print(f"\n{'='*70}")
    print(f"TEST: {label}")
    print(f"{'='*70}")
    
    large_text = generate_large_text(TARGET_CHARS)
    user_msg = f"Count chars:\n\n{large_text}\n\nHow many?"
    
    payload = json.dumps({
        "model": "claude-opus-4-6",
        "max_tokens": 50,
        "messages": [{"role": "user", "content": [{"type": "text", "text": user_msg}]}]
    })
    
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {token}",
        "Anthropic-Version": "2023-06-01",
        "Anthropic-Beta": "context-1m-2025-08-07,oauth-2025-04-20",
        "Anthropic-Dangerous-Direct-Browser-Access": "true",
        "X-App": "cli",
    }
    
    start = time.time()
    try:
        req = urllib.request.Request(
            "http://localhost:8317/v1/messages?beta=true",
            data=payload.encode("utf-8"),
            headers=headers,
            method="POST"
        )
        resp = urllib.request.urlopen(req, timeout=300)
        resp_body = resp.read().decode("utf-8")
        elapsed = time.time() - start
        
        body = json.loads(resp_body)
        usage = body.get("usage", {})
        input_t = usage.get('input_tokens', 0)
        
        print(f"✓ Status: 200 OK ({elapsed:.1f}s)")
        print(f"  Input tokens: {input_t:,}")
        if input_t > 200_000:
            print(f"  → 1M context is ACTIVE (tokens > 200K)")
        else:
            print(f"  → Using default context (tokens <= 200K)")
        return True
            
    except urllib.error.HTTPError as e:
        elapsed = time.time() - start
        err_body = e.read().decode("utf-8")
        print(f"✗ FAILED - HTTP {e.code} ({elapsed:.1f}s)")
        try:
            err = json.loads(err_body)
            msg = err.get("error", {}).get("message", err_body[:200])
            print(f"  Error: {msg}")
        except:
            print(f"  Error: {err_body[:200]}")
        return False

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 test_filter_context1m.py <bearer_token>")
        sys.exit(1)

    token = sys.argv[1]
    
    print("="*70)
    print("Testing filter-context-1m config option")
    print("="*70)
    print(f"Payload: {TARGET_CHARS:,} chars (~221K tokens)")
    print()
    print("INSTRUCTIONS:")
    print("1. Run this test with filter-context-1m = false (default)")
    print("   → Should succeed with 221K tokens")
    print()
    print("2. Update config.yaml: claude-header-defaults.filter-context-1m = true")
    print("3. Restart server")
    print("4. Run this test again")
    print("   → Should fail with 'prompt too long > 200K'")
    
    test_with_beta_header(token, "Client sends context-1m-2025-08-07 in Anthropic-Beta header")

if __name__ == "__main__":
    main()
