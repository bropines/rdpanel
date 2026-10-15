package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/oidc"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// registerProviders mounts admin CRUD for identity providers.
func (c *authCore) registerProviders(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/identity-providers", c.handleListProviders)
		r.Post("/identity-providers", c.handleCreateProvider)
		r.Get("/identity-providers/{id}", c.handleGetProvider)
		r.Put("/identity-providers/{id}", c.handleUpdateProvider)
		r.Delete("/identity-providers/{id}", c.handleDeleteProvider)
		r.Post("/identity-providers/{id}/test", c.handleTestProvider)
	})
}

// ProviderDTO is components.schemas.IdentityProvider. The client secret is
// write-only: only has_client_secret is returned.
type ProviderDTO struct {
	ID                  int64     `json:"id"`
	Slug                string    `json:"slug"`
	Kind                string    `json:"kind"`
	DisplayName         string    `json:"display_name"`
	ClientID            string    `json:"client_id"`
	HasClientSecret     bool      `json:"has_client_secret"`
	Issuer              *string   `json:"issuer"`
	Scopes              []string  `json:"scopes"`
	PKCEEnabled         bool      `json:"pkce_enabled"`
	PKCEMethod          string    `json:"pkce_method"`
	AutoRegister        bool      `json:"auto_register"`
	LinkByVerifiedEmail bool      `json:"link_by_verified_email"`
	Enabled             bool      `json:"enabled"`
	SortOrder           int       `json:"sort_order"`
	CallbackURL         string    `json:"callback_url"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (c *authCore) providerDTO(p *store.IdentityProvider) ProviderDTO {
	d := ProviderDTO{ID: p.ID, Slug: p.Slug, Kind: string(p.Kind), DisplayName: p.DisplayName, ClientID: p.ClientID,
		HasClientSecret: len(p.ClientSecretEnc) > 0, Issuer: strPtr(p.Issuer), Scopes: p.Scopes,
		PKCEEnabled: p.PKCEEnabled, PKCEMethod: p.PKCEMethod, AutoRegister: p.AutoRegister,
		LinkByVerifiedEmail: p.LinkByVerifiedEmail, Enabled: p.Enabled, SortOrder: p.SortOrder,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if c.d.OIDC != nil {
		d.CallbackURL = c.d.OIDC.CallbackURL()
	}
	if d.Scopes == nil {
		d.Scopes = []string{}
	}
	return d
}

// providerInput is components.schemas.IdentityProviderInput.
type providerInput struct {
	Slug        string `json:"slug"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	ClientID    string `json:"client_id"`
	// ClientSecret: omitted/null keeps the stored secret; "" clears it.
	ClientSecret        *string  `json:"client_secret"`
	Issuer              string   `json:"issuer"`
	Scopes              []string `json:"scopes"`
	PKCEEnabled         *bool    `json:"pkce_enabled"`
	PKCEMethod          string   `json:"pkce_method"`
	AutoRegister        bool     `json:"auto_register"`
	LinkByVerifiedEmail bool     `json:"link_by_verified_email"`
	Enabled             *bool    `json:"enabled"`
	SortOrder           int      `json:"sort_order"`
}

