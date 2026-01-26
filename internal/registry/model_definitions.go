// Package registry provides model definitions for various AI service providers.
// This file contains static model definitions that can be used by clients
// when registering their supported models.
package registry

// GetClaudeModels returns the standard Claude model definitions
func GetClaudeModels() []*ModelInfo {
	return []*ModelInfo{
		Claude("claude-haiku-4-5-20251001").Display("Claude 4.5 Haiku").Created(1759276800).Context(200000, 64000).B(),
		Claude("claude-sonnet-4-5-20250929").Display("Claude 4.5 Sonnet").Created(1759104000).Canonical("claude-sonnet-4-5").Context(200000, 64000).B(),
		Claude("claude-sonnet-4-5-thinking").Display("Claude 4.5 Sonnet Thinking").Created(1759104000).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-sonnet-4-5-thinking-low").Display("Claude 4.5 Sonnet Thinking Low").Created(1759104000).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-sonnet-4-5-thinking-medium").Display("Claude 4.5 Sonnet Thinking Medium").Created(1759104000).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-sonnet-4-5-thinking-high").Display("Claude 4.5 Sonnet Thinking High").Created(1759104000).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-opus-4-5-thinking").Display("Claude 4.5 Opus Thinking").Created(1761955200).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-opus-4-5-thinking-low").Display("Claude 4.5 Opus Thinking Low").Created(1761955200).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-opus-4-5-thinking-medium").Display("Claude 4.5 Opus Thinking Medium").Created(1761955200).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-opus-4-5-thinking-high").Display("Claude 4.5 Opus Thinking High").Created(1761955200).Context(200000, 64000).Thinking(8192, 32768).B(),
		Claude("claude-opus-4-5-20251101").Display("Claude 4.5 Opus").Desc("Premium model combining maximum intelligence with practical performance").Created(1761955200).Canonical("claude-opus-4-5").Context(200000, 64000).B(),
		Claude("claude-opus-4-1-20250805").Display("Claude 4.1 Opus").Created(1722945600).Context(200000, 32000).B(),
		Claude("claude-opus-4-20250514").Display("Claude 4 Opus").Created(1715644800).Canonical("claude-opus-4").Context(200000, 32000).B(),
		Claude("claude-sonnet-4-20250514").Display("Claude 4 Sonnet").Created(1715644800).Canonical("claude-sonnet-4").Context(200000, 64000).B(),
		Claude("claude-3-7-sonnet-20250219").Display("Claude 3.7 Sonnet").Created(1708300800).Context(128000, 8192).B(),
		Claude("claude-3-5-haiku-20241022").Display("Claude 3.5 Haiku").Created(1729555200).Context(128000, 8192).B(),
	}
}

