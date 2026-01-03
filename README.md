# llm-mux

**AI Gateway for Subscription-Based LLMs**

[![GitHub release](https://img.shields.io/github/v/release/nghyane/llm-mux)](https://github.com/nghyane/llm-mux/releases)
[![GitHub stars](https://img.shields.io/github/stars/nghyane/llm-mux)](https://github.com/nghyane/llm-mux/stargazers)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker](https://img.shields.io/docker/pulls/nghyane/llm-mux)](https://hub.docker.com/r/nghyane/llm-mux)
[![Docs](https://img.shields.io/badge/docs-online-blue)](https://nghyane.github.io/llm-mux/)

Turn your Claude Pro, GitHub Copilot, and Gemini subscriptions into standard LLM APIs. No API keys needed.

## Features

- **Multi-Provider** — Claude, Copilot, Gemini, Codex, Qwen, Kiro, iFlow, Cline, and more
- **Multi-Format** — OpenAI, Anthropic, Gemini, Ollama compatible endpoints
- **Multi-Account** — Load balance across accounts, auto-retry on quota limits
- **Zero Config** — OAuth login, no API keys required
- **Management API** — Usage statistics, auth management, runtime configuration
- **Extended Thinking** — Support for Claude's extended thinking mode
- **AMP CLI Compatible** — Drop-in replacement for Amp CLI with model mapping

## Quick Start

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash

# Login to a provider
llm-mux --antigravity-login   # Google Gemini
llm-mux --claude-login        # Claude Pro/Max
llm-mux --copilot-login       # GitHub Copilot

# Start server
llm-mux

# Test
curl http://localhost:8317/v1/models
```

## Usage

```
Base URL: http://localhost:8317
API Key:  unused (or any string)
```

```bash
# OpenAI format
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-pro",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'

# With extended thinking (Claude)
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "messages": [{"role": "user", "content": "Solve this problem"}],
    "thinking": {"type": "enabled", "budget_tokens": 10000}
  }'
```

Works with: **Cursor, Aider, Claude Code, Cline, Continue, OpenCode, LangChain, Open WebUI**, and any OpenAI/Anthropic/Gemini compatible tool.

## Documentation

📖 **https://nghyane.github.io/llm-mux/**

- [Installation](https://nghyane.github.io/llm-mux/#/installation) — Install, update, uninstall
- [Providers](https://nghyane.github.io/llm-mux/#/providers) — All providers and login commands
- [Configuration](https://nghyane.github.io/llm-mux/#/configuration) — Config file reference
- [Integrations](https://nghyane.github.io/llm-mux/#/integrations/) — Editor and framework setup
- [Docker](https://nghyane.github.io/llm-mux/#/docker) — Container deployment
- [Service Management](https://nghyane.github.io/llm-mux/#/service-management) — Background service setup
- [API Reference](https://nghyane.github.io/llm-mux/#/api-reference) — Complete API documentation

## Management API

llm-mux includes a comprehensive management API for monitoring and configuration:

```bash
# Generate management key
llm-mux --init

# Get usage statistics
curl -H "X-Management-Key: $KEY" \
  http://localhost:8317/v1/management/usage?days=7

# Upload auth files (batch)
curl -H "X-Management-Key: $KEY" \
  -F "files=@auth1.json" -F "files=@auth2.json" \
  http://localhost:8317/v1/management/auth/upload

# Update configuration
curl -H "X-Management-Key: $KEY" \
  -X PUT http://localhost:8317/v1/management/config.yaml \
  --data-binary @config.yaml
```

**Features:**
- Usage statistics with provider/account/model breakdown
- Time range filtering for analytics
- Batch authentication file upload
- Runtime configuration updates
- OAuth flow management

See [management-api.yaml](https://nghyane.github.io/llm-mux/#/management-api.yaml) for full specification.

## License

MIT
