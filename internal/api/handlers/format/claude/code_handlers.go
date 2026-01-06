package claude

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/api/handlers/format"
	"github.com/nghyane/llm-mux/internal/constant"
	"github.com/nghyane/llm-mux/internal/interfaces"
	"github.com/nghyane/llm-mux/internal/json"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/tidwall/gjson"
)

type ClaudeCodeAPIHandler struct {
	*format.BaseAPIHandler
}

func NewClaudeCodeAPIHandler(apiHandlers *format.BaseAPIHandler) *ClaudeCodeAPIHandler {
	return &ClaudeCodeAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

func (h *ClaudeCodeAPIHandler) HandlerType() string {
	return constant.Claude
}

func (h *ClaudeCodeAPIHandler) Models() []map[string]any {
	return registry.GetGlobalRegistry().GetAvailableModels("claude")
}

func (h *ClaudeCodeAPIHandler) ClaudeMessages(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	streamResult := gjson.GetBytes(rawJSON, "stream")
	if !streamResult.Exists() || streamResult.Type == gjson.False {
		h.handleNonStreamingResponse(c, rawJSON)
	} else {
		h.handleStreamingResponse(c, rawJSON)
	}
}

func (h *ClaudeCodeAPIHandler) ClaudeCountTokens(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *ClaudeCodeAPIHandler) ClaudeModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": h.Models(),
	})
}

func (h *ClaudeCodeAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Decompress gzipped responses - Claude API sometimes returns gzip without Content-Encoding header
	// This fixes title generation and other non-streaming responses that arrive compressed
	if len(resp) >= 2 && resp[0] == 0x1f && resp[1] == 0x8b {
		gr := executor.GzipReaderPool.Get().(*gzip.Reader)
		if err := gr.Reset(bytes.NewReader(resp)); err != nil {
			executor.GzipReaderPool.Put(gr)
			log.Warnf("failed to reset gzip reader: %v", err)
		} else {
			defer func() {
				gr.Close()
				executor.GzipReaderPool.Put(gr)
			}()
			if decompressed, err := io.ReadAll(gr); err != nil {
				log.Warnf("failed to read decompressed Claude response: %v", err)
			} else {
				resp = decompressed
			}
		}
	}

	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *ClaudeCodeAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

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

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	h.forwardClaudeStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
}

func (h *ClaudeCodeAPIHandler) forwardClaudeStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
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
			if len(chunk) > 0 {
				_, _ = c.Writer.Write(chunk)
				flusher.Flush()
			}

		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				// An error occurred: emit as a proper SSE error event
				errorBytes, _ := json.Marshal(h.toClaudeError(errMsg))
				_, _ = c.Writer.WriteString("event: error\n")
				_, _ = c.Writer.WriteString("data: ")
				_, _ = c.Writer.Write(errorBytes)
				_, _ = c.Writer.WriteString("\n\n")
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

type claudeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type claudeErrorResponse struct {
	Type  string            `json:"type"`
	Error claudeErrorDetail `json:"error"`
}

func (h *ClaudeCodeAPIHandler) toClaudeError(msg *interfaces.ErrorMessage) claudeErrorResponse {
	return claudeErrorResponse{
		Type: "error",
		Error: claudeErrorDetail{
			Type:    "api_error",
			Message: msg.Error.Error(),
		},
	}
}
