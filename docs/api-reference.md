# API Reference

llm-mux provides a unified API that accepts requests in multiple formats and routes them to your authenticated providers.

## Base URL

```
http://localhost:8317
```

## Authentication

By default, no API key is required for local use (`disable-auth: true`).

If authentication is enabled, include your API key:

```bash
curl -H "Authorization: Bearer your-api-key" http://localhost:8317/v1/models
```

---

## Supported Protocols

llm-mux accepts requests in these formats and translates them to the appropriate provider:

| Protocol | Base Path | Use Case |
|----------|-----------|----------|
| [OpenAI](#openai-api) | `/v1/` | Most AI tools, SDKs |
| [Anthropic](#anthropic-api) | `/v1/` | Claude-native tools |
| [Gemini](#gemini-api) | `/v1beta/` | Google AI Studio |
| [Ollama](#ollama-api) | `/api/` | Ollama-compatible UIs |

---

## OpenAI API

### Chat Completions

```bash
POST /v1/chat/completions
```

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-pro",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Streaming:**

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

### Completions (Legacy)

```bash
POST /v1/completions
```

### Responses API

For OpenAI's newer Responses API format (used by Codex CLI):

```bash
POST /v1/responses
```

### List Models

```bash
GET /v1/models
```

```bash
curl http://localhost:8317/v1/models
```

---

## Anthropic API

### Messages

```bash
POST /v1/messages
```

```bash
curl http://localhost:8317/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: unused" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Token Counting

```bash
POST /v1/messages/count_tokens
```

---

## Gemini API

### Generate Content

```bash
POST /v1beta/models/{model}:generateContent
POST /v1beta/models/{model}:streamGenerateContent
```

```bash
curl http://localhost:8317/v1beta/models/gemini-2.5-pro:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello!"}]}]
  }'
```

### List Models

```bash
GET /v1beta/models
```

---

## Ollama API

### Chat

```bash
POST /api/chat
```

```bash
curl http://localhost:8317/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-pro",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Generate

```bash
POST /api/generate
```

---

## Model Naming

Use any of these model name formats:

```bash
# Direct model name
"model": "gemini-2.5-pro"

# Provider-prefixed (force specific provider)
"model": "gemini://gemini-2.5-pro"
"model": "claude://claude-sonnet-4-20250514"
```

### Available Models

Check available models from your authenticated providers:

```bash
curl http://localhost:8317/v1/models | jq '.data[].id'
```

See [Providers](providers.md) for the full list of supported models.

---

## Features

### Tool Calling

Supported across all providers with automatic format translation:

```json
{
  "model": "gemini-2.5-pro",
  "messages": [{"role": "user", "content": "What's the weather?"}],
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "parameters": {
        "type": "object",
        "properties": {
          "location": {"type": "string"}
        }
      }
    }
  }]
}
```

### Extended Thinking

Enable reasoning tokens for supported models:

```json
{
  "model": "gemini-2.5-pro",
  "messages": [{"role": "user", "content": "Solve this step by step..."}],
  "thinking": {
    "type": "enabled",
    "budget_tokens": 10000
  }
}
```

### Streaming

All endpoints support streaming with `"stream": true`.

---

## SDK Examples

### Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8317/v1",
    api_key="unused"
)

response = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)
```

### Python (Anthropic SDK)

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:8317",
    api_key="unused"
)

message = client.messages.create(
    model="claude-sonnet-4-20250514",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello!"}]
)
print(message.content[0].text)
```

### JavaScript/TypeScript

```typescript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://localhost:8317/v1',
  apiKey: 'unused',
});

const response = await client.chat.completions.create({
  model: 'gemini-2.5-pro',
  messages: [{ role: 'user', content: 'Hello!' }],
});
```

### cURL

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "Hello!"}]}'
```

---

## Error Handling

Errors follow the OpenAI error format:

```json
{
  "error": {
    "message": "Model not found",
    "type": "invalid_request_error",
    "code": "model_not_found"
  }
}
```

| HTTP Code | Meaning |
|-----------|---------|
| 400 | Bad request (invalid parameters) |
| 401 | Unauthorized (API key required) |
| 404 | Model not found |
| 429 | Rate limited (quota exceeded) |
| 500 | Internal server error |
| 503 | Service unavailable (no providers) |

---

## Management API

Management endpoints require authentication via the `X-Management-Key` header.

### Get Management Key

```bash
llm-mux --init
# Output: Management key: abc123...

# Regenerate
llm-mux --init --force
```

### Endpoints

All requests require:
```bash
-H "X-Management-Key: your-key"
```

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v0/management/config` | GET | View current runtime config |
| `/v0/management/config.yaml` | GET | Get config file contents |
| `/v0/management/config.yaml` | PUT | Update config file |
| `/v0/management/usage` | GET | Get usage statistics |
| `/v0/management/api-keys` | GET | List configured API keys |
| `/v0/management/api-keys` | PUT | Add/update API keys |
| `/v0/management/api-keys` | DELETE | Remove API keys |
| `/v0/management/logs` | GET | View server logs |
| `/v0/management/logs` | DELETE | Clear logs |
| `/v0/management/debug` | GET/PUT | Get or toggle debug mode |

### Examples

```bash
# View config
curl -H "X-Management-Key: $KEY" http://localhost:8317/v0/management/config

# Get usage stats
curl -H "X-Management-Key: $KEY" http://localhost:8317/v0/management/usage

# Toggle debug mode
curl -X PUT -H "X-Management-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"value": true}' \
  http://localhost:8317/v0/management/debug
```
