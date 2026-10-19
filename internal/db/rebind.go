package db

import (
	"strconv"
	"strings"
)

// Rebind converts '?' parameter placeholders in query into PostgreSQL '$1', '$2', ... placeholders.
// It preserves '?' characters inside single-quoted string literals ('...'), block comments (/* ... */),
// and line comments (-- ...).
func Rebind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 16)

	paramIndex := 1
	n := len(query)

	for i := 0; i < n; {
		c := query[i]

		// Single-quoted string literal: '...'
		if c == '\'' {
			b.WriteByte(c)
			i++
			for i < n {
				ch := query[i]
				b.WriteByte(ch)
				if ch == '\'' {
					// Check for escaped quote ''
					if i+1 < n && query[i+1] == '\'' {
						i++
						b.WriteByte(query[i])
					} else {
						i++
						break
					}
				}
				i++
			}
			continue
		}

		// Line comment: -- ...
		if c == '-' && i+1 < n && query[i+1] == '-' {
			b.WriteByte(c)
			b.WriteByte(query[i+1])
			i += 2
			for i < n {
				ch := query[i]
				b.WriteByte(ch)
				i++
				if ch == '\n' {
					break
				}
			}
			continue
		}

		// Block comment: /* ... */
		if c == '/' && i+1 < n && query[i+1] == '*' {
			b.WriteByte(c)
			b.WriteByte(query[i+1])
			i += 2
			for i < n {
				ch := query[i]
				b.WriteByte(ch)
				if ch == '*' && i+1 < n && query[i+1] == '/' {
					i++
					b.WriteByte(query[i])
					i++
					break
				}
				i++
			}
			continue
		}

		// Placeholder: ?
		if c == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(paramIndex))
			paramIndex++
			i++
			continue
		}

		b.WriteByte(c)
		i++
	}

	return b.String()
}
