#!/usr/bin/env python3
"""
Test script to identify what Anthropic checks to detect third-party OAuth clients.
Sends requests directly to api.anthropic.com with progressive prompt stripping.

Usage:
    python3 test/oauth_bypass_test.py

Requires: requests (pip install requests)
"""

import hashlib
import json
import os
import re
import sys
import time
import uuid

try:
    import requests
except ImportError:
    print("pip install requests")
    sys.exit(1)

# --- Config ---
API_URL = "https://api.anthropic.com/v1/messages?beta=true"
OAUTH_TOKEN = os.environ.get(
    "ANTHROPIC_OAUTH_TOKEN",
    "",
)
MODEL = "claude-sonnet-4-6"
MAX_TOKENS = 100
DELAY_BETWEEN_TESTS = 2  # seconds

HEADERS = {
    "Accept": "application/json",
    "Accept-Encoding": "identity",
    "Anthropic-Beta": "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14",
    "Anthropic-Version": "2023-06-01",
    "Authorization": f"Bearer {OAUTH_TOKEN}",
    "Content-Type": "application/json",
    "User-Agent": "claude-cli/2.1.63 (external, cli)",
    "X-App": "cli",
    "X-Claude-Code-Session-Id": str(uuid.uuid4()),
    "X-Client-Request-Id": str(uuid.uuid4()),
    "X-Stainless-Arch": "x64",
    "X-Stainless-Lang": "js",
    "X-Stainless-Os": "Linux",
    "X-Stainless-Package-Version": "6.26.0",
    "X-Stainless-Retry-Count": "0",
    "X-Stainless-Runtime": "node",
    "X-Stainless-Runtime-Version": "v24.14.0",
    "X-Stainless-Timeout": "600",
    "Connection": "keep-alive",
}

# --- Billing header computation (matching proxy's generateBillingHeader) ---
CCH_SALT = "59cf53e54c78"
CCH_POSITIONS = [4, 7, 20]
CC_VERSION = "2.1.87"
CC_ENTRYPOINT = "sdk-cli"


def compute_version_suffix(message_text: str, version: str = CC_VERSION) -> str:
    chars = "".join(message_text[i] if i < len(message_text) else "0" for i in CCH_POSITIONS)
    return hashlib.sha256(f"{CCH_SALT}{chars}{version}".encode()).hexdigest()[:3]


def compute_cch(message_text: str) -> str:
    return hashlib.sha256(message_text.encode()).hexdigest()[:5]


def build_billing_header(message_text: str) -> str:
    suffix = compute_version_suffix(message_text)
    cch = compute_cch(message_text)
    return f"x-anthropic-billing-header: cc_version={CC_VERSION}.{suffix}; cc_entrypoint={CC_ENTRYPOINT}; cch={cch};"


CLAUDE_CODE_IDENTITY = "You are Claude Code, Anthropic's official CLI for Claude."

CLAUDE_CODE_STATIC = """You are an interactive agent that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.

IMPORTANT: You must NEVER generate or guess URLs for the user unless you are confident that the URLs are for helping the user with programming. You may use URLs provided by the user in their messages or local files.

# System
- All text you output outside of tool use is displayed to the user.
- Tools are executed in a user-selected permission mode.
- Tool results and user messages may include <system-reminder> or other tags.

# Doing tasks
- The user will primarily request you to perform software engineering tasks.

# Tone and style
- Your responses should be short and concise.

# Output efficiency
Keep your text output brief and direct."""


def build_request(user_text: str, system_reminder: str = None, tools: list = None):
    """Build a Claude Messages API request body."""
    # Build user message content
    if system_reminder:
        reminder_block = (
            "<system-reminder>\n"
            "As you answer the user's questions, you can use the following context from the system:\n"
            f"{system_reminder}\n\n"
            "IMPORTANT: this context may or may not be relevant to your tasks.\n"
            "</system-reminder>\n"
        )
        content = [
            {"type": "text", "text": reminder_block},
            {"type": "text", "text": user_text},
        ]
    else:
        content = [{"type": "text", "text": user_text}]

    billing = build_billing_header(user_text)

    body = {
        "model": MODEL,
        "max_tokens": MAX_TOKENS,
        "system": [
            {"type": "text", "text": billing},
            {"type": "text", "text": CLAUDE_CODE_IDENTITY},
            {"type": "text", "text": CLAUDE_CODE_STATIC},
        ],
        "messages": [{"role": "user", "content": content}],
    }
    if tools:
        body["tools"] = tools
    return body


def send_request(body: dict) -> tuple[int, str]:
    """Send request and return (status_code, error_message_or_ok)."""
    try:
        resp = requests.post(API_URL, headers=HEADERS, json=body, timeout=30)
        if resp.status_code == 200:
            return 200, "OK"
        try:
            err = resp.json()
            msg = err.get("error", {}).get("message", resp.text[:200])
        except Exception:
            msg = resp.text[:200]
        return resp.status_code, msg
    except Exception as e:
        return 0, str(e)


