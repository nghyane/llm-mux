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

## API Keys

### Gemini

```yaml
gemini-api-key:
  - api-key: "your-key"
    base-url: ""              # Optional: custom endpoint
    proxy-url: ""             # Per-key proxy
    headers: {}               # Extra headers
    excluded-models: []       # Models to skip
```

### Claude

```yaml
claude-api-key:
  - api-key: "sk-ant-..."
    base-url: "https://api.anthropic.com"
    proxy-url: ""
    headers: {}
    excluded-models: []
    models:                   # Model aliases
      - name: "claude-3-opus"
        alias: "opus"
```

### OpenAI-Compatible Providers

```yaml
openai-compatibility:
  - name: "openai"
    base-url: "https://api.openai.com/v1"
    headers: {}               # Extra headers
    api-key-entries:
      - api-key: "sk-..."
        proxy-url: ""         # Per-key proxy
    models:
      - name: "gpt-4o"
        alias: ""             # Optional alias
```

Examples: OpenAI, DeepSeek (`https://api.deepseek.com/v1`), Groq (`https://api.groq.com/openai/v1`)

> **Legacy**: `codex-api-key` also works for OpenAI keys but `openai-compatibility` is preferred.

### Vertex-Compatible Providers

```yaml
vertex-api-key:
  - api-key: "your-api-key"
    base-url: "https://zenmux.ai/api"  # Required
    proxy-url: ""
    headers: {}
    models:
      - name: "gemini-2.5-pro"
        alias: "zenmux-gemini"
```

> For Google Cloud Vertex AI, use `llm-mux --vertex-import /path/to/service-account.json`

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
