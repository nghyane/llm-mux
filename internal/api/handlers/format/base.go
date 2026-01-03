package format

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/interfaces"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/gjson"
)

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

type BaseAPIHandler struct {
	AuthManager           *provider.Manager
	Cfg                   *config.SDKConfig
	Routing               *config.RoutingConfig
	OpenAICompatProviders []string
}

func NewBaseAPIHandlers(cfg *config.SDKConfig, routing *config.RoutingConfig, authManager *provider.Manager, openAICompatProviders []string) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:                   cfg,
		Routing:               routing,
		AuthManager:           authManager,
		OpenAICompatProviders: openAICompatProviders,
	}
}

func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) { h.Cfg = cfg }

func (h *BaseAPIHandler) UpdateRouting(routing *config.RoutingConfig) { h.Routing = routing }

func (h *BaseAPIHandler) getFallbackChain(model string) []string {
	if h.Routing == nil {
		return nil
	}
	return h.Routing.GetFallbackChain(model)
}

// Models returns all available models as maps from the global registry.
func (h *BaseAPIHandler) Models() []map[string]any {
	return registry.GetGlobalRegistry().GetAvailableModels("openai")
}

func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	alt, hasAlt := c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	newCtx, cancel := context.WithCancel(ctx)
	newCtx = context.WithValue(newCtx, ctxKeyGin, c)
	newCtx = context.WithValue(newCtx, ctxKeyHandler, handler)
	return newCtx, func(params ...any) {
		if h.Cfg.RequestLog && len(params) == 1 {
			switch data := params[0].(type) {
			case []byte:
				appendAPIResponse(c, data)
			case error:
				appendAPIResponse(c, []byte(data.Error()))
			case string:
				appendAPIResponse(c, []byte(data))
			}
		}
		cancel()
	}
}

// Context keys to avoid string allocation on each request
type ctxKey int

const (
	ctxKeyGin ctxKey = iota
	ctxKeyHandler
)

func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}
	c.Set("API_RESPONSE", bytes.Clone(data))
}

// buildRequestOpts creates request and options, cloning payload/metadata only once (shared reference)
func buildRequestOpts(normalizedModel string, rawJSON []byte, metadata map[string]any, handlerType string, alt string, stream bool) (provider.Request, provider.Options) {
	payload := cloneBytes(rawJSON)
	meta := cloneMetadata(metadata)

	sourceFormat := provider.Format(handlerType)

	req := provider.Request{
		Model:    normalizedModel,
		Payload:  payload,
		Metadata: meta,
	}
	opts := provider.Options{
		Stream:          stream,
		Alt:             alt,
		OriginalRequest: payload, // Same slice, no second clone
		SourceFormat:    sourceFormat,
		Metadata:        meta, // Same map, no second clone
	}
	return req, opts
}

// extractErrorDetails extracts status code and headers from error interface
func extractErrorDetails(err error) (int, http.Header) {
	status := http.StatusInternalServerError
	if se, ok := err.(interface{ StatusCode() int }); ok {
		if code := se.StatusCode(); code > 0 {
			status = code
		}
	}
	var addon http.Header
	if he, ok := err.(interface{ Headers() http.Header }); ok {
		if hdr := he.Headers(); hdr != nil {
			addon = hdr.Clone()
		}
	}
	return status, addon
}

func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	req, opts := buildRequestOpts(normalizedModel, rawJSON, metadata, handlerType, alt, false)
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err == nil {
		return resp.Payload, nil
	}

	fallbacks := h.getFallbackChain(normalizedModel)
	for _, fallbackModel := range fallbacks {
		fbProviders, fbNormalizedModel, fbMetadata, _ := h.getRequestDetails(fallbackModel)
		if len(fbProviders) == 0 {
			continue
		}
		fbReq, fbOpts := buildRequestOpts(fbNormalizedModel, rawJSON, fbMetadata, handlerType, alt, false)
		fbResp, fbErr := h.AuthManager.Execute(ctx, fbProviders, fbReq, fbOpts)
		if fbErr == nil {
			return fbResp.Payload, nil
		}
	}

	status, addon := extractErrorDetails(err)
	return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
}

func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	req, opts := buildRequestOpts(normalizedModel, rawJSON, metadata, handlerType, alt, false)
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		status, addon := extractErrorDetails(err)
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	return resp.Payload, nil
}

