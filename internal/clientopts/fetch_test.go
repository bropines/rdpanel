package clientopts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// allowLoopbackProbes lets the fetched-candidate probe reach the loopback
// fake STUN servers of these tests (production always applies the guard).
func allowLoopbackProbes(t *testing.T) {
	t.Helper()
	orig := probeFetched
	probeFetched = func(ctx context.Context, target string) ProbeResult { return ProbeOne(ctx, target, true) }
	t.Cleanup(func() { probeFetched = orig })
}

func TestFetchPublicSTUNSuccess(t *testing.T) {
	allowLoopbackProbes(t)
	stunAddr, cleanupStun := startFakeSTUNServer(t)
	defer cleanupStun()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "# STUN list\n%s\n198.51.100.1:3478\n", stunAddr)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := FetchPublicSTUN(ctx, ts.URL, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FetchedCount != 2 {
		t.Fatalf("expected 2 fetched, got %d", res.FetchedCount)
	}
	if res.WorkingCount != 1 {
		t.Fatalf("expected 1 working, got %d", res.WorkingCount)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(res.Items))
	}
	if res.Items[0].Server != "stun:"+stunAddr {
		t.Fatalf("expected server stun:%s, got %s", stunAddr, res.Items[0].Server)
	}
}

// TestFetchPublicSTUNGuardsCandidates: candidates from a downloaded list that
// point at loopback/private addresses are refused, never probed.
func TestFetchPublicSTUNGuardsCandidates(t *testing.T) {
	stunAddr, cleanupStun := startFakeSTUNServer(t)
	defer cleanupStun()
	var probed atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s\n10.0.0.1:3478\n[::1]:3478\n169.254.169.254:3478\n", stunAddr)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	orig := probeFetched
	probeFetched = func(ctx context.Context, target string) ProbeResult {
		res := orig(ctx, target)
		if !strings.Contains(res.Error, "SSRF guard") {
			probed.Store(true)
		}
		return res
	}
	t.Cleanup(func() { probeFetched = orig })

	res, err := FetchPublicSTUN(ctx, ts.URL, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FetchedCount != 4 || res.WorkingCount != 0 || len(res.Items) != 0 {
		t.Fatalf("private candidates were probed: %+v", res)
	}
	if probed.Load() {
		t.Fatal("a private candidate got past the SSRF guard")
	}
}

func TestFetchPublicSTUNFallbackOnHTTPError(t *testing.T) {
	allowLoopbackProbes(t)
	stunAddr, cleanupStun := startFakeSTUNServer(t)
	defer cleanupStun()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Temporarily override CuratedFallbackSTUNs to include our fake STUN server
	orig := CuratedFallbackSTUNs
	CuratedFallbackSTUNs = []string{"stun:" + stunAddr}
	defer func() { CuratedFallbackSTUNs = orig }()

	res, err := FetchPublicSTUN(ctx, ts.URL, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.WorkingCount != 1 {
		t.Fatalf("expected 1 working from fallback, got %d", res.WorkingCount)
	}
}

func TestNormalizeSTUN(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"stun.cloudflare.com", "stun:stun.cloudflare.com:3478"},
		{"stun.cloudflare.com:1234", "stun:stun.cloudflare.com:1234"},
		{"stun:stun.l.google.com:19302", "stun:stun.l.google.com:19302"},
		{"stuns:turns.example.com:5349", "stuns:turns.example.com:5349"},
		{"   ", ""},
	}
	for _, tc := range tests {
		got := normalizeSTUN(tc.in)
		if got != tc.want {
			t.Errorf("normalizeSTUN(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
