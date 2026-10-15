package config

import "log/slog"

// redacted is what a non-empty Secret renders as anywhere outside of Reveal.
const redacted = "[redacted]"

// Secret is a string configuration value that must never be printed or logged.
// Every textual representation (fmt, JSON, YAML, slog) is redacted; use Reveal
// to obtain the raw value.
type Secret string

// Reveal returns the raw secret value.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether the secret is non-empty.
func (s Secret) IsSet() bool { return s != "" }

// String implements fmt.Stringer with a redacted value.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

// GoString implements fmt.GoStringer so %#v is redacted too.
func (s Secret) GoString() string { return `config.Secret("` + s.String() + `")` }

// MarshalText implements encoding.TextMarshaler (used by JSON) with a redacted value.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// MarshalYAML implements yaml.Marshaler with a redacted value.
func (s Secret) MarshalYAML() (any, error) { return s.String(), nil }

// LogValue implements slog.LogValuer with a redacted value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }
