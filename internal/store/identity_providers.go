package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// ProviderKind is the type of an identity provider.
type ProviderKind string

// Identity provider kinds (identity_providers.kind).
const (
	ProviderGitHub  ProviderKind = "github"
	ProviderGoogle  ProviderKind = "google"
	ProviderOIDC    ProviderKind = "oidc"
	ProviderLinuxDo ProviderKind = "linuxdo"
)

// IdentityProvider is a row of identity_providers. ClientSecretEnc is the
// AEAD ciphertext (associated data from ProviderSecretAD); it is never
// returned by the API.
type IdentityProvider struct {
	ID                  int64
	Slug                string
	Kind                ProviderKind
	DisplayName         string
	ClientID            string
	ClientSecretEnc     []byte
	Issuer              string
	Scopes              []string
	PKCEEnabled         bool
	PKCEMethod          string
	AutoRegister        bool
	LinkByVerifiedEmail bool
	Enabled             bool
	SortOrder           int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ProviderSecretAD is the AEAD associated data binding a client secret to its row.
func ProviderSecretAD(id int64) string {
	return "identity_providers.client_secret_enc:" + strconv.FormatInt(id, 10)
}

// IdentityProviders is the identity_providers repository.
type IdentityProviders struct {
	q   db.Querier
	now func() time.Time
}

const identityProviderColumns = `id, slug, kind, display_name, client_id, client_secret_enc, issuer, scopes,
	pkce_enabled, pkce_method, auto_register, link_by_verified_email, enabled, sort_order, created_at, updated_at`

func scanIdentityProvider(row interface{ Scan(...any) error }) (*IdentityProvider, error) {
	var (
		p                                 IdentityProvider
		issuer                            sql.NullString
		scopes                            string
		pkce, autoReg, linkEmail, enabled int
		createdAt, updatedAt              int64
	)
	err := row.Scan(&p.ID, &p.Slug, &p.Kind, &p.DisplayName, &p.ClientID, &p.ClientSecretEnc, &issuer, &scopes,
		&pkce, &p.PKCEMethod, &autoReg, &linkEmail, &enabled, &p.SortOrder, &createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(scopes), &p.Scopes); err != nil {
		return nil, fmt.Errorf("store: identity provider %d scopes: %w", p.ID, err)
	}
	if p.Scopes == nil {
		p.Scopes = []string{}
	}
	p.Issuer = issuer.String
	p.PKCEEnabled, p.AutoRegister, p.LinkByVerifiedEmail, p.Enabled = pkce == 1, autoReg == 1, linkEmail == 1, enabled == 1
	p.CreatedAt, p.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &p, nil
}

func scopesJSON(s []string) (string, error) {
	if s == nil {
		s = []string{}
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// Create inserts p (ID honoured when non-zero, for the legacy importer).
// ID, CreatedAt and UpdatedAt are set.
func (r *IdentityProviders) Create(ctx context.Context, p *IdentityProvider) error {
	now := r.now()
	if p.PKCEMethod == "" {
		p.PKCEMethod = "S256"
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	scopes, err := scopesJSON(p.Scopes)
	if err != nil {
		return err
	}
	var id sql.NullInt64
	if p.ID != 0 {
		id = sql.NullInt64{Int64: p.ID, Valid: true}
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO identity_providers (id, slug, kind, display_name, client_id,
		client_secret_enc, issuer, scopes, pkce_enabled, pkce_method, auto_register, link_by_verified_email, enabled,
		sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		id, p.Slug, p.Kind, p.DisplayName, p.ClientID, p.ClientSecretEnc, nullStr(p.Issuer), scopes,
		boolInt(p.PKCEEnabled), p.PKCEMethod, boolInt(p.AutoRegister), boolInt(p.LinkByVerifiedEmail),
		boolInt(p.Enabled), p.SortOrder, toMs(p.CreatedAt), toMs(p.UpdatedAt)).Scan(&p.ID))
}

// Get returns a provider by id.
func (r *IdentityProviders) Get(ctx context.Context, id int64) (*IdentityProvider, error) {
	return scanIdentityProvider(r.q.QueryRowContext(ctx,
		`SELECT `+identityProviderColumns+` FROM identity_providers WHERE id = ?`, id))
}

// GetBySlug returns a provider by slug (exact match).
func (r *IdentityProviders) GetBySlug(ctx context.Context, slug string) (*IdentityProvider, error) {
	return scanIdentityProvider(r.q.QueryRowContext(ctx,
		`SELECT `+identityProviderColumns+` FROM identity_providers WHERE slug = ?`, slug))
}

// List returns providers ordered by sort_order, id; enabledOnly filters.
func (r *IdentityProviders) List(ctx context.Context, enabledOnly bool) ([]*IdentityProvider, error) {
	q := `SELECT ` + identityProviderColumns + ` FROM identity_providers`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	rows, err := r.q.QueryContext(ctx, q+` ORDER BY sort_order, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*IdentityProvider
	for rows.Next() {
		p, err := scanIdentityProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}

// Update writes every column of p, including ClientSecretEnc.
func (r *IdentityProviders) Update(ctx context.Context, p *IdentityProvider) error {
	p.UpdatedAt = r.now()
	scopes, err := scopesJSON(p.Scopes)
	if err != nil {
		return err
	}
	return expectOne(r.q.ExecContext(ctx, `UPDATE identity_providers SET slug = ?, kind = ?, display_name = ?,
		client_id = ?, client_secret_enc = ?, issuer = ?, scopes = ?, pkce_enabled = ?, pkce_method = ?,
		auto_register = ?, link_by_verified_email = ?, enabled = ?, sort_order = ?, updated_at = ? WHERE id = ?`,
		p.Slug, p.Kind, p.DisplayName, p.ClientID, p.ClientSecretEnc, nullStr(p.Issuer), scopes,
		boolInt(p.PKCEEnabled), p.PKCEMethod, boolInt(p.AutoRegister), boolInt(p.LinkByVerifiedEmail),
		boolInt(p.Enabled), p.SortOrder, toMs(p.UpdatedAt), p.ID))
}

// Delete removes a provider (its identities and pending states cascade).
func (r *IdentityProviders) Delete(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM identity_providers WHERE id = ?`, id))
}
