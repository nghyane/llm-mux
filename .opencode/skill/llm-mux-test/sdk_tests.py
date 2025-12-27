#!/usr/bin/env python3
"""
llm-mux SDK tests using official OpenAI & Anthropic SDKs.

Usage:
    pip install openai anthropic
    python sdk_tests.py                # Run all
    python sdk_tests.py openai         # OpenAI only
    python sdk_tests.py anthropic      # Anthropic only
    python sdk_tests.py <pattern>      # Match test name
    LLM_MUX_URL=http://host:port python sdk_tests.py
"""

import os
import sys

BASE_URL = os.getenv("LLM_MUX_URL", "http://localhost:8318")

_oa = _an = None

def oa():
    """Lazy OpenAI client"""
    global _oa
    if not _oa:
        from openai import OpenAI
        _oa = OpenAI(base_url=f"{BASE_URL}/v1", api_key="x")
    return _oa

def an():
    """Lazy Anthropic client"""
    global _an
    if not _an:
        from anthropic import Anthropic
        _an = Anthropic(base_url=BASE_URL, api_key="x")
    return _an


# =============================================================================
# OpenAI SDK Tests
# =============================================================================

def openai_chat():
    """Basic chat completion"""
    r = oa().chat.completions.create(
        model="gemini-2.5-flash",
        messages=[{"role": "user", "content": "Say hi"}],
        max_tokens=20,
    )
    assert r.choices[0].message.content
    return r.choices[0].message.content[:40]


def openai_tool():
    """Tool call with tool_choice=auto"""
    r = oa().chat.completions.create(
        model="gemini-2.5-flash",
        messages=[{"role": "user", "content": "Weather in Tokyo?"}],
        tools=[{
            "type": "function",
            "function": {
                "name": "get_weather",
                "parameters": {"type": "object", "properties": {"loc": {"type": "string"}}}
            }
        }],
        tool_choice="auto",
        max_tokens=100,
    )
    tc = r.choices[0].message.tool_calls
    if tc:
        return f"{tc[0].function.name}({tc[0].function.arguments[:30]})"
    # Model may respond with text instead
    return f"text: {r.choices[0].message.content[:30]}"


def openai_tool_result():
    """Multi-turn with tool result"""
    r = oa().chat.completions.create(
        model="gemini-2.5-flash",
        messages=[
            {"role": "user", "content": "Calculate 17*23"},
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [{
                    "id": "call_1",
                    "type": "function",
                    "function": {"name": "calc", "arguments": '{"expr":"17*23"}'}
                }]
            },
            {"role": "tool", "tool_call_id": "call_1", "content": "391"}
        ],
        tools=[{
            "type": "function",
            "function": {
                "name": "calc",
                "parameters": {"type": "object", "properties": {"expr": {"type": "string"}}}
            }
        }],
        max_tokens=50,
    )
    content = r.choices[0].message.content
    assert content and "391" in content
    return content[:40]


def openai_stream():
    """Streaming with delta content"""
    stream = oa().chat.completions.create(
        model="gemini-2.5-flash",
        messages=[{"role": "user", "content": "Count 1 to 3"}],
        max_tokens=50,
        stream=True,
    )
    chunks = []
    for chunk in stream:
        delta = chunk.choices[0].delta
        if delta.content:
            chunks.append(delta.content)
    assert chunks
    return f"{len(chunks)} chunks: {''.join(chunks)[:30]}"


def openai_claude_backend():
    """OpenAI format -> Claude backend"""
    r = oa().chat.completions.create(
        model="claude-sonnet-4",
        messages=[{"role": "user", "content": "Say ok only"}],
        max_tokens=10,
    )
    assert r.choices[0].message.content
    return r.choices[0].message.content[:20]


# =============================================================================
# Anthropic SDK Tests
# =============================================================================

def anthropic_chat():
    """Basic message"""
    r = an().messages.create(
        model="claude-sonnet-4",
        max_tokens=20,
        messages=[{"role": "user", "content": "Say hi"}],
    )
    # SDK returns typed content blocks
    text_block = next((b for b in r.content if b.type == "text"), None)
    assert text_block and text_block.text
    return text_block.text[:40]


def anthropic_tool():
    """Tool use with tool_choice"""
    r = an().messages.create(
        model="claude-sonnet-4",
        max_tokens=100,
        messages=[{"role": "user", "content": "Weather Tokyo?"}],
        tools=[{
            "name": "get_weather",
            "description": "Get weather",
            "input_schema": {"type": "object", "properties": {"loc": {"type": "string"}}}
        }],
        tool_choice={"type": "tool", "name": "get_weather"},
    )
    tool_block = next((b for b in r.content if b.type == "tool_use"), None)
    assert tool_block
    return f"{tool_block.name}({tool_block.input})"


def anthropic_tool_result():
    """Multi-turn with tool_result - validates role normalization"""
    r = an().messages.create(
        model="claude-sonnet-4",
        max_tokens=50,
        messages=[
            {"role": "user", "content": "Calculate 17*23"},
            {
                "role": "assistant",
                "content": [{"type": "tool_use", "id": "t1", "name": "calc", "input": {"expr": "17*23"}}]
            },
            {
                "role": "user",
                "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "391"}]
            }
        ],
        tools=[{
            "name": "calc",
            "description": "Calculate",
            "input_schema": {"type": "object", "properties": {"expr": {"type": "string"}}}
        }],
    )
    text_block = next((b for b in r.content if b.type == "text"), None)
    assert text_block and "391" in text_block.text
    return text_block.text[:40]


