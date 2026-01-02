# API Reference

Base URL: `http://localhost:8317` | Auth: `disable-auth: true` (default, no key required)

---

## Endpoints

### OpenAI Compatible (`/v1/`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/chat/completions` | Chat completions |
| POST | `/v1/completions` | Legacy completions |
| POST | `/v1/responses` | Responses API (Codex CLI) |
| GET | `/v1/models` | List available models |

### Anthropic Compatible (`/v1/`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/messages` | Messages API |
| POST | `/v1/messages/count_tokens` | Token counting |

### Gemini Compatible (`/v1beta/`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1beta/models/{model}:generateContent` | Generate content |
| POST | `/v1beta/models/{model}:streamGenerateContent` | Stream content |
| GET | `/v1beta/models` | List models |

### Ollama Compatible (`/api/`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/chat` | Chat |
| POST | `/api/generate` | Generate |
| GET | `/api/tags` | List models |

---

## Quick Examples

```bash
# OpenAI format
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "Hello!"}]}'

# Anthropic format
curl http://localhost:8317/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "messages": [{"role": "user", "content": "Hello!"}]}'

# Streaming (add to any request)
"stream": true

# List models
curl http://localhost:8317/v1/models
```

---

## Model Naming

```bash
# Direct name
"model": "gemini-2.5-pro"

# Force provider
"model": "gemini://gemini-2.5-pro"
"model": "claude://claude-sonnet-4-20250514"
```

See [Providers](providers.md) for available models.

---

## Features

| Feature | Usage |
|---------|-------|
| **Streaming** | `"stream": true` |
| **Tool Calling** | Standard OpenAI tools format, auto-translated |
| **Extended Thinking** | `"thinking": {"type": "enabled", "budget_tokens": 10000}` |

---

## Error Codes

| Code | Meaning |
|------|---------|
| 400 | Bad request |
| 401 | Unauthorized |
| 404 | Model not found |
| 429 | Rate limited |
| 503 | No providers available |

---

## Management API

See [management-api.yaml](management-api.yaml) for full OpenAPI specification.

Base path: `/v1/management` | Auth: `X-Management-Key` header

```bash
# Generate management key
llm-mux --init

# Example request
curl -H "X-Management-Key: $KEY" http://localhost:8317/v1/management/config
```