func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, errChan
	}
	req, opts := buildRequestOpts(normalizedModel, rawJSON, metadata, handlerType, alt, true)
	chunks, err := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	if err == nil {
		return h.wrapStreamChannel(chunks)
	}

	fallbacks := h.getFallbackChain(normalizedModel)
	for _, fallbackModel := range fallbacks {
		fbProviders, fbNormalizedModel, fbMetadata, _ := h.getRequestDetails(fallbackModel)
		if len(fbProviders) == 0 {
			continue
		}
		fbReq, fbOpts := buildRequestOpts(fbNormalizedModel, rawJSON, fbMetadata, handlerType, alt, true)
		fbChunks, fbErr := h.AuthManager.ExecuteStream(ctx, fbProviders, fbReq, fbOpts)
		if fbErr == nil {
			return h.wrapStreamChannel(fbChunks)
		}
	}

	errChan := make(chan *interfaces.ErrorMessage, 1)
	status, addon := extractErrorDetails(err)
	errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	close(errChan)
	return nil, errChan
}

func (h *BaseAPIHandler) wrapStreamChannel(chunks <-chan provider.StreamChunk) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	dataChan := make(chan []byte, 32) // Match upstream RunSSEStream buffer size
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		for chunk := range chunks {
			if chunk.Err != nil {
				status, addon := extractErrorDetails(chunk.Err)
				errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: chunk.Err, Addon: addon}
				return
			}
			if len(chunk.Payload) > 0 {
				// Check if payload is an error message
				if bytes.HasPrefix(chunk.Payload, []byte("data: {\"error\":")) {
					// Extract JSON part after "data: "
					jsonStart := bytes.Index(chunk.Payload, []byte("data: "))
					if jsonStart >= 0 {
						jsonData := chunk.Payload[jsonStart+6:] // Skip "data: "
						// Remove trailing \n\n
						jsonData = bytes.TrimSuffix(jsonData, []byte("\n\n"))
						if gjson.ValidBytes(jsonData) {
							if msg := gjson.GetBytes(jsonData, "error.message"); msg.Exists() {
								err := fmt.Errorf("streaming error: %s", msg.String())
								status, addon := extractErrorDetails(err)
								errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
								return
							}
						}
					}
					// Fallback
					err := fmt.Errorf("streaming error")
					status, addon := extractErrorDetails(err)
					errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
					return
				}
				dataChan <- chunk.Payload // No clone needed, executor already owns this
			}
		}
	}()
	return dataChan, errChan
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, metadata map[string]any, err *interfaces.ErrorMessage) {
	resolvedModelName := util.ResolveAutoModel(modelName)
	specifiedProvider := util.ExtractProviderFromPrefixedModelID(resolvedModelName)
	cleanModelName := util.NormalizeIncomingModelID(resolvedModelName)

	if h.Routing != nil {
		cleanModelName = h.Routing.ResolveModelAlias(cleanModelName)
	}

	providerName, extractedModelName, isDynamic := h.parseDynamicModel(cleanModelName)
	normalizedModel, metadata = util.NormalizeGeminiThinkingModel(cleanModelName)

	if isDynamic {
		providers = []string{providerName}
		normalizedModel = extractedModelName
	} else if specifiedProvider != "" {
		providers = []string{specifiedProvider}
	} else {
		// GetProviderName uses canonical index for cross-provider routing
		// Translation happens in executeWithProvider via GetModelIDForProvider
		providers = util.GetProviderName(normalizedModel)
	}

	if len(providers) == 0 {
		return nil, "", nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}
	return providers, normalizedModel, metadata, nil
}

func (h *BaseAPIHandler) parseDynamicModel(modelName string) (providerName, model string, isDynamic bool) {
	if parts := strings.SplitN(modelName, "://", 2); len(parts) == 2 {
		for _, pName := range h.OpenAICompatProviders {
			if pName == parts[0] {
				return parts[0], parts[1], true
			}
		}
	}
	return "", modelName, false
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	return bytes.Clone(src)
}

func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.Addon != nil {
		for key, values := range msg.Addon {
			if len(values) == 0 {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}
	c.Status(status)
	if msg != nil && msg.Error != nil {
		_, _ = c.Writer.Write([]byte(msg.Error.Error()))
	} else {
		_, _ = c.Writer.Write([]byte(http.StatusText(status)))
	}
}

func (h *BaseAPIHandler) LoggingAPIResponseError(ctx context.Context, err *interfaces.ErrorMessage) {
	if !h.Cfg.RequestLog {
		return
	}
	ginContext, ok := ctx.Value(ctxKeyGin).(*gin.Context)
	if !ok {
		return
	}
	if apiResponseErrors, isExist := ginContext.Get("API_RESPONSE_ERROR"); isExist {
		if slices, isOk := apiResponseErrors.([]*interfaces.ErrorMessage); isOk {
			ginContext.Set("API_RESPONSE_ERROR", append(slices, err))
			return
		}
	}
	ginContext.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{err})
}

type APIHandlerCancelFunc func(params ...any)
