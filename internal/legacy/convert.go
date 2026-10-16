package legacy

import (
	"crypto/sha1" //nolint:gosec // UUIDv5 is defined over SHA-1; not used for security.
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // --legacy-tz must work in minimal containers without zoneinfo.
	"unicode/utf8"
)

// Layouts accepted for legacy `timestamp` TEXT values. mattn/go-sqlite3 writes
// "2006-01-02 15:04:05.999999999-07:00"; the others cover rows written by other
// tools. Fractional seconds are optional when parsing with .999999999.
var offsetLayouts = []string{
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999 -0700 MST", // Go time.Time.String()
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04Z07:00",
	"2006-01-02T15:04Z07:00",
}

var localLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseLegacyTime parses a legacy timestamp. Values with an explicit offset are
// converted losslessly; values without one are interpreted in loc. Pure
// integers are unix seconds (or milliseconds when >= 1e12).
func parseLegacyTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if i := strings.Index(s, " m="); i > 0 { // monotonic suffix from time.Time.String()
		s = s[:i]
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n >= 1e12 || n <= -1e12 {
			return time.UnixMilli(n).UTC(), nil
		}
		return time.Unix(n, 0).UTC(), nil
	}
	for _, l := range offsetLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	for _, l := range localLayouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

// ParseLocation resolves a --legacy-tz value: an IANA zone name ("Asia/Shanghai"),
// "UTC"/"Local", or a fixed offset ("+08:00", "-0530").
func ParseLocation(s string) (*time.Location, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "utc") || s == "Z" {
		return time.UTC, nil
	}
	if s[0] == '+' || s[0] == '-' {
		if t, err := time.Parse("-07:00", s); err == nil {
			_, off := t.Zone()
			return time.FixedZone(s, off), nil
		}
		if t, err := time.Parse("-0700", s); err == nil {
			_, off := t.Zone()
			return time.FixedZone(s, off), nil
		}
		return nil, fmt.Errorf("invalid UTC offset %q", s)
	}
	loc, err := time.LoadLocation(s)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q: %w", s, err)
	}
	return loc, nil
}

// parseBool accepts the legacy numeric 0/1 booleans and, defensively, text
// spellings. NULL and unknown values are false.
func parseBool(v sql.NullString) bool {
	if !v.Valid {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(v.String))
	switch s {
	case "true", "t", "yes", "y", "on":
		return true
	case "", "false", "f", "no", "n", "off":
		return false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f != 0
	}
	return false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullStr maps "" to NULL.
func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// parseTagNames parses address_books.tags leniently: a JSON array of strings.
// Empty, null or invalid text yields no tags (legacy AutoJson read it as []).
// Non-string elements and empty names are ignored; duplicates are removed
// while keeping the first-seen order. ok is false when the text was not valid.
func parseTagNames(s string) (names []string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return nil, true
	}
	var raw []any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	for _, v := range raw {
		name, isStr := v.(string)
		if !isStr || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, true
}

// splitScopes splits the legacy comma-separated scopes into a JSON array.
func splitScopes(s string) string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// normaliseAuditInfo returns valid JSON for file_transfer_audits.info:
// the text itself when it is valid JSON, "{}" when empty, otherwise
// {"raw": "<text>"}.
func normaliseAuditInfo(s string) (string, bool) {
	if strings.TrimSpace(s) == "" {
		return "{}", true
	}
	if json.Valid([]byte(s)) && utf8.ValidString(s) {
		return s, true
	}
	b, _ := json.Marshal(map[string]string{"raw": strings.ToValidUTF8(s, "�")})
	return string(b), false
}

var (
	reBcrypt    = regexp.MustCompile(`^\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}$`)
	reLegacyMD5 = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// passwordAlgo sniffs a legacy users.password value. known is false for a
// non-empty value in an unrecognised format.
func passwordAlgo(hash string) (algo string, known bool) {
	switch {
	case hash == "":
		return "", true
	case reBcrypt.MatchString(hash):
		return "bcrypt", true
	case reLegacyMD5.MatchString(hash):
		return "legacy_md5_rustdesk_api", true
	default:
		return "", false
	}
}

// uuidNamespace is the UUIDv5 namespace for ids derived from legacy rows:
// UUIDv5(NameSpaceURL, "https://github.com/bropines/rustdesk-panel/legacy").
// The URL is the project's former repository name and must stay as is: changing
// it would change every derived id and break re-imports.
var uuidNamespace = uuidV5(mustParseUUID("6ba7b811-9dad-11d1-80b4-00c04fd430c8"),
	"https://github.com/bropines/rustdesk-panel/legacy")

// legacyUUID returns a deterministic UUIDv5 for a legacy row, e.g.
// legacyUUID("audit_conns", 42). Re-running the import yields the same value.
func legacyUUID(table string, id int64) string {
	u := uuidV5(uuidNamespace, table+":"+strconv.FormatInt(id, 10))
	return formatUUID(u)
}

func uuidV5(ns [16]byte, name string) [16]byte {
	h := sha1.New() //nolint:gosec
	h.Write(ns[:])
	h.Write([]byte(name))
	var u [16]byte
	copy(u[:], h.Sum(nil))
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return u
}

func mustParseUUID(s string) [16]byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic("legacy: bad uuid " + s)
	}
	var u [16]byte
	copy(u[:], b)
	return u
}

func formatUUID(u [16]byte) string {
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
