package manager

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

type Health struct {
	store                 *Store
	botReady, pollerReady *atomic.Bool
}

func NewHealth(s *Store, b, p *atomic.Bool) *Health {
	return &Health{store: s, botReady: b, pollerReady: p}
}
func (h *Health) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !h.botReady.Load() || !h.pollerReady.Load() || h.store.Writable(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	return mux
}
