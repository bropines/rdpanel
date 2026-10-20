package federation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/store"
)

// PeerStatus is the connection status of a peered RD Panel.
type PeerStatus string

const (
	StatusUnknown PeerStatus = "unknown"
	StatusActive  PeerStatus = "active"
	// StatusUnreachable marks a peer whose last sync failed: unreachable,
	// token rejected, or an invalid answer. Peer.LastError says which.
	StatusUnreachable PeerStatus = "unreachable"
)

// MinTokenLen is the shortest accepted peer token (the peer's
// RDPANEL_FEDERATION_TOKEN has the same minimum).
const MinTokenLen = 16

// Peer represents an external peered RD Panel instance.
type Peer struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	APIURL     string     `json:"api_url"`
	TokenHash  string     `json:"-"`
	SyncRelays bool       `json:"sync_relays"`
	Status     PeerStatus `json:"status"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
	LastError  *string    `json:"last_error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// FederatedRelay represents a relay announced by a peered RD Panel.
type FederatedRelay struct {
	ID         int64     `json:"id"`
	PeerID     int64     `json:"peer_id"`
	PeerName   string    `json:"peer_name,omitempty"`
	RemoteName string    `json:"remote_name"`
	PublicHost string    `json:"public_host"`
	Region     string    `json:"region,omitempty"`
	Health     string    `json:"health"`
	LastSyncAt time.Time `json:"last_sync_at"`
}

// Store persists federation peers and announced relays.
//
// The token this panel presents to a peer is needed in plaintext for every
// sync, so it is kept AEAD-sealed in the panel settings under
// "federation.peer.<id>.token" (crypto.FederationPeerTokenAD); the
// federation_peers.token_hash column only holds its SHA-256.
type Store struct {
	DB *db.DB
	// AEAD seals peer tokens (required by Create and Token).
	AEAD *crypto.AEAD
	Now  func() time.Time
}

// ErrNoToken means a peer has no stored token (it was registered by a
// version that kept tokens in memory only): delete and re-add it.
var ErrNoToken = errors.New("federation: no stored token for this peer")

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func ms(t time.Time) int64 { return t.UTC().UnixMilli() }

func tm(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// HashToken computes sha256 hex string for the peer auth token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func peerTokenKey(id int64) string { return "federation.peer." + strconv.FormatInt(id, 10) + ".token" }

// sealedToken is the settings value holding a peer token.
type sealedToken struct {
	TokenEnc []byte `json:"token_enc"`
}

// CreatePeerInput is input for registering a new peer panel.
type CreatePeerInput struct {
	Name       string
	APIURL     string
	AuthToken  string
	SyncRelays bool
}

// NormalizeAPIURL validates a peer base URL (http(s)://host[:port][/prefix],
// no credentials, query or fragment) and trims trailing slashes.
func NormalizeAPIURL(raw string) (string, *url.URL, error) {
	apiURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(apiURL)
	switch {
	case err != nil, u.Scheme != "http" && u.Scheme != "https", u.Host == "", u.Hostname() == "":
		return "", nil, fmt.Errorf("%w: api_url must be an http:// or https:// URL", store.ErrInvalid)
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", nil, fmt.Errorf("%w: api_url must not contain credentials, a query or a fragment", store.ErrInvalid)
	case len(apiURL) > 255:
		return "", nil, fmt.Errorf("%w: api_url is too long", store.ErrInvalid)
	}
	return apiURL, u, nil
}

// Create registers a new peered panel and stores its token sealed.
func (s *Store) Create(ctx context.Context, in CreatePeerInput) (Peer, error) {
	name := strings.TrimSpace(in.Name)
	if len(name) < 1 || len(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
		return Peer{}, fmt.Errorf("%w: name must be 1-64 chars", store.ErrInvalid)
	}
	apiURL, _, err := NormalizeAPIURL(in.APIURL)
	if err != nil {
		return Peer{}, err
	}
	token := strings.TrimSpace(in.AuthToken)
	if len(token) < MinTokenLen || strings.ContainsAny(token, " \t\r\n") {
		return Peer{}, fmt.Errorf("%w: auth_token must be the peer's federation token (at least %d characters, no spaces)",
			store.ErrInvalid, MinTokenLen)
	}
	if s.AEAD == nil {
		return Peer{}, errors.New("federation: secrets key not configured")
	}

	now := ms(s.now())
	var id int64
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		q := s.DB.Wrap(tx)
		err := q.QueryRowContext(ctx, `
			INSERT INTO federation_peers (name, api_url, token_hash, sync_relays, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			name, apiURL, HashToken(token), in.SyncRelays, string(StatusUnknown), now, now).Scan(&id)
		if err != nil {
			if db.IsUniqueViolation(err) || strings.Contains(err.Error(), "UNIQUE") {
				return store.ErrConflict
			}
			return err
		}
		enc, err := s.AEAD.SealString(token, crypto.FederationPeerTokenAD(id))
		if err != nil {
			return err
		}
		raw, err := json.Marshal(sealedToken{TokenEnc: enc})
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO settings (key, value, updated_by, updated_at) VALUES (?, ?, NULL, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`, peerTokenKey(id), string(raw), now)
		return err
	})
	if err != nil {
		return Peer{}, err
	}

	return s.Get(ctx, id)
}

