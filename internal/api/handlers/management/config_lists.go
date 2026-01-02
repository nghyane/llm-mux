package management

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
)

// Generic helpers for list[string]
func (h *Handler) putStringList(c *gin.Context, set func([]string), after func()) {
	data, err := c.GetRawData()
	if err != nil {
		respondBadRequest(c, "failed to read body")
		return
	}
	var arr []string
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			respondBadRequest(c, "invalid body")
			return
		}
		arr = obj.Items
	}
	set(arr)
	if after != nil {
		after()
	}
	h.persist(c)
}

func (h *Handler) patchStringList(c *gin.Context, target *[]string, after func()) {
	var body struct {
		Old   *string `json:"old"`
		New   *string `json:"new"`
		Index *int    `json:"index"`
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondBadRequest(c, "invalid body")
		return
	}
	if body.Index != nil && body.Value != nil && *body.Index >= 0 && *body.Index < len(*target) {
		(*target)[*body.Index] = *body.Value
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	if body.Old != nil && body.New != nil {
		for i := range *target {
			if (*target)[i] == *body.Old {
				(*target)[i] = *body.New
				if after != nil {
					after()
				}
				h.persist(c)
				return
			}
		}
		*target = append(*target, *body.New)
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	respondBadRequest(c, "missing fields")
}

func (h *Handler) deleteFromStringList(c *gin.Context, target *[]string, after func()) {
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(*target) {
			*target = append((*target)[:idx], (*target)[idx+1:]...)
			if after != nil {
				after()
			}
			h.persist(c)
			return
		}
	}
	if val := strings.TrimSpace(c.Query("value")); val != "" {
		out := make([]string, 0, len(*target))
		for _, v := range *target {
			if strings.TrimSpace(v) != val {
				out = append(out, v)
			}
		}
		*target = out
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	respondBadRequest(c, "missing index or value")
}

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"api_keys": cfg.APIKeys})
}
func (h *Handler) PutAPIKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		h.cfg.APIKeys = append([]string(nil), v...)
		h.cfg.Access.Providers = nil
	}, nil)
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	h.patchStringList(c, &h.cfg.APIKeys, func() { h.cfg.Access.Providers = nil })
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.deleteFromStringList(c, &h.cfg.APIKeys, func() { h.cfg.Access.Providers = nil })
}

// oauth-excluded-models: map[string][]string
func (h *Handler) GetOAuthExcludedModels(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"oauth_excluded_models": config.NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)})
}

func (h *Handler) PutOAuthExcludedModels(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		respondBadRequest(c, "failed to read body")
		return
	}
	var entries map[string][]string
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			respondBadRequest(c, "invalid body")
			return
		}
		entries = wrapper.Items
	}
	h.cfgMu.Lock()
	h.cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(entries)
	h.cfgMu.Unlock()
	h.persist(c)
}

func (h *Handler) PatchOAuthExcludedModels(c *gin.Context) {
	var body struct {
		Provider *string  `json:"provider"`
		Models   []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Provider == nil {
		respondBadRequest(c, "invalid body")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(*body.Provider))
	if provider == "" {
		respondBadRequest(c, "invalid provider")
		return
	}
	normalized := config.NormalizeExcludedModels(body.Models)
	h.cfgMu.Lock()
	defer h.cfgMu.Unlock()
	if len(normalized) == 0 {
		if h.cfg.OAuthExcludedModels == nil {
			respondNotFound(c, "provider not found")
			return
		}
		if _, ok := h.cfg.OAuthExcludedModels[provider]; !ok {
			respondNotFound(c, "provider not found")
			return
		}
		delete(h.cfg.OAuthExcludedModels, provider)
		if len(h.cfg.OAuthExcludedModels) == 0 {
			h.cfg.OAuthExcludedModels = nil
		}
		h.persist(c)
		return
	}
	if h.cfg.OAuthExcludedModels == nil {
		h.cfg.OAuthExcludedModels = make(map[string][]string)
	}
	h.cfg.OAuthExcludedModels[provider] = normalized
	h.persist(c)
}

func (h *Handler) DeleteOAuthExcludedModels(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		respondBadRequest(c, "missing provider")
		return
	}
	h.cfgMu.Lock()
	defer h.cfgMu.Unlock()
	if h.cfg.OAuthExcludedModels == nil {
		respondNotFound(c, "provider not found")
		return
	}
	if _, ok := h.cfg.OAuthExcludedModels[provider]; !ok {
		respondNotFound(c, "provider not found")
		return
	}
	delete(h.cfg.OAuthExcludedModels, provider)
	if len(h.cfg.OAuthExcludedModels) == 0 {
		h.cfg.OAuthExcludedModels = nil
	}
	h.persist(c)
}

// providers: []Provider
func (h *Handler) GetProviders(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"providers": cfg.Providers})
}

func (h *Handler) PutProviders(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		respondBadRequest(c, "failed to read body")
		return
	}
	var arr []config.Provider
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.Provider `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			respondBadRequest(c, "invalid body")
			return
		}
		arr = obj.Items
	}
	h.cfgMu.Lock()
	h.cfg.Providers = config.SanitizeProviders(arr)
	h.cfgMu.Unlock()
	h.persist(c)
}

func (h *Handler) DeleteProvider(c *gin.Context) {
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		h.cfgMu.Lock()
		if err == nil && idx >= 0 && idx < len(h.cfg.Providers) {
			h.cfg.Providers = append(h.cfg.Providers[:idx], h.cfg.Providers[idx+1:]...)
			h.cfgMu.Unlock()
			h.persist(c)
			return
		}
		h.cfgMu.Unlock()
	}
	respondBadRequest(c, "missing or invalid index")
}
