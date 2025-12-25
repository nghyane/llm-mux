# Docker

## Quick Start

```bash
docker run -d \
  --name llm-mux \
  -p 8317:8317 \
  -v ./config.yaml:/llm-mux/config.yaml \
  -v ./auths:/llm-mux/auth \
  nghyane/llm-mux:latest
```

## Docker Compose

```yaml
services:
  llm-mux:
    image: nghyane/llm-mux:latest
    container_name: llm-mux
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/llm-mux/config.yaml
      - ./auths:/llm-mux/auth
    environment:
      - TZ=UTC
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8317/v1/models"]
      interval: 30s
      timeout: 10s
      retries: 3
```

```bash
docker compose up -d
```

---

## Volume Mounts

| Host | Container | Description |
|------|-----------|-------------|
| `./config.yaml` | `/llm-mux/config.yaml` | Config file |
| `./auths/` | `/llm-mux/auth` | OAuth tokens |

### Minimal config.yaml

```yaml
port: 8317
auth-dir: "/llm-mux/auth"
disable-auth: true
```

---

## Authentication

OAuth requires a browser. Options:

**Option 1: Copy tokens from host**
```bash
llm-mux --antigravity-login          # Login on host
cp -r ~/.config/llm-mux/auth/* ./auths/
```

**Option 2: API keys only** (no OAuth needed)
```yaml
openai-compatibility:
  - name: "openai"
    base-url: "https://api.openai.com/v1"
    api-key-entries:
      - api-key: "sk-..."
    models:
      - name: "gpt-4o"
```

**Option 3: Get management key**
```bash
docker exec llm-mux ./llm-mux --init
```

---

## Build from Source

```bash
git clone https://github.com/nghyane/llm-mux.git && cd llm-mux
docker build -t llm-mux:local .
```

---

## Environment Variables

For cloud deployments, see [Configuration - Environment Variables](configuration.md#environment-variables-cloud-deployment).

```yaml
environment:
  - PGSTORE_DSN=postgresql://user:pass@postgres:5432/db
  # or
  - OBJECTSTORE_ENDPOINT=https://s3.amazonaws.com
  - OBJECTSTORE_BUCKET=llm-mux-tokens
```

---

## Common Commands

```bash
docker compose up -d      # Start
docker compose down       # Stop
docker compose pull && docker compose up -d  # Update
docker logs -f llm-mux    # Logs
docker exec -it llm-mux sh  # Shell
```
