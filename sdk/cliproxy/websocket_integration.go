package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/watcher"
	"github.com/nghyane/llm-mux/internal/wsrelay"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// WebsocketCallbacks provide handlers for websocket lifecycle events.
type WebsocketCallbacks struct {
	OnConnected    func(string)
	OnDisconnected func(string, error)
}

// createWebsocketCallbacks creates websocket event handlers that integrate with auth system.
func createWebsocketCallbacks(coreManager *coreauth.Manager, emitAuthUpdate func(context.Context, watcher.AuthUpdate)) WebsocketCallbacks {
	return WebsocketCallbacks{
		OnConnected: func(channelID string) {
			wsOnConnected(channelID, coreManager, emitAuthUpdate)
		},
		OnDisconnected: func(channelID string, reason error) {
			wsOnDisconnected(channelID, reason, emitAuthUpdate)
		},
	}
}

// wsOnConnected handles websocket provider connection events.
func wsOnConnected(channelID string, coreManager *coreauth.Manager, emitAuthUpdate func(context.Context, watcher.AuthUpdate)) {
	if channelID == "" {
		return
	}
	if !strings.HasPrefix(strings.ToLower(channelID), "aistudio-") {
		return
	}
	if coreManager != nil {
		if existing, ok := coreManager.GetByID(channelID); ok && existing != nil {
			if !existing.Disabled && existing.Status == coreauth.StatusActive {
				return
			}
		}
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         channelID,  // keep channel identifier as ID
		Provider:   "aistudio", // logical provider for switch routing
		Label:      channelID,  // display original channel id
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"email": channelID}, // metadata drives logging and usage tracking
	}
	log.Infof("websocket provider connected: %s", channelID)
	emitAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	})
}

// wsOnDisconnected handles websocket provider disconnection events.
func wsOnDisconnected(channelID string, reason error, emitAuthUpdate func(context.Context, watcher.AuthUpdate)) {
	if channelID == "" {
		return
	}
	if reason != nil {
		if strings.Contains(reason.Error(), "replaced by new connection") {
			log.Infof("websocket provider replaced: %s", channelID)
			return
		}
		log.Warnf("websocket provider disconnected: %s (%v)", channelID, reason)
	} else {
		log.Infof("websocket provider disconnected: %s", channelID)
	}
	ctx := context.Background()
	emitAuthUpdate(ctx, watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionDelete,
		ID:     channelID,
	})
}

// createWebsocketGateway creates and configures the websocket gateway.
func createWebsocketGateway(callbacks WebsocketCallbacks) *wsrelay.Manager {
	opts := wsrelay.Options{
		Path:           "/v1/ws",
		OnConnected:    callbacks.OnConnected,
		OnDisconnected: callbacks.OnDisconnected,
		LogDebugf:      log.Debugf,
		LogInfof:       log.Infof,
		LogWarnf:       log.Warnf,
	}
	return wsrelay.NewManager(opts)
}
