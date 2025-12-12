package cliproxy

import (
	"context"

	"github.com/nghyane/llm-mux/internal/watcher"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// handleAuthUpdate applies an authentication update to the system.
func handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate, coreManager *coreauth.Manager, cfg interface{}, execRegistrar func(*coreauth.Auth), modelRegistrar func(*coreauth.Auth)) {
	if coreManager == nil {
		return
	}
	switch update.Action {
	case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
		if update.Auth == nil || update.Auth.ID == "" {
			return
		}
		applyCoreAuthAddOrUpdate(ctx, update.Auth, coreManager, execRegistrar, modelRegistrar)
	case watcher.AuthUpdateActionDelete:
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		if id == "" {
			return
		}
		applyCoreAuthRemoval(ctx, id, coreManager)
	default:
		log.Debugf("received unknown auth update action: %v", update.Action)
	}
}

// applyCoreAuthAddOrUpdate registers or updates an authentication entry.
func applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth, coreManager *coreauth.Manager, execRegistrar func(*coreauth.Auth), modelRegistrar func(*coreauth.Auth)) {
	if auth == nil || auth.ID == "" || coreManager == nil {
		return
	}

	auth = auth.Clone()
	execRegistrar(auth)
	modelRegistrar(auth)

	if existing, ok := coreManager.GetByID(auth.ID); ok && existing != nil {
		auth.CreatedAt = existing.CreatedAt
		auth.LastRefreshedAt = existing.LastRefreshedAt
		auth.NextRefreshAfter = existing.NextRefreshAfter
		if _, err := coreManager.Update(ctx, auth); err != nil {
			log.Errorf("failed to update auth %s: %v", auth.ID, err)
		}
		return
	}
	if _, err := coreManager.Register(ctx, auth); err != nil {
		log.Errorf("failed to register auth %s: %v", auth.ID, err)
	}
}

// applyCoreAuthRemoval disables an authentication entry.
func applyCoreAuthRemoval(ctx context.Context, id string, coreManager *coreauth.Manager) {
	if id == "" || coreManager == nil {
		return
	}

	GlobalModelRegistry().UnregisterClient(id)

	if existing, ok := coreManager.GetByID(id); ok && existing != nil {
		existing.Disabled = true
		existing.Status = coreauth.StatusDisabled
		if _, err := coreManager.Update(ctx, existing); err != nil {
			log.Errorf("failed to disable auth %s: %v", id, err)
		}
	}
}
