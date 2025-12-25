# Providers

llm-mux supports multiple AI providers through OAuth authentication. Login once and your tokens are securely stored locally.

## Supported Providers

| Provider | Command | Subscription Required |
|----------|---------|----------------------|
| [Google Gemini](#google-gemini) | `--antigravity-login` | Google One AI Premium or Free Tier |
| [Claude](#claude) | `--claude-login` | Claude Pro / Max |
| [GitHub Copilot](#github-copilot) | `--copilot-login` | GitHub Copilot subscription |
| [OpenAI Codex](#openai-codex) | `--codex-login` | ChatGPT Plus/Pro (GPT-5 access) |
| [Qwen](#qwen) | `--qwen-login` | Alibaba Cloud account |
| [Kiro](#kiro) | `--kiro-login` | AWS/Amazon Q Developer |
| [Cline](#cline) | `--cline-login` | Cline subscription |
| [iFlow](#iflow) | `--iflow-login` | iFlow account |

---

## Google Gemini

Access Gemini models through Google One AI Premium or free tier.

```bash
llm-mux --antigravity-login
```

**Available Models:**
- `gemini-2.5-pro`
- `gemini-2.5-flash`
- `gemini-3-pro-preview`

**Alternative login (legacy):**
```bash
llm-mux --login
```

---

## Claude

Access Claude models through Anthropic's Claude Pro/Max subscription.

```bash
llm-mux --claude-login
```

**Available Models:**
- `claude-sonnet-4-20250514`
- `claude-opus-4-5-20251101`
- `claude-3-5-sonnet-20241022`

---

## GitHub Copilot

Access GPT models through GitHub Copilot subscription.

```bash
llm-mux --copilot-login
```

This uses GitHub's device flow authentication:
1. Run the command
2. Copy the code displayed
3. Open the URL in your browser
4. Paste the code to authorize

**Available Models:**
- `gpt-4.1`
- `gpt-4o`
- `gpt-5`
- `gpt-5-mini`
- `gpt-5.1`
- `gpt-5.2`

---

## OpenAI Codex

Access GPT-5 series through ChatGPT Plus/Pro subscription.

```bash
llm-mux --codex-login
```

**Note:** GPT-5 models may require specific subscription tiers.

---

## Qwen

Access Alibaba Cloud's Qwen models.

```bash
llm-mux --qwen-login
```

---

## Kiro

Access Amazon Q Developer (formerly CodeWhisperer).

```bash
llm-mux --kiro-login
```

This uses a refresh token flow:
1. Run the command
2. Follow prompts to enter your refresh token

---

## Cline

Access Cline API integration.

```bash
llm-mux --cline-login
```

This uses a refresh token exported from VS Code:
1. Open VS Code with Cline extension
2. Export refresh token from Cline settings
3. Run the command and paste the token

See [Cline Integration Guide](integrations/cline.md) for details.

---

## iFlow

Access iFlow integration.

```bash
# OAuth login
llm-mux --iflow-login

# Or use cookie-based auth
llm-mux --iflow-cookie
```

---

## Vertex AI

Import a Google Cloud service account for Vertex AI access.

```bash
llm-mux --vertex-import /path/to/service-account.json
```

This imports the service account key and configures Vertex AI access.

---

## Multiple Accounts

Login multiple times with different accounts to enable load balancing:

```bash
# Login with multiple Google accounts
llm-mux --antigravity-login  # Account 1
llm-mux --antigravity-login  # Account 2

# Login with multiple providers
llm-mux --claude-login
llm-mux --copilot-login
```

llm-mux automatically:
- Rotates requests across accounts
- Handles quota limits by switching accounts
- Retries failed requests on alternate accounts

---

## Login Options

| Flag | Description |
|------|-------------|
| `--no-browser` | Don't auto-open browser during OAuth |

Example:
```bash
llm-mux --claude-login --no-browser
# Manually open the displayed URL
```

---

## Token Storage

OAuth tokens are stored in `~/.config/llm-mux/auth/`:

```
~/.config/llm-mux/auth/
├── antigravity/
│   └── token_xxxxx.json
├── claude/
│   └── token_xxxxx.json
└── copilot/
    └── token_xxxxx.json
```

Tokens are automatically refreshed before expiration.

---

## Check Available Models

After logging in, verify available models:

```bash
curl http://localhost:8317/v1/models
```

Response shows all models from your authenticated providers.
