# Docker

Run llm-mux in a Docker container for isolated deployments.

## Quick Start

```bash
docker run -d \
  --name llm-mux \
  -p 8317:8317 \
  -v ./config.yaml:/llm-mux/config.yaml \
  -v ./auths:/root/.llm-mux \
  nghyane/llm-mux:latest
```

---

## Docker Compose

Create a `docker-compose.yml`:

```yaml
services:
  llm-mux:
    image: nghyane/llm-mux:latest
    container_name: llm-mux
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/llm-mux/config.yaml
      - ./auths:/root/.llm-mux
      - ./logs:/llm-mux/logs
    environment:
      - TZ=UTC
    restart: unless-stopped
```

Start the service:

```bash
docker compose up -d
```

---

## Configuration

### Volume Mounts

| Host Path | Container Path | Description |
|-----------|----------------|-------------|
| `./config.yaml` | `/llm-mux/config.yaml` | Configuration file |
| `./auths/` | `/root/.llm-mux` | OAuth tokens |
| `./logs/` | `/llm-mux/logs` | Log files (optional) |

### Create Config File

Before starting, create a `config.yaml`:

```yaml
port: 8317
auth-dir: "/root/.llm-mux"
disable-auth: true
```

---

## Authentication in Docker

OAuth login requires a browser, which isn't available in containers. Options:

### Option 1: Login on Host, Copy Tokens

```bash
# Login on host machine
llm-mux --antigravity-login

# Copy tokens to Docker volume
cp -r ~/.config/llm-mux/auth/* ./auths/
```

### Option 2: Use API Keys Only

Configure API keys in `config.yaml` instead of OAuth:

```yaml
gemini-api-key:
  - api-key: "your-api-key"

claude-api-key:
  - api-key: "sk-ant-..."
    base-url: "https://api.anthropic.com"
```

### Option 3: Remote OAuth (Management API)

Use the management API to trigger OAuth from a web interface:

```bash
# Start container with management key
docker run -d \
  --name llm-mux \
  -p 8317:8317 \
  -v ./config.yaml:/llm-mux/config.yaml \
  -v ./auths:/root/.llm-mux \
  nghyane/llm-mux:latest

# Initialize and get management key
docker exec llm-mux ./llm-mux --init

# Use management API for OAuth
curl -X POST \
  -H "X-Management-Key: your-key" \
  http://localhost:8317/v0/management/oauth/start \
  -d '{"provider": "claude"}'
```

---

## Build from Source

```bash
# Clone repository
git clone https://github.com/nghyane/llm-mux.git
cd llm-mux

# Build image
docker build -t llm-mux:local .

# Or with docker compose
docker compose build
```

### Build Arguments

```bash
docker build \
  --build-arg VERSION=v1.0.0 \
  --build-arg COMMIT=$(git rev-parse HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t llm-mux:local .
```

---

## Environment Variables

For cloud deployments with shared storage:

```yaml
services:
  llm-mux:
    image: nghyane/llm-mux:latest
    environment:
      # PostgreSQL token store
      - PGSTORE_DSN=postgresql://user:pass@postgres:5432/llmmux
      - PGSTORE_SCHEMA=public
      
      # Or S3-compatible storage
      - OBJECTSTORE_ENDPOINT=https://s3.amazonaws.com
      - OBJECTSTORE_BUCKET=llm-mux-tokens
      - OBJECTSTORE_ACCESS_KEY=AKIA...
      - OBJECTSTORE_SECRET_KEY=xxxxx
```

---

## Health Check

Add a health check to your compose file:

```yaml
services:
  llm-mux:
    image: nghyane/llm-mux:latest
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8317/v1/models"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 10s
```

---

## Reverse Proxy

### Nginx

```nginx
server {
    listen 443 ssl;
    server_name api.example.com;

    location / {
        proxy_pass http://localhost:8317;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400;
    }
}
```

### Traefik

```yaml
services:
  llm-mux:
    image: nghyane/llm-mux:latest
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.llm-mux.rule=Host(`api.example.com`)"
      - "traefik.http.services.llm-mux.loadbalancer.server.port=8317"
```

---

## Logs

```bash
# View logs
docker logs llm-mux

# Follow logs
docker logs -f llm-mux

# Last 100 lines
docker logs --tail 100 llm-mux
```

---

## Commands

```bash
# Start
docker compose up -d

# Stop
docker compose down

# Restart
docker compose restart

# Update to latest
docker compose pull
docker compose up -d

# Shell access
docker exec -it llm-mux sh
```