def anthropic_multi_tool():
    """Multiple tool_results in one user message"""
    r = an().messages.create(
        model="claude-sonnet-4",
        max_tokens=80,
        messages=[
            {"role": "user", "content": "Weather Tokyo and Paris?"},
            {
                "role": "assistant",
                "content": [
                    {"type": "tool_use", "id": "t1", "name": "weather", "input": {"loc": "Tokyo"}},
                    {"type": "tool_use", "id": "t2", "name": "weather", "input": {"loc": "Paris"}}
                ]
            },
            {
                "role": "user",
                "content": [
                    {"type": "tool_result", "tool_use_id": "t1", "content": "Tokyo: 22C"},
                    {"type": "tool_result", "tool_use_id": "t2", "content": "Paris: 15C"}
                ]
            }
        ],
        tools=[{
            "name": "weather",
            "description": "Weather",
            "input_schema": {"type": "object", "properties": {"loc": {"type": "string"}}}
        }],
    )
    text_block = next((b for b in r.content if b.type == "text"), None)
    assert text_block
    return text_block.text[:50]


def anthropic_thinking_disable():
    """Auto-disable thinking when history lacks thinking blocks"""
    # This should NOT error - thinking is auto-disabled
    r = an().messages.create(
        model="claude-sonnet-4-5-thinking",
        max_tokens=50,
        thinking={"type": "enabled", "budget_tokens": 5000},
        messages=[
            {"role": "user", "content": "Time?"},
            {
                "role": "assistant",
                "content": [{"type": "tool_use", "id": "t1", "name": "time", "input": {}}]
            },
            {
                "role": "user",
                "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "12:00 UTC"}]
            }
        ],
        tools=[{
            "name": "time",
            "description": "Time",
            "input_schema": {"type": "object", "properties": {}}
        }],
    )
    text_block = next((b for b in r.content if b.type == "text"), None)
    assert text_block, "Should succeed with auto-disabled thinking"
    return "auto-disabled OK"


def anthropic_stream():
    """Streaming using MessageStreamManager"""
    text_parts = []
    # Use .stream() which returns MessageStreamManager
    with an().messages.stream(
        model="claude-sonnet-4",
        max_tokens=50,
        messages=[{"role": "user", "content": "Count 1 to 3"}],
    ) as stream:
        # text_stream yields only text deltas
        for text in stream.text_stream:
            text_parts.append(text)
    assert text_parts
    return f"{len(text_parts)} parts: {''.join(text_parts)[:30]}"


def anthropic_stream_events():
    """Streaming with full event access"""
    events = []
    with an().messages.stream(
        model="claude-sonnet-4",
        max_tokens=50,
        messages=[{"role": "user", "content": "Hi"}],
    ) as stream:
        for event in stream:
            events.append(event.type)
    # Should have message_start, content_block_start/delta/stop, message_delta, message_stop
    assert "message_start" in events or "text" in events
    return f"{len(events)} events"


def anthropic_gemini_backend():
    """Anthropic format -> Gemini backend"""
    r = an().messages.create(
        model="gemini-2.5-flash",
        max_tokens=20,
        messages=[{"role": "user", "content": "Say ok only"}],
    )
    text_block = next((b for b in r.content if b.type == "text"), None)
    assert text_block
    return text_block.text[:20]


# =============================================================================
# Runner
# =============================================================================

TESTS = {
    "openai": [
        openai_chat,
        openai_tool,
        openai_tool_result,
        openai_stream,
        openai_claude_backend,
    ],
    "anthropic": [
        anthropic_chat,
        anthropic_tool,
        anthropic_tool_result,
        anthropic_multi_tool,
        anthropic_thinking_disable,
        anthropic_stream,
        anthropic_stream_events,
        anthropic_gemini_backend,
    ],
}


def run_tests(tests):
    passed = failed = 0
    for t in tests:
        try:
            result = t()
            print(f"  \033[32m✓\033[0m {t.__name__}: {result}")
            passed += 1
        except Exception as e:
            print(f"  \033[31m✗\033[0m {t.__name__}: {type(e).__name__}: {e}")
            failed += 1
    return passed, failed


def main():
    args = sys.argv[1:]
    
    if not args:
        groups = list(TESTS.items())
    elif args[0] in TESTS:
        groups = [(args[0], TESTS[args[0]])]
    else:
        # Match by function name
        pattern = args[0].lower()
        matched = []
        for sdk, tests in TESTS.items():
            for t in tests:
                if pattern in t.__name__.lower():
                    matched.append((sdk, [t]))
        groups = matched
    
    if not groups:
        print(f"No tests match: {args[0]}")
        print(f"Available: openai, anthropic, or test names")
        return 1
    
    total_p = total_f = 0
    for name, tests in groups:
        print(f"\n\033[1m[{name}]\033[0m")
        p, f = run_tests(tests)
        total_p += p
        total_f += f
    
    print(f"\n{'='*40}")
    if total_f == 0:
        print(f"\033[32m{total_p} passed\033[0m")
    else:
        print(f"\033[31m{total_p} passed, {total_f} failed\033[0m")
    return 0 if total_f == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
