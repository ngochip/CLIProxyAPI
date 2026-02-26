#!/usr/bin/env python3
"""Test 1M context: so sánh /v1/messages vs /v1/chat/completions."""

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

def test_endpoint(url, headers, payload_json, label):
    print(f"\n{'='*60}")
    print(f"TEST: {label}")
    print(f"URL: {url}")
    print(f"{'='*60}")
    
    start = time.time()
    try:
        req = urllib.request.Request(url, data=payload_json.encode("utf-8"), headers=headers, method="POST")
        resp = urllib.request.urlopen(req, timeout=300)
        resp_body = resp.read().decode("utf-8")
        elapsed = time.time() - start
        
        print(f"Status: {resp.status}")
        print(f"Time: {elapsed:.1f}s")
        
        body = json.loads(resp_body)
        if "usage" in body:
            usage = body["usage"]
            input_t = usage.get('input_tokens', usage.get('prompt_tokens', 0))
            output_t = usage.get('output_tokens', usage.get('completion_tokens', 0))
            cache_c = usage.get('cache_creation_input_tokens', 0)
            cache_r = usage.get('cache_read_input_tokens', 0)
            total = input_t + cache_c + cache_r
            print(f"Input tokens: {total:,} (direct:{input_t:,} cache_create:{cache_c:,} cache_read:{cache_r:,})")
            print(f"Output tokens: {output_t:,}")
        
        if "content" in body:
            for block in body["content"]:
                if block.get("type") == "text":
                    print(f"Response: {block['text'][:200]}")
        if "choices" in body and body["choices"]:
            msg = body["choices"][0].get("message", {}).get("content", "")
            print(f"Response: {msg[:200]}")
        if "error" in body:
            print(f"Error: {json.dumps(body['error'], indent=2)}")
        return True
            
    except urllib.error.HTTPError as e:
        elapsed = time.time() - start
        err_body = e.read().decode("utf-8")
        print(f"FAILED - HTTP {e.code} ({elapsed:.1f}s)")
        print(f"Error: {err_body[:500]}")
        return False
    except Exception as e:
        print(f"FAILED: {e}")
        return False

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 test_1m_context.py <bearer_token>")
        sys.exit(1)

    token = sys.argv[1]
    
    print(f"Generating {TARGET_CHARS:,} chars of text...")
    large_text = generate_large_text(TARGET_CHARS)
    user_msg = f"Count the approximate characters in this document:\n\n{large_text}\n\nHow many characters approximately?"

    # Test 1: /v1/messages (Claude native)
    msg_payload = json.dumps({
        "model": "claude-opus-4-6",
        "max_tokens": 100,
        "messages": [{"role": "user", "content": [{"type": "text", "text": user_msg}]}]
    })
    msg_headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {token}",
        "Anthropic-Version": "2023-06-01",
        "Anthropic-Beta": "context-1m-2025-08-07,oauth-2025-04-20,prompt-caching-2024-07-31",
        "Anthropic-Dangerous-Direct-Browser-Access": "true",
        "X-App": "cli",
    }
    test_endpoint("http://localhost:8317/v1/messages?beta=true", msg_headers, msg_payload, "/v1/messages - claude-opus-4-6")

    # Test 2: /v1/chat/completions (OpenAI format) - same model
    chat_payload = json.dumps({
        "model": "claude-opus-4-6",
        "max_tokens": 100,
        "stream": False,
        "messages": [{"role": "user", "content": user_msg}]
    })
    chat_headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {token}",
        "Anthropic-Beta": "context-1m-2025-08-07,oauth-2025-04-20,prompt-caching-2024-07-31",
    }
    test_endpoint("http://localhost:8317/v1/chat/completions", chat_headers, chat_payload, "/v1/chat/completions - claude-opus-4-6")

    # Test 3: /v1/chat/completions WITHOUT Anthropic-Beta header
    chat_headers_no_beta = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {token}",
    }
    test_endpoint("http://localhost:8317/v1/chat/completions", chat_headers_no_beta, chat_payload, "/v1/chat/completions - NO Anthropic-Beta header")

if __name__ == "__main__":
    main()
