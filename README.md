# llm-mux

[![GitHub stars](https://img.shields.io/github/stars/nghyane/llm-mux?style=social)](https://github.com/nghyane/llm-mux)
[![GitHub release](https://img.shields.io/github/v/release/nghyane/llm-mux)](https://github.com/nghyane/llm-mux/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)](https://github.com/nghyane/llm-mux)

**Turn your existing AI subscriptions into standard API endpoints.**  
A local gateway that enables your Claude Pro, GitHub Copilot, and Google Gemini subscriptions to work with **any** AI tool.

> **Universal Compatibility:** Supports OpenAI, Anthropic (Claude), Google (Gemini), and Ollama API formats.
> **Zero Overhead:** No separate API billing. No per-token charges. Runs locally.

---

## Prerequisites

- **macOS** 12+ / **Linux** (Ubuntu 20.04+, Debian 11+) / **Windows** 10+
- Active subscription to at least one supported provider (Google One AI Premium, Claude Pro/Max, GitHub Copilot, etc.)
- No additional dependencies required for pre-built binaries

---

## ⚡️ Quick Install

No dependencies required. These scripts download the pre-compiled binary for your architecture.

### macOS / Linux
```bash
curl -fsSL https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh | bash
```

### Windows (PowerShell)
```powershell
irm https://raw.githubusercontent.com/nghyane/llm-mux/main/install.ps1 | iex
```

---

## 🚀 Getting Started

### 1. Initialize
Run this once to set up the configuration directory:
```bash
llm-mux --init
```
> This creates `~/.config/llm-mux/` with default settings.

### 2. Authenticate Providers
Login to the services you have subscriptions for. This opens your browser to cache OAuth tokens locally.

```bash
# For Google Gemini (Free Tier or Advanced)
llm-mux --antigravity-login

# For Claude Pro / Max
llm-mux --claude-login

# For GitHub Copilot
llm-mux --copilot-login
```

<details>
<summary><strong>View other login commands (OpenAI Codex, Qwen, Amazon Q)</strong></summary>

| Provider | Command | Description |
|:---|:---|:---|
| **OpenAI Codex** | `llm-mux --codex-login` | Access GPT-5 series (if eligible) |
| **Qwen** | `llm-mux --qwen-login` | Alibaba Cloud Qwen models |
| **Kiro** | `llm-mux --kiro-login` | AWS/Amazon Q Developer |
| **Cline** | `llm-mux --cline-login` | Cline API integration |
| **iFlow** | `llm-mux --iflow-login` | iFlow integration |

</details>

### 3. Start the Server
```bash
llm-mux
```
> The server runs on `http://localhost:8317` by default. To change the port, edit `port: 8317` in `~/.config/llm-mux/config.yaml`.

### 4. Verify
Check if the server is running and models are available:
```bash
curl http://localhost:8317/v1/models
```

---

## 🛠 Integration Guide

Point your tools to the local proxy. 
**Base URL:** `http://localhost:8317/v1`  
**API Key:** `any-string` (unused, but required by some clients)

> **Security Note:** llm-mux is designed for **local use only**. If exposing to a network, configure proper authentication in `~/.config/llm-mux/config.yaml`.

### Cursor
1. Go to **Settings** > **Models**.
2. Toggle "OpenAI API Base URL" to ON.
3. Enter: `http://localhost:8317/v1`

### VS Code (Cline / Roo Code)
1. **API Provider:** OpenAI Compatible
2. **Base URL:** `http://localhost:8317/v1`
3. **Model ID:** `claude-sonnet-4-20250514` (or any available model)

### Aider / Claude Code
```bash
# Using Claude Sonnet via llm-mux
aider --openai-api-base http://localhost:8317/v1 --model claude-sonnet-4-20250514
```

### Python (OpenAI SDK)
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8317/v1",
    api_key="unused"
)

response = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)
```

---

## 🤖 Supported Models

llm-mux automatically maps your subscription access to these model identifiers.

| Provider | Top Models |
|:---|:---|
| **Google** | `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-3-pro-preview` |
| **Anthropic** | `claude-sonnet-4-20250514`, `claude-opus-4-5-20251101`, `claude-3-5-sonnet-20241022` |
| **GitHub** | `gpt-4.1`, `gpt-4o`, `gpt-5`, `gpt-5-mini`, `gpt-5.1`, `gpt-5.2` |

> **Note:** Run `curl http://localhost:8317/v1/models` to see the exact list available to your account.

---

## Architecture

llm-mux is a **universal AI API gateway** that translates between different LLM API formats and routes requests to your authenticated provider backends through an **Intermediate Representation (IR)** layer.

```mermaid
flowchart TB
    subgraph Inbound["Inbound Protocols"]
        OpenAI["OpenAI"]
        Anthropic["Anthropic"]
        GeminiIn["Gemini"]
        Ollama["Ollama"]
    end

    subgraph IR["Intermediate Representation (IR)"]
        ToIR["to_ir/ normalize"]
        Unified["UnifiedChatRequest"]
        Registry["Model Registry"]
        Select["Provider Selection"]
        FromIR["from_ir/ convert"]
    end

    subgraph Outbound["Outbound Providers (13+)"]
        Gemini["Gemini"]
        Antigravity["Antigravity"]
        Vertex["Vertex AI"]
        AIStudio["AI Studio"]
        Claude["Claude"]
        Codex["Codex"]
        Copilot["Copilot"]
        Qwen["Qwen"]
        Kiro["Kiro"]
        Others["iFlow / Cline / ..."]
    end

    OpenAI --> ToIR
    Anthropic --> ToIR
    GeminiIn --> ToIR
    Ollama --> ToIR

    ToIR --> Unified --> Registry --> Select --> FromIR

    FromIR --> Gemini
    FromIR --> Antigravity
    FromIR --> Vertex
    FromIR --> AIStudio
    FromIR --> Claude
    FromIR --> Codex
    FromIR --> Copilot
    FromIR --> Qwen
    FromIR --> Kiro
    FromIR --> Others
```

### Key Components

| Component | Description |
|:---|:---|
| **Model Registry** | Reference-counted model tracking with canonical ID mapping across providers |
| **Auth Manager** | Multi-account credential rotation with automatic token refresh |
| **Load Balancer** | Provider failover with exponential backoff retry and quota tracking |
| **IR Translator** | Normalizes tool calls, thinking tokens, MCP servers, and streaming events |

---

## 🔌 Supported API Protocols

You can use `llm-mux` as a drop-in replacement for these providers without changing your client code structure.

| Protocol | Endpoint | Use Case |
|:---|:---|:---|
| **OpenAI** | `POST /v1/chat/completions` | Cursor, VS Code, LangChain |
| **Anthropic** | `POST /v1/messages` | Native Anthropic SDKs, Claude-native tools |
| **Gemini** | `POST /v1beta/models/...` | Google AI Studio tools, Vertex AI SDKs |
| **Ollama** | `POST /api/chat` | WebUIs built for Ollama |

---

## ⚙️ Advanced Configuration

<details>
<summary><strong>Load Balancing & Multiple Accounts</strong></summary>

You can login multiple times with different accounts to distribute load and increase quotas.
```bash
llm-mux --antigravity-login   # Google Account 1
llm-mux --antigravity-login   # Google Account 2
llm-mux --claude-login        # Claude Account 1
```
The system will automatically rotate requests and handle failovers.
</details>

<details>
<summary><strong>Docker Usage</strong></summary>

If you prefer containerization:
```bash
docker run -p 8317:8317 -v ~/.config/llm-mux:/root/.config/llm-mux nghyane/llm-mux
```
</details>

<details>
<summary><strong>Custom Proxies</strong></summary>

Edit `~/.config/llm-mux/config.yaml` to add upstream proxies:
```yaml
proxy-url: "socks5://user:pass@proxy.example.com:1080"
```
</details>

---

## Troubleshooting

<details>
<summary><strong>Port already in use (EADDRINUSE)</strong></summary>

```bash
# Find process using port 8317
lsof -i :8317
# Kill the process or change port in config.yaml
```
</details>

<details>
<summary><strong>No models available</strong></summary>

- Ensure you've completed authentication for at least one provider
- Check if tokens are valid: re-run the login command (e.g., `llm-mux --claude-login`)
- Verify with `curl http://localhost:8317/v1/models`
</details>

<details>
<summary><strong>Authentication token expired</strong></summary>

Tokens are automatically refreshed, but if issues persist:
```bash
# Re-authenticate the affected provider
llm-mux --antigravity-login  # For Gemini
llm-mux --claude-login       # For Claude
```
</details>

<details>
<summary><strong>Connection refused / Server not running</strong></summary>

Ensure the server is running in a separate terminal:
```bash
llm-mux
```
Check if the server started successfully and is listening on the expected port.
</details>

---

## License
MIT License — see [LICENSE](LICENSE)
