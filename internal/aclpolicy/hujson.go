package aclpolicy

import (
	"bytes"
	"errors"
	"unicode"
)

// Standardize converts HuJSON (JSON with //, /* */ comments and trailing commas)
// into strictly standard RFC 8259 JSON without any external dependencies.
func Standardize(input []byte) ([]byte, error) {
	var out bytes.Buffer
	n := len(input)
	inString := false
	escaped := false

	// First pass: strip comments while respecting string literals
	for i := 0; i < n; i++ {
		b := input[i]

		if inString {
			out.WriteByte(b)
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		// Not in string
		if b == '"' {
			inString = true
			escaped = false
			out.WriteByte(b)
			continue
		}

		// Check for comments
		if b == '/' && i+1 < n {
			next := input[i+1]
			if next == '/' {
				// Single-line comment: skip until newline
				i += 2
				for i < n && input[i] != '\n' && input[i] != '\r' {
					i++
				}
				if i < n {
					out.WriteByte(input[i]) // preserve newline for line counts
				}
				continue
			} else if next == '*' {
				// Multi-line comment: skip until */
				i += 2
				closed := false
				for i+1 < n {
					if input[i] == '*' && input[i+1] == '/' {
						i += 1
						closed = true
						break
					}
					if input[i] == '\n' {
						out.WriteByte('\n') // preserve newline
					}
					i++
				}
				if !closed {
					return nil, errors.New("unclosed multi-line comment")
				}
				continue
			}
		}

		out.WriteByte(b)
	}

	if inString {
		return nil, errors.New("unclosed string literal")
	}

	// Second pass: remove trailing commas before '}' or ']'
	cleaned := out.Bytes()
	var finalOut bytes.Buffer
	inString = false
	escaped = false
	cn := len(cleaned)

	for i := 0; i < cn; i++ {
		b := cleaned[i]

		if inString {
			finalOut.WriteByte(b)
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		if b == '"' {
			inString = true
			escaped = false
			finalOut.WriteByte(b)
			continue
		}

		// Check if current char is a comma followed only by whitespace until '}' or ']'
		if b == ',' {
			isTrailing := false
			for j := i + 1; j < cn; j++ {
				nextChar := cleaned[j]
				if unicode.IsSpace(rune(nextChar)) {
					continue
				}
				if nextChar == '}' || nextChar == ']' {
					isTrailing = true
				}
				break
			}
			if isTrailing {
				// Omit trailing comma
				continue
			}
		}

		finalOut.WriteByte(b)
	}

	return finalOut.Bytes(), nil
}
