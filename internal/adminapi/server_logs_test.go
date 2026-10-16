package adminapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/logs"
	"github.com/bropines/rdpanel/internal/store"
)

func seedLogs(b *logs.Buffer) {
	b.Write(logs.SourceHBBS, "[2026-10-16 13:27:07.704684 +03:00] INFO [src/rendezvous_server.rs:1671] Key: SthVIVP5bdoO+gtCN2nxcTYXc8+ajGFuBwmw1gaHSIE=")
	b.Write(logs.SourceHBBS, "[2026-10-16 13:27:08.000000 +03:00] WARN [src/peer.rs:1] punch timeout 123456789")
	b.Write(logs.SourceHBBR, "[2026-10-16 13:27:09.000000 +03:00] ERROR [src/relay_server.rs:2] relay failed")
	b.Write(logs.SourcePanel, `{"time":"2026-10-16T10:27:10Z","level":"DEBUG","msg":"tick"}`)
	b.Write(logs.SourcePanel, `{"time":"2026-10-16T10:27:11Z","level":"WARN","msg":"login failed","password":"hunter22"}`)
}

func TestServerLogsListFiltersAndPaging(t *testing.T) {
	buf := logs.NewBuffer(100)
	seedLogs(buf)
	e := newB2Env(t, b2opts{logs: buf, logCapture: LogCaptureFiles})
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("joe", store.RoleUser)

	if code, _, _ := user.do(http.MethodGet, "/server/logs", nil); code != http.StatusForbidden {
		t.Fatalf("user: %d", code)
	}
	if code, _, _ := e.client().do(http.MethodGet, "/server/logs", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}

	code, m, _ := admin.do(http.MethodGet, "/server/logs", nil)
	if code != http.StatusOK || m["enabled"] != true || m["capture"] != "files" || m["capacity"] != float64(100) {
		t.Fatalf("list: %d %v", code, m)
	}
	items := m["items"].([]any)
	if len(items) != 5 || m["next_before"] != nil {
		t.Fatalf("items: %v", m)
	}
	first := items[0].(map[string]any)
	if first["source"] != "hbbs" || first["level"] != "info" || first["time"] != "2026-10-16T10:27:07.704684Z" {
		t.Fatalf("entry: %v", first)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "SthVIVP5") || strings.Contains(string(raw), "hunter22") {
		t.Fatalf("secret leaked: %s", raw)
	}
	srcs := m["sources"].([]any)
	if len(srcs) != 3 || srcs[0].(map[string]any)["name"] != "hbbr" {
		t.Fatalf("sources: %v", srcs)
	}

	_, m, _ = admin.do(http.MethodGet, "/server/logs?source=hbbs,panel&level=warn", nil)
	if items := m["items"].([]any); len(items) != 2 {
		t.Fatalf("filter: %v", items)
	}
	_, m, _ = admin.do(http.MethodGet, "/server/logs?q=RELAY+FAILED", nil)
	if items := m["items"].([]any); len(items) != 1 || items[0].(map[string]any)["source"] != "hbbr" {
		t.Fatalf("q: %v", items)
	}

	// Paging: 2 + 2 + 1, newest first.
	var seqs []float64
	path := "/server/logs?limit=2"
	for i := 0; i < 4; i++ {
		_, m, _ = admin.do(http.MethodGet, path, nil)
		for _, it := range m["items"].([]any) {
			seqs = append(seqs, it.(map[string]any)["seq"].(float64))
		}
		nb, ok := m["next_before"].(float64)
		if !ok {
			break
		}
		path = fmt.Sprintf("/server/logs?limit=2&before=%d", int64(nb))
	}
	if fmt.Sprint(seqs) != "[4 5 2 3 1]" {
		t.Fatalf("pages: %v", seqs)
	}

	for _, bad := range []string{"?source=nginx", "?level=fatal", "?limit=0", "?limit=5000", "?before=x", "?after=-1", "?q=" + strings.Repeat("a", 201)} {
		if code, m, _ := admin.do(http.MethodGet, "/server/logs"+bad, nil); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", bad, code, m)
		}
	}
}

