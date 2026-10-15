package legacy

import (
	"database/sql"
	"regexp"
	"testing"
	"time"
)

func TestParseLegacyTime(t *testing.T) {
	sh := time.FixedZone("+08", 8*3600)
	cases := []struct {
		in   string
		loc  *time.Location
		want time.Time
	}{
		{"2024-05-01 10:00:00.123456789+08:00", time.UTC, time.Date(2024, 5, 1, 2, 0, 0, 123456789, time.UTC)},
		{"2024-05-01 10:00:00+08:00", time.UTC, time.Date(2024, 5, 1, 2, 0, 0, 0, time.UTC)},
		{"2024-05-01T10:00:00Z", time.UTC, time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)},
		{"2024-05-01T10:00:00.5-03:00", time.UTC, time.Date(2024, 5, 1, 13, 0, 0, 500000000, time.UTC)},
		{"2024-05-01 10:00:00 +0800 CST m=+0.000001", time.UTC, time.Date(2024, 5, 1, 2, 0, 0, 0, time.UTC)},
		{"2024-05-01 10:00:00", sh, time.Date(2024, 5, 1, 2, 0, 0, 0, time.UTC)},
		{"2024-05-01 10:00:00", time.UTC, time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)},
		{"2024-05-01", time.UTC, time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)},
		{"1714528800", time.UTC, time.Unix(1714528800, 0).UTC()},
		{"1714528800123", time.UTC, time.UnixMilli(1714528800123).UTC()},
	}
	for _, c := range cases {
		got, err := parseLegacyTime(c.in, c.loc)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "garbage", "2024-13-45 99:00:00"} {
		if _, err := parseLegacyTime(bad, time.UTC); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseLocation(t *testing.T) {
	for _, s := range []string{"", "UTC", "Asia/Shanghai", "+08:00", "-0530"} {
		if _, err := ParseLocation(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	loc, _ := ParseLocation("+08:00")
	if _, off := time.Date(2024, 1, 1, 0, 0, 0, 0, loc).Zone(); off != 8*3600 {
		t.Fatalf("offset %d", off)
	}
	if _, err := ParseLocation("Mars/Base"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseBool(t *testing.T) {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	for _, v := range []sql.NullString{ns("1"), ns("true"), ns("TRUE"), ns("1.0"), ns("2")} {
		if !parseBool(v) {
			t.Errorf("%q should be true", v.String)
		}
	}
	for _, v := range []sql.NullString{{}, ns("0"), ns("false"), ns(""), ns("0.0"), ns("nonsense")} {
		if parseBool(v) {
			t.Errorf("%q should be false", v.String)
		}
	}
}

func TestParseTagNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"", nil, true},
		{"null", nil, true},
		{"[]", nil, true},
		{`["a","b","a",""]`, []string{"a", "b"}, true},
		{`[1, "x", null, {"y":1}]`, []string{"x"}, true},
		{"not json", nil, false},
		{`{"a":1}`, nil, false},
	}
	for _, c := range cases {
		got, ok := parseTagNames(c.in)
		if ok != c.ok || len(got) != len(c.want) {
			t.Errorf("%q: got %v %v", c.in, got, ok)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v", c.in, got)
			}
		}
	}
}

func TestSplitScopesAndInfo(t *testing.T) {
	if got := splitScopes(" openid, email,profile,,email "); got != `["openid","email","profile"]` {
		t.Fatal(got)
	}
	if got := splitScopes(""); got != `[]` {
		t.Fatal(got)
	}
	if got, ok := normaliseAuditInfo(`{"a":1}`); got != `{"a":1}` || !ok {
		t.Fatal(got)
	}
	if got, ok := normaliseAuditInfo(""); got != `{}` || !ok {
		t.Fatal(got)
	}
	if got, ok := normaliseAuditInfo(`oops "x"`); got != `{"raw":"oops \"x\""}` || ok {
		t.Fatal(got)
	}
}

func TestPasswordAlgo(t *testing.T) {
	cases := map[string]string{
		"": "",
		"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0": "bcrypt",
		"5f4dcc3b5aa765d61d8327deb882cf99":                             "legacy_md5_rustdesk_api",
	}
	for in, want := range cases {
		got, known := passwordAlgo(in)
		if got != want || !known {
			t.Errorf("%q: %q %v", in, got, known)
		}
	}
	for _, in := range []string{"plaintext", "5F4DCC3B5AA765D61D8327DEB882CF99", "$2a$10$short"} {
		if _, known := passwordAlgo(in); known {
			t.Errorf("%q should be unknown", in)
		}
	}
}

func TestLegacyUUID(t *testing.T) {
	a, b := legacyUUID("audit_conns", 1), legacyUUID("audit_conns", 1)
	if a != b || a == legacyUUID("audit_conns", 2) || a == legacyUUID("audit_files", 1) {
		t.Fatal("not deterministic / not distinct")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("not a v5 uuid: %s", a)
	}
	// RFC 4122 appendix B style check against a known vector:
	// uuid5(NAMESPACE_DNS, "python.org") = 886313e1-3b8a-5372-9b90-0c9aee199e5d
	if got := formatUUID(uuidV5(mustParseUUID("6ba7b810-9dad-11d1-80b4-00c04fd430c8"), "python.org")); got != "886313e1-3b8a-5372-9b90-0c9aee199e5d" {
		t.Fatalf("uuidv5 vector: %s", got)
	}
}
