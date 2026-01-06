package executor

import (
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ApplyPayloadConfig(cfg *config.Config, model string, payload []byte) []byte {
	return applyPayloadConfigWithRoot(cfg, model, "", "", payload)
}

// ApplyPayloadConfigWithRoot is an exported version for sub-packages.
func ApplyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload []byte) []byte {
	return applyPayloadConfigWithRoot(cfg, model, protocol, root, payload)
}

func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload []byte) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	rules := cfg.Payload
	if len(rules.Default) == 0 && len(rules.Override) == 0 {
		return payload
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return payload
	}
	out := payload

	for i := range rules.Default {
		rule := &rules.Default[i]
		if !payloadRuleMatchesModel(rule, model, protocol) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(out, fullPath).Exists() {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}

	for i := range rules.Override {
		rule := &rules.Override[i]
		if !payloadRuleMatchesModel(rule, model, protocol) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	return out
}

func payloadRuleMatchesModel(rule *config.PayloadRule, model, protocol string) bool {
	if rule == nil {
		return false
	}
	if len(rule.Models) == 0 {
		return false
	}
	for _, entry := range rule.Models {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
			continue
		}
		if matchModelPattern(name, model) {
			return true
		}
	}
	return false
}

func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	p = strings.TrimPrefix(p, ".")
	return r + "." + p
}

func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}

	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
