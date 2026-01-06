package ollama

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/api/handlers/format"
	"github.com/nghyane/llm-mux/internal/api/handlers/format/openai"
	"github.com/nghyane/llm-mux/internal/constant"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/tidwall/gjson"
)

const (
	OllamaVersion = "0.12.10"
)

type OllamaAPIHandler struct {
	*format.BaseAPIHandler
}

func NewOllamaAPIHandler(apiHandlers *format.BaseAPIHandler) *OllamaAPIHandler {
	return &OllamaAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

func (h *OllamaAPIHandler) HandlerType() string {
	return constant.Ollama
}

func (h *OllamaAPIHandler) Models() []map[string]any {
	return registry.GetGlobalRegistry().GetAvailableModels("openai")
}

func (h *OllamaAPIHandler) Version(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))
	c.JSON(http.StatusOK, gin.H{
		"version": OllamaVersion,
	})
}

func (h *OllamaAPIHandler) Tags(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	modelRegistry := registry.GetGlobalRegistry()
	allModels := modelRegistry.GetAvailableModels("openai")

	ollamaModels := make([]map[string]any, 0)
	for _, model := range allModels {
		modelID := ""
		if id, ok := model["id"].(string); ok {
			modelID = id
		} else if idVal := model["id"]; idVal != nil {
			modelID = fmt.Sprintf("%v", idVal)
		}

		if modelID == "" {
			continue
		}

		// Remove "models/" prefix if present
		modelID = strings.TrimPrefix(modelID, "models/")

		ollamaModels = append(ollamaModels, map[string]any{
			"name":        modelID,
			"model":       modelID,
			"modified_at": time.Now().UTC().Format(time.RFC3339),
			"size":        0,
			"digest":      "",
			"details": map[string]any{
				"parent_model":       "",
				"format":             "gguf",
				"family":             "Ollama",
				"families":           []string{"Ollama"},
				"parameter_size":     "0B",
				"quantization_level": "Q4_0",
			},
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"models": ollamaModels,
	})
}

func (h *OllamaAPIHandler) Show(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	var requestBody map[string]any
	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	modelName := ""
	if name, ok := requestBody["name"].(string); ok {
		modelName = name
	} else if name, ok := requestBody["model"].(string); ok {
		modelName = name
	}

	if modelName == "" {
		modelName = "unknown"
	}

	// Generate Ollama show response
	showResponse := from_ir.ToOllamaShowResponse(modelName)
	c.Data(http.StatusOK, "application/json", showResponse)
}

func (h *OllamaAPIHandler) Chat(c *gin.Context) {
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

	// Parse Ollama request
	ollamaRequest := gjson.ParseBytes(rawJSON)
	stream := ollamaRequest.Get("stream").Bool()

	// Extract model name
	modelName := ollamaRequest.Get("model").String()
	if modelName == "" {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: "model is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Convert Ollama request to OpenAI format using new IR translator
	irReq, err := to_ir.ParseOllamaRequest(rawJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Failed to parse request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	irReq.Model = modelName

	openaiRequest, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert request: %v", err),
				Type:    "server_error",
			},
		})
		return
	}

	// Use OpenAI handler to process the request
	openaiHandler := openai.NewOpenAIAPIHandler(h.BaseAPIHandler)

	if stream {
		h.handleOllamaChatStream(c, openaiHandler, openaiRequest, modelName)
	} else {
		h.handleOllamaChatNonStream(c, openaiHandler, openaiRequest, modelName)
	}
}

func (h *OllamaAPIHandler) Generate(c *gin.Context) {
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

	// Parse Ollama request
	ollamaRequest := gjson.ParseBytes(rawJSON)
	stream := ollamaRequest.Get("stream").Bool()

	// Extract model name
	modelName := ollamaRequest.Get("model").String()
	if modelName == "" {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: "model is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Convert Ollama request to OpenAI format using new IR translator
	irReq, err := to_ir.ParseOllamaRequest(rawJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Failed to parse request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	irReq.Model = modelName

	openaiRequest, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert request: %v", err),
				Type:    "server_error",
			},
		})
		return
	}

	// Use OpenAI handler to process the request
	openaiHandler := openai.NewOpenAIAPIHandler(h.BaseAPIHandler)

	if stream {
		h.handleOllamaGenerateStream(c, openaiHandler, openaiRequest, modelName)
	} else {
		h.handleOllamaGenerateNonStream(c, openaiHandler, openaiRequest, modelName)
	}
}