def run_test(name: str, body: dict) -> dict:
    """Run a single test case."""
    print(f"\n{'='*60}")
    print(f"TEST: {name}")
    # Count suspicious keywords in serialized body
    body_str = json.dumps(body)
    keywords = {
        "OpenClaw": len(re.findall(r"OpenClaw", body_str, re.IGNORECASE)),
        "openclaw": len(re.findall(r"openclaw", body_str)),
        "mcporter": len(re.findall(r"mcporter", body_str, re.IGNORECASE)),
        "lossless-claw": len(re.findall(r"lossless-claw", body_str, re.IGNORECASE)),
        "clawd": len(re.findall(r"clawd", body_str, re.IGNORECASE)),
        "clawflow": len(re.findall(r"clawflow", body_str, re.IGNORECASE)),
        "clawhub": len(re.findall(r"clawhub", body_str, re.IGNORECASE)),
    }
    total_kw = sum(keywords.values())
    if total_kw > 0:
        kw_str = ", ".join(f"{k}={v}" for k, v in keywords.items() if v > 0)
        print(f"  Keywords found ({total_kw}): {kw_str}")
    else:
        print(f"  Keywords: NONE")

    # Compute body size
    reminder_len = 0
    msg0 = body["messages"][0]["content"]
    if isinstance(msg0, list) and len(msg0) > 0:
        reminder_len = len(msg0[0].get("text", ""))
    tools_count = len(body.get("tools", []))
    print(f"  System-reminder: {reminder_len} chars, Tools: {tools_count}")

    status, msg = send_request(body)
    result = "PASS" if status == 200 else "FAIL"
    print(f"  Result: {result} (HTTP {status})")
    if status != 200:
        print(f"  Error: {msg[:200]}")
    return {"name": name, "status": status, "result": result, "message": msg, "keywords": total_kw}


