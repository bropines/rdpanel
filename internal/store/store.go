// Package store holds typed repositories over the panel database.
//
// Pattern for new repositories (other streams: devices, address books, ...):
//
//   - one file per aggregate with a struct holding a db.Querier;
//   - add a field to Store and construct it in build();
//   - map driver errors with mapErr so callers only see ErrNotFound,
//     ErrConflict and ErrReferenced;
//   - timestamps go through toMs/fromMs (UTC unix milliseconds).
//
// Use Store.Tx to run several repository calls atomically.
package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Store groups the repositories. A Store obtained from Tx is bound to that
// transaction and must not be used after the callback returns.
type Store struct {
	db  *db.DB
	q   db.Querier
	now func() time.Time

	Users      *Users
	UserGroups *UserGroups
	AuthTokens *AuthTokens
	Settings   *Settings
	AdminAudit *AdminAudit
}

// New returns a Store backed by d.
func New(d *db.DB) *Store {
	return build(d, d.DB, time.Now)
}

// WithClock returns a copy of s using now for timestamps (tests).
func (s *Store) WithClock(now func() time.Time) *Store {
	return build(s.db, s.q, now)
}

func build(d *db.DB, q db.Querier, now func() time.Time) *Store {
	s := &Store{db: d, q: q, now: now}
	s.Users = &Users{q: q, now: now}
	s.UserGroups = &UserGroups{q: q, now: now}
	s.AuthTokens = &AuthTokens{q: q, now: now}
	s.Settings = &Settings{q: q, now: now}
	s.AdminAudit = &AdminAudit{q: q, now: now}
	return s
}

// Querier exposes the underlying querier (the DB or the current transaction)
// for repositories defined outside this package.
func (s *Store) Querier() db.Querier { return s.q }

// Now returns the store clock's current time.
func (s *Store) Now() time.Time { return s.now() }

// Tx runs fn with a Store bound to a single transaction. Calling Tx on a
// transactional Store runs fn in the existing transaction (no nesting).
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	if _, inTx := s.q.(*sql.Tx); inTx {
		return fn(s)
	}
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return fn(build(s.db, tx, s.now))
	})
}
