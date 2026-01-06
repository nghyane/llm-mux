package gemini

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/api/handlers/format"
	"github.com/nghyane/llm-mux/internal/constant"
	"github.com/nghyane/llm-mux/internal/interfaces"
	"github.com/nghyane/llm-mux/internal/registry"
)

type GeminiAPIHandler struct {
	*format.BaseAPIHandler
}

func NewGeminiAPIHandler(apiHandlers *format.BaseAPIHandler) *GeminiAPIHandler {
	return &GeminiAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

func (h *GeminiAPIHandler) HandlerType() string {
	return constant.Gemini
}

func (h *GeminiAPIHandler) Models() []map[string]any {
	return registry.GetGlobalRegistry().GetAvailableModels("gemini")
}

func (h *GeminiAPIHandler) GeminiModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"models": h.Models(),
	})
}

func (h *GeminiAPIHandler) GeminiGetHandler(c *gin.Context) {
	var request struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&request); err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	switch request.Action {
	case "gemini-3-pro-preview":
		c.JSON(http.StatusOK, gin.H{
			"name":             "models/gemini-3-pro-preview",
			"version":          "3",
			"displayName":      "Gemini 3 Pro Preview",
			"description":      "Gemini 3 Pro Preview",
			"inputTokenLimit":  1048576,
			"outputTokenLimit": 65536,
			"supportedGenerationMethods": []string{
				"generateContent",
				"countTokens",
				"createCachedContent",
				"batchGenerateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		},
		)
	case "gemini-2.5-pro":
		c.JSON(http.StatusOK, gin.H{
			"name":             "models/gemini-2.5-pro",
			"version":          "2.5",
			"displayName":      "Gemini 2.5 Pro",
			"description":      "Stable release (June 17th, 2025) of Gemini 2.5 Pro",
			"inputTokenLimit":  1048576,
			"outputTokenLimit": 65536,
			"supportedGenerationMethods": []string{
				"generateContent",
				"countTokens",
				"createCachedContent",
				"batchGenerateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		},
		)
	case "gemini-2.5-flash":
		c.JSON(http.StatusOK, gin.H{
			"name":             "models/gemini-2.5-flash",
			"version":          "001",
			"displayName":      "Gemini 2.5 Flash",
			"description":      "Stable version of Gemini 2.5 Flash, our mid-size multimodal model that supports up to 1 million tokens, released in June of 2025.",
			"inputTokenLimit":  1048576,
			"outputTokenLimit": 65536,
			"supportedGenerationMethods": []string{
				"generateContent",
				"countTokens",
				"createCachedContent",
				"batchGenerateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		})
	case "gpt-5":
		c.JSON(http.StatusOK, gin.H{
			"name":             "gpt-5",
			"version":          "001",
			"displayName":      "GPT 5",
			"description":      "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			"inputTokenLimit":  400000,
			"outputTokenLimit": 128000,
			"supportedGenerationMethods": []string{
				"generateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		})
	default:
		c.JSON(http.StatusNotFound, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: "Not Found",
				Type:    "not_found",
			},
		})
	}
}

func (h *GeminiAPIHandler) GeminiHandler(c *gin.Context) {
	var request struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&request); err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	action := strings.Split(request.Action, ":")
	if len(action) != 2 {
		c.JSON(http.StatusNotFound, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("%s not found.", c.Request.URL.Path),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	method := action[1]
	rawJSON, _ := c.GetRawData()

	switch method {
	case "generateContent":
		h.handleGenerateContent(c, action[0], rawJSON)
	case "streamGenerateContent":
		h.handleStreamGenerateContent(c, action[0], rawJSON)
	case "countTokens":
		h.handleCountTokens(c, action[0], rawJSON)
	}
}

func (h *GeminiAPIHandler) handleStreamGenerateContent(c *gin.Context, modelName string, rawJSON []byte) {
	alt := h.GetAlt(c)

	if alt == "" {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	h.forwardGeminiStream(c, flusher, alt, func(err error) { cliCancel(err) }, dataChan, errChan)
}

func (h *GeminiAPIHandler) handleCountTokens(c *gin.Context, modelName string, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	resp, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *GeminiAPIHandler) handleGenerateContent(c *gin.Context, modelName string, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *GeminiAPIHandler) forwardGeminiStream(c *gin.Context, flusher http.Flusher, alt string, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if !ok {
				cancel(nil)
				return
			}
			if alt == "" {
				// Skip [DONE] markers for native Gemini format
				if bytes.Equal(chunk, []byte("data: [DONE]")) || bytes.Equal(chunk, []byte("[DONE]")) {
					continue
				}
				// Only add "data: " prefix if chunk doesn't already have it
				if !bytes.HasPrefix(chunk, []byte("data:")) {
					_, _ = c.Writer.Write([]byte("data: "))
				}
				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n\n"))
			} else {
				_, _ = c.Writer.Write(chunk)
			}
			flusher.Flush()
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				h.WriteErrorResponse(c, errMsg)
				flusher.Flush()
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		}
	}
}
