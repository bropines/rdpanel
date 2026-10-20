package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/logs"
)

func TestPanelLinesTeedWithoutLogsDir(t *testing.T) {
	env, _ := testEnv(t, nil)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	b, w := newLogBuffer(cfg, &stderr)
	log := NewLogger(cfg.Log, w)
	log.Error("db down", slog.String("password", "hunter22"))
	if !strings.Contains(stderr.String(), `"msg":"db down"`) || !strings.Contains(stderr.String(), "hunter22") {
		t.Fatalf("stderr must keep the original line: %s", stderr.String())
	}
	items := b.Query(logs.Filter{}).Items
	if len(items) != 1 || items[0].Source != logs.SourcePanel || items[0].Level != logs.LevelError ||
		strings.Contains(items[0].Raw, "hunter22") {
		t.Fatalf("buffer: %+v", items)
	}

	// With a logs dir the panel's lines come from its s6-log file instead.
	env2, _ := testEnv(t, map[string]string{"RDPANEL_LOGS_DIR": t.TempDir()})
	cfg2, _ := config.Load(env2)
	_, w2 := newLogBuffer(cfg2, &stderr)
	if w2 != &stderr {
		t.Fatal("panel lines would be stored twice")
	}
	// RDPANEL_LOGS_BUFFER=0 disables everything.
	env3, _ := testEnv(t, map[string]string{"RDPANEL_LOGS_BUFFER": "0"})
	cfg3, _ := config.Load(env3)
	if b3, w3 := newLogBuffer(cfg3, &stderr); b3.Enabled() || w3 != &stderr {
		t.Fatal("disabled buffer still tees")
	}
	if _, err := config.Load(func() config.LookupFunc {
		e, _ := testEnv(t, map[string]string{"RDPANEL_LOGS_BUFFER": "-1"})
		return e
	}()); err == nil {
		t.Fatal("negative buffer accepted")
	}
}

// TestWebhookSecretsRedacted: the webhook URL (token in path/query) and the
// signing secret never reach Server -> Logs.
func TestWebhookSecretsRedacted(t *testing.T) {
	const (
		hook   = "https://hooks.example.com/services/T0/B0/pathtoken123?token=querytoken456"
		secret = "whsec-signing-secret-789"
	)
	env, _ := testEnv(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false",
		"RDPANEL_WEBHOOK_URL": hook, "RDPANEL_WEBHOOK_SECRET": secret})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Logs.Write(logs.SourcePanel, `{"level":"WARN","msg":"webhook: send error","err":"Post \"`+hook+`\": refused","secret":"`+secret+`"}`)
	a.Logs.Write(logs.SourcePanel, `{"level":"WARN","msg":"proxy saw query token=querytoken456"}`)
	items := a.Logs.Query(logs.Filter{}).Items
	if len(items) == 0 {
		t.Fatal("nothing buffered")
	}
	for _, e := range items {
		for _, leak := range []string{"pathtoken123", "querytoken456", secret} {
			if strings.Contains(e.Raw, leak) {
				t.Fatalf("%q leaked: %s", leak, e.Raw)
			}
		}
	}
}

func TestAppTailsLogsDirAndRedactsKeys(t *testing.T) {
	logsDir := t.TempDir()
	env, dataDir := testEnv(t, map[string]string{"RDPANEL_LOGS_DIR": logsDir, "RDPANEL_HBBS_ENABLED": "false"})
	const pub = "SthVIVP5bdoO+gtCN2nxcTYXc8+ajGFuBwmw1gaHSIE="
	if err := os.WriteFile(filepath.Join(dataDir, "id_ed25519.pub"), []byte(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "id_ed25519"), []byte("cHJpdmF0ZS1rZXktbWF0ZXJpYWwtMTIz"), 0o600); err != nil {
		t.Fatal(err)
	}
	for src, line := range map[string]string{
		"hbbs":  "[2026-10-16 12:54:43.068584 +00:00] INFO [src/rendezvous_server.rs:1671] Key: " + pub,
		"hbbr":  "[2026-10-16 12:54:43.068939 +00:00] INFO [src/relay_server.rs:82] Listening on tcp :21117 cHJpdmF0ZS1rZXktbWF0ZXJpYWwtMTIz",
		"panel": `{"time":"2026-10-16T12:54:43.078Z","level":"INFO","msg":"starting rdpanel"}`,
	} {
		if err := os.MkdirAll(filepath.Join(logsDir, src), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logsDir, src, "current"), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	a.startWorkers(ctx, &wg)
	defer func() { cancel(); wg.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for len(a.Logs.Query(logs.Filter{}).Items) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("tailers: %+v", a.Logs.Query(logs.Filter{}).Items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, e := range a.Logs.Query(logs.Filter{}).Items {
		if strings.Contains(e.Raw, pub) || strings.Contains(e.Raw, "cHJpdmF0ZS1rZXkt") {
			t.Fatalf("key leaked: %+v", e)
		}
	}
	if got := a.logSources(); len(got) != 3 || a.logCapture() != "files" {
		t.Fatalf("sources %v capture %s", got, a.logCapture())
	}
}
