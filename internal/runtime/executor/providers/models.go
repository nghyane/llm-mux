package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/tidwall/gjson"
)

const (
	cloudCodeModelsPath = "/v1internal:fetchAvailableModels"
	glAPIModelsPath     = "/v1beta/models"
	// GLAPIModelsPath is exported for use in sub-packages.
	GLAPIModelsPath = glAPIModelsPath
)

type ModelAliasFunc func(upstreamName string) string

func DefaultGeminiAlias(upstreamName string) string {
	return upstreamName
}

type CloudCodeFetchConfig struct {
	BaseURLs     []string
	Token        string
	ProviderType string
	UserAgent    string
	Host         string
	AliasFunc    ModelAliasFunc
}

func FetchCloudCodeModels(ctx context.Context, httpClient *http.Client, cfg CloudCodeFetchConfig) []*registry.ModelInfo {
	if cfg.Token == "" || len(cfg.BaseURLs) == 0 {
		return nil
	}

	aliasFunc := cfg.AliasFunc
	if aliasFunc == nil {
		aliasFunc = DefaultGeminiAlias
	}

	handler := executor.NewRetryHandler(executor.AntigravityRetryConfig())

	for idx := 0; idx < len(cfg.BaseURLs); idx++ {
		baseURL := cfg.BaseURLs[idx]
		hasNext := idx+1 < len(cfg.BaseURLs)

		modelsURL := baseURL + cloudCodeModelsPath
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader([]byte(`{}`)))
		if err != nil {
			log.Errorf("%s: failed to create models request: %v", cfg.ProviderType, err)
			return nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+cfg.Token)
		if cfg.UserAgent != "" {
			httpReq.Header.Set("User-Agent", cfg.UserAgent)
		}
		if cfg.Host != "" {
			httpReq.Host = cfg.Host
		}

		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				log.Errorf("%s: request timed out", cfg.ProviderType)
				return nil
			}
			action, _ := handler.HandleError(ctx, err, hasNext)
			if action == executor.RetryActionContinueNext {
				log.Debugf("%s: models request error on %s, retrying with fallback", cfg.ProviderType, baseURL)
				continue
			}
			return nil
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("%s: close response body error: %v", cfg.ProviderType, errClose)
		}
		if errRead != nil {
			if hasNext {
				log.Debugf("%s: models read error on %s, retrying with fallback", cfg.ProviderType, baseURL)
				continue
			}
			return nil
		}

		action, _ := handler.HandleResponse(ctx, httpResp.StatusCode, bodyBytes, hasNext)
		if action == executor.RetryActionContinueNext {
			log.Debugf("%s: models request status %d on %s, trying next", cfg.ProviderType, httpResp.StatusCode, baseURL)
			continue
		}
		if action != executor.RetryActionSuccess {
			return nil
		}

		return ParseCloudCodeModels(bodyBytes, cfg.ProviderType, aliasFunc)
	}
	return nil
}

func ParseCloudCodeModels(body []byte, providerType string, aliasFunc ModelAliasFunc) []*registry.ModelInfo {
	result := gjson.GetBytes(body, "models")
	if !result.Exists() {
		return nil
	}

	if aliasFunc == nil {
		aliasFunc = DefaultGeminiAlias
	}

	now := time.Now().Unix()
	models := make([]*registry.ModelInfo, 0, len(result.Map()))

	for originalName := range result.Map() {
		aliasName := aliasFunc(originalName)
		if aliasName == "" {
			continue
		}

		modelInfo := &registry.ModelInfo{
			ID:           aliasName,
			Name:         aliasName,
			Object:       "model",
			Created:      now,
			OwnedBy:      providerType,
			Type:         providerType,
			UpstreamName: originalName,
		}

		registry.ApplyGeminiMeta(modelInfo)
		models = append(models, modelInfo)
	}
	return models
}

type GLAPIFetchConfig struct {
	BaseURL      string
	APIKey       string
	Bearer       string
	ProviderType string
}

func FetchGLAPIModels(ctx context.Context, httpClient *http.Client, cfg GLAPIFetchConfig) []*registry.ModelInfo {
	if cfg.APIKey == "" && cfg.Bearer == "" {
		return nil
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = executor.GeminiDefaultBaseURL
	}

	modelsURL := baseURL + glAPIModelsPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		log.Errorf("%s: failed to create models request: %v", cfg.ProviderType, err)
		return nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", cfg.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.Bearer)
	}

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Errorf("%s: request timed out", cfg.ProviderType)
			return nil
		}
		log.Errorf("%s: models request error: %v", cfg.ProviderType, err)
		return nil
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		log.Errorf("%s: models request failed with status %d", cfg.ProviderType, httpResp.StatusCode)
		return nil
	}

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		log.Errorf("%s: failed to read models response: %v", cfg.ProviderType, err)
		return nil
	}

	return ParseGLAPIModels(bodyBytes, cfg.ProviderType)
}

func ParseGLAPIModels(body []byte, providerType string) []*registry.ModelInfo {
	modelsArray := gjson.GetBytes(body, "models")
	if !modelsArray.Exists() || !modelsArray.IsArray() {
		return nil
	}

	now := time.Now().Unix()
	var models []*registry.ModelInfo

	modelsArray.ForEach(func(_, value gjson.Result) bool {
		fullName := value.Get("name").String()
		modelID := strings.TrimPrefix(fullName, "models/")
		if modelID == "" {
			return true
		}

		if !strings.HasPrefix(modelID, "gemini-") {
			return true
		}

		displayName := value.Get("displayName").String()
		description := value.Get("description").String()
		version := value.Get("version").String()
		inputTokenLimit := value.Get("inputTokenLimit").Int()
		outputTokenLimit := value.Get("outputTokenLimit").Int()

		modelInfo := &registry.ModelInfo{
			ID:               modelID,
			Name:             modelID,
			Object:           "model",
			Created:          now,
			OwnedBy:          providerType,
			Type:             providerType,
			DisplayName:      displayName,
			Description:      description,
			Version:          version,
			InputTokenLimit:  int(inputTokenLimit),
			OutputTokenLimit: int(outputTokenLimit),
		}

		registry.ApplyGeminiMeta(modelInfo)

		models = append(models, modelInfo)
		return true
	})

	return models
}
