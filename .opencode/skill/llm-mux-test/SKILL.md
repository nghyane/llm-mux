---
name: llm-mux-test
description: Test llm-mux IR translator - cross-format API translation
---

## Quick Start

```bash
# Check server
curl -s http://localhost:8318/v1/models | jq -r '.data[:3][].id'

# SDK tests (install once)
pip install openai anthropic
python .opencode/skill/llm-mux-test/sdk_tests.py
```

## SDK Test Commands

```bash
# All tests
python sdk_tests.py

# By SDK
python sdk_tests.py openai
python sdk_tests.py anthropic

# By name pattern
python sdk_tests.py tool_result
python sdk_tests.py thinking
python sdk_tests.py stream
```

## Key Test Cases

| Test | What it validates |
|------|-------------------|
| `tool_result` | Multi-turn tool flow, role normalization |
| `multi_tool_result` | Multiple tool_results in one message |
| `thinking_auto_disable` | Auto-disable thinking when signature missing |
| `*_backend` | Cross-format translation (OpenAI↔Claude↔Gemini) |

## Manual curl Tests

### OpenAI Format
```bash
curl -s http://localhost:8318/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hi"}]}' | jq -r '.choices[0].message.content'
```

### Claude Format
```bash
curl -s http://localhost:8318/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4","max_tokens":50,"messages":[{"role":"user","content":"Hi"}]}' | jq -r '.content[0].text'
```

### Gemini Format
```bash
curl -s "http://localhost:8318/v1beta/models/gemini-2.5-flash:generateContent" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}' | jq -r '.candidates[0].content.parts[0].text'
```
