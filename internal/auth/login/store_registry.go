package login

import (
	"sync"

	"github.com/nghyane/llm-mux/internal/provider"
)

type PendingWriteNotifier interface {
	MarkPendingWrite(path string)
}

var (
	storeMu         sync.RWMutex
	registeredStore provider.Store

	pendingWriteMu       sync.RWMutex
	pendingWriteNotifier PendingWriteNotifier
)

func RegisterPendingWriteNotifier(n PendingWriteNotifier) {
	pendingWriteMu.Lock()
	pendingWriteNotifier = n
	pendingWriteMu.Unlock()
}

func NotifyPendingWrite(path string) {
	pendingWriteMu.RLock()
	n := pendingWriteNotifier
	pendingWriteMu.RUnlock()
	if n != nil {
		n.MarkPendingWrite(path)
	}
}

func RegisterTokenStore(store provider.Store) {
	storeMu.Lock()
	registeredStore = store
	storeMu.Unlock()
}

func GetTokenStore() provider.Store {
	storeMu.RLock()
	s := registeredStore
	storeMu.RUnlock()
	if s != nil {
		return s
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if registeredStore == nil {
		registeredStore = NewFileTokenStore()
	}
	return registeredStore
}
