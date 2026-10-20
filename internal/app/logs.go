package app

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bropines/rdpanel/internal/adminapi"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/logs"
)

// Option customises New.
type Option func(*App)

// WithLogBuffer makes the app serve Server -> Logs from b (created by the
// caller before the logger, so the panel's own startup lines are captured).
func WithLogBuffer(b *logs.Buffer) Option { return func(a *App) { a.Logs = b } }

// newLogBuffer builds the log buffer for cfg and returns the writer the
// process logger should write to: w itself, or w teed into the buffer when
// the panel's lines are not captured from an s6-log directory.
func newLogBuffer(cfg *config.Config, w io.Writer) (*logs.Buffer, io.Writer) {
	b := logs.NewBuffer(cfg.Logs.Buffer)
	if b.Enabled() && cfg.Logs.Dir == "" {
		w = io.MultiWriter(w, b.Writer(logs.SourcePanel))
	}
	return b, w
}

// initLogs registers the buffer's metrics and teaches its redactor the
// secrets this process knows. Call after loadKeys.
func (a *App) initLogs() {
	if a.Logs == nil {
		a.Logs = logs.NewBuffer(a.Config.Logs.Buffer)
	}
	a.Logs.Register(a.Registry)
	if !a.Logs.Enabled() {
		return
	}
	cfg := a.Config
	secrets := []string{
		cfg.SecretKey.Reveal(), cfg.Auth.JWTKey.Reveal(), cfg.SMTP.Password.Reveal(),
		cfg.Webhook.Secret.Reveal(),
	}
	// Webhook URLs (Slack, Teams, ...) carry their token in the path or query.
	secrets = append(secrets, webhookURLSecrets(cfg.Webhook.URL)...)
	secrets = append(secrets, a.keySecrets...)
	// The hbbs key pair (the s6 image keeps both halves next to each other).
	if pk := cfg.HBBS.PublicKeyFile; pk != "" {
		for _, p := range []string{pk, filepath.Join(filepath.Dir(pk), "id_ed25519")} {
			if raw, err := os.ReadFile(p); err == nil {
				secrets = append(secrets, strings.TrimSpace(string(raw)))
			}
		}
	}
	a.Logs.AddSecrets(secrets...)
	if cfg.Logs.Dir != "" {
		a.Log.Info("server logs: following s6-log directories", slog.String("dir", cfg.Logs.Dir), slog.Int("lines_per_source", a.Logs.Capacity()))
	}
}

// webhookURLSecrets are the forms of a webhook URL that may show up in log
// lines: the whole URL and its query. The webhook service itself only logs
// scheme://host.
func webhookURLSecrets(raw string) []string {
	if raw == "" {
		return nil
	}
	out := []string{raw}
	if u, err := url.Parse(raw); err == nil && u.RawQuery != "" {
		out = append(out, u.RawQuery)
	}
	return out
}

// keyLogSecrets are the encodings of loaded key material the redactor hides.
func keyLogSecrets(aeadKey, jwtKey []byte) []string {
	out := []string{string(jwtKey)}
	if len(aeadKey) > 0 {
		out = append(out, base64.StdEncoding.EncodeToString(aeadKey), base64.RawURLEncoding.EncodeToString(aeadKey),
			base64.RawStdEncoding.EncodeToString(aeadKey))
	}
	return out
}

// logTailers follows <logs.dir>/<source> for every file source.
func (a *App) logTailers() map[string]func(context.Context) {
	if !a.Logs.Enabled() || a.Config.Logs.Dir == "" {
		return nil
	}
	out := map[string]func(context.Context){}
	for _, src := range logs.FileSources {
		t := &logs.Tailer{Dir: filepath.Join(a.Config.Logs.Dir, src), Source: src, Buf: a.Logs, Log: a.Log.With("component", "logs")}
		out["logs-"+src] = t.Run
	}
	return out
}

// logSources are the sources the Logs page offers.
func (a *App) logSources() []string {
	if a.Config.Logs.Dir != "" {
		return logs.FileSources
	}
	return []string{logs.SourcePanel}
}

// logCapture reports how lines get into the buffer (adminapi.LogCapture*).
func (a *App) logCapture() string {
	if a.Config.Logs.Dir != "" {
		return adminapi.LogCaptureFiles
	}
	return adminapi.LogCapturePanel
}
