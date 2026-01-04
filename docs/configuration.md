# Configuration

Config file: `~/.config/llm-mux/config.yaml`

```bash
llm-mux init  # Creates config, auth dir, and management key
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
stream-timeout: 300                     # Stream timeout in seconds
disable-cooling: false                  # Skip cooldown after quota errors
quota-window: 60                        # Quota tracking window in seconds
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
| `openai` | OpenAI-compatible APIs | `base-url`, `api-key`, `models` |
| `vertex-compat` | Vertex AI-compatible | `base-url`, `api-key`, `models` |

### All Provider Fields

| Field | Description |
|-------|-------------|
| `type` | Provider type (required) |
| `name` | Display name (recommended for openai/vertex-compat) |
| `api-key` | Single API key |
| `api-keys` | Multiple keys: `[{key: "...", proxy-url: "..."}]` |
| `base-url` | Custom API endpoint |
| `proxy-url` | Per-provider proxy (http/https/socks5) |
| `headers` | Custom HTTP headers |
| `models` | Model list: `[{name: "...", alias: "..."}]` |
| `excluded-models` | Models to skip (wildcards: `*flash*`, `gemini-*`) |

### Examples

**Multiple API keys with per-key proxy:**
```yaml
- type: gemini
  api-keys:
    - key: "AIzaSy...01"
    - key: "AIzaSy...02"
      proxy-url: "socks5://proxy2:1080"
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

Environment variables override config file values. All use `LLM_MUX_` prefix.

### Core Settings

| Variable | Description | Example |
|----------|-------------|---------|
| `LLM_MUX_PORT` | Server port | `8317` |
| `LLM_MUX_DEBUG` | Enable debug logging | `true` |
| `LLM_MUX_DISABLE_AUTH` | Disable API key authentication | `true` |
| `LLM_MUX_API_KEYS` | Comma-separated API keys | `key1,key2,key3` |
| `LLM_MUX_PROXY_URL` | Global proxy URL | `socks5://proxy:1080` |
| `LLM_MUX_AUTH_DIR` | OAuth tokens directory | `~/.config/llm-mux/auth` |
| `LLM_MUX_LOGGING_TO_FILE` | Enable file logging | `true` |
| `LLM_MUX_REQUEST_RETRY` | Retry attempts | `3` |
| `LLM_MUX_MAX_RETRY_INTERVAL` | Max retry interval (seconds) | `30` |
| `LLM_MUX_STREAM_TIMEOUT` | Stream timeout (seconds) | `300` |

### Management API

| Variable | Description |
|----------|-------------|
| `LLM_MUX_MANAGEMENT_KEY` | Management API authentication key |
| `LLM_MUX_ALLOW_REMOTE` | Allow remote management access (`true` or `1`) |

> **Note:** Remote management requires both:
> - A management key (`LLM_MUX_MANAGEMENT_KEY` or `~/.config/llm-mux/credentials.json`)
> - Remote access enabled (`LLM_MUX_ALLOW_REMOTE=true` or `allow-remote: true` in config)

### Usage Statistics

| Variable | Description | Example |
|----------|-------------|---------|
| `LLM_MUX_USAGE_DSN` | Database connection string | `sqlite://~/.config/llm-mux/usage.db` |
| `LLM_MUX_USAGE_RETENTION_DAYS` | Days to keep usage records | `30` |

### Storage Backend Selection

Use `LLM_MUX_STORE_TYPE` to explicitly select a storage backend for multi-instance deployments:

| Value | Backend |
|-------|---------|
| `local` | Local filesystem (default) |
| `postgres`, `pg` | PostgreSQL |
| `git` | Git repository |
| `s3`, `object`, `minio` | S3-compatible object storage |

### PostgreSQL Storage

```bash
LLM_MUX_STORE_TYPE=postgres
LLM_MUX_PGSTORE_DSN=postgresql://user:pass@host:5432/db
LLM_MUX_PGSTORE_SCHEMA=llm_mux          # optional
```

### Git Storage

