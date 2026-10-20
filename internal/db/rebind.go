package db

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMixedPlaceholders is returned for a query that mixes plain ('?') and
// numbered ('?N') placeholders. SQLite numbers a plain '?' after the largest
// index seen so far, which has no faithful PostgreSQL equivalent.
var ErrMixedPlaceholders = errors.New("db: query mixes ? and ?N placeholders")

// Rebind converts SQLite parameter placeholders in query to PostgreSQL ones:
// plain '?' become sequential '$1', '$2', ... and numbered '?N' become '$N'
// (so a value bound once can be referenced several times). A query that mixes
// both styles is rejected with ErrMixedPlaceholders.
//
// Single-quoted literals ('...'), double-quoted identifiers ("..."), line
// comments (-- ...) and block comments (/* ... */) are copied verbatim.
func Rebind(query string) (string, error) {
	return rewrite(query, false)
}

// postgresSQL adapts a query written for SQLite (the reference dialect of the
// repositories) to PostgreSQL: placeholders are rebound as in Rebind, and LIKE
// becomes ILIKE because SQLite's LIKE is case-insensitive (ASCII) while
// PostgreSQL's is not; searches must behave the same on both engines.
func postgresSQL(query string) (string, error) {
	return rewrite(query, true)
}

func rewrite(query string, ilike bool) (string, error) {
	var b strings.Builder
	b.Grow(len(query) + 16)

	var plain, numbered bool
	next := 1
	n := len(query)
	for i := 0; i < n; {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			// Literal or quoted identifier; a doubled quote is an escaped one.
			j := i + 1
			for j < n {
				if query[j] == c {
					if j+1 < n && query[j+1] == c {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(query[i:j])
			i = j
		case c == '-' && i+1 < n && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = n
			} else {
				j += i + 1
			}
			b.WriteString(query[i:j])
			i = j
		case c == '/' && i+1 < n && query[i+1] == '*':
			j := strings.Index(query[i+2:], "*/")
			if j < 0 {
				j = n
			} else {
				j += i + 4
			}
			b.WriteString(query[i:j])
			i = j
		case c == '?':
			j := i + 1
			for j < n && query[j] >= '0' && query[j] <= '9' {
				j++
			}
			b.WriteByte('$')
			if j > i+1 {
				numbered = true
				b.WriteString(query[i+1 : j])
			} else {
				plain = true
				fmt.Fprintf(&b, "%d", next)
				next++
			}
			if plain && numbered {
				return "", ErrMixedPlaceholders
			}
			i = j
		case ilike && (c == 'l' || c == 'L') && isKeywordAt(query, i, "like"):
			b.WriteString("ILIKE")
			i += 4
		case isIdentByte(c):
			// Copy a whole identifier so keywords are only matched as words.
			j := i + 1
			for j < n && isIdentByte(query[j]) {
				j++
			}
			b.WriteString(query[i:j])
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// isKeywordAt reports whether the identifier starting at i is exactly kw
// (case-insensitive). i must be at the start of an identifier.
func isKeywordAt(s string, i int, kw string) bool {
	end := i + len(kw)
	if end > len(s) || !strings.EqualFold(s[i:end], kw) {
		return false
	}
	return end == len(s) || !isIdentByte(s[end])
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
