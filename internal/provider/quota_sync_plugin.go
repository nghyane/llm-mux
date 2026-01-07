package provider

import (
	"context"

	"github.com/nghyane/llm-mux/internal/usage"
)

// QuotaSyncPlugin syncs usage data to QuotaManager for selection decisions
type QuotaSyncPlugin struct {
	manager *QuotaManager
}

// NewQuotaSyncPlugin creates a plugin that syncs usage to quota manager
func NewQuotaSyncPlugin(manager *QuotaManager) *QuotaSyncPlugin {
	return &QuotaSyncPlugin{manager: manager}
}

func (p *QuotaSyncPlugin) HandleUsage(ctx context.Context, record usage.Record) {
	if p.manager == nil {
		return
	}

	var tokens int64
	if record.Usage != nil {
		tokens = record.Usage.TotalTokens
	}

	p.manager.RecordRequestEnd(record.AuthID, record.Provider, tokens, record.Failed)
}
