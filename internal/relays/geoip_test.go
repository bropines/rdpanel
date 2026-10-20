package relays

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGeoIPRefreshKeepsDatasetOnError: a failed download or an invalid file
// leaves the loaded dataset in place; download errors are typed.
func TestGeoIPRefreshKeepsDatasetOnError(t *testing.T) {
	engine := NewGeoIPEngine("")
	if _, err := engine.ParseAndLoad(strings.NewReader("192.0.2.0/24,US\n"), "first.csv"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.csv":
			_, _ = io.WriteString(w, "203.0.113.0/24,JP\n198.51.100.0/24,NL\n")
		case "/empty.csv":
			_, _ = io.WriteString(w, "# nothing\n")
		default:
			http.Error(w, "nope", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	st, err := engine.RefreshFromURL(ctx, srv.URL+"/broken")
	if !errors.Is(err, ErrGeoIPDownload) || st.Source != "first.csv" || st.PrefixCount != 1 {
		t.Fatalf("HTTP 500: status %+v, err %v", st, err)
	}
	if _, err := engine.RefreshFromURL(ctx, "http://127.0.0.1:1/unreachable"); !errors.Is(err, ErrGeoIPDownload) {
		t.Fatalf("dial error: %v", err)
	}
	st, err = engine.RefreshFromURL(ctx, srv.URL+"/empty.csv")
	if err == nil || errors.Is(err, ErrGeoIPDownload) || st.Source != "first.csv" {
		t.Fatalf("empty file: status %+v, err %v", st, err)
	}
	if res := engine.Lookup("192.0.2.1"); !res.Found || res.CountryCode != "US" {
		t.Fatalf("dataset lost after failed refreshes: %+v", res)
	}
	if st, err = engine.RefreshFromURL(ctx, srv.URL+"/ok.csv"); err != nil || st.PrefixCount != 2 {
		t.Fatalf("ok: %+v %v", st, err)
	}
}

func TestGeoIPCapReader(t *testing.T) {
	r := &capReader{r: strings.NewReader("0123456789"), n: 4}
	if _, err := io.ReadAll(r); !errors.Is(err, errGeoIPTooLarge) {
		t.Fatalf("oversized body: err %v", err)
	}
	r = &capReader{r: strings.NewReader("0123"), n: 4}
	if b, err := io.ReadAll(r); err != nil || string(b) != "0123" {
		t.Fatalf("body at the cap: %q %v", b, err)
	}
}

func TestGeoIPEngineBuiltin(t *testing.T) {
	engine := NewGeoIPEngine("")
	engine.LoadBuiltin()

	st := engine.Status()
	if !st.Loaded {
		t.Fatal("expected engine to be loaded")
	}
	if st.CountryCount < 10 {
		t.Fatalf("expected at least 10 countries, got %d", st.CountryCount)
	}

	// Test known Russian IP
	res := engine.Lookup("95.165.172.169")
	if !res.Found || res.CountryCode != "RU" {
		t.Fatalf("expected RU for 95.165.172.169, got %+v", res)
	}
	if res.CountryName != "Russia" {
		t.Fatalf("expected Russia name, got %q", res.CountryName)
	}

	// Test known German IP (Hetzner)
	resDE := engine.Lookup("88.198.50.2")
	if !resDE.Found || resDE.CountryCode != "DE" {
		t.Fatalf("expected DE for 88.198.50.2, got %+v", resDE)
	}

	// Test unmapped IP
	resNone := engine.Lookup("10.0.0.1")
	if resNone.Found {
		t.Fatalf("expected not found for 10.0.0.1, got %+v", resNone)
	}

	// Test country prefixes query
	ruCIDRs := engine.CountryCIDRsFor("RU")
	if len(ruCIDRs) == 0 {
		t.Fatal("expected non-empty CIDRs for RU")
	}
}

func TestGeoIPEngineParseAndLoad(t *testing.T) {
	engine := NewGeoIPEngine("")
	csvData := `
# Sample CSV
192.0.2.0/24,US
198.51.100.0,198.51.100.255,NL
203.0.113.0/24,JP
`
	st, err := engine.ParseAndLoad(strings.NewReader(csvData), "test.csv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.PrefixCount < 3 {
		t.Fatalf("expected at least 3 prefixes, got %d", st.PrefixCount)
	}

	resUS := engine.Lookup("192.0.2.42")
	if !resUS.Found || resUS.CountryCode != "US" {
		t.Fatalf("expected US for 192.0.2.42, got %+v", resUS)
	}

	resNL := engine.Lookup("198.51.100.10")
	if !resNL.Found || resNL.CountryCode != "NL" {
		t.Fatalf("expected NL for 198.51.100.10, got %+v", resNL)
	}
}
