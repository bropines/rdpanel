package relays

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
