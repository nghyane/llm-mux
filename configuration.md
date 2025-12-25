# Configuration

llm-mux uses a YAML configuration file located at `~/.config/llm-mux/config.yaml`.

## Initialize Configuration

```bash
llm-mux --init
```

This creates:
- `~/.config/llm-mux/config.yaml` - Main configuration
- `~/.config/llm-mux/auth/` - OAuth token storage
- `~/.config/llm-mux/.credentials` - Management API key

---

## Configuration Reference

```yaml
# Server port (default: 8317)
port: 8317

# Authentication directory for OAuth tokens
auth-dir: "$XDG_CONFIG_HOME/llm-mux/auth"

# Disable API key requirement for requests (default: true for local use)
disable-auth: true

# Debug mode - verbose logging
debug: false

# Log to file instead of stdout
logging-to-file: false

# HTTP/SOCKS5 proxy for outbound requests
proxy-url: ""

# Request retry settings
request-retry: 3
max-retry-interval: 30

# Quota exceeded behavior
quota-exceeded:
  switch-project: true       # Try another account on quota limit
  switch-preview-model: true # Fallback to preview models

# Usage statistics
usage-statistics-enabled: false
```

---

## API Keys

### Gemini API Key

```yaml
gemini-api-key:
  - api-key: "your-api-key"
    proxy-url: ""  # Optional per-key proxy
    excluded-models:
      - "gemini-1.0-pro"  # Models to exclude
```

### Claude API Key

```yaml
claude-api-key:
  - api-key: "sk-ant-..."
    base-url: "https://api.anthropic.com"  # Optional custom endpoint
    proxy-url: ""
    models:
      - name: "claude-3-opus"
        alias: "claude-opus"  # Optional alias
```

### OpenAI-Compatible Providers

```yaml
openai-compatibility:
  - name: "deepseek"
    base-url: "https://api.deepseek.com/v1"
    api-key-entries:
      - api-key: "sk-..."
        proxy-url: ""
    models:
      - name: "deepseek-chat"
        alias: "deepseek"
```

---

## Environment Variables

Environment variables override config file settings for cloud deployments.

### PostgreSQL Token Store

Store OAuth tokens in PostgreSQL for multi-instance deployments:

```bash
PGSTORE_DSN=postgresql://user:pass@localhost:5432/llmmux
PGSTORE_SCHEMA=public
PGSTORE_LOCAL_PATH=/var/lib/llm-mux
```

### Git-Backed Config Store

Sync configuration via Git repository:

```bash
GITSTORE_GIT_URL=https://github.com/org/llm-mux-config.git
GITSTORE_GIT_USERNAME=git-user
GITSTORE_GIT_TOKEN=ghp_xxxxx
GITSTORE_LOCAL_PATH=/data/llm-mux/gitstore
```

### S3/Object Store

Store tokens in S3-compatible storage:

```bash
OBJECTSTORE_ENDPOINT=https://s3.amazonaws.com
OBJECTSTORE_BUCKET=llm-mux-tokens
OBJECTSTORE_ACCESS_KEY=AKIA...
OBJECTSTORE_SECRET_KEY=xxxxx
OBJECTSTORE_LOCAL_PATH=/data/llm-mux/objectstore
```

---

## Management API

A management key is generated during `--init` for accessing the management API.

### View Current Key

```bash
llm-mux --init
# Output: Management key: abc123...
```

### Regenerate Key

```bash
llm-mux --init --force
```

### API Endpoints

All management endpoints require the `X-Management-Key` header:

```bash
curl -H "X-Management-Key: your-key" http://localhost:8317/v0/management/config
```

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v0/management/config` | GET | View current config |
| `/v0/management/config.yaml` | GET/PUT | Get or update config file |
| `/v0/management/usage` | GET | Usage statistics |
| `/v0/management/api-keys` | GET/PUT/DELETE | Manage API keys |
| `/v0/management/logs` | GET/DELETE | View or clear logs |

---

## CLI Flags

| Flag | Description |
|------|-------------|
| `--config PATH` | Custom config file path |
| `--init` | Initialize config and generate management key |
| `--init --force` | Regenerate management key |
| `--no-browser` | Don't auto-open browser during OAuth |
| `--update` | Check for updates and install |

---

## Proxy Configuration

### Global Proxy

Set for all outbound requests:

```yaml
proxy-url: "socks5://user:pass@proxy.example.com:1080"
```

### Per-Provider Proxy

Set proxy for specific API keys:

```yaml
gemini-api-key:
  - api-key: "key1"
    proxy-url: "http://proxy1.example.com:8080"
  - api-key: "key2"
    proxy-url: "socks5://proxy2.example.com:1080"
```

Supported protocols: `http://`, `https://`, `socks5://`