// GetOpenAIModels returns the standard OpenAI model definitions
func GetOpenAIModels() []*ModelInfo {
	return []*ModelInfo{
		OpenAI("gpt-5").Display("GPT 5").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1754524800).Version("gpt-5-2025-08-07").B(),
		OpenAI("gpt-5-minimal").Display("GPT 5 Minimal").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1754524800).Version("gpt-5-2025-08-07").B(),
		OpenAI("gpt-5-low").Display("GPT 5 Low").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1754524800).Version("gpt-5-2025-08-07").B(),
		OpenAI("gpt-5-medium").Display("GPT 5 Medium").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1754524800).Version("gpt-5-2025-08-07").B(),
		OpenAI("gpt-5-high").Display("GPT 5 High").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1754524800).Version("gpt-5-2025-08-07").B(),
		OpenAI("gpt-5-codex").Display("GPT 5 Codex").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1757894400).Version("gpt-5-2025-09-15").B(),
		OpenAI("gpt-5-codex-low").Display("GPT 5 Codex Low").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1757894400).Version("gpt-5-2025-09-15").B(),
		OpenAI("gpt-5-codex-medium").Display("GPT 5 Codex Medium").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1757894400).Version("gpt-5-2025-09-15").B(),
		OpenAI("gpt-5-codex-high").Display("GPT 5 Codex High").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1757894400).Version("gpt-5-2025-09-15").B(),
		OpenAI("gpt-5-codex-mini").Display("GPT 5 Codex Mini").Desc("Stable version of GPT 5 Codex Mini: cheaper, faster, but less capable version of GPT 5 Codex.").Created(1762473600).Version("gpt-5-2025-11-07").B(),
		OpenAI("gpt-5-codex-mini-medium").Display("GPT 5 Codex Mini Medium").Desc("Stable version of GPT 5 Codex Mini: cheaper, faster, but less capable version of GPT 5 Codex.").Created(1762473600).Version("gpt-5-2025-11-07").B(),
		OpenAI("gpt-5-codex-mini-high").Display("GPT 5 Codex Mini High").Desc("Stable version of GPT 5 Codex Mini: cheaper, faster, but less capable version of GPT 5 Codex.").Created(1762473600).Version("gpt-5-2025-11-07").B(),
		OpenAI("gpt-5.1").Display("GPT 5").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-none").Display("GPT 5 Low").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-low").Display("GPT 5 Low").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-medium").Display("GPT 5 Medium").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-high").Display("GPT 5 High").Desc("Stable version of GPT 5, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex").Display("GPT 5 Codex").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-low").Display("GPT 5 Codex Low").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-medium").Display("GPT 5 Codex Medium").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-high").Display("GPT 5 Codex High").Desc("Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-mini").Display("GPT 5 Codex Mini").Desc("Stable version of GPT 5 Codex Mini: cheaper, faster, but less capable version of GPT 5 Codex.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-mini-medium").Display("GPT 5 Codex Mini Medium").Desc("Stable version of GPT 5 Codex Mini: cheaper, faster, but less capable version of GPT 5 Codex.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-mini-high").Display("GPT 5 Codex Mini High").Desc("Stable version of GPT 5 Codex Mini: cheaper, faster, but less capable version of GPT 5 Codex.").Created(1762905600).Version("gpt-5.1-2025-11-12").B(),
		OpenAI("gpt-5.1-codex-max").Display("GPT 5 Codex Max").Desc("Stable version of GPT 5 Codex Max").Created(1763424000).Version("gpt-5.1-max").B(),
		OpenAI("gpt-5.1-codex-max-low").Display("GPT 5 Codex Max Low").Desc("Stable version of GPT 5 Codex Max Low").Created(1763424000).Version("gpt-5.1-max").B(),
		OpenAI("gpt-5.1-codex-max-medium").Display("GPT 5 Codex Max Medium").Desc("Stable version of GPT 5 Codex Max Medium").Created(1763424000).Version("gpt-5.1-max").B(),
		OpenAI("gpt-5.1-codex-max-high").Display("GPT 5 Codex Max High").Desc("Stable version of GPT 5 Codex Max High").Created(1763424000).Version("gpt-5.1-max").B(),
		OpenAI("gpt-5.1-codex-max-xhigh").Display("GPT 5 Codex Max XHigh").Desc("Stable version of GPT 5 Codex Max XHigh").Created(1763424000).Version("gpt-5.1-max").B(),
	}
}

// GetQwenModels returns the standard Qwen model definitions
func GetQwenModels() []*ModelInfo {
	return []*ModelInfo{
		Qwen("qwen3-coder-plus").Display("Qwen3 Coder Plus").Desc("Advanced code generation and understanding model").Created(1753228800).Version("3.0").Context(32768, 8192).B(),
		Qwen("qwen3-coder-flash").Display("Qwen3 Coder Flash").Desc("Fast code generation model").Created(1753228800).Version("3.0").Context(8192, 2048).B(),
		Qwen("vision-model").Display("Qwen3 Vision Model").Desc("Vision model model").Created(1758672000).Version("3.0").Context(32768, 2048).B(),
	}
}

