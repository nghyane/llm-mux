# llm-mux

A unified gateway to access multiple LLM providers through any API format, without managing API keys.

Authenticate once via OAuth, then use OpenAI, Gemini, Claude, or Ollama API formats to call any provider. Free within CLI tool quotas.

## Quick Start

```bash
# Install and start service
curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash

# Authenticate with a provider
llm-mux --login  # or --copilot-login, --antigravity-login, etc.

# Call any model using your preferred API format
curl http://localhost:8318/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Hello"}]}'
```

## Features

| Traditional API Access | llm-mux |
|------------------------|---------|
| Requires API keys per provider | Single OAuth authentication |
| Pay per token | Free (within CLI quotas) |
| Learn each provider's API | Use any API format with any provider |
| Manual token management | Auto-refresh tokens |

**Key Capabilities:**
- Multi-format API support (OpenAI, Gemini, Claude, Ollama)
- Dynamic model registry with quota tracking
- Automatic tool call normalization
- OAuth session management with token refresh
- Load balancing across providers

## Installation

### Script (Recommended)

```bash
# Install with auto-start service
curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash
```

**Options:**
```bash
# Binary only (no service)
bash -s -- --no-service

# Specific version
bash -s -- --version v1.0.0

# Custom directory
bash -s -- --dir /custom/path
```

The installer:
1. Downloads the binary for your platform
2. Installs to `/usr/local/bin` (or `~/.local/bin`)
3. Creates default config at `~/.config/llm-mux/config.yaml`
4. Sets up auto-start service (launchd/systemd)

### Homebrew

```bash
brew tap nghyane/tap
brew install llm-mux
brew services start llm-mux
```

### Docker

```bash
docker pull nghyane/llm-mux
docker run -p 8318:8318 -v ~/.config/llm-mux:/root/.config/llm-mux nghyane/llm-mux
```

### From Source

```bash
git clone https://github.com/nghyane/llm-mux.git
cd llm-mux
go build -o llm-mux ./cmd/server/
./llm-mux --init
./llm-mux
```

## Authentication

Login to providers using their respective CLI OAuth flows:

```bash
llm-mux --login              # Gemini CLI
llm-mux --antigravity-login  # Antigravity (multi-model)
llm-mux --claude-login       # Claude
llm-mux --codex-login        # OpenAI Codex
llm-mux --copilot-login      # GitHub Copilot
llm-mux --qwen-login         # Qwen
llm-mux --iflow-login        # iFlow
llm-mux --cline-login        # Cline
llm-mux --kiro-login         # Amazon Q/Kiro
```

Tokens are stored in `~/.config/llm-mux/auth/` and automatically refreshed.

## Providers and Models

### Google

| Provider | Login Flag | Available Models |
|----------|-----------|------------------|
| Gemini CLI | `--login` | gemini-2.5-pro, gemini-2.5-flash, gemini-2.5-flash-lite, gemini-3-pro-preview |
| Antigravity | `--antigravity-login` | Gemini + Claude Sonnet/Opus 4.5 + GPT-OSS + Computer Use |
| AI Studio | `--login` | Gemini models + image generation |

### Anthropic

| Provider | Login Flag | Available Models |
|----------|-----------|------------------|
| Claude | `--claude-login` | claude-sonnet-4-5, claude-opus-4-5 |
| Kiro | `--kiro-login` | Claude models via Amazon Q |

### OpenAI

| Provider | Login Flag | Available Models |
|----------|-----------|------------------|
| Codex | `--codex-login` | gpt-5.1, gpt-5.1-codex, gpt-5.1-codex-max |
| GitHub Copilot | `--copilot-login` | gpt-4.1, gpt-4o, gpt-5-mini, gpt-5.1-codex-max |

### Other Providers