// Token returns the token this panel presents to peer id (ErrNoToken when
// none is stored).
func (s *Store) Token(ctx context.Context, id int64) (string, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, peerTokenKey(id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	var v sealedToken
	if err := json.Unmarshal([]byte(raw), &v); err != nil || len(v.TokenEnc) == 0 {
		return "", ErrNoToken
	}
	if s.AEAD == nil {
		return "", errors.New("federation: secrets key not configured")
	}
	return s.AEAD.OpenString(v.TokenEnc, crypto.FederationPeerTokenAD(id))
}

// List returns all registered peers.
func (s *Store) List(ctx context.Context) ([]Peer, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, api_url, token_hash, sync_relays, status, last_sync_at, last_error, created_at, updated_at
		FROM federation_peers ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var p Peer
		var syncRelays int
		var status string
		var lastSyncAt sql.NullInt64
		var lastError sql.NullString
		var createdAt, updatedAt int64

		if err := rows.Scan(&p.ID, &p.Name, &p.APIURL, &p.TokenHash, &syncRelays, &status, &lastSyncAt, &lastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.SyncRelays = syncRelays != 0
		p.Status = PeerStatus(status)
		p.LastSyncAt = tm(lastSyncAt)
		if lastError.Valid {
			p.LastError = &lastError.String
		}
		p.CreatedAt = time.UnixMilli(createdAt).UTC()
		p.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// Get returns one peer by ID.
func (s *Store) Get(ctx context.Context, id int64) (Peer, error) {
	var p Peer
	var syncRelays int
	var status string
	var lastSyncAt sql.NullInt64
	var lastError sql.NullString
	var createdAt, updatedAt int64

	err := s.DB.QueryRowContext(ctx, `
		SELECT id, name, api_url, token_hash, sync_relays, status, last_sync_at, last_error, created_at, updated_at
		FROM federation_peers WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.APIURL, &p.TokenHash, &syncRelays, &status, &lastSyncAt, &lastError, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Peer{}, store.ErrNotFound
		}
		return Peer{}, err
	}
	p.SyncRelays = syncRelays != 0
	p.Status = PeerStatus(status)
	p.LastSyncAt = tm(lastSyncAt)
	if lastError.Valid {
		p.LastError = &lastError.String
	}
	p.CreatedAt = time.UnixMilli(createdAt).UTC()
	p.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return p, nil
}

// Delete removes a peer, its stored token and its announced relays (via CASCADE).
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		q := s.DB.Wrap(tx)
		res, err := q.ExecContext(ctx, `DELETE FROM federation_peers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		_, err = q.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, peerTokenKey(id))
		return err
	})
}

// UpdateStatus records a peer's sync attempt and current connectivity status.
func (s *Store) UpdateStatus(ctx context.Context, id int64, status PeerStatus, errMsg *string) error {
	now := ms(s.now())
	var errVal any
	if errMsg != nil {
		errVal = *errMsg
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE federation_peers
		SET status = ?, last_sync_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?`, string(status), now, errVal, now, id)
	return err
}

// ReplaceFederatedRelays atomically replaces the relays associated with a peer.
func (s *Store) ReplaceFederatedRelays(ctx context.Context, peerID int64, relays []FederatedRelay) error {
	now := ms(s.now())
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		q := s.DB.Wrap(tx)
		if _, err := q.ExecContext(ctx, `DELETE FROM federated_relays WHERE peer_id = ?`, peerID); err != nil {
			return err
		}
		for _, r := range relays {
			if _, err := q.ExecContext(ctx, `
				INSERT INTO federated_relays (peer_id, remote_name, public_host, region, health, last_sync_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				peerID, r.RemoteName, r.PublicHost, r.Region, r.Health, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListFederatedRelays returns all federated relays from all active peers.
// They are shown for visibility only: nothing adds them to hbbs' rotation.
func (s *Store) ListFederatedRelays(ctx context.Context) ([]FederatedRelay, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT r.id, r.peer_id, p.name, r.remote_name, r.public_host, r.region, r.health, r.last_sync_at
		FROM federated_relays r
		JOIN federation_peers p ON p.id = r.peer_id
		WHERE p.status = 'active'
		ORDER BY p.name ASC, r.remote_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []FederatedRelay
	for rows.Next() {
		var r FederatedRelay
		var region sql.NullString
		var lastSyncAt int64
		if err := rows.Scan(&r.ID, &r.PeerID, &r.PeerName, &r.RemoteName, &r.PublicHost, &region, &r.Health, &lastSyncAt); err != nil {
			return nil, err
		}
		if region.Valid {
			r.Region = region.String
		}
		r.LastSyncAt = time.UnixMilli(lastSyncAt).UTC()
		res = append(res, r)
	}
	return res, rows.Err()
}
