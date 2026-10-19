package relays

import (
	"strings"
	"testing"
)

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
