package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		cur, latest string
		want        Status
	}{
		{"v0.1.0", "v0.2.0", StatusUpdateAvailable},
		{"0.1.0", "v0.1.0", StatusUpToDate},
		{"v0.2.0", "v0.1.9", StatusUpToDate},
		{"v0.1.1-3-gabc1234-dirty", "v0.1.1", StatusUpToDate},
		{"v0.1.1-3-gabc1234", "v0.1.2", StatusUpdateAvailable},
		{"v0.2.0-rc.1", "v0.2.0", StatusUpdateAvailable},
		{"v0.2.0", "v0.2.0-rc.1", StatusUpToDate},
		{"v1.10.0", "v1.9.0", StatusUpToDate},
		{"dev", "v0.1.0", StatusUnknown},
		{"v0.1.0", "nightly", StatusUnknown},
		{"abc1234", "v0.1.0", StatusUnknown},
	} {
		if got := Compare(tc.cur, tc.latest); got != tc.want {
			t.Errorf("Compare(%q, %q) = %s, want %s", tc.cur, tc.latest, got, tc.want)
		}
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestCheckerCachesAndTolerantOfFailures(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") != "rdpanel-test" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if fail.Load() {
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0","html_url":"https://github.com/bropines/rustdesk-panel/releases/tag/v0.3.0","published_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()
	clk := &clock{t: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)}
	c := New("rdpanel-test")
	c.URL, c.Now = srv.URL, clk.now
	ctx := context.Background()

	r := c.Latest(ctx)
	if r.Latest == nil || r.Latest.Version != "v0.3.0" || r.Err != "" || !r.CheckedAt.Equal(clk.t) {
		t.Fatalf("first check: %+v", r)
	}
	if r.Latest.URL == "" || r.Latest.PublishedAt.IsZero() {
		t.Fatalf("release fields: %+v", r.Latest)
	}
	clk.t = clk.t.Add(5 * time.Hour)
	c.Latest(ctx)
	if calls.Load() != 1 {
		t.Fatalf("cached answer refetched: %d calls", calls.Load())
	}

	// Stale + failing: the last good release is kept, the error reported.
	fail.Store(true)
	clk.t = clk.t.Add(2 * time.Hour)
	r = c.Latest(ctx)
	if calls.Load() != 2 || r.Err == "" || r.Latest == nil || r.Latest.Version != "v0.3.0" {
		t.Fatalf("failed refresh: calls=%d %+v", calls.Load(), r)
	}
	// Failures are retried only after Retry.
	clk.t = clk.t.Add(10 * time.Minute)
	c.Latest(ctx)
	if calls.Load() != 2 {
		t.Fatalf("retried too early: %d calls", calls.Load())
	}
	fail.Store(false)
	clk.t = clk.t.Add(DefaultRetry)
	if r = c.Latest(ctx); r.Err != "" || calls.Load() != 3 {
		t.Fatalf("recovery: calls=%d %+v", calls.Load(), r)
	}
}

func TestCheckerNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := New("rdpanel-test")
	c.URL = srv.URL
	if r := c.Latest(context.Background()); r.Latest != nil || r.Err != "" {
		t.Fatalf("no release: %+v", r)
	}
}

func TestCheckerUnreachable(t *testing.T) {
	c := New("rdpanel-test")
	c.URL = "http://127.0.0.1:1/unreachable"
	if r := c.Latest(context.Background()); r.Latest != nil || r.Err == "" {
		t.Fatalf("unreachable: %+v", r)
	}
}
