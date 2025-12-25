# Configuration

Config file: `~/.config/llm-mux/config.yaml`

```bash
llm-mux --init  # Creates config, auth dir, and management key
```

---

## Core Settings

```yaml
port: 8317                              # Server port
auth-dir: "~/.config/llm-mux/auth"      # OAuth tokens location
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

Unified `providers` array for all API backends:

```yaml
providers:
  - type: gemini
    api-key: "AIzaSy..."

  - type: anthropic
    api-key: "sk-ant-..."

  - type: openai
    name: "deepseek"
    base-url: "https://api.deepseek.com/v1"
    api-key: "sk-..."
    models:
      - name: "deepseek-chat"

  - type: vertex-compat
    name: "zenmux"
    base-url: "https://zenmux.ai/api"
    api-key: "vk-..."
    models:
      - name: "gemini-2.5-pro"
```

### Provider Types

| Type | Description | Required Fields |
|------|-------------|-----------------|
| `gemini` | Google Gemini API | `api-key` |
| `anthropic` | Claude API (official or compatible) | `api-key` |
| `openai` | OpenAI-compatible APIs | `name`, `base-url`, `api-key`, `models` |
| `vertex-compat` | Vertex AI-compatible | `name`, `base-url`, `api-key`, `models` |

### All Provider Fields

| Field | Description |
|-------|-------------|
| `type` | Provider type (required) |
| `name` | Display name |
| `api-key` | Single API key |
| `api-keys` | Multiple keys for load balancing |
| `base-url` | Custom API endpoint |
| `proxy-url` | Per-provider proxy (http/https/socks5) |
| `headers` | Custom HTTP headers |
| `models` | Model list with optional `alias` |
| `excluded-models` | Models to skip (wildcards: `*flash*`, `gemini-*`) |

### Examples

**Multiple API keys:**
```yaml
- type: gemini
  api-keys:
    - "AIzaSy...01"
    - "AIzaSy...02"
```

**Custom Claude endpoint (OpenRouter, Bedrock proxy):**
```yaml
- type: anthropic
  name: "openrouter-claude"
  base-url: "https://openrouter.ai/api/v1"
  api-key: "sk-or-..."
  models:
    - name: "anthropic/claude-3.5-sonnet"
      alias: "claude-sonnet"
```

**Model aliases:**
```yaml
- type: openai
  name: "groq"
  base-url: "https://api.groq.com/openai/v1"
  api-key: "gsk_..."
  models:
    - name: "llama-3.3-70b-versatile"
      alias: "llama70b"
```

**Exclude models:**
```yaml
- type: gemini
  api-key: "AIzaSy..."
  excluded-models:
    - "gemini-2.5-pro"      # exact match
    - "gemini-1.5-*"        # prefix
    - "*-preview"           # suffix
    - "*flash*"             # substring
```

---

## Environment Variables

Cloud deployment options:

```bash
# PostgreSQL token store
PGSTORE_DSN=postgresql://user:pass@host:5432/db

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
remote-management:
  allow-remote: false       # Management API from non-localhost
  secret-key: "admin"       # Management API key

ws-auth: false              # WebSocket authentication
usage-statistics-enabled: true
use-canonical-translator: true  # IR translator (recommended)
```

See [API Reference](api-reference.md#management-api) for management endpoints.