// GetIFlowModels returns supported models for iFlow OAuth accounts.
func GetIFlowModels() []*ModelInfo {
	return []*ModelInfo{
		IFlow("tstars2.0").Display("TStars-2.0").Desc("iFlow TStars-2.0 multimodal assistant").Created(1746489600).B(),
		IFlow("qwen3-coder-plus").Display("Qwen3-Coder-Plus").Desc("Qwen3 Coder Plus code generation").Created(1753228800).B(),
		IFlow("qwen3-max").Display("Qwen3-Max").Desc("Qwen3 flagship model").Created(1758672000).B(),
		IFlow("qwen3-vl-plus").Display("Qwen3-VL-Plus").Desc("Qwen3 multimodal vision-language").Created(1758672000).B(),
		IFlow("qwen3-max-preview").Display("Qwen3-Max-Preview").Desc("Qwen3 Max preview build").Created(1757030400).B(),
		IFlow("kimi-k2-0905").Display("Kimi-K2-Instruct-0905").Desc("Moonshot Kimi K2 instruct 0905").Created(1757030400).B(),
		IFlow("glm-4.6").Display("GLM-4.6").Desc("Zhipu GLM 4.6 general model").Created(1759190400).B(),
		IFlow("kimi-k2").Display("Kimi-K2").Desc("Moonshot Kimi K2 general model").Created(1752192000).B(),
		IFlow("kimi-k2-thinking").Display("Kimi-K2-Thinking").Desc("Moonshot Kimi K2 general model").Created(1762387200).B(),
		IFlow("deepseek-v3.2-chat").Display("DeepSeek-V3.2").Desc("DeepSeek V3.2").Created(1764576000).B(),
		IFlow("deepseek-v3.2").Display("DeepSeek-V3.2-Exp").Desc("DeepSeek V3.2 experimental").Created(1759104000).B(),
		IFlow("deepseek-v3.1").Display("DeepSeek-V3.1-Terminus").Desc("DeepSeek V3.1 Terminus").Created(1756339200).B(),
		IFlow("deepseek-r1").Display("DeepSeek-R1").Desc("DeepSeek reasoning model R1").Created(1737331200).B(),
		IFlow("deepseek-v3").Display("DeepSeek-V3-671B").Desc("DeepSeek V3 671B").Created(1734307200).B(),
		IFlow("qwen3-32b").Display("Qwen3-32B").Desc("Qwen3 32B").Created(1747094400).B(),
		IFlow("qwen3-235b-a22b-thinking-2507").Display("Qwen3-235B-A22B-Thinking").Desc("Qwen3 235B A22B Thinking (2507)").Created(1753401600).B(),
		IFlow("qwen3-235b-a22b-instruct").Display("Qwen3-235B-A22B-Instruct").Desc("Qwen3 235B A22B Instruct").Created(1753401600).B(),
		IFlow("qwen3-235b").Display("Qwen3-235B-A22B").Desc("Qwen3 235B A22B").Created(1753401600).B(),
		IFlow("minimax-m2").Display("MiniMax-M2").Desc("MiniMax M2").Created(1758672000).B(),
	}
}

// GetClineModels returns the standard Cline model definitions
func GetClineModels() []*ModelInfo {
	return []*ModelInfo{
		Cline("minimax/minimax-m2").Display("MiniMax M2").Desc("MiniMax M2 via Cline").Created(1758672000).B(),
		Cline("x-ai/grok-code-fast-1").Display("Grok Code Fast 1").Desc("X.AI Grok Code Fast 1 via Cline").Created(1735689600).B(),
	}
}

