package logs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRingKeepsNewestPerSource(t *testing.T) {
	b := NewBuffer(3)
	for i := 1; i <= 5; i++ {
		b.Write(SourceHBBS, fmt.Sprintf("hbbs line %d", i))
	}
	b.Write(SourceHBBR, "hbbr line")
	p := b.Query(Filter{Sources: []string{SourceHBBS}})
	if len(p.Items) != 3 || p.Items[0].Message != "hbbs line 3" || p.Items[2].Message != "hbbs line 5" {
		t.Fatalf("ring: %+v", p.Items)
	}
	// hbbr is not evicted by hbbs traffic.
	if p := b.Query(Filter{Sources: []string{SourceHBBR}}); len(p.Items) != 1 {
		t.Fatalf("hbbr: %+v", p.Items)
	}
	all := b.Query(Filter{})
	if len(all.Items) != 4 || all.Items[3].Source != SourceHBBR {
		t.Fatalf("merged order: %+v", all.Items)
	}
	for i := 1; i < len(all.Items); i++ {
		if all.Items[i].Seq <= all.Items[i-1].Seq {
			t.Fatal("not ascending by seq")
		}
	}
}

func TestDisabledBuffer(t *testing.T) {
	for _, b := range []*Buffer{nil, NewBuffer(0)} {
		b.Write(SourcePanel, "x")
		if b.Enabled() || len(b.Query(Filter{}).Items) != 0 || b.Subscribe(Filter{}, 0) != nil {
			t.Fatal("disabled buffer stored something")
		}
		_, _ = b.Writer(SourcePanel).Write([]byte("y\n"))
	}
}

