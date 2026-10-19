package db

import (
	"testing"
)

func TestRebind(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no params",
			input:    "SELECT id, name FROM users",
			expected: "SELECT id, name FROM users",
		},
		{
			name:     "single param",
			input:    "SELECT * FROM users WHERE id = ?",
			expected: "SELECT * FROM users WHERE id = $1",
		},
		{
			name:     "multiple params",
			input:    "INSERT INTO items (a, b, c) VALUES (?, ?, ?)",
			expected: "INSERT INTO items (a, b, c) VALUES ($1, $2, $3)",
		},
		{
			name:     "question mark inside single-quoted string",
			input:    "SELECT * FROM logs WHERE message = 'what?' AND user_id = ?",
			expected: "SELECT * FROM logs WHERE message = 'what?' AND user_id = $1",
		},
		{
			name:     "escaped single quotes inside string",
			input:    "SELECT * FROM logs WHERE message = 'it''s fine? really' AND id = ?",
			expected: "SELECT * FROM logs WHERE message = 'it''s fine? really' AND id = $1",
		},
		{
			name:     "question mark in line comment",
			input:    "-- is this valid? yes\nSELECT * FROM users WHERE id = ?",
			expected: "-- is this valid? yes\nSELECT * FROM users WHERE id = $1",
		},
		{
			name:     "question mark in block comment",
			input:    "/* does it handle ? yes */ SELECT * FROM users WHERE id = ?",
			expected: "/* does it handle ? yes */ SELECT * FROM users WHERE id = $1",
		},
		{
			name:     "over 10 params",
			input:    "INSERT INTO tbl (c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			expected: "INSERT INTO tbl (c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Rebind(tc.input)
			if got != tc.expected {
				t.Errorf("Rebind(%q)\ngot:  %q\nwant: %q", tc.input, got, tc.expected)
			}
		})
	}
}
