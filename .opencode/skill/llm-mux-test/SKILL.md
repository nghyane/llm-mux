---
name: llm-mux-test
description: Test llm-mux API endpoints (OpenAI/Claude/Gemini) with chat, stream, and tool calling
---

## 1. Setup

```bash
PORT=$(grep -E "^port:" ~/.config/llm-mux/config.yaml | awk '{print $2}')
[ -z "$PORT" ] && echo "Error: PORT not found in config" && exit 1
curl -sf "http://localhost:$PORT/" > /dev/null || echo "Warning: Service not running. Use build-deploy skill."
```

> If service needs restart, use the `build-deploy` skill.

## 2. List Models

```bash
curl -s http://localhost:$PORT/v1/models | jq -r '.data[].id'
```

## 3. Cross-Format Testing

Test translation giữa các API format và backend model.

### OpenAI Format -> Gemini Backend

```bash
# Chat
curl -s http://localhost:$PORT/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Say hi"}]}'

# Stream
curl -s http://localhost:$PORT/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Say hi"}], "stream": true}'

# Tool Calling
curl -s http://localhost:$PORT/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Weather in Tokyo?"}], "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}}]}'
```

### Claude Format -> Gemini Backend

```bash
# Chat
curl -s http://localhost:$PORT/v1/messages \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model": "gemini-2.5-flash", "max_tokens": 100, "messages": [{"role": "user", "content": "Say hi"}]}'

# Stream
curl -s http://localhost:$PORT/v1/messages \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model": "gemini-2.5-flash", "max_tokens": 100, "messages": [{"role": "user", "content": "Say hi"}], "stream": true}'

# Tool Calling
curl -s http://localhost:$PORT/v1/messages \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model": "gemini-2.5-flash", "max_tokens": 100, "messages": [{"role": "user", "content": "Weather in Tokyo?"}], "tools": [{"name": "get_weather", "input_schema": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}]}'
```

### Gemini Format -> GPT Backend

```bash
# Chat
curl -s "http://localhost:$PORT/v1beta/models/gpt-5:generateContent" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"role": "user", "parts": [{"text": "Say hi"}]}]}'

# Stream
curl -s "http://localhost:$PORT/v1beta/models/gpt-5:streamGenerateContent?alt=sse" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"role": "user", "parts": [{"text": "Say hi"}]}]}'

# Tool Calling
curl -s "http://localhost:$PORT/v1beta/models/gpt-5:generateContent" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"role": "user", "parts": [{"text": "Weather in Tokyo?"}]}], "tools": [{"function_declarations": [{"name": "get_weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}]}]}'
```
