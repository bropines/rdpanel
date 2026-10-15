// Package events is the in-process pub/sub for presence and audit events,
// exposed to the panel as Server-Sent Events at /api/v1/events.
//
// TODO(stream C): broker, SSE handler (session-authenticated), presence
// transitions from internal/hbbs.
package events

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/store"
)

// Deps are the collaborators the SSE endpoint needs.
type Deps struct {
	Log   *slog.Logger
	Store *store.Store
}

// Register mounts the SSE endpoint on r (already scoped to /api/v1/events).
// Note: the HTTP server has no WriteTimeout so long-lived streams work.
func Register(r chi.Router, d Deps) {
	_ = r
	_ = d
}
