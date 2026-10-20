package db

import (
	"database/sql"
	"errors"
	"reflect"
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
			name:     "question mark in quoted identifier",
			input:    `SELECT "odd?""name" FROM t WHERE id = ?`,
			expected: `SELECT "odd?""name" FROM t WHERE id = $1`,
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
		{
			name:     "numbered params keep their index",
			input:    "SELECT * FROM b WHERE owner = ?1 OR EXISTS (SELECT 1 FROM s WHERE s.user = ?1 AND s.x = ?2) LIMIT ?3 OFFSET ?12",
			expected: "SELECT * FROM b WHERE owner = $1 OR EXISTS (SELECT 1 FROM s WHERE s.user = $1 AND s.x = $2) LIMIT $3 OFFSET $12",
		},
		{
			name:     "numbered params out of order",
			input:    "SELECT ?2, ?1",
			expected: "SELECT $2, $1",
		},
		{
			name:     "LIKE untouched",
			input:    "SELECT 1 WHERE a LIKE ? ESCAPE '\\'",
			expected: "SELECT 1 WHERE a LIKE $1 ESCAPE '\\'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Rebind(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.expected {
				t.Errorf("Rebind(%q)\ngot:  %q\nwant: %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestRebindRejectsMixedPlaceholders(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM t WHERE a = ?1 AND b = ?",
		"SELECT * FROM t WHERE a = ? AND b = ?2",
	} {
		if _, err := Rebind(q); !errors.Is(err, ErrMixedPlaceholders) {
			t.Errorf("Rebind(%q) err = %v, want ErrMixedPlaceholders", q, err)
		}
	}
	// Literals and comments do not count.
	if _, err := Rebind("SELECT '?1', ? -- ?2"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPostgresQuery(t *testing.T) {
	tests := []struct{ in, want string }{
		{"SELECT 1 WHERE a LIKE ? ESCAPE '\\' OR b like ?", "SELECT 1 WHERE a ILIKE $1 ESCAPE '\\' OR b ILIKE $2"},
		{"SELECT 1 WHERE a NOT LIKE ?1", "SELECT 1 WHERE a NOT ILIKE $1"},
		{"SELECT 1 WHERE (lower(x) LIKE '%a%')", "SELECT 1 WHERE (lower(x) ILIKE '%a%')"},
		// Only the keyword: identifiers, literals and comments stay.
		{"SELECT likes, is_like, \"like\", 'LIKE' FROM t -- LIKE", "SELECT likes, is_like, \"like\", 'LIKE' FROM t -- LIKE"},
		{"SELECT 1 WHERE x ILIKE ?", "SELECT 1 WHERE x ILIKE $1"},
	}
	for _, tc := range tests {
		got, _, err := postgresQuery(tc.in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("postgresQuery(%q)\ngot:  %q\nwant: %q", tc.in, got, tc.want)
		}
	}
	if _, _, err := postgresQuery("SELECT ?1, ?", []any{1, 2}); !errors.Is(err, ErrMixedPlaceholders) {
		t.Errorf("mixed placeholders: %v", err)
	}
}

// TestPostgresQueryDropsUnusedArgs: SQLite ignores arguments no ?N refers to
// (a query builder may bind ?1 for a clause it then leaves out); PostgreSQL
// cannot type them, so they are dropped and the rest renumbered.
func TestPostgresQueryDropsUnusedArgs(t *testing.T) {
	q, args, err := postgresQuery("SELECT count(*) FROM b WHERE b.id = ?2 AND b.owner = ?4 OR b.x = ?2", []any{"viewer", 7, "unused", true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "SELECT count(*) FROM b WHERE b.id = $1 AND b.owner = $2 OR b.x = $1"; q != want {
		t.Errorf("query %q, want %q", q, want)
	}
	if want := []any{7, int64(1)}; !reflect.DeepEqual(args, want) {
		t.Errorf("args %#v, want %#v", args, want)
	}
	// No placeholder at all (a builder bound ?1 for a clause it left out).
	if q, args, _ = postgresQuery("SELECT count(*) FROM b", []any{"viewer"}); q != "SELECT count(*) FROM b" || args != nil {
		t.Errorf("got %q %v", q, args)
	}
	// All referenced: unchanged.
	q, args, _ = postgresQuery("SELECT ?2, ?1", []any{1, 2})
	if q != "SELECT $2, $1" || !reflect.DeepEqual(args, []any{1, 2}) {
		t.Errorf("got %q %v", q, args)
	}
}

func TestPostgresArgs(t *testing.T) {
	yes := true
	var nilBool *bool
	in := []any{true, false, &yes, nilBool, sql.NullBool{Bool: true, Valid: true}, sql.NullBool{}, int64(7), "x", nil}
	got := postgresArgs(in)
	want := []any{int64(1), int64(0), int64(1), nil, int64(1), nil, int64(7), "x", nil}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("postgresArgs = %#v, want %#v", got, want)
	}
	if in[0] != true {
		t.Fatal("caller's slice modified")
	}
	plain := []any{1, "a"}
	if out := postgresArgs(plain); &out[0] != &plain[0] {
		t.Fatal("slice without booleans should be returned as is")
	}
}