func TestUnknownSourceIsOther(t *testing.T) {
	b := NewBuffer(10)
	b.Write("whatever", "line")
	if p := b.Query(Filter{Sources: []string{SourceOther}}); len(p.Items) != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestQueryFiltersAndPagination(t *testing.T) {
	b := NewBuffer(100)
	b.Write(SourceHBBS, "[2026-10-16 13:27:07.704684 +03:00] INFO [src/peer.rs:84] peer registered 123")
	b.Write(SourceHBBS, "[2026-10-16 13:27:08.000000 +03:00] WARN [src/rendezvous_server.rs:1] punch timeout")
	b.Write(SourceHBBR, "[2026-10-16 13:27:09.000000 +03:00] ERROR [src/relay_server.rs:2] relay failed")
	b.Write(SourcePanel, `{"time":"2026-10-16T10:27:10Z","level":"DEBUG","msg":"tick"}`)
	b.Write(SourcePanel, `{"time":"2026-10-16T10:27:11Z","level":"WARN","msg":"Punch slow"}`)

	if p := b.Query(Filter{MinLevel: LevelWarn}); len(p.Items) != 3 {
		t.Fatalf("level: %+v", p.Items)
	}
	if p := b.Query(Filter{Q: "PUNCH"}); len(p.Items) != 2 {
		t.Fatalf("q: %+v", p.Items)
	}
	if p := b.Query(Filter{Sources: []string{SourceHBBS, SourcePanel}, MinLevel: LevelWarn}); len(p.Items) != 2 {
		t.Fatalf("sources+level: %+v", p.Items)
	}

	// Pages of 2, newest first, walking back with Before.
	var got []string
	var before uint64
	for i := 0; i < 5; i++ {
		p := b.Query(Filter{Limit: 2, Before: before})
		for j := len(p.Items) - 1; j >= 0; j-- {
			got = append(got, p.Items[j].Message)
		}
		if !p.More {
			break
		}
		before = p.Items[0].Seq
	}
	if len(got) != 5 || got[0] != "Punch slow" || got[4] != "peer registered 123" {
		t.Fatalf("pages: %q", got)
	}
	if p := b.Query(Filter{After: 3}); len(p.Items) != 2 {
		t.Fatalf("after: %+v", p.Items)
	}
}

func TestSubscriptionDeliversAndDrops(t *testing.T) {
	reg := prometheus.NewRegistry()
	b := NewBuffer(100)
	b.Register(reg)
	s := b.Subscribe(Filter{Sources: []string{SourceHBBR}, MinLevel: LevelWarn}, 2)
	defer s.Close()
	b.Write(SourceHBBS, "[2026-10-16 13:27:08.000000 +03:00] ERROR [x] other source")
	b.Write(SourceHBBR, "[2026-10-16 13:27:08.000000 +03:00] INFO [x] too low")
	for i := 0; i < 4; i++ {
		b.Write(SourceHBBR, fmt.Sprintf("[2026-10-16 13:27:08.000000 +03:00] WARN [x] w%d", i))
	}
	e := <-s.C()
	if e.Message != "w0" || e.Source != SourceHBBR {
		t.Fatalf("first: %+v", e)
	}
	<-s.C()
	select {
	case e := <-s.C():
		t.Fatalf("unexpected %+v", e)
	default:
	}
	if d := s.TakeDropped(); d != 2 {
		t.Fatalf("dropped %d", d)
	}
	if v := testutil.ToFloat64(b.dropped.WithLabelValues(SourceHBBR)); v != 2 {
		t.Fatalf("metric %v", v)
	}
	if v := testutil.ToFloat64(b.lines.WithLabelValues(SourceHBBR)); v != 5 {
		t.Fatalf("lines metric %v", v)
	}
	s.Close()
	s.Close()
	if b.Subscribers() != 0 {
		t.Fatal("not unsubscribed")
	}
}

func TestParseHBB(t *testing.T) {
	e, ok := ParseHBB("[2026-10-16 13:27:07.704684 +03:00] INFO [src/rendezvous_server.rs:1671] Listening on tcp :21116")
	if !ok || e.Level != LevelInfo || e.Message != "Listening on tcp :21116" {
		t.Fatalf("%+v %v", e, ok)
	}
	if want := time.Date(2026, 10, 16, 10, 27, 7, 704684000, time.UTC); !e.Time.Equal(want) {
		t.Fatalf("time %v", e.Time)
	}
	e, ok = ParseHBB("[2026-10-16 12:54:43.112597 +00:00] DEBUG [<unnamed>:0] PRAGMA page_size = 4096")
	if !ok || e.Level != LevelDebug {
		t.Fatalf("%+v", e)
	}
	if e, ok = ParseHBB("[2026-10-16 12:54:43.1 +0000] TRACE [m] t"); !ok || e.Level != LevelDebug {
		t.Fatalf("compact offset: %+v %v", e, ok)
	}
	for _, bad := range []string{"plain text", "[not a date] INFO x", "[2026-10-16 12:54:43 +00:00] BOGUS [x] y"} {
		if _, ok := ParseHBB(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseSlogJSON(t *testing.T) {
	e, ok := ParseSlogJSON(`{"time":"2026-10-16T12:54:43.078393526Z","level":"WARN+2","msg":"applied migration","version":1,"source":"00001 identity.sql","obj":{"a": 1}}`)
	if !ok || e.Level != LevelWarn || e.Message != `applied migration version=1 source="00001 identity.sql" obj={"a":1}` {
		t.Fatalf("%+v %v", e, ok)
	}
	if e.Time.Nanosecond() != 78393526 {
		t.Fatalf("time %v", e.Time)
	}
	for _, bad := range []string{`{"msg":"no level"}`, `{"level":"INFO"`, `not json`, `{"level":"INFO","msg":1}`} {
		if _, ok := ParseSlogJSON(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseSlogText(t *testing.T) {
	e, ok := ParseSlogText(`time=2026-10-16T13:27:07.704+03:00 level=ERROR msg="db \"down\"" err="dial tcp: refused" n=3`)
	if !ok || e.Level != LevelError || e.Message != `db "down" err="dial tcp: refused" n=3` {
		t.Fatalf("%+v %v", e, ok)
	}
	if e.Time.Hour() != 10 {
		t.Fatalf("time %v", e.Time)
	}
	if _, ok := ParseSlogText(`time=x level=INFO broken "msg`); ok {
		t.Fatal("accepted broken line")
	}
}

func TestContinuationLinesInherit(t *testing.T) {
	b := NewBuffer(10)
	b.Write(SourceHBBS, "[2026-10-16 12:54:43.199576 +00:00] WARN [<unnamed>:0] create table …")
	b.Write(SourceHBBS, "")
	b.Write(SourceHBBS, "create table if not exists peer (")
	b.Write(SourcePanel, "panic: boom")
	items := b.Query(Filter{}).Items
	if len(items) != 3 {
		t.Fatalf("blank line stored: %+v", items)
	}
	if items[1].Level != LevelWarn || !items[1].Time.Equal(items[0].Time) || items[1].Message != "create table if not exists peer (" {
		t.Fatalf("continuation: %+v", items[1])
	}
	if items[2].Level != LevelError || items[2].Time.IsZero() {
		t.Fatalf("panic: %+v", items[2])
	}
}

func TestLongLinesAreCut(t *testing.T) {
	b := NewBuffer(10)
	b.Write(SourcePanel, strings.Repeat("é", MaxLineBytes))
	raw := b.Query(Filter{}).Items[0].Raw
	if len(raw) > MaxLineBytes+len("…") || !strings.HasSuffix(raw, "…") {
		t.Fatalf("len %d", len(raw))
	}
}

func TestRedaction(t *testing.T) {
	const pub = "SthVIVP5bdoO+gtCN2nxcTYXc8+ajGFuBwmw1gaHSIE="
	const priv = "pRiVaTeKeYmAtErIaL0123456789abcdefSthVIVP5bdoO+gtCN2nxcTYXc8+ajGFuBwmw1gaHSIE="
	r := NewRedactor(priv, pub, "short")
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXZhbHVl"
	cases := []struct{ in, mustNot, must string }{
		{"[2026-10-16 12:54:43 +00:00] INFO [src/rendezvous_server.rs:1671] Key: " + pub, pub, "Key: [REDACTED]"},
		{"loaded " + priv, "pRiVaTeKeY", "loaded [REDACTED]"},
		{"Key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "AAAAAAAAAAAAAAAAAAAA", "Key: [REDACTED]"},
		{"token " + jwt + " used", "eyJhbGci", "[REDACTED]"},
		{"Authorization: Bearer abcdefghijklmnop", "abcdefghijklmnop", "Authorization: "},
		{`{"level":"INFO","msg":"x","headers":{"authorization":"Basic dXNlcjpwYXNz"}}`, "dXNlcjpwYXNz", `"authorization":"[REDACTED]"`},
		{`{"level":"INFO","msg":"login","password":"hunter22","access_token":"abc.def"}`, "hunter22", `"password":"[REDACTED]"`},
		{`{"level":"INFO","msg":"x","client_secret":12345678}`, "12345678", `"client_secret":"[REDACTED]"`},
		{"level=INFO msg=x password=hunter22 smtp_password='p a s s' api_key=zzzzzzzzzz", "hunter22", "password=[REDACTED]"},
		{"secret_key = s3cr3tvalue!", "s3cr3tvalue", "secret_key = [REDACTED]"},
		{"GET /api/oidc/callback?code=AUTHCODE123&state=STATE456 200", "AUTHCODE123", "code=[REDACTED]&state=[REDACTED]"},
		{"dial smtp://mailer:hunter22@mail.example.com:587", "hunter22", "smtp://mailer:[REDACTED]@mail.example.com"},
		{"Cookie: rdpanel_session=abcdef0123456789; rdpanel_csrf=zzz", "abcdef0123456789", "Cookie: [REDACTED]"},
		{"KEY_PRIV=" + priv, "pRiVaTeKeY", ""},
	}
	for _, c := range cases {
		out := r.Redact(c.in)
		if strings.Contains(out, c.mustNot) || (c.must != "" && !strings.Contains(out, c.must)) {
			t.Errorf("Redact(%q) = %q", c.in, out)
		}
	}
	// Benign text survives: setting names logged as key=..., short words.
	for _, keep := range []string{
		`{"level":"INFO","msg":"reconcile: corrected drift","target":"hbbs","key":"must_login"}`,
		"reconcile: corrected drift key=must_login",
		"[2026-10-16 12:54:43 +00:00] INFO [src/relay_server.rs:82] Listening on tcp :21117",
		"applied migration version=1 source=00001_identity.sql",
		"short secret word: short",
	} {
		want := keep
		if strings.HasPrefix(keep, "short") {
			want = "short secret word: short" // "short" literal ignored (< 8 bytes)
		}
		if out := r.Redact(keep); out != want {
			t.Errorf("Redact(%q) = %q, want unchanged", keep, out)
		}
	}
}

func TestBufferRedactsBeforeStoring(t *testing.T) {
	b := NewBuffer(10)
	b.AddSecrets("jwt-signing-key-material")
	b.Write(SourcePanel, `{"time":"2026-10-16T10:00:00Z","level":"INFO","msg":"oops jwt-signing-key-material","token":"abc"}`)
	e := b.Query(Filter{}).Items[0]
	for _, s := range []string{e.Raw, e.Message} {
		if strings.Contains(s, "jwt-signing-key-material") || strings.Contains(s, `"abc"`) || strings.Contains(s, "token=abc") {
			t.Fatalf("secret stored: %q", s)
		}
	}
	if !strings.HasPrefix(e.Message, "oops [REDACTED]") {
		t.Fatalf("message %q", e.Message)
	}
	// Searching for the secret finds nothing.
	if p := b.Query(Filter{Q: "signing-key"}); len(p.Items) != 0 {
		t.Fatal("secret searchable")
	}
}

func TestWriterSplitsLines(t *testing.T) {
	b := NewBuffer(10)
	w := b.Writer(SourcePanel)
	_, _ = w.Write([]byte(`{"level":"INFO","msg":"a"}` + "\n" + `{"level":"ERROR",`))
	_, _ = w.Write([]byte(`"msg":"b"}` + "\n"))
	items := b.Query(Filter{}).Items
	if len(items) != 2 || items[1].Message != "b" || items[1].Level != LevelError {
		t.Fatalf("%+v", items)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func messages(b *Buffer) []string {
	var out []string
	for _, e := range b.Query(Filter{}).Items {
		out = append(out, e.Message)
	}
	return out
}

func TestTailerReplaysFollowsAndRotates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string, appendMode bool) {
		t.Helper()
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendMode {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(filepath.Join(dir, name), flag, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	write("@400000006a0000000000000a.s", "old-1\n", false)
	write("@400000006a0000010000000a.s", "arch-1\narch-2\n", false)
	write("current", "cur-1\ncur-", false)
	write("lock", "", false)

	b := NewBuffer(100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&Tailer{Dir: dir, Source: SourceHBBS, Buf: b, Interval: 10 * time.Millisecond}).Run(ctx)
		close(done)
	}()
	waitFor(t, "replay", func() bool { return len(messages(b)) == 3 })
	if got := strings.Join(messages(b), ","); got != "arch-1,arch-2,cur-1" {
		t.Fatalf("replay: %s", got)
	}
	// The partial line completes; new lines follow.
	write("current", "2\ncur-3\n", true)
	waitFor(t, "follow", func() bool { return len(messages(b)) == 5 })
	// s6-log rotation: current -> @archive, then a new current. Lines
	// appended to the old file just before the rename are not lost.
	write("current", "cur-4\n", true)
	if err := os.Rename(filepath.Join(dir, "current"), filepath.Join(dir, "@400000006a0000020000000a.s")); err != nil {
		t.Fatal(err)
	}
	write("current", "new-1\n", false)
	waitFor(t, "rotate", func() bool { return len(messages(b)) == 7 })
	if got := strings.Join(messages(b), ","); got != "arch-1,arch-2,cur-1,cur-2,cur-3,cur-4,new-1" {
		t.Fatalf("after rotate: %s", got)
	}
	cancel()
	<-done
}

func TestTailerWaitsForDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hbbr")
	b := NewBuffer(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&Tailer{Dir: dir, Source: SourceHBBR, Buf: b, Interval: 10 * time.Millisecond}).Run(ctx)
	time.Sleep(30 * time.Millisecond)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "current"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "late file", func() bool { return len(messages(b)) == 1 })
}
