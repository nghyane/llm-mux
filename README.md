# llm-mux

**Free LLM API gateway** that converts OAuth-authenticated CLI tools into OpenAI-compatible endpoints.

Use Claude, Gemini, GPT, and other models **without API keys** - authenticate once with your existing CLI tools.

## Why llm-mux?

| Traditional API Access | llm-mux |
|------------------------|---------|
| Requires API keys | Uses your existing CLI OAuth |
| Pay per token | Free (within CLI quotas) |
| One provider per key | All providers, one endpoint |
| Different APIs per provider | Unified OpenAI-compatible API |

## Quick Start

```bash
# Install
brew tap nghyane/tap && brew install llm-mux

# Authenticate (one-time)
llm-mux --login              # Gemini
llm-mux --claude-login       # Claude
llm-mux --copilot-login      # GitHub Copilot

# Start
brew services start llm-mux

# Use
curl http://localhost:8318/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Hello!"}]}'
```

## Supported Providers

| Provider | Auth | Key Models |
|----------|------|------------|
| **Gemini CLI** | OAuth | gemini-2.5-pro, gemini-2.5-flash, gemini-3-pro-preview |
| **AI Studio** | OAuth | gemini-2.5-pro, gemini-2.5-flash, gemini-3-pro-preview, gemini-*-image |
| **Antigravity** | OAuth | gemini-2.5-flash, gemini-3-pro, claude-sonnet-4-5, claude-opus-4-5-thinking, gpt-oss |
| **Claude** | OAuth | claude-sonnet-4-5, claude-opus-4-5 |
| **OpenAI Codex** | OAuth | gpt-5.1, gpt-5.1-codex, gpt-5.1-codex-max |
| **GitHub Copilot** | OAuth | gpt-4.1, gpt-4o, gpt-5-mini, gpt-5.1-codex-max |
| **Kiro** | OAuth | claude-sonnet-4-5, claude-opus-4-5 (via Amazon Q) |
| **iFlow** | OAuth | qwen3-coder-plus, deepseek-r1, kimi-k2 |
| **Vertex AI** | API Key | gemini-2.5-pro, gemini-2.5-flash, gemini-3-pro-preview |

## Features

### Unified API
All providers accessible through OpenAI-compatible endpoints:
```
POST /v1/chat/completions     # OpenAI Chat API
POST /v1/completions          # Completions API
GET  /v1/models               # List available models
POST /v1beta/models/*         # Gemini-native API
POST /api/chat                # Ollama-compatible
```

### IR-based Translation
Hub-and-spoke architecture minimizes code duplication:

```
    Clients                        Providers
    ───────                        ─────────
    OpenAI ────┐                ┌──── Gemini CLI
    Claude ────┤                ├──── AI Studio
    Ollama ────┼─── Unified ────┼──── Claude
    Gemini ────┤       IR       ├──── Codex
    Kiro   ────┘                └──── Copilot
```

Instead of n² format converters, each provider implements just 2 translations (to/from IR).

### Smart Tool Call Normalization
Automatically fixes parameter naming mismatches between models and clients:
- `filePath` ↔ `file_path` (case conversion)
- `path` → `target_file` (semantic synonyms)
- Array → scalar extraction when schema expects single value

### Dynamic Model Registry
- Reference counting tracks available OAuth sessions
- Auto-hides models when quota exceeded
- Graceful recovery when quota refreshes

## Installation

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
go build -o llm-mux ./cmd/server/
./llm-mux -config config.yaml
```

## Configuration

```yaml
port: 8318
auth-dir: "~/.config/llm-mux/auth"
use-canonical-translator: true

# Optional API keys for non-OAuth providers
api-keys:
  - "your-vertex-api-key"
```

## Authentication

Each provider uses its own OAuth flow:

```bash
llm-mux --login              # Gemini CLI (Google account)
llm-mux --antigravity-login  # Antigravity (Claude via Google)
llm-mux --claude-login       # Claude (Anthropic account)
llm-mux --codex-login        # OpenAI Codex (OpenAI account)
llm-mux --copilot-login      # GitHub Copilot (GitHub account)
llm-mux --kiro-login         # Kiro (Amazon Q/AWS account)
llm-mux --qwen-login         # Qwen (Alibaba account)
llm-mux --iflow-login        # iFlow
llm-mux --cline-login        # Cline
```

Tokens are stored in `~/.config/llm-mux/auth/` and auto-refresh.

## SDK

```go
import "github.com/nghyane/llm-mux/sdk/cliproxy"

svc, _ := cliproxy.NewBuilder().
    WithConfig(cfg).
    Build()

svc.Run(ctx)
```

## How It Works

1. **OAuth Capture**: llm-mux performs the same OAuth flow as official CLI tools
2. **Token Management**: Stores and auto-refreshes tokens
3. **Request Translation**: Converts incoming OpenAI-format requests to provider-native format via IR
4. **Response Translation**: Converts provider responses back to OpenAI format
5. **Load Balancing**: Routes to available OAuth sessions, handles quota limits

## License

MIT License - see [LICENSE](LICENSE)
