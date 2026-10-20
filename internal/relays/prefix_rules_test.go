package relays

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/store"
)

func TestPrefixRuleValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rule     PrefixRule
		wantErr  bool
		wantCIDR string
	}{
		{
			name:     "valid IPv4 subnet",
			rule:     PrefixRule{CIDR: "192.168.1.0/24", RelayHost: "relay.example.com:21117"},
			wantErr:  false,
			wantCIDR: "192.168.1.0/24",
		},
		{
			name:     "bare IP auto /32",
			rule:     PrefixRule{CIDR: "1.2.3.4", RelayHost: "relay.example.com:21117"},
			wantErr:  false,
			wantCIDR: "1.2.3.4/32",
		},
		{
			name:     "valid IPv6 subnet",
			rule:     PrefixRule{CIDR: "2001:db8::/32", RelayHost: "relay.example.com:21117"},
			wantErr:  false,
			wantCIDR: "2001:db8::/32",
		},
		{
			name:    "empty CIDR",
			rule:    PrefixRule{CIDR: "", RelayHost: "relay.example.com:21117"},
			wantErr: true,
		},
		{
			name:    "invalid CIDR",
			rule:    PrefixRule{CIDR: "999.999.999.999/24", RelayHost: "relay.example.com:21117"},
			wantErr: true,
		},
		{
			name:    "empty host",
			rule:    PrefixRule{CIDR: "10.0.0.0/8", RelayHost: ""},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.rule
			err := r.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && r.CIDR != tc.wantCIDR {
				t.Errorf("CIDR = %q, want %q", r.CIDR, tc.wantCIDR)
			}
		})
	}
}

func TestFormatAndWriteRelayMap(t *testing.T) {
	rules := []PrefixRule{
		{CIDR: "185.0.0.0/8", RelayHost: "desk-eu.bropines.ru:21117"},
		{CIDR: "192.168.0.0/16", RelayHost: "desk-local.bropines.ru:21117"},
	}
	formatted := FormatRelayMap(rules)
	expected := "185.0.0.0/8 desk-eu.bropines.ru:21117\n192.168.0.0/16 desk-local.bropines.ru:21117\n"
	if formatted != expected {
		t.Fatalf("FormatRelayMap = %q, want %q", formatted, expected)
	}

	dir := t.TempDir()
	if err := WriteRelayMapFile(dir, formatted); err != nil {
		t.Fatalf("WriteRelayMapFile err = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "relay_map.txt"))
	if err != nil {
		t.Fatalf("ReadFile err = %v", err)
	}
	if string(data) != expected {
		t.Fatalf("read content = %q, want %q", string(data), expected)
	}

	// Idempotent write should not fail
	if err := WriteRelayMapFile(dir, formatted); err != nil {
		t.Fatalf("WriteRelayMapFile second err = %v", err)
	}
}

func TestPrefixRuleStoreCRUD(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)

	// List initially empty
	rules, err := h.st.ListPrefixRules(ctx)
	if err != nil {
		t.Fatalf("ListPrefixRules err = %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("initial rules len = %d, want 0", len(rules))
	}

	// Create
	rule := PrefixRule{
		CIDR:      "185.162.0.0/16",
		RelayHost: "desk-ru.bropines.ru:21117",
		Comment:   "Russia traffic",
	}
	if err := h.st.CreatePrefixRule(ctx, &rule); err != nil {
		t.Fatalf("CreatePrefixRule err = %v", err)
	}
	if rule.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// List
	rules, err = h.st.ListPrefixRules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListPrefixRules after create = %v, err = %v", rules, err)
	}
	if rules[0].CIDR != "185.162.0.0/16" || rules[0].RelayHost != "desk-ru.bropines.ru:21117" {
		t.Errorf("unexpected rule = %+v", rules[0])
	}

	// Delete
	if err := h.st.DeletePrefixRule(ctx, rule.ID); err != nil {
		t.Fatalf("DeletePrefixRule err = %v", err)
	}
	rules, err = h.st.ListPrefixRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Fatalf("ListPrefixRules after delete = %v, err = %v", rules, err)
	}
}

func TestCountryPrefixRules(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)

	countries := ListCountryPrefixInfo()
	if len(countries) == 0 {
		t.Fatal("expected non-empty countries list")
	}

	// Import RU
	res, err := h.svc.ImportCountryRules(ctx, "RU", "relay-ru.example.com", "")
	if err != nil {
		t.Fatalf("ImportCountryRules err = %v", err)
	}
	if res.CountryCode != "RU" || res.AddedCount <= 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Repeat import should not add duplicates
	res2, err := h.svc.ImportCountryRules(ctx, "RU", "relay-ru.example.com", "")
	if err != nil {
		t.Fatalf("repeat ImportCountryRules err = %v", err)
	}
	if res2.AddedCount != 0 {
		t.Fatalf("expected 0 added on repeat, got %d", res2.AddedCount)
	}

	// Unsupported country
	if _, err := h.svc.ImportCountryRules(ctx, "XX", "relay.example.com", ""); err == nil {
		t.Fatal("expected error for unsupported country XX")
	}
}

