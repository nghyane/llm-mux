package management

import (
	"context"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/auth/vertex"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/util"
)

// ImportVertexCredential handles uploading a Vertex service account JSON and saving it as an auth record.
func (h *Handler) ImportVertexCredential(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	if h.cfg.AuthDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth directory not configured"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		return
	}

	var serviceAccount map[string]any
	if err := json.Unmarshal(data, &serviceAccount); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "message": err.Error()})
		return
	}

	normalizedSA, err := vertex.NormalizeServiceAccountMap(serviceAccount)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid service account", "message": err.Error()})
		return
	}
	serviceAccount = normalizedSA

	projectID := strings.TrimSpace(valueAsString(serviceAccount["project_id"]))
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project_id missing"})
		return
	}
	email := strings.TrimSpace(valueAsString(serviceAccount["client_email"]))

	location := strings.TrimSpace(c.PostForm("location"))
	if location == "" {
		location = strings.TrimSpace(c.Query("location"))
	}
	if location == "" {
		location = "us-central1"
	}

	fileName := fmt.Sprintf("vertex-%s.json", util.SanitizeFilePart(projectID))
	label := util.LabelForVertex(projectID, email)
	storage := &vertex.VertexCredentialStorage{
		ServiceAccount: serviceAccount,
		ProjectID:      projectID,
		Email:          email,
		Location:       location,
		Type:           "vertex",
	}
	metadata := map[string]any{
		"service_account": serviceAccount,
		"project_id":      projectID,
		"email":           email,
		"location":        location,
		"type":            "vertex",
		"label":           label,
	}
	record := &provider.Auth{
		ID:       fileName,
		Provider: "vertex",
		FileName: fileName,
		Storage:  storage,
		Label:    label,
		Metadata: metadata,
	}

	ctx := context.Background()
	if reqCtx := c.Request.Context(); reqCtx != nil {
		ctx = reqCtx
	}
	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"auth-file":  savedPath,
		"project_id": projectID,
		"email":      email,
		"location":   location,
	})
}

func valueAsString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
