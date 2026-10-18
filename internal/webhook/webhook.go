package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/bropines/rdpanel/internal/events"
)

// Config configures the webhook dispatcher.
type Config struct {
	URL    string
	Secret string
	Bus    *events.Bus
	Log    *slog.Logger
	Client *http.Client
}

// Service subscribes to critical events and sends HTTP POST notifications.
type Service struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client
}

// New creates a new webhook Service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Service{cfg: cfg, log: cfg.Log, client: cfg.Client}
}

// Payload is the JSON structure sent to the webhook endpoint.
type Payload struct {
	Event     string    `json:"event"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// Run listens for critical events on the event bus until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	if s.cfg.URL == "" || s.cfg.Bus == nil {
		return
	}
	sub := s.cfg.Bus.Subscribe(func(e events.Event) bool {
		switch e.Type {
		case events.RelayHealth:
			if st, ok := e.Data.(events.RelayState); ok {
				return st.Health == "offline" || st.Health == "degraded"
			}
			return true
		case events.HBBSHealth:
			if h, ok := e.Data.(events.Health); ok {
				return !h.Up
			}
			return true
		}
		return false
	}, 64)
	defer sub.Close()

	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return
		}
		s.dispatch(ctx, ev)
	}
}

func (s *Service) dispatch(ctx context.Context, ev events.Event) {
	body, err := json.Marshal(Payload{
		Event:     string(ev.Type),
		Timestamp: ev.Time.UTC(),
		Data:      ev.Data,
	})
	if err != nil {
		s.log.Warn("webhook: marshal error", slog.Any("err", err))
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		s.log.Warn("webhook: request error", slog.Any("err", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rdpanel-webhook/1.0")
	if s.cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(s.cfg.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Signature-256", "sha256="+sig)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("webhook: send error", slog.String("url", s.cfg.URL), slog.Any("err", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.log.Warn("webhook: non-2xx status", slog.String("url", s.cfg.URL), slog.Int("status", resp.StatusCode))
	}
}
