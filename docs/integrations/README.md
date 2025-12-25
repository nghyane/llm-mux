# Integrations

Setup guides for popular AI tools and editors.

## Quick Setup

All integrations use the same configuration:

| Setting | Value |
|---------|-------|
| **Base URL** | `http://localhost:8317/v1` |
| **API Key** | `unused` (or any string) |

---

## Editors & IDEs

### Cursor

1. Open **Settings** → **Models**
2. Enable **OpenAI API Key**
3. Set Base URL: `http://localhost:8317/v1`
4. Enter any API key (e.g., `unused`)
5. Select a model from the dropdown or enter: `gemini-2.5-pro`

### VS Code + Continue

1. Install [Continue](https://marketplace.visualstudio.com/items?itemName=Continue.continue) extension
2. Open Continue settings (`~/.continue/config.json`)
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
2. Open Cline settings
3. Set:
   - **API Provider**: OpenAI Compatible
   - **Base URL**: `http://localhost:8317/v1`
   - **API Key**: `unused`
   - **Model**: `claude-sonnet-4-20250514`

See [Cline detailed guide](cline.md).

### VS Code + Roo Code

1. Install Roo Code extension
2. Configure:
   - **Provider**: OpenAI Compatible
   - **Base URL**: `http://localhost:8317/v1`
   - **Model ID**: `gemini-2.5-pro`

### JetBrains IDEs

For IntelliJ, PyCharm, WebStorm, etc:

1. Install AI Assistant or compatible plugin
2. Configure OpenAI-compatible endpoint
3. Set Base URL: `http://localhost:8317/v1`

---

## CLI Tools

### Aider

```bash
aider --openai-api-base http://localhost:8317/v1 \
      --openai-api-key unused \
      --model gemini-2.5-pro
```

Or set environment variables:

```bash
export OPENAI_API_BASE=http://localhost:8317/v1
export OPENAI_API_KEY=unused
aider --model gemini-2.5-pro
```

### Claude Code (claude-cli)

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=unused
claude
```

### OpenAI CLI

```bash
export OPENAI_API_BASE=http://localhost:8317/v1
export OPENAI_API_KEY=unused
openai api chat.completions.create -m gemini-2.5-pro -g user "Hello"
```

### LangChain CLI

```bash
export OPENAI_API_BASE=http://localhost:8317/v1
export OPENAI_API_KEY=unused
langchain run your-chain
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
