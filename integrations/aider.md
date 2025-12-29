# Aider Integration

[Aider](https://aider.chat) is an AI pair programming tool that works with llm-mux.

## Setup

### Option 1: Environment Variables

```bash
export OPENAI_API_BASE=http://localhost:8317/v1
export OPENAI_API_KEY=unused
aider --model gemini-2.5-pro
```

### Option 2: Command Line Flags

```bash
aider --openai-api-base http://localhost:8317/v1 \
      --openai-api-key unused \
      --model gemini-2.5-pro
```

### Option 3: Config File

Create or edit `~/.aider.conf.yml`:

```yaml
openai-api-base: http://localhost:8317/v1
openai-api-key: unused
model: gemini-2.5-pro
```

---

## Model Selection

Aider supports multiple models. Use any model available from your authenticated providers:

```bash
# Gemini
aider --model gemini-2.5-pro

# Claude (via llm-mux)
aider --model claude-sonnet-4

# GPT via Copilot
aider --model gpt-4o
```

---

## Verify Connection

```bash
# Check available models
curl http://localhost:8317/v1/models

# Start aider
aider --model gemini-2.5-pro
```

---

## Troubleshooting

| Issue | Solution |
|-------|----------|
| "Model not found" | Check `curl localhost:8317/v1/models` for available models |
| Connection refused | Ensure llm-mux is running: `llm-mux` or check service status |
| Rate limited | Add more accounts or wait for quota reset |

---

## Advanced: Custom Models

For models with special names (aliases), use the exact name from `/v1/models`:

```bash
aider --model "llama-3.3-70b-versatile"
```

See [Configuration](../configuration.md#providers) for setting up model aliases.
