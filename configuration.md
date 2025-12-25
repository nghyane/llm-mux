# Configuration

Config file: `~/.config/llm-mux/config.yaml`

```bash
llm-mux --init  # Creates config, auth dir, and management key
```

---

## Core Settings

```yaml
port: 8317                              # Server port
auth-dir: "$XDG_CONFIG_HOME/llm-mux/auth"  # OAuth tokens location
disable-auth: true                      # No API key required (local use)
debug: false                            # Verbose logging
logging-to-file: false                  # Log to file vs stdout
proxy-url: ""                           # Global proxy (http/https/socks5)
```

## Request Handling

```yaml
request-retry: 3                        # Retry attempts
max-retry-interval: 30                  # Max seconds between retries
disable-cooling: false                  # Skip cooldown after quota errors
```

## TLS

```yaml
tls:
  enable: true
  cert: "/path/to/cert.pem"
  key: "/path/to/key.pem"
```

---

## Providers

All API providers are configured in a unified `providers` array:

```yaml
providers:
  # Gemini (Google AI)
  - type: gemini
    api-key: "your-gemini-key"
    proxy-url: ""                     # Optional
    excluded-models: []               # Optional

  # Anthropic (Claude)
  - type: anthropic
    api-key: "sk-ant-..."
    base-url: ""                      # Optional, defaults to api.anthropic.com
    proxy-url: ""

  # OpenAI-compatible (OpenAI, DeepSeek, Groq, etc.)
  - type: openai
    name: "openai"                    # Display name
    base-url: "https://api.openai.com/v1"
    api-keys:                         # Multiple keys for load balancing
      - key: "sk-..."
        proxy-url: ""                 # Per-key proxy
    models:                           # Required: list available models
      - name: "gpt-4o"
      - name: "gpt-4-turbo"
        alias: "gpt4"                 # Optional alias

  # DeepSeek example
  - type: openai
    name: "deepseek"
    base-url: "https://api.deepseek.com/v1"
    api-keys:
      - key: "sk-..."
    models:
      - name: "deepseek-chat"
        alias: "deepseek"

  # Groq example
  - type: openai
    name: "groq"
    base-url: "https://api.groq.com/openai/v1"
    api-keys:
      - key: "gsk_..."
    models:
      - name: "llama-3.3-70b-versatile"

  # Vertex-compatible (zenmux, etc.)
  - type: vertex-compat
    name: "zenmux"
    base-url: "https://zenmux.ai/api"
    api-key: "your-key"
    models:
      - name: "gemini-2.5-pro"
        alias: "zenmux-gemini"
```

### Provider Types

| Type | Description | Requires |
|------|-------------|----------|
| `gemini` | Google Gemini API | `api-key` |
| `anthropic` | Anthropic Claude API | `api-key` |
| `openai` | OpenAI-compatible APIs | `base-url`, `api-keys`, `models` |
| `vertex-compat` | Vertex AI-compatible | `base-url`, `api-key`, `models` |

### Provider Fields

| Field | Description |
|-------|-------------|
| `type` | Provider type (required) |
| `name` | Display name (required for openai) |
| `api-key` | Single API key |
| `api-keys` | Multiple API keys with per-key proxy |
| `base-url` | API endpoint URL |
| `proxy-url` | HTTP/SOCKS5 proxy |
| `headers` | Custom HTTP headers |
| `models` | Available models with optional aliases |
| `excluded-models` | Models to skip |
| `enabled` | Set to `false` to disable |

---

## Environment Variables (Cloud Deployment)

```bash
# PostgreSQL token store
PGSTORE_DSN=postgresql://user:pass@host:5432/db
PGSTORE_SCHEMA=public

# S3-compatible storage
OBJECTSTORE_ENDPOINT=https://s3.amazonaws.com
OBJECTSTORE_BUCKET=llm-mux-tokens
OBJECTSTORE_ACCESS_KEY=...
OBJECTSTORE_SECRET_KEY=...

# Git-backed config
GITSTORE_GIT_URL=https://github.com/org/config.git
GITSTORE_GIT_TOKEN=ghp_...
```

---

## Advanced

```yaml
# Management API access from non-localhost
remote-management:
  allow-remote: false

# WebSocket auth
ws-auth: false

# Usage tracking
usage-statistics-enabled: false
```

See [API Reference](api-reference.md#management-api) for management endpoints.
