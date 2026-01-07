package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/nghyane/llm-mux/internal/json"
)

const (
	antigravityQuotaEndpoint = "https://cloudcode-pa.googleapis.com/v1internal:getQuotaInfo"
	quotaFetchTimeout        = 10 * time.Second
)

func fetchAntigravityQuota(ctx context.Context, accessToken string) *RealQuotaSnapshot {
	if accessToken == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, quotaFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, antigravityQuotaEndpoint, nil)
	if err != nil {
		return nil
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var quotaResp struct {
		RemainingFraction float64 `json:"remainingFraction"`
		ResetTime         string  `json:"resetTime"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&quotaResp); err != nil {
		return nil
	}

	snapshot := &RealQuotaSnapshot{
		RemainingFraction: quotaResp.RemainingFraction,
		FetchedAt:         time.Now(),
	}

	if quotaResp.ResetTime != "" {
		if t, err := time.Parse(time.RFC3339, quotaResp.ResetTime); err == nil {
			snapshot.WindowResetAt = t
		}
	}

	return snapshot
}