| Provider | Login Flag | Available Models |
|----------|-----------|------------------|
| iFlow | `--iflow-login` | qwen3-coder-plus, deepseek-r1, kimi-k2, glm-4.6 |
| Cline | `--cline-login` | minimax-m2, grok-code-fast-1 |
| Qwen | `--qwen-login` | qwen3-coder-plus, qwen3-coder-flash |

## API Reference

llm-mux supports multiple API formats. Use any format with any provider.

### Supported Formats

| Format | Endpoints |
|--------|-----------|
| **OpenAI** | `/v1/chat/completions`, `/v1/completions`, `/v1/models` |
| **Gemini** | `/v1beta/models/{model}:generateContent`, `/v1beta/models/{model}:streamGenerateContent` |
| **Claude** | `/v1/messages` |
| **Ollama** | `/api/chat`, `/api/generate`, `/api/tags` |

### Examples

**OpenAI format:**
```bash
curl http://localhost:8318/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Hello"}]}'
```

**Gemini format:**
```bash
curl http://localhost:8318/v1beta/models/gemini-2.5-flash:generateContent -H "Content-Type: application/json" \
  -d '{"contents": [{"parts": [{"text": "Hello"}]}]}'
```

**Ollama format:**
```bash
curl http://localhost:8318/api/chat -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Hello"}]}'
```

**Claude format:**
```bash
curl http://localhost:8318/v1/messages -H "Content-Type: application/json" \
  -d '{"model": "claude-sonnet-4-5", "messages": [{"role": "user", "content": "Hello"}]}'
```

### List Available Models

```bash
# OpenAI format
curl http://localhost:8318/v1/models

# Ollama format
curl http://localhost:8318/api/tags
```

## Configuration

Initialize default config:
```bash
llm-mux --init
```

**Config location:** `~/.config/llm-mux/config.yaml`
**Token storage:** `~/.config/llm-mux/auth/`

## Service Management

### macOS (launchd)

```bash
launchctl stop com.llm-mux     # Stop service
launchctl start com.llm-mux    # Start service
launchctl unload ~/Library/LaunchAgents/com.llm-mux.plist  # Disable auto-start
```

Service file: `~/Library/LaunchAgents/com.llm-mux.plist`
Logs: `~/.local/var/log/llm-mux.log`

### Linux (systemd)

```bash
systemctl --user stop llm-mux      # Stop service
systemctl --user start llm-mux     # Start service
systemctl --user status llm-mux    # Check status
systemctl --user disable llm-mux   # Disable auto-start
```

Service file: `~/.config/systemd/user/llm-mux.service`
Logs: `journalctl --user -u llm-mux`

## Architecture

```
  Client API Formats              Providers
  ──────────────────              ─────────
    OpenAI ─────┐                 ┌─── Gemini CLI
    Claude ─────┤                 ├─── Antigravity
    Gemini ─────┼── Unified IR ───┼─── Claude/Kiro
    Ollama ─────┘                 ├─── Codex/Copilot
                                  └─── iFlow/Cline/Qwen
```

**Core Components:**

1. **Format Translation Layer**: Converts any API format to/from unified Intermediate Representation (IR), enabling 2n translations instead of n²
2. **Provider Adapters**: OAuth session management and provider-specific request handling
3. **Dynamic Model Registry**: Tracks available models and quota status across all authenticated providers
4. **Tool Call Normalizer**: Auto-fixes parameter name mismatches between formats (e.g., `filePath` ↔ `file_path`)
5. **Load Balancer**: Routes requests to available OAuth sessions, handles quota exhaustion

## How It Works

1. **OAuth Flow**: Performs the same OAuth authentication as official CLI tools
2. **Token Management**: Stores credentials securely, auto-refreshes expired tokens
3. **Request Processing**:
   - Client sends request in any supported format
   - Translates to unified IR
   - Converts to provider-native format
4. **Response Processing**:
   - Receives provider response
   - Translates to IR
   - Converts back to original request format
5. **Quota Handling**: Automatically routes to alternative providers when quotas are exceeded

## License

MIT License - see [LICENSE](LICENSE)