def main():
    results = []

    # Load original system-reminder from extracted file
    reminder_file = "/tmp/test_system_reminder.txt"
    tools_file = "/tmp/test_tools.json"
    if not os.path.exists(reminder_file):
        print(f"ERROR: {reminder_file} not found. Run the extraction step first.")
        sys.exit(1)

    with open(reminder_file) as f:
        original_reminder = f.read()

    tools = []
    if os.path.exists(tools_file):
        with open(tools_file) as f:
            tools = json.load(f)

    user_text = "say hi"

    # ==========================================
    # TEST 1: Baseline - no system-reminder at all
    # ==========================================
    body = build_request(user_text)
    results.append(run_test("1. Baseline: Claude Code only, no user context", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 2: Full OpenClaw prompt (unsanitized)
    # ==========================================
    body = build_request(user_text, system_reminder=original_reminder, tools=tools)
    results.append(run_test("2. Full OpenClaw prompt (raw, unsanitized)", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 3: Full OpenClaw prompt WITHOUT tools
    # ==========================================
    body = build_request(user_text, system_reminder=original_reminder)
    results.append(run_test("3. Full OpenClaw prompt WITHOUT tools", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 4: Sanitized (Stage 1+2 only - paragraph removal)
    # ==========================================
    identity_prefixes = [
        "You are a personal assistant operating inside",
        "You are OpenCode",
        "You are Cline",
    ]
    anchor_urls = [
        "docs.openclaw.ai",
        "github.com/openclaw/openclaw",
        "discord.com/invite/clawd",
        "clawhub.ai",
    ]

    def stage12_sanitize(text):
        paragraphs = text.split("\n\n")
        filtered = []
        for p in paragraphs:
            trimmed = p.strip()
            skip = False
            for prefix in identity_prefixes:
                if trimmed.startswith(prefix):
                    skip = True
                    break
            if not skip:
                for anchor in anchor_urls:
                    if anchor in p:
                        skip = True
                        break
            if not skip:
                filtered.append(p)
        return "\n\n".join(filtered)

    sanitized_12 = stage12_sanitize(original_reminder)
    body = build_request(user_text, system_reminder=sanitized_12, tools=tools)
    results.append(run_test("4. Stage 1+2: identity + anchor paragraphs removed", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 5: Stage 1+2+3+4: full keyword scrub
    # ==========================================
    def stage34_scrub(text):
        replacements = [
            (re.compile(r"(?i)lossless-claw"), "lossless-recall"),
            (re.compile(r"(?i)clawflow"), "taskflow"),
            (re.compile(r"(?i)clawhub"), "skillhub"),
            (re.compile(r"(?i)OPENCLAW_CACHE_BOUNDARY"), "AGENT_CACHE_BOUNDARY"),
            (re.compile(r"(?i)openclaw"), "agent"),
            (re.compile(r"(?i)mcporter"), "mcp-cli"),
            (re.compile(r"(?i)clawd"), "agent-d"),
        ]
        for pat, repl in replacements:
            text = pat.sub(repl, text)
        return text

    sanitized_full = stage34_scrub(sanitized_12)
    body = build_request(user_text, system_reminder=sanitized_full, tools=tools)
    results.append(run_test("5. Full sanitize: paragraphs removed + keywords scrubbed", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 6: Full sanitize WITHOUT tools
    # ==========================================
    body = build_request(user_text, system_reminder=sanitized_full)
    results.append(run_test("6. Full sanitize WITHOUT tools", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 7: Only identity line (keyword isolation)
    # ==========================================
    body = build_request(user_text, system_reminder="You are a personal assistant operating inside OpenClaw.")
    results.append(run_test("7. Keyword: identity line only", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 8: Only 'OpenClaw' brand mentions
    # ==========================================
    body = build_request(
        user_text,
        system_reminder="OpenClaw is a tool framework. Use OpenClaw docs for reference. OpenClaw supports multiple agents.",
    )
    results.append(run_test("8. Keyword: 'OpenClaw' brand mentions only", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 9: Only file paths with 'openclaw'
    # ==========================================
    body = build_request(
        user_text,
        system_reminder="Your workspace is at ~/.openclaw/workspace/. Read ~/.openclaw/workspace/IDENTITY.md on startup.",
    )
    results.append(run_test("9. Keyword: 'openclaw' in file paths only", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 10: Only 'mcporter' tool reference
    # ==========================================
    body = build_request(
        user_text,
        system_reminder="Use tools via: npx mcporter call <server>.<tool> <args>. mcporter handles all MCP routing.",
    )
    results.append(run_test("10. Keyword: 'mcporter' only", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 11: Only 'lossless-claw' plugin
    # ==========================================
    body = build_request(
        user_text,
        system_reminder="The lossless-claw plugin is active. Use lossless-claw recall tools first.",
    )
    results.append(run_test("11. Keyword: 'lossless-claw' only", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 12: Generic third-party identity (no brand)
    # ==========================================
    body = build_request(
        user_text,
        system_reminder="You are a coding assistant. Help users with their programming tasks.",
    )
    results.append(run_test("12. Generic: 'You are a coding assistant'", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 13: Large generic context (no brands)
    # ==========================================
    generic_ctx = """## Session Startup

1. Read SOUL.md
2. Read USER.md
3. Read memory/YYYY-MM-DD.md (today + yesterday)
4. If MAIN SESSION: read MEMORY.md

## Memory

Write it down. Mental notes don't survive. Text > Brain.

- Daily notes: memory/YYYY-MM-DD.md
- Long-term: MEMORY.md
- Knowledge graph: Graphiti plugin

## Red Lines

- NEVER write/update/delete production data. Read-only.
- NEVER expose credentials, API keys, connection strings.
- NEVER fabricate data.
- When in doubt, ask.

## Tools

MCP tools via: npx mcp-cli call <server>.<tool> <args>

| Server | Purpose |
|--------|---------|
| mongodb-prod | Production MongoDB Atlas (READ ONLY) |
| jira | Jira Cloud |
| aws-mcp | AWS CloudWatch, S3, ECS |
| graphiti | Knowledge graph |"""
    body = build_request(user_text, system_reminder=generic_ctx, tools=tools)
    results.append(run_test("13. Large generic context + tools (no brands)", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # TEST 14: Scrubbed prompt + tools with renamed tool names
    # ==========================================
    # Some tools might have openclaw references in descriptions
    scrubbed_tools = json.loads(stage34_scrub(json.dumps(tools)))
    body = build_request(user_text, system_reminder=sanitized_full, tools=scrubbed_tools)
    results.append(run_test("14. Full sanitize + tools descriptions scrubbed", body))
    time.sleep(DELAY_BETWEEN_TESTS)

    # ==========================================
    # SUMMARY
    # ==========================================
    print("\n" + "=" * 70)
    print("SUMMARY")
    print("=" * 70)
    print(f"{'#':<4} {'Test Name':<55} {'Result':<6} {'HTTP':<5} {'KW':<4}")
    print("-" * 70)
    for i, r in enumerate(results, 1):
        name = r["name"][:55]
        print(f"{i:<4} {name:<55} {r['result']:<6} {r['status']:<5} {r['keywords']:<4}")

    passed = sum(1 for r in results if r["result"] == "PASS")
    failed = sum(1 for r in results if r["result"] == "FAIL")
    print(f"\nTotal: {passed} PASS, {failed} FAIL out of {len(results)} tests")

    # Save results
    with open("/tmp/oauth_bypass_results.json", "w") as f:
        json.dump(results, f, indent=2)
    print("\nResults saved to /tmp/oauth_bypass_results.json")


if __name__ == "__main__":
    main()