func TestServerLogsDisabled(t *testing.T) {
	e := newB2Env(t, b2opts{logs: logs.NewBuffer(0)})
	admin, _ := e.as("root", store.RoleAdmin)
	code, m, _ := admin.do(http.MethodGet, "/server/logs", nil)
	if code != http.StatusOK || m["enabled"] != false || m["capture"] != "off" || len(m["items"].([]any)) != 0 {
		t.Fatalf("%d %v", code, m)
	}
	for _, p := range []string{"/server/logs/stream", "/server/logs/download"} {
		if code, m, _ := admin.do(http.MethodGet, p, nil); code != http.StatusServiceUnavailable || errCode(m) != "logs_disabled" {
			t.Fatalf("%s: %d %v", p, code, m)
		}
	}
	// nil buffer behaves the same.
	e2 := newB2Env(t, b2opts{})
	a2, _ := e2.as("root", store.RoleAdmin)
	if _, m, _ := a2.do(http.MethodGet, "/server/logs", nil); m["enabled"] != false {
		t.Fatalf("nil: %v", m)
	}
}

func TestServerLogsDownload(t *testing.T) {
	buf := logs.NewBuffer(100)
	seedLogs(buf)
	e := newB2Env(t, b2opts{logs: buf})
	admin, _ := e.as("root", store.RoleAdmin)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/admin/server/logs/download?level=warn", nil)
	resp, err := admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename="rdpanel-logs-20261015-120000.txt"`) {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "[hbbs] [2026-10-16 13:27:08") || !strings.HasPrefix(lines[2], "[panel] {") {
		t.Fatalf("body: %q", body)
	}
	if strings.Contains(string(body), "hunter22") {
		t.Fatal("secret in download")
	}
}

// sseReader reads "event"/"data" frames from a stream.
type sseReader struct{ r *bufio.Reader }

func (s sseReader) next(t *testing.T) (event, data string) {
	t.Helper()
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if event != "" || data != "" {
				return event, data
			}
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func openLogStream(t *testing.T, e *b2env, c *client, query string) (sseReader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/admin/server/logs/stream"+query, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	return sseReader{bufio.NewReader(resp.Body)}, func() { cancel(); resp.Body.Close() }
}

func TestServerLogsStream(t *testing.T) {
	buf := logs.NewBuffer(100)
	seedLogs(buf) // seq 1..5
	e := newB2Env(t, b2opts{logs: buf})
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("joe", store.RoleUser)

	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/admin/server/logs/stream", nil)
	if resp, err := user.hc.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("user stream: %v %v", resp.StatusCode, err)
	}

	// after=3 replays seq 4 and 5 (filtered to panel), then live lines.
	s, stop := openLogStream(t, e, admin, "?source=panel,hbbr&after=3")
	defer stop()
	ev, data := s.next(t)
	if ev != "log" || !strings.Contains(data, `"message":"tick"`) {
		t.Fatalf("replay 1: %s %s", ev, data)
	}
	ev, data = s.next(t)
	if ev != "log" || !strings.Contains(data, `"seq":5`) || strings.Contains(data, "hunter22") {
		t.Fatalf("replay 2: %s %s", ev, data)
	}
	waitSubs(t, buf, 1)
	buf.Write(logs.SourceHBBS, "hbbs is filtered out")
	buf.Write(logs.SourceHBBR, "[2026-10-16 13:27:09.000000 +03:00] INFO [x] live relay line")
	ev, data = s.next(t)
	var got logEntryDTO
	if err := json.Unmarshal([]byte(data), &got); err != nil || ev != "log" || got.Source != "hbbr" || got.Message != "live relay line" || got.Seq != 7 {
		t.Fatalf("live: %s %s %v", ev, data, err)
	}
	stop()
	waitSubs(t, buf, 0)
}

func TestServerLogsStreamEndsWhenSessionRevoked(t *testing.T) {
	old := logStreamKeepalive
	logStreamKeepalive = 50 * time.Millisecond
	defer func() { logStreamKeepalive = old }()
	buf := logs.NewBuffer(10)
	e := newB2Env(t, b2opts{logs: buf})
	admin, u := e.as("root", store.RoleAdmin)
	s, stop := openLogStream(t, e, admin, "")
	defer stop()
	waitSubs(t, buf, 1)
	if n, err := e.st.AuthTokens.RevokeForUser(e.ctx, u.ID, store.TokenWeb, "test"); err != nil || n == 0 {
		t.Fatal(err)
	}
	if ev, _ := s.next(t); ev != "unauthenticated" {
		t.Fatalf("event %q", ev)
	}
}

func waitSubs(t *testing.T, b *logs.Buffer, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.Subscribers() != n {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers %d, want %d", b.Subscribers(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
