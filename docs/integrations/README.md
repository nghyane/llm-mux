# Integrations

Setup guides for popular AI tools and editors.

## Quick Setup

All integrations use the same configuration:

| Setting | Value |
|---------|-------|
| **Base URL** | `http://localhost:8317/v1` (OpenAI) or `http://localhost:8317` (Anthropic/Gemini) |
| **API Key** | `unused` (or any string) |

---

## Editors & IDEs

### Cursor

1. Open **Settings** → **Models**
2. Enable **OpenAI API Key**
3. Set Base URL: `http://localhost:8317/v1`
4. Enter any API key (e.g., `unused`)
5. Select a model from the dropdown or enter: `gemini-2.5-pro`

**Note:** Overriding Base URL applies to all OpenAI models in Cursor.

### VS Code + Continue

1. Install [Continue](https://marketplace.visualstudio.com/items?itemName=Continue.continue) extension
2. Open Continue settings (`Ctrl+Shift+P` → "Continue: Open Config")
3. Add configuration:

```json
{
  "models": [{
    "title": "llm-mux",
    "provider": "openai",
    "model": "gemini-2.5-pro",
    "apiBase": "http://localhost:8317/v1",
    "apiKey": "unused"
  }]
}
```

### VS Code + Cline

1. Install [Cline](https://marketplace.visualstudio.com/items?itemName=saoudrizwan.claude-dev) extension
2. Open Cline settings (gear icon in Cline panel)
3. Set:
   - **API Provider**: OpenAI Compatible
   - **Base URL**: `http://localhost:8317/v1`
   - **API Key**: `unused`
   - **Model**: `claude-sonnet-4-20250514`

See [Cline detailed guide](cline.md).

### Zed

1. Open Assistant Panel (`Cmd+Shift+A`)
2. Click Configure (gear icon)
3. Add OpenAI Compatible provider
4. Or edit `settings.json`:

```json
{
  "language_models": {
    "openai_compatible": [{
      "name": "llm-mux",
      "url": "http://localhost:8317/v1",
      "api_key": "unused",
      "available_models": [
        {"name": "gemini-2.5-pro"},
        {"name": "claude-sonnet-4"}
      ]
    }]
  }
}
```

---

## CLI Tools

### OpenCode

OpenCode supports custom providers via `opencode.json`. Create or edit `~/.config/opencode/opencode.json`:

```json
{
  "model": "llm-mux/claude-sonnet-4",
  "provider": {
    "llm-mux": {
      "npm": "@ai-sdk/anthropic",
      "name": "LLM-Mux",
      "options": {
        "baseURL": "http://localhost:8317/v1",
        "apiKey": "unused"
      },
      "models": {
        "claude-sonnet-4": {
          "id": "claude-sonnet-4",
          "name": "Claude Sonnet 4",
          "tool_call": true,
          "reasoning": true
        },
        "gemini-2.5-pro": {
          "id": "gemini-2.5-pro",
          "name": "Gemini 2.5 Pro",
          "tool_call": true
        }
      }
    }
  }
}
```

For Gemini native format, use `@ai-sdk/google` with `/v1beta`:

```json
{
  "provider": {
    "llm-mux-gemini": {
      "npm": "@ai-sdk/google",
      "options": {
        "baseURL": "http://localhost:8317/v1beta",
        "apiKey": "unused"
      }
    }
  }
}
```

### Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=unused
claude
```

### Codex CLI

```bash
export OPENAI_BASE_URL=http://localhost:8317/v1
export OPENAI_API_KEY=unused
codex
```

### Gemini CLI

```bash
export GOOGLE_GEMINI_BASE_URL=http://localhost:8317
gemini
```

---

## Frameworks

### LangChain (Python)

```python
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    base_url="http://localhost:8317/v1",
    api_key="unused",
    model="gemini-2.5-pro"
)

response = llm.invoke("Hello!")
```

### LlamaIndex

```python
from llama_index.llms.openai import OpenAI

llm = OpenAI(
    api_base="http://localhost:8317/v1",
    api_key="unused",
    model="gemini-2.5-pro"
)
```

### Vercel AI SDK

```typescript
import { openai } from '@ai-sdk/openai';
import { generateText } from 'ai';

const result = await generateText({
  model: openai('gemini-2.5-pro', {
    baseURL: 'http://localhost:8317/v1',
  }),
  prompt: 'Hello!',
});
```

---

## Web UIs

### Open WebUI

```bash
docker run -d \
  -p 3000:8080 \
  -e OPENAI_API_BASE_URL=http://host.docker.internal:8317/v1 \
  -e OPENAI_API_KEY=unused \
  ghcr.io/open-webui/open-webui:main
```

### LibreChat

Add to `librechat.yaml`:

```yaml
endpoints:
  custom:
    - name: "llm-mux"
      apiKey: "unused"
      baseURL: "http://localhost:8317/v1"
      models:
        default: ["gemini-2.5-pro", "claude-sonnet-4-20250514"]
```

### Ollama WebUI (compatible)

Any Ollama-compatible UI works with:

```
Base URL: http://localhost:8317/api
```

---

## Test Integration

```bash
curl http://localhost:8317/v1/models
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "Hi"}]}'
```

See [Troubleshooting](../troubleshooting.md) for common issues.
