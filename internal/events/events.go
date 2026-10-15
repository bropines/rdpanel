// Package events is the in-process pub/sub for presence, device, audit and
// hbbs health events, exposed to the panel as Server-Sent Events at
// /api/v1/events.
//
// Stream format (text/event-stream):
//
//	retry: 5000
//
//	id: 42
//	event: presence.changed
//	data: {"id":"123456789","online":true,"since":"...","source":"hbbs"}
//
//	event: resync
//	data: {"dropped":17}
//
//	: keepalive
//
// "resync" tells the client that events were dropped for it (slow consumer)
// and it should refetch state over the REST API. Last-Event-ID is not
// supported: on reconnect clients refetch state.
//
// Optional query parameter types=presence.changed,hbbs.health restricts the
// stream to the given event types.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Role values understood by Principal.IsAdmin.
const RoleAdmin = "admin"

// Principal is the authenticated panel user of a stream.
type Principal struct {
	UserID int64
	Role   string // "admin" or "user"
}

// IsAdmin reports whether p has the admin role.
func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

// ErrUnauthenticated is the conventional Authenticator error for "no valid session".
var ErrUnauthenticated = errors.New("events: unauthenticated")

// Authenticator resolves the panel session of a request (stream B implements
// it on top of the session cookie). It is called once when the stream opens
// and again on every keepalive, so a revoked session ends the stream.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(r *http.Request) (Principal, error)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(r *http.Request) (Principal, error) { return f(r) }

// DenyAll is the default Authenticator: every request is rejected.
var DenyAll Authenticator = AuthenticatorFunc(func(*http.Request) (Principal, error) {
	return Principal{}, ErrUnauthenticated
})

// Defaults for the SSE handler.
const (
	DefaultKeepalive = 15 * time.Second
	retryMillis      = 5000
)

// Deps are the collaborators the SSE endpoint needs.
type Deps struct {
	Log   *slog.Logger
	Store *store.Store
	Bus   *Bus
	// Auth authenticates the stream; nil means DenyAll.
	Auth Authenticator
	// Keepalive is the comment/heartbeat interval (default 15 s).
	Keepalive time.Duration
	// Buffer is the per-stream queue length (default DefaultBuffer).
	Buffer int
}

// Register mounts the SSE endpoint on r (already scoped to /api/v1/events).
// Note: the HTTP server has no WriteTimeout so long-lived streams work.
func Register(r chi.Router, d Deps) {
	r.Get("/", Handler(d).ServeHTTP)
}

// Handler returns the SSE handler.
func Handler(d Deps) http.Handler {
	if d.Auth == nil {
		d.Auth = DenyAll
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Keepalive <= 0 {
		d.Keepalive = DefaultKeepalive
	}
	return &sseHandler{d: d}
}

type sseHandler struct{ d Deps }

func (h *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.d.Auth.Authenticate(r)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	if h.d.Bus == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "unavailable", "event stream unavailable")
		return
	}
	types := parseTypes(r.URL.Query().Get("types"))

	rc := http.NewResponseController(w)
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Accel-Buffering", "no") // nginx/angie: do not buffer the stream
	w.WriteHeader(http.StatusOK)

	var principal = p // refreshed on keepalive; read only by this goroutine
	sub := h.d.Bus.Subscribe(func(e Event) bool {
		return (types == nil || types[e.Type]) && e.VisibleTo(p)
	}, h.d.Buffer)
	defer sub.Close()

	if _, err := fmt.Fprintf(w, "retry: %d\n\n", retryMillis); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		h.d.Log.Warn("sse: response writer cannot flush", slog.Any("err", err))
		return
	}

	ctx := r.Context()
	ka := time.NewTicker(h.d.Keepalive)
	defer ka.Stop()
	var batch []Event
	for {
		select {
		case <-ctx.Done():
			return
		case <-ka.C:
			np, err := h.d.Auth.Authenticate(r)
			if err != nil {
				_, _ = fmt.Fprint(w, "event: unauthenticated\ndata: {}\n\n")
				_ = rc.Flush()
				return
			}
			principal = np
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
		case <-sub.Ready():
			var dropped uint64
			batch, dropped = sub.Drain(batch[:0])
			if dropped > 0 {
				if _, err := fmt.Fprintf(w, "event: resync\ndata: {\"dropped\":%d}\n\n", dropped); err != nil {
					return
				}
			}
			for _, e := range batch {
				if !e.VisibleTo(principal) {
					continue
				}
				if err := writeEvent(w, e); err != nil {
					if !errors.Is(err, errEncode) {
						return
					}
					h.d.Log.Error("sse: encode event", slog.String("type", string(e.Type)), slog.Any("err", err))
				}
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

var errEncode = errors.New("encode")

func writeEvent(w http.ResponseWriter, e Event) error {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return fmt.Errorf("%w: %v", errEncode, err)
	}
	// json.Marshal output never contains raw newlines, so one data: line suffices.
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data)
	return err
}

func parseTypes(q string) map[Type]bool {
	if q == "" {
		return nil
	}
	m := map[Type]bool{}
	for _, t := range strings.Split(q, ",") {
		if t = strings.TrimSpace(t); t != "" {
			m[Type(t)] = true
		}
	}
	return m
}
