package store

import (
	"database/sql"
	"strings"
	"time"
)

// toMs converts t to UTC unix milliseconds (the storage format of every timestamp).
func toMs(t time.Time) int64 { return t.UnixMilli() }

// fromMs converts stored unix milliseconds to a UTC time.
func fromMs(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// nullMs converts an optional time to a nullable column value.
func nullMs(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: toMs(*t), Valid: true}
}

// ptrMs converts a nullable column value to an optional time.
func ptrMs(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMs(v.Int64)
	return &t
}

// nullStr maps "" to NULL.
func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// nullID maps an optional id to a nullable column value.
func nullID(id *int64) sql.NullInt64 {
	if id == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *id, Valid: true}
}

// ptrID converts a nullable id column to *int64.
func ptrID(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

// boolInt maps a bool to the 0/1 storage form.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// escapeLike escapes %, _ and \ for use in a LIKE pattern with ESCAPE '\'.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// clampLimit normalises a page size.
func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}