var (
	slugRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	scopeRe = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]{1,128}$`)
)

// apply validates in and copies it onto p; returns a validation message.
func (in *providerInput) apply(p *store.IdentityProvider) string {
	in.Slug, in.ClientID, in.Issuer = strings.TrimSpace(in.Slug), strings.TrimSpace(in.ClientID), strings.TrimSpace(in.Issuer)
	if !slugRe.MatchString(in.Slug) {
		return "slug must be 1-64 characters: letters, digits, '.', '_' or '-'"
	}
	kind := store.ProviderKind(in.Kind)
	if !oidc.SupportedKind(kind) {
		return "kind must be github, google or oidc"
	}
	if in.ClientID == "" || len(in.ClientID) > 512 {
		return "client_id is required"
	}
	if len(in.DisplayName) > 128 {
		return "display_name is too long"
	}
	if kind == store.ProviderOIDC {
		u, err := url.Parse(in.Issuer)
		if in.Issuer == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return "issuer must be an absolute http(s) URL without query or fragment"
		}
	} else {
		in.Issuer = ""
	}
	if len(in.Scopes) > 32 {
		return "too many scopes"
	}
	for _, s := range in.Scopes {
		if !scopeRe.MatchString(s) {
			return "invalid scope " + strconv.Quote(s)
		}
	}
	method := in.PKCEMethod
	if method == "" {
		method = "S256"
	}
	if method != "S256" && method != "plain" {
		return "pkce_method must be S256 or plain"
	}
	if in.ClientSecret != nil && len(*in.ClientSecret) > 4096 {
		return "client_secret is too long"
	}
	p.Slug, p.Kind, p.DisplayName, p.ClientID, p.Issuer = in.Slug, kind, strings.TrimSpace(in.DisplayName), in.ClientID, in.Issuer
	p.Scopes = in.Scopes
	if p.Scopes == nil {
		p.Scopes = []string{}
	}
	p.PKCEEnabled = in.PKCEEnabled == nil || *in.PKCEEnabled
	p.PKCEMethod = method
	p.AutoRegister, p.LinkByVerifiedEmail = in.AutoRegister, in.LinkByVerifiedEmail
	p.Enabled = in.Enabled == nil || *in.Enabled
	p.SortOrder = in.SortOrder
	return ""
}

// sealSecret applies a secret change (nil = keep) to p, which must have an id.
func (c *authCore) sealSecret(p *store.IdentityProvider, secret *string) error {
	if secret == nil {
		return nil
	}
	if *secret == "" {
		p.ClientSecretEnc = nil
		return nil
	}
	enc, err := c.d.AEAD.SealString(*secret, store.ProviderSecretAD(p.ID))
	if err != nil {
		return err
	}
	p.ClientSecretEnc = enc
	return nil
}

func providerAuditDetails(p *store.IdentityProvider, secretChanged bool) map[string]any {
	return map[string]any{"slug": p.Slug, "kind": string(p.Kind), "enabled": p.Enabled,
		"auto_register": p.AutoRegister, "link_by_verified_email": p.LinkByVerifiedEmail,
		"secret_changed": secretChanged}
}

func (c *authCore) handleListProviders(w http.ResponseWriter, r *http.Request) {
	provs, err := c.d.Store.IdentityProviders.List(r.Context(), false)
	if err != nil {
		internalError(w)
		return
	}
	out := make([]ProviderDTO, 0, len(provs))
	for _, p := range provs {
		out = append(out, c.providerDTO(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (c *authCore) loadProvider(w http.ResponseWriter, r *http.Request) *store.IdentityProvider {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		httpx.NotFound(w, r)
		return nil
	}
	p, err := c.d.Store.IdentityProviders.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return nil
	}
	if err != nil {
		internalError(w)
		return nil
	}
	return p
}

func (c *authCore) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	if p := c.loadProvider(w, r); p != nil {
		httpx.WriteJSON(w, http.StatusOK, c.providerDTO(p))
	}
}

func writeProviderStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrConflict) {
		httpx.WriteError(w, http.StatusConflict, "conflict", "a provider with this slug already exists")
		return
	}
	internalError(w)
}

func (c *authCore) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in providerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	p := &store.IdentityProvider{}
	if msg := in.apply(p); msg != "" {
		badRequest(w, msg)
		return
	}
	err := c.d.Store.Tx(ctx, func(tx *store.Store) error {
		if err := tx.IdentityProviders.Create(ctx, p); err != nil {
			return err
		}
		if in.ClientSecret != nil && *in.ClientSecret != "" {
			if err := c.sealSecret(p, in.ClientSecret); err != nil {
				return err
			}
			return tx.IdentityProviders.Update(ctx, p)
		}
		return nil
	})
	if err != nil {
		writeProviderStoreErr(w, err)
		return
	}
	c.audit(ctx, r, "identity_provider.create", "identity_provider", strconv.FormatInt(p.ID, 10),
		providerAuditDetails(p, in.ClientSecret != nil && *in.ClientSecret != ""))
	httpx.WriteJSON(w, http.StatusCreated, c.providerDTO(p))
}

func (c *authCore) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := c.loadProvider(w, r)
	if p == nil {
		return
	}
	var in providerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.apply(p); msg != "" {
		badRequest(w, msg)
		return
	}
	if err := c.sealSecret(p, in.ClientSecret); err != nil {
		internalError(w)
		return
	}
	if err := c.d.Store.IdentityProviders.Update(ctx, p); err != nil {
		writeProviderStoreErr(w, err)
		return
	}
	c.audit(ctx, r, "identity_provider.update", "identity_provider", strconv.FormatInt(p.ID, 10),
		providerAuditDetails(p, in.ClientSecret != nil))
	httpx.WriteJSON(w, http.StatusOK, c.providerDTO(p))
}

func (c *authCore) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := c.loadProvider(w, r)
	if p == nil {
		return
	}
	if err := c.d.Store.IdentityProviders.Delete(ctx, p.ID); err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, "identity_provider.delete", "identity_provider", strconv.FormatInt(p.ID, 10),
		map[string]any{"slug": p.Slug})
	w.WriteHeader(http.StatusNoContent)
}

// providerTestDTO is components.schemas.IdentityProviderTestResult.
type providerTestDTO struct {
	OK                    bool   `json:"ok"`
	Error                 string `json:"error,omitempty"`
	Issuer                string `json:"issuer,omitempty"`
	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string `json:"token_endpoint,omitempty"`
	UserinfoEndpoint      string `json:"userinfo_endpoint,omitempty"`
	JWKSURI               string `json:"jwks_uri,omitempty"`
	CallbackURL           string `json:"callback_url"`
}

// handleTestProvider runs (uncached) discovery and reports the endpoints.
func (c *authCore) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	p := c.loadProvider(w, r)
	if p == nil {
		return
	}
	if c.d.OIDC == nil {
		httpx.WriteError(w, http.StatusConflict, "oidc_not_configured", "OIDC is not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out := providerTestDTO{CallbackURL: c.d.OIDC.CallbackURL()}
	m, err := c.d.OIDC.Discover(ctx, p)
	if err != nil {
		out.Error = err.Error()
	} else {
		out.OK = true
		out.Issuer, out.AuthorizationEndpoint, out.TokenEndpoint = m.Issuer, m.AuthorizationEndpoint, m.TokenEndpoint
		out.UserinfoEndpoint, out.JWKSURI = m.UserinfoEndpoint, m.JWKSURI
	}
	if out.CallbackURL == "" {
		out.OK = false
		if out.Error == "" {
			out.Error = oidc.ErrNotConfigured.Error()
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