```bash
LLM_MUX_STORE_TYPE=git
LLM_MUX_GITSTORE_URL=https://github.com/org/config.git
LLM_MUX_GITSTORE_USERNAME=user
LLM_MUX_GITSTORE_TOKEN=ghp_...
```

### S3/Object Storage

```bash
LLM_MUX_STORE_TYPE=s3
LLM_MUX_OBJECTSTORE_ENDPOINT=https://s3.amazonaws.com
LLM_MUX_OBJECTSTORE_BUCKET=llm-mux-tokens
LLM_MUX_OBJECTSTORE_ACCESS_KEY=...
LLM_MUX_OBJECTSTORE_SECRET_KEY=...
```

All remote stores sync to the standard XDG paths (`~/.config/llm-mux/config.yaml` and `~/.config/llm-mux/auth/`).

---

## Quota Handling

```yaml
quota-exceeded:
  switch-project: true      # Switch to another account on quota limit
  switch-preview-model: true  # Fallback to preview models
```

---

## Routing

Control provider priority, model aliases, and fallback chains:

```yaml
routing:
  # Provider priority (lower = higher priority)
  provider-priority:
    claude: 1           # Primary
    antigravity: 2      # Fallback
    gemini-cli: 3
    github-copilot: 4

  # Model name aliases (normalize across providers)
  aliases:
    "claude-sonnet-4.5": "claude-sonnet-4-5"
    "claude-opus-4.5": "claude-opus-4-5"
    "gpt-4": "gpt-4o"

  # Model fallback chains (when all providers fail)
  fallbacks:
    "claude-opus-4-5":
      - "claude-sonnet-4-5"
      - "gpt-4o"
    "gpt-5":
      - "gpt-4o"
      - "gemini-2.5-pro"
```

### Valid Provider Names

| Provider | Name |
|----------|------|
| Claude Pro/Max | `claude` |
| Antigravity | `antigravity` |
| Gemini CLI | `gemini-cli` |
| Gemini API | `gemini` |
| Vertex AI | `vertex` |
| AI Studio | `aistudio` |
| OpenAI Codex | `codex` |
| GitHub Copilot | `github-copilot` |
| Qwen | `qwen` |
| iFlow | `iflow` |
| Cline | `cline` |
| Kiro | `kiro` |

---

## Usage Statistics

```yaml
usage:
  dsn: "sqlite://~/.config/llm-mux/usage.db"  # or postgres://...
  batch-size: 100             # Records per batch write
  flush-interval: "5s"        # Duration between flushes
  retention-days: 30          # Days to keep records
```

Supported database backends:
- `sqlite:///path/to/db.sqlite` — Local SQLite (default)
- `postgres://user:pass@host:5432/db` — PostgreSQL

---

## OAuth Model Exclusions

Exclude specific models from OAuth providers:

```yaml
oauth-excluded-models:
  gemini:
    - "gemini-1.0-*"
    - "*-vision"
  claude:
    - "claude-2*"
```

---

## Amp CLI Integration

For Amp CLI compatibility:

```yaml
ampcode:
  upstream-url: ""          # Optional upstream proxy
  upstream-api-key: ""
  restrict-management-to-localhost: true
  model-mappings:
    - from: "claude-opus-4.5"
      to: "claude-sonnet-4"
```

---

## Payload Rules

Apply default or override parameters to specific models:

```yaml
payload:
  default:
    - models:
        - name: "gemini-*"
          protocol: "gemini"
      params:
        temperature: 0.7
  override:
    - models:
        - name: "claude-*"
          protocol: "anthropic"
      params:
        max_tokens: 8192
```

---

## Advanced

```yaml
remote-management:
  allow-remote: false       # Management API from non-localhost

ws-auth: false              # WebSocket authentication
use-canonical-translator: true  # IR translator (recommended)
```

For remote management via environment variables:
```bash
export LLM_MUX_MANAGEMENT_KEY=your-secret-key
export LLM_MUX_ALLOW_REMOTE=true
```

See [API Reference](api-reference.md#management-api) for management endpoints.