// GetGitHubCopilotModels returns models via GitHub Copilot API (Priority=2 fallback)
func GetGitHubCopilotModels() []*ModelInfo {
	return []*ModelInfo{
		// OpenAI models via GitHub Copilot
		Copilot("gpt-4.1").Display("GPT-4.1").Desc("OpenAI GPT-4.1 via GitHub Copilot").Created(1754524800).B(),
		Copilot("gpt-4o").Display("GPT-4o").Desc("OpenAI GPT-4o via GitHub Copilot").Created(1715558400).B(),
		Copilot("gpt-5").Display("GPT-5").Desc("OpenAI GPT-5 via GitHub Copilot").Created(1762473600).B(),
		Copilot("gpt-5-mini").Display("GPT-5 Mini").Desc("OpenAI GPT-5 Mini via GitHub Copilot").Created(1762473600).B(),
		Copilot("gpt-5.1").Display("GPT-5.1").Desc("OpenAI GPT-5.1 via GitHub Copilot").Created(1763424000).B(),
		Copilot("gpt-5.2").Display("GPT-5.2").Desc("OpenAI GPT-5.2 via GitHub Copilot").Created(1763424000).B(),
		// Claude models via GitHub Copilot
		Copilot("claude-sonnet-4").Display("Claude Sonnet 4").Desc("Anthropic Claude Sonnet 4 via GitHub Copilot").Created(1763424000).B(),
		Copilot("claude-sonnet-4.5").Display("Claude Sonnet 4.5").Desc("Anthropic Claude Sonnet 4.5 via GitHub Copilot").Created(1763424000).B(),
		Copilot("claude-haiku-4.5").Display("Claude Haiku 4.5").Desc("Anthropic Claude Haiku 4.5 via GitHub Copilot").Created(1763424000).B(),
		Copilot("claude-opus-4.5").Display("Claude Opus 4.5").Desc("Anthropic Claude Opus 4.5 via GitHub Copilot").Created(1763424000).B(),
		// Google models via GitHub Copilot
		Copilot("gemini-2.5-pro").Display("Gemini 2.5 Pro").Desc("Google Gemini 2.5 Pro via GitHub Copilot").Created(1763424000).B(),
		Copilot("gemini-3-flash").Display("Gemini 3 Flash").Desc("Google Gemini 3 Flash via GitHub Copilot").Created(1763424000).B(),
		Copilot("gemini-3-pro-preview").Display("Gemini 3 Pro Preview").Desc("Google Gemini 3 Pro Preview via GitHub Copilot").Created(1763424000).B(),
		// xAI models via GitHub Copilot
		Copilot("grok-code-fast-1").Display("Grok Code Fast 1").Desc("xAI Grok Code Fast 1 via GitHub Copilot").Created(1763424000).B(),
	}
}

// GetKiroModels returns the standard Kiro (Amazon Q / CodeWhisperer) model definitions
func GetKiroModels() []*ModelInfo {
	return []*ModelInfo{
		// Primary models from Kiro MODEL_MAPPING
		Kiro("claude-sonnet-4-5").Display("Claude Sonnet 4.5").Desc("Claude Sonnet 4.5 via Kiro/Amazon Q").Created(1758672000).B(),
		Kiro("claude-sonnet-4-5-20250929").Display("Claude Sonnet 4.5 (20250929)").Desc("Claude Sonnet 4.5 via Kiro/Amazon Q").Created(1758672000).Canonical("claude-sonnet-4-5").B(),
		Kiro("claude-sonnet-4-20250514").Display("Claude Sonnet 4 (20250514)").Desc("Claude Sonnet 4 via Kiro/Amazon Q").Created(1747267200).Canonical("claude-sonnet-4").B(),
		Kiro("claude-3-7-sonnet-20250219").Display("Claude 3.7 Sonnet").Desc("Claude 3.7 Sonnet via Kiro/Amazon Q").Created(1739923200).B(),
		// Amazon Q prefixed aliases
		Kiro("amazonq-claude-sonnet-4-20250514").Display("Amazon Q Claude Sonnet 4").Desc("Claude Sonnet 4 via Amazon Q").Created(1747267200).Canonical("claude-sonnet-4").B(),
		Kiro("amazonq-claude-3-7-sonnet-20250219").Display("Amazon Q Claude 3.7 Sonnet").Desc("Claude 3.7 Sonnet via Amazon Q").Created(1739923200).B(),
		// Additional Claude models available via Kiro
		Kiro("claude-4-sonnet").Display("Claude 4 Sonnet").Desc("Claude 4 Sonnet via Kiro/Amazon Q").Created(1747267200).Canonical("claude-sonnet-4").B(),
		Kiro("claude-opus-4-20250514").Display("Claude 4 Opus").Desc("Claude 4 Opus via Kiro/Amazon Q").Created(1747267200).Canonical("claude-opus-4").B(),
		Kiro("claude-opus-4-5-20251101").Display("Claude 4.5 Opus").Desc("Claude 4.5 Opus via Kiro/Amazon Q").Created(1761955200).Canonical("claude-opus-4-5").B(),
		Kiro("claude-3-5-sonnet-20241022").Display("Claude 3.5 Sonnet").Desc("Claude 3.5 Sonnet via Kiro/Amazon Q").Created(1729555200).B(),
		Kiro("claude-3-5-haiku-20241022").Display("Claude 3.5 Haiku").Desc("Claude 3.5 Haiku via Kiro/Amazon Q").Created(1729555200).B(),
	}
}
