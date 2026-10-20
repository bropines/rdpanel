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

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
)

// Store groups the repositories. A Store obtained from Tx is bound to that
// transaction and must not be used after the callback returns.
type Store struct {
	db   *db.DB
	q    db.Querier
	opts options

	Users      *Users
	UserGroups *UserGroups
	AuthTokens *AuthTokens
	Settings   *Settings
	AdminAudit *AdminAudit

	Devices            *Devices
	DeviceGroups       *DeviceGroups
	AddressBooks       *AddressBooks
	ConnectionAudits   *ConnectionAudits
	FileTransferAudits *FileTransferAudits
	AlarmAudits        *AlarmAudits
	AuditNonces        *AuditNonces
	LoginEvents        *LoginEvents
	// Identity / OIDC / 2FA (stream B1).
	IdentityProviders *IdentityProviders
	UserIdentities    *UserIdentities
	OAuthStates       *OAuthStates
	TOTPSecrets       *TOTPSecrets
	RecoveryCodes     *RecoveryCodes
	// PunchRequests holds hbbs punch requests collected by the panel (stream B2b).
	PunchRequests *PunchRequests
	// Passkeys (WebAuthn) for panel login.
	WebAuthnCredentials *WebAuthnCredentials
	WebAuthnChallenges  *WebAuthnChallenges
	// APITokens are personal tokens for the admin API.
	APITokens *APITokens
	// ClientStrategies manages client MDM configuration strategies.
	ClientStrategies *ClientStrategies
	// ControlRoles and ConnectionACLs manage remote session permissions and access matrix.
	ControlRoles   *ControlRoles
	ConnectionACLs *ConnectionACLs
	// LDAPSettings manages directory service configuration.
	LDAPSettings *LDAPSettingsRepo
}

// Option configures a Store.
type Option func(*options)

type options struct {
	now   func() time.Time
	aead  *crypto.AEAD
	newID func() string
}

// WithSecrets sets the AEAD used to seal secrets that must be returned to
// clients (address-book passwords and hashes). Repositories that need it fail
// with ErrNoSecrets when it is missing.
func WithSecrets(a *crypto.AEAD) Option { return func(o *options) { o.aead = a } }

// WithIDGenerator replaces the random UUIDv4 generator used for public ids
// (address-book guids, connection-audit guids). Tests use deterministic ids.
func WithIDGenerator(fn func() string) Option { return func(o *options) { o.newID = fn } }

// New returns a Store backed by d.
func New(d *db.DB, opts ...Option) *Store {
	o := options{now: time.Now, newID: NewUUID}
	for _, fn := range opts {
		fn(&o)
	}
	return build(d, d.Wrap(d.DB), o)
}

// WithClock returns a copy of s using now for timestamps (tests).
func (s *Store) WithClock(now func() time.Time) *Store {
	o := s.opts
	o.now = now
	return build(s.db, s.q, o)
}

func build(d *db.DB, q db.Querier, o options) *Store {
	now := o.now
	s := &Store{db: d, q: q, opts: o}
	s.Users = &Users{q: q, now: now}
	s.UserGroups = &UserGroups{q: q, now: now}
	s.AuthTokens = &AuthTokens{q: q, now: now}
	s.Settings = &Settings{q: q, now: now}
	s.AdminAudit = &AdminAudit{q: q, now: now}
	s.Devices = &Devices{q: q, now: now}
	s.DeviceGroups = &DeviceGroups{q: q, now: now}
	s.AddressBooks = &AddressBooks{q: q, d: d, now: now, aead: o.aead, newID: o.newID}
	s.ConnectionAudits = &ConnectionAudits{q: q, now: now, newID: o.newID}
	s.FileTransferAudits = &FileTransferAudits{q: q, now: now}
	s.AlarmAudits = &AlarmAudits{q: q, now: now}
	s.AuditNonces = &AuditNonces{q: q, now: now}
	s.LoginEvents = &LoginEvents{q: q, now: now}
	s.IdentityProviders = &IdentityProviders{q: q, now: now}
	s.UserIdentities = &UserIdentities{q: q, now: now}
	s.OAuthStates = &OAuthStates{q: q, now: now}
	s.TOTPSecrets = &TOTPSecrets{q: q, now: now}
	s.RecoveryCodes = &RecoveryCodes{q: q, now: now}
	s.PunchRequests = &PunchRequests{q: q, d: d, now: now}
	s.WebAuthnCredentials = &WebAuthnCredentials{q: q, now: now}
	s.WebAuthnChallenges = &WebAuthnChallenges{q: q, now: now}
	s.APITokens = &APITokens{q: q, now: now}
	s.ClientStrategies = &ClientStrategies{q: q, d: d, now: now}
	s.ControlRoles = &ControlRoles{q: q, now: now}
	s.ConnectionACLs = &ConnectionACLs{q: q, now: now}
	s.LDAPSettings = &LDAPSettingsRepo{q: q, now: now}
	return s
}

// Querier exposes the underlying querier (the DB or the current transaction)
// for repositories defined outside this package.
func (s *Store) Querier() db.Querier { return s.q }

// Now returns the store clock's current time.
func (s *Store) Now() time.Time { return s.opts.now() }

// NewID returns a fresh public id from the configured generator.
func (s *Store) NewID() string { return s.opts.newID() }

// Tx runs fn with a Store bound to a single transaction. Calling Tx on a
// transactional Store runs fn in the existing transaction (no nesting).
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	if _, inTx := s.q.(*sql.Tx); inTx {
		return fn(s)
	}
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return fn(build(s.db, s.db.Wrap(tx), s.opts))
	})
}

// atomic runs fn with q bound to a transaction: the current one when q already
// is a transaction, otherwise a new one on d.
func atomic(ctx context.Context, d *db.DB, q db.Querier, fn func(q db.Querier) error) error {
	if _, inTx := q.(*sql.Tx); inTx || d == nil {
		return fn(q)
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error { return fn(d.Wrap(tx)) })
}
