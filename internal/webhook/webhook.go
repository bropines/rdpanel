package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bropines/rdpanel/internal/events"
)

// Signature headers. With a secret, X-Signature-256 is
// "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)) where
// timestamp is the X-RDPanel-Timestamp value (Unix seconds); receivers should
// reject stale timestamps to stop replays (docs/configuration.md#webhooks).
const (
	HeaderSignature = "X-Signature-256"
	HeaderTimestamp = "X-RDPanel-Timestamp"
)

// Config configures the webhook dispatcher.
type Config struct {
	URL    string
	Secret string
	Bus    *events.Bus
	Log    *slog.Logger
	Client *http.Client
	Now    func() time.Time
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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, log: cfg.Log, client: cfg.Client}
}

// Sign returns the X-Signature-256 value for body sent at timestamp ts
// (Unix seconds, the X-RDPanel-Timestamp value).
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SafeURL is the scheme and host of a webhook URL: what may be logged (paths
// and queries of webhook URLs often embed tokens).
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid url)"
	}
	return u.Scheme + "://" + u.Host
}

// safeErr strips the request URL that *url.Error repeats.
func safeErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + " " + SafeURL(ue.URL) + ": " + ue.Err.Error()
	}
	return err.Error()
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
		s.log.Warn("webhook: request error", slog.String("url", SafeURL(s.cfg.URL)), slog.String("err", safeErr(err)))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rdpanel-webhook/1.0")
	ts := s.cfg.Now().Unix()
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	if s.cfg.Secret != "" {
		req.Header.Set(HeaderSignature, Sign(s.cfg.Secret, ts, body))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("webhook: send error", slog.String("url", SafeURL(s.cfg.URL)), slog.String("err", safeErr(err)))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.log.Warn("webhook: non-2xx status", slog.String("url", SafeURL(s.cfg.URL)), slog.Int("status", resp.StatusCode))
	}
}
