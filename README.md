# llm-mux

**AI Gateway for Subscription-Based LLMs**

[![GitHub release](https://img.shields.io/github/v/release/nghyane/llm-mux)](https://github.com/nghyane/llm-mux/releases)
[![GitHub stars](https://img.shields.io/github/stars/nghyane/llm-mux)](https://github.com/nghyane/llm-mux/stargazers)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker](https://img.shields.io/docker/pulls/nghyane/llm-mux)](https://hub.docker.com/r/nghyane/llm-mux)
[![Docs](https://img.shields.io/badge/docs-online-blue)](https://nghyane.github.io/llm-mux/)

Turn your Claude Pro, GitHub Copilot, and Gemini subscriptions into standard LLM APIs. No API keys needed.

## Features

- **Multi-Provider** — Claude, Copilot, Gemini, Codex, Qwen, Kiro, and more
- **Multi-Format** — OpenAI, Anthropic, Gemini, Ollama compatible endpoints
- **Multi-Account** — Load balance across accounts, auto-retry on quota limits
- **Zero Config** — OAuth login, no API keys required

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
  -d '{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "Hello!"}]}'
```

Works with: **Cursor, Aider, Claude Code, Cline, Continue, OpenCode, LangChain, Open WebUI**, and any OpenAI/Anthropic/Gemini compatible tool.

## Documentation

📖 **https://nghyane.github.io/llm-mux/**

- [Installation](https://nghyane.github.io/llm-mux/#/installation) — Install, update, uninstall
- [Providers](https://nghyane.github.io/llm-mux/#/providers) — All providers and login commands
- [Configuration](https://nghyane.github.io/llm-mux/#/configuration) — Config file reference
- [Integrations](https://nghyane.github.io/llm-mux/#/integrations/) — Editor and framework setup
- [Docker](https://nghyane.github.io/llm-mux/#/docker) — Container deployment

## License

MIT
