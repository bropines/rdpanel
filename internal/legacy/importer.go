package legacy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// importer carries the state of one run. Maps translate legacy ids into what
// was actually imported so dependent rows can detect orphans.
type importer struct {
	ctx   context.Context
	src   *sql.DB
	tx    *sql.Tx
	opts  *Options
	now   time.Time
	nowMs int64
	rep   *Report

	legacySeq map[string]int64 // legacy sqlite_sequence

	groups         map[int64]bool // imported user_groups ids
	defaultGroupID sql.NullInt64  // fallback for users with group 0 / orphan group
	deviceGroups   map[int64]bool
	users          map[int64]*userInfo
	providers      map[string]providerInfo // by slug
	providerByID   map[int64]providerInfo
	userHasIdent   map[int64]bool
	collections    map[int64]int64 // legacy collection id -> address_books.id (same value)
	personalBook   map[int64]int64 // user id -> personal address_books.id
	tagIDs         map[tagKey]int64
	badTimestamps  map[string]int
}

type userInfo struct {
	username      string
	legacyGroupID int64
	active        bool
	hasPassword   bool
}

type providerInfo struct {
	id   int64
	slug string
	kind string
}

type tagKey struct {
	book int64
	name string
}

func (im *importer) run() error {
	im.groups = map[int64]bool{}
	im.deviceGroups = map[int64]bool{}
	im.users = map[int64]*userInfo{}
	im.providers = map[string]providerInfo{}
	im.providerByID = map[int64]providerInfo{}
	im.userHasIdent = map[int64]bool{}
	im.collections = map[int64]int64{}
	im.personalBook = map[int64]int64{}
	im.tagIDs = map[tagKey]int64{}
	im.badTimestamps = map[string]int{}

	if err := im.loadLegacySequences(); err != nil {
		return err
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"groups", im.importGroups},
		{"device_groups", im.importDeviceGroups},
		{"users", im.importUsers},
		{"oauths", im.importProviders},
		{"user_thirds", im.importIdentities},
		{"peers", im.importPeers},
		{"address_book_collections", im.importCollections},
		{"address_book_collection_rules", im.importRules},
		{"tags", im.importTags},
		{"address_books", im.importEntries},
		{"user_tokens", im.importTokens},
		{"login_logs", im.importLoginLogs},
		{"audit_conns", im.importAuditConns},
		{"audit_files", im.importAuditFiles},
		{"share_records", im.importShareRecords},
		{"server_cmds", im.importServerCmds},
	}
	for _, s := range steps {
		if err := im.ctx.Err(); err != nil {
			return err
		}
		if err := s.fn(); err != nil {
			return fmt.Errorf("legacy: import %s: %w", s.name, err)
		}
		c := im.rep.Tables[s.name]
		im.opts.Log.Info("imported legacy table", "table", s.name, "read", c.Read, "imported", c.Imported,
			"deduped", c.Deduped, "orphaned", c.Orphaned, "skipped", c.Skipped)
	}
	im.postChecks()
	for _, t := range tableOrder {
		if n := im.badTimestamps[t.legacy]; n > 0 {
			im.count(t.legacy).extra("unparseable_timestamps", n)
			im.rep.warn("bad_timestamp", t.legacy, nil,
				"%d timestamp value(s) could not be parsed; the import time or a sibling timestamp was used instead", n)
		}
	}
	return nil
}

func (im *importer) count(table string) *Counts { return im.rep.Tables[table] }

func (c *Counts) extra(key string, n int) {
	if c.Extra == nil {
		c.Extra = map[string]int{}
	}
	c.Extra[key] += n
}

// exec runs a statement on the target transaction.
func (im *importer) exec(query string, args ...any) (sql.Result, error) {
	return im.tx.ExecContext(im.ctx, query, args...)
}

// insertID runs an INSERT and returns the new row id.
func (im *importer) insertID(query string, args ...any) (int64, error) {
	res, err := im.exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// eachRow runs query against the source and calls fn for every row. A
// missing legacy table is treated as empty (with a warning).
func (im *importer) eachRow(table, query string, fn func(*sql.Rows) error) error {
	ok, err := tableExists(im.ctx, im.src, table)
	if err != nil {
		return err
	}
	if !ok {
		im.rep.warn("missing_table", table, nil, "legacy table %s does not exist; treated as empty", table)
		return nil
	}
	rows, err := im.src.QueryContext(im.ctx, query)
	if err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		im.count(table).Read++
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ms converts a legacy timestamp column (read as TEXT) to unix milliseconds.
func (im *importer) ms(table string, v sql.NullString) (int64, bool) {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return 0, false
	}
	t, err := parseLegacyTime(v.String, im.opts.LegacyLocation)
	if err != nil {
		im.badTimestamps[table]++
		return 0, false
	}
	return t.UnixMilli(), true
}

// stamps returns (created_at, updated_at) in ms. created falls back to
// updated, then to the import time; updated falls back to created.
func (im *importer) stamps(table string, created, updated sql.NullString) (int64, int64) {
	c, cok := im.ms(table, created)
	u, uok := im.ms(table, updated)
	switch {
	case cok && uok:
	case cok:
		u = c
	case uok:
		c = u
	default:
		c, u = im.nowMs, im.nowMs
	}
	return c, u
}

// unixSecMs converts a legacy unix-seconds integer; 0 (or negative) = unset.
func unixSecMs(sec int64) sql.NullInt64 {
	if sec <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: sec * 1000, Valid: true}
}

func (im *importer) loadLegacySequences() error {
	im.legacySeq = map[string]int64{}
	ok, err := tableExists(im.ctx, im.src, "sqlite_sequence")
	if err != nil || !ok {
		return err
	}
	rows, err := im.src.QueryContext(im.ctx, `SELECT CAST(name AS TEXT), COALESCE(CAST(seq AS INTEGER), 0) FROM sqlite_sequence`)
	if err != nil {
		return fmt.Errorf("read sqlite_sequence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name sql.NullString
		var seq int64
		if err := rows.Scan(&name, &seq); err != nil {
			return err
		}
		im.legacySeq[name.String] = seq
	}
	return rows.Err()
}

// bumpSequence makes the target AUTOINCREMENT sequence of table at least
// max(legacy sequence of legacyTable, current max id), so ids that legacy
// ever allocated (including deleted rows still embedded in old JWTs or
// cached guids) are never handed out again.
func (im *importer) bumpSequence(table, legacyTable string) error {
	var maxID int64
	if err := im.tx.QueryRowContext(im.ctx, `SELECT COALESCE(max(id), 0) FROM `+table).Scan(&maxID); err != nil {
		return err
	}
	seq := maxID
	if s := im.legacySeq[legacyTable]; s > seq {
		seq = s
	}
	if seq == 0 {
		return nil
	}
	res, err := im.exec(`UPDATE sqlite_sequence SET seq = max(seq, ?) WHERE name = ?`, seq, table)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = im.exec(`INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)`, table, seq)
	}
	return err
}

// postChecks adds warnings that need the whole import.
func (im *importer) postChecks() {
	for _, id := range sortedKeys(im.users) {
		u := im.users[id]
		if !u.hasPassword && !im.userHasIdent[id] {
			im.rep.warn("user_no_login_method", "users", id,
				"user %q has no local password and no linked identity; it cannot log in until an admin sets a password", u.username)
		}
	}
}