func TestPrefixRuleValidationRejectsInjection(t *testing.T) {
	for _, host := range []string{
		"a.example.com:21117\n0.0.0.0/0 evil.example.com:21117",
		"a.example.com:21117\r\n0.0.0.0/0 evil.example.com:21117",
		"a.example.com 0.0.0.0/0",
		"a.example.com:21117\tb.example.com",
		"a.example.com,,\nb.example.com",
		"http://a.example.com",
	} {
		r := PrefixRule{CIDR: "10.0.0.0/8", RelayHost: host}
		if err := r.Validate(); err == nil {
			t.Errorf("relay_host %q: want error, got rule %+v", host, r)
		} else if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("relay_host %q: error %v does not wrap store.ErrInvalid", host, err)
		}
	}

	r := PrefixRule{CIDR: " 10.1.2.3/8 ", RelayHost: " a.example.com:21117, b.example.com ,a.example.com:21117"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.CIDR != "10.0.0.0/8" || r.RelayHost != "a.example.com:21117,b.example.com" {
		t.Fatalf("normalized rule = %+v", r)
	}
	r = PrefixRule{CIDR: "2001:db8::1/32", RelayHost: "[2001:db8::2]:21117"}
	if err := r.Validate(); err != nil || r.CIDR != "2001:db8::/32" {
		t.Fatalf("ipv6 rule = %+v, err %v", r, err)
	}
}

func TestFormatRelayMapSkipsInvalidRows(t *testing.T) {
	got := FormatRelayMap([]PrefixRule{
		{CIDR: "10.1.0.0/8", RelayHost: "ok.example.com:21117"},
		{CIDR: "192.168.0.0/16", RelayHost: "a:21117\n0.0.0.0/0 evil.example.com:21117"},
		{CIDR: "not-a-cidr", RelayHost: "ok.example.com"},
	})
	if got != "10.0.0.0/8 ok.example.com:21117\n" {
		t.Fatalf("FormatRelayMap = %q", got)
	}
}

// TestSyncPrefixRulesConcurrent: concurrent rule changes leave relay_map.txt
// matching the final rule set (list + write are serialised).
func TestSyncPrefixRulesConcurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	geo := &overlapDetector{}
	h := newHarness(t, func(c *Config) {
		c.ListDir = dir
		c.Local = geo
		c.LocalUp = func() bool { return true }
	})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rule := &PrefixRule{CIDR: fmt.Sprintf("10.%d.0.0/16", i), RelayHost: "relay.example.com:21117"}
			if err := h.svc.CreatePrefixRule(ctx, rule); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rules, err := h.st.ListPrefixRules(ctx)
	if err != nil || len(rules) != 20 {
		t.Fatalf("rules %d, err %v", len(rules), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "relay_map.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != FormatRelayMap(rules) {
		t.Fatalf("relay_map.txt is stale:\n%s\nwant\n%s", data, FormatRelayMap(rules))
	}
	if geo.maxInflight() != 1 {
		t.Fatalf("%d syncs ran at once, want them serialised", geo.maxInflight())
	}
}

// overlapDetector is a LocalControl whose ReloadGeo records how many calls
// overlap (each holds for a moment so unserialised callers would collide).
type overlapDetector struct {
	fakeLocalControl
	mu            sync.Mutex
	inflight, max int
}

func (o *overlapDetector) ReloadGeo(context.Context) (string, error) {
	o.mu.Lock()
	o.inflight++
	o.max = max(o.max, o.inflight)
	o.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	o.mu.Lock()
	o.inflight--
	o.mu.Unlock()
	return "ok", nil
}

func (o *overlapDetector) maxInflight() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.max
}

func TestImportCountryRulesUsesGeoIPData(t *testing.T) {
	ctx := context.Background()
	geo := NewGeoIPEngine("")
	if _, err := geo.ParseAndLoad(strings.NewReader("192.0.2.0/24,RU\n198.51.100.0/24,RU\n"), "test.csv"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *Config) { c.GeoIP = geo })

	res, err := h.svc.ImportCountryRules(ctx, "ru", "relay-ru.example.com:21117", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.AddedCount != 2 {
		t.Fatalf("added %d, want the 2 prefixes of the loaded dataset", res.AddedCount)
	}
	rules, _ := h.st.ListPrefixRules(ctx)
	if len(rules) != 2 || rules[0].CIDR != "192.0.2.0/24" || rules[1].CIDR != "198.51.100.0/24" {
		t.Fatalf("rules = %+v", rules)
	}

	// No GeoIP data for DE in the dataset: the curated list is used.
	res, err = h.svc.ImportCountryRules(ctx, "DE", "relay-de.example.com", "")
	if err != nil || res.AddedCount != len(CountryCIDRs["DE"]) {
		t.Fatalf("DE import = %+v, %v", res, err)
	}

	// Input problems are ErrInvalid; store failures are not.
	for _, tc := range []struct{ code, host string }{
		{"XX", "relay.example.com"},
		{"RU", ""},
		{"RU", "relay.example.com\n0.0.0.0/0 evil.example.com"},
	} {
		if _, err := h.svc.ImportCountryRules(ctx, tc.code, tc.host, ""); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("import %q %q: err %v, want ErrInvalid", tc.code, tc.host, err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h.svc.ImportCountryRules(cctx, "NL", "relay.example.com", ""); err == nil || errors.Is(err, store.ErrInvalid) {
		t.Fatalf("import with a failing store: err %v, want a non-validation error", err)
	}
}