func (h *OllamaAPIHandler) handleOllamaChatStream(c *gin.Context, _ *openai.OpenAIAPIHandler, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Transfer-Encoding", "chunked")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get the http.Flusher interface to manually flush the response
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

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	defer func() {
		cliCancel(nil)
	}()

	// Execute streaming request using OpenAI handler's method
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, constant.OpenAI, modelName, openaiRequest, h.GetAlt(c))

	// Process streaming chunks
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream ended, send final chunk with done: true
				finalChunk, _ := from_ir.OpenAIChunkToOllamaChat([]byte("[DONE]"), modelName)
				if len(finalChunk) > 0 {
					c.Writer.Write(finalChunk)
					flusher.Flush()
				}
				return
			}

			// Convert OpenAI chunk to Ollama format
			// Remove "data: " prefix if present (SSE format)
			chunkData := chunk
			if bytes.HasPrefix(chunkData, []byte("data: ")) {
				chunkData = bytes.TrimSpace(chunkData[6:])
			}

			// Skip empty chunks or [DONE] markers (we'll handle [DONE] separately)
			if len(chunkData) == 0 || bytes.Equal(chunkData, []byte("[DONE]")) {
				continue
			}

			ollamaChunk, err := from_ir.OpenAIChunkToOllamaChat(chunkData, modelName)
			if err == nil && len(ollamaChunk) > 0 {
				c.Writer.Write(ollamaChunk)
				flusher.Flush()
			}
		case errMsg, ok := <-errChan:
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
			cliCancel(execErr)
			return
		}
	}
}

func (h *OllamaAPIHandler) handleOllamaChatNonStream(c *gin.Context, _ *openai.OpenAIAPIHandler, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	defer func() {
		cliCancel(nil)
	}()

	// Execute non-streaming request
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, constant.OpenAI, modelName, openaiRequest, h.GetAlt(c))
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Convert OpenAI response to Ollama format
	ollamaResponse, err := from_ir.OpenAIToOllamaChat(resp, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert response: %v", err),
				Type:    "server_error",
			},
		})
		cliCancel(err)
		return
	}

	c.Data(http.StatusOK, "application/json", ollamaResponse)
	cliCancel()
}

func (h *OllamaAPIHandler) handleOllamaGenerateStream(c *gin.Context, _ *openai.OpenAIAPIHandler, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Transfer-Encoding", "chunked")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get the http.Flusher interface to manually flush the response
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

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	defer func() {
		cliCancel(nil)
	}()

	// Execute streaming request using OpenAI handler's method
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, constant.OpenAI, modelName, openaiRequest, h.GetAlt(c))

	// Process streaming chunks
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream ended, send final chunk with done: true
				finalChunk, _ := from_ir.OpenAIChunkToOllamaGenerate([]byte("[DONE]"), modelName)
				if len(finalChunk) > 0 {
					c.Writer.Write(finalChunk)
					flusher.Flush()
				}
				return
			}

			// Convert OpenAI chunk to Ollama format
			// Remove "data: " prefix if present (SSE format)
			chunkData := chunk
			if bytes.HasPrefix(chunkData, []byte("data: ")) {
				chunkData = bytes.TrimSpace(chunkData[6:])
			}

			// Skip empty chunks or [DONE] markers (we'll handle [DONE] separately)
			if len(chunkData) == 0 || bytes.Equal(chunkData, []byte("[DONE]")) {
				continue
			}

			ollamaChunk, err := from_ir.OpenAIChunkToOllamaGenerate(chunkData, modelName)
			if err == nil && len(ollamaChunk) > 0 {
				c.Writer.Write(ollamaChunk)
				flusher.Flush()
			}
		case errMsg, ok := <-errChan:
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
			cliCancel(execErr)
			return
		}
	}
}

func (h *OllamaAPIHandler) handleOllamaGenerateNonStream(c *gin.Context, _ *openai.OpenAIAPIHandler, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	defer func() {
		cliCancel(nil)
	}()

	// Execute non-streaming request
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, constant.OpenAI, modelName, openaiRequest, h.GetAlt(c))
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Convert OpenAI response to Ollama generate format
	ollamaResponse, err := from_ir.OpenAIToOllamaGenerate(resp, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, format.ErrorResponse{
			Error: format.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert response: %v", err),
				Type:    "server_error",
			},
		})
		cliCancel(err)
		return
	}

	c.Data(http.StatusOK, "application/json", ollamaResponse)
	cliCancel()
}
