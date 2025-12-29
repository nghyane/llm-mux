# llm-mux

**AI Gateway for Subscription-Based LLMs**

Turn your Claude Pro, GitHub Copilot, and Gemini subscriptions into standard LLM APIs. No API keys needed.

---

## Features

- **Multi-Provider** — Claude, Copilot, Gemini, Codex, Qwen, Kiro, and more
- **Multi-Format** — OpenAI, Anthropic, Gemini, Ollama compatible endpoints
- **Multi-Account** — Load balance across accounts, auto-retry on quota limits
- **Zero Config** — OAuth login, no API keys required
- **Protocol Translation** — IR-based translator converts between all formats

---

## Quick Start

```bash
# 1. Install
curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash

# 2. Login to a provider
llm-mux --antigravity-login   # Google Gemini
llm-mux --claude-login        # Claude Pro/Max
llm-mux --copilot-login       # GitHub Copilot

# 3. Start the server
llm-mux

# 4. Verify
curl http://localhost:8317/v1/models
```

---

## API Formats

| Format | Endpoint | Use With |
|--------|----------|----------|
| **OpenAI** | `/v1/chat/completions` | Cursor, Aider, LangChain |
| **Anthropic** | `/v1/messages` | Claude Code, Cline |
| **Gemini** | `/v1beta/models/{model}:generateContent` | Gemini CLI |
| **Ollama** | `/api/chat` | Open WebUI |

```
Base URL: http://localhost:8317
API Key:  unused
```

---

## Quick Links

- **Base URL:** `http://localhost:8317/v1`
- **Default Port:** `8317`
- **Config Location:** `~/.config/llm-mux/config.yaml`
- **GitHub:** [nghyane/llm-mux](https://github.com/nghyane/llm-mux)

---

## Documentation

| Guide | Description |
|-------|-------------|
| [Installation](installation.md) | Install options, update, uninstall |
| [Providers](providers.md) | All supported providers and login commands |
| [Configuration](configuration.md) | Config file reference |
| [API Reference](api-reference.md) | Supported API formats and endpoints |
| [Integrations](integrations/README.md) | Cursor, VS Code, Aider, LangChain, etc. |
| [Docker](docker.md) | Container deployment |
| [Service Management](service-management.md) | Background service on macOS/Linux/Windows |
| [Troubleshooting](troubleshooting.md) | Common issues and solutions |
