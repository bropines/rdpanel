package logs

import (
	"regexp"
	"sort"
	"strings"
)

// Redacted replaces secret values.
const Redacted = "[REDACTED]"

// Redactor removes secrets from log lines before they are stored: known
// literal values (the hbbs key pair, the JWT and secrets-at-rest keys, the
// SMTP password) and anything that looks like a credential: JWTs, bearer
// tokens, Authorization/Cookie headers, password/secret/token/... key=value
// pairs and JSON fields, long values of a bare "key", and credentials in URLs.
//
// It errs on the side of hiding too much: the raw service output stays
// available in `docker logs` for operators with host access.
type Redactor struct {
	literals []string
}

// NewRedactor returns a redactor that also replaces the given literals.
func NewRedactor(secrets ...string) *Redactor { return (&Redactor{}).With(secrets...) }

// minSecretLen is the shortest literal worth replacing (shorter values would
// mangle unrelated text).
const minSecretLen = 8

// With returns a copy that also replaces secrets.
func (r *Redactor) With(secrets ...string) *Redactor {
	out := &Redactor{literals: append([]string(nil), r.literals...)}
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) < minSecretLen {
			continue
		}
		dup := false
		for _, have := range out.literals {
			if have == s {
				dup = true
				break
			}
		}
		if !dup {
			out.literals = append(out.literals, s)
		}
	}
	// Longest first, so a secret containing another is replaced whole.
	sort.SliceStable(out.literals, func(i, j int) bool { return len(out.literals[i]) > len(out.literals[j]) })
	return out
}

// sensitive names (substring of the key, case-insensitive).
const sensitive = `password|passwd|passphrase|secret|token|api[_-]?key|private[_-]?key|privkey|key[_-]?priv|credential|session[_-]?id|cookie|authorization|otp[_-]?code|totp`

var rules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Headers whose whole value is a credential.
	{regexp.MustCompile(`(?i)\b((?:set-)?cookie)(["']?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|[^\r\n]*)`), `${1}${2}` + Redacted},
	{regexp.MustCompile(`(?i)\b((?:proxy-)?authorization)(["']?\s*[:=]\s*["']?)((?:bearer|basic|token|digest)\s+)?[^\s"',;]+`), `${1}${2}${3}` + Redacted},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), `${1} ` + Redacted},
	// JSON fields: "password":"...", "access_token":"...", "client_secret":123.
	{regexp.MustCompile(`(?i)("[A-Za-z0-9_.-]*(?:` + sensitive + `)[A-Za-z0-9_.-]*")(\s*:\s*)("(?:[^"\\]|\\.)*"|[^,}\s]+)`), `${1}${2}"` + Redacted + `"`},
	// JSON "key"/"pk"/"sk" with a long (key-like) value; short values are
	// setting names (reconciler logs key=must_login).
	{regexp.MustCompile(`(?i)("(?:key|pk|sk)")(\s*:\s*)"[A-Za-z0-9+/_=-]{20,}"`), `${1}${2}"` + Redacted + `"`},
	// URL query credentials: ?code=...&state=..., &token=...
	{regexp.MustCompile(`(?i)([?&](?:code|state|id_token|access_token|refresh_token|token|key|secret|password|sig|signature)=)[^&\s"'#]+`), `${1}` + Redacted},
	// Userinfo in URLs: scheme://user:pass@host
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s/:@"']+:)[^\s/@"']+@`), `${1}` + Redacted + `@`},
	// key=value / key: value text (logfmt, hbbs messages).
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:` + sensitive + `)[A-Za-z0-9_.-]*)(\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s,;&"'}\]]+)`), `${1}${2}` + Redacted},
	// Bare key/pk/sk with a long value: hbbs prints "Key: <public key>".
	{regexp.MustCompile(`(?i)\b(key|pk|sk)(\s*[=:]\s*)[A-Za-z0-9+/_-]{20,}={0,2}`), `${1}${2}` + Redacted},
	// JWTs anywhere (header.payload.signature, base64url).
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`), Redacted},
}

// Redact returns s with secrets replaced by [REDACTED].
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	for _, lit := range r.literals {
		if strings.Contains(s, lit) {
			s = strings.ReplaceAll(s, lit, Redacted)
		}
	}
	for _, rule := range rules {
		s = rule.re.ReplaceAllString(s, rule.repl)
	}
	return s
}
