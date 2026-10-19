package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxBody bounds every response read from an identity provider.
const maxBody = 1 << 20

// discoveryTTL is how long discovery documents and key sets are cached.
const discoveryTTL = time.Hour

// jwksMinRefresh rate-limits key set refetches triggered by unknown key ids.
const jwksMinRefresh = 30 * time.Second

// Metadata is the subset of an OpenID Provider configuration we use.
type Metadata struct {
	Issuer                   string   `json:"issuer"`
	AuthorizationEndpoint    string   `json:"authorization_endpoint"`
	TokenEndpoint            string   `json:"token_endpoint"`
	UserinfoEndpoint         string   `json:"userinfo_endpoint,omitempty"`
	JWKSURI                  string   `json:"jwks_uri"`
	TokenEndpointAuthMethods []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethods     []string `json:"code_challenge_methods_supported,omitempty"`
}

type cachedMeta struct {
	meta    *Metadata
	fetched time.Time
}

type cachedKeys struct {
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

// cache holds discovery documents and JWKS per URL.
type cache struct {
	mu   sync.Mutex
	meta map[string]cachedMeta
	keys map[string]cachedKeys
}

func newCache() *cache {
	return &cache{meta: map[string]cachedMeta{}, keys: map[string]cachedKeys{}}
}

func getJSON(ctx context.Context, hc *http.Client, rawURL string, hdr map[string]string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", redactURL(rawURL), resp.StatusCode)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("GET %s: invalid JSON: %w", redactURL(rawURL), err)
	}
	return nil
}

// redactURL drops the query string (may carry tokens) for error messages.
func redactURL(s string) string {
	if u, err := url.Parse(s); err == nil {
		u.RawQuery, u.Fragment, u.User = "", "", nil
		return u.String()
	}
	return "<url>"
}

func validEndpoint(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// discover fetches (or returns the cached) discovery document of issuer.
func (c *cache) discover(ctx context.Context, hc *http.Client, issuer string, force bool, now time.Time) (*Metadata, error) {
	issuer = strings.TrimSpace(issuer)
	if !validEndpoint(issuer) {
		return nil, errors.New("issuer must be an absolute http(s) URL")
	}
	c.mu.Lock()
	if m, ok := c.meta[issuer]; ok && !force && now.Sub(m.fetched) < discoveryTTL {
		c.mu.Unlock()
		return m.meta, nil
	}
	c.mu.Unlock()

	wellKnown := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	var m Metadata
	if err := getJSON(ctx, hc, wellKnown, nil, &m); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	// The issuer in the document must match the configured one (OIDC
	// Discovery §4.3); tolerate only a trailing-slash difference.
	if m.Issuer != issuer && strings.TrimRight(m.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return nil, fmt.Errorf("discovery: issuer mismatch: document says %q", m.Issuer)
	}
	for name, ep := range map[string]string{
		"authorization_endpoint": m.AuthorizationEndpoint, "token_endpoint": m.TokenEndpoint, "jwks_uri": m.JWKSURI,
	} {
		if !validEndpoint(ep) {
			return nil, fmt.Errorf("discovery: missing or invalid %s", name)
		}
	}
	if m.UserinfoEndpoint != "" && !validEndpoint(m.UserinfoEndpoint) {
		m.UserinfoEndpoint = ""
	}
	c.mu.Lock()
	c.meta[issuer] = cachedMeta{meta: &m, fetched: now}
	c.mu.Unlock()
	return &m, nil
}

// jwk is one JSON Web Key (RSA or EC public key).
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func b64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := b64(k.N)
		if err != nil || len(n) < 256 {
			return nil, errors.New("bad RSA modulus (need >= 2048 bits)")
		}
		e, err := b64(k.E)
		if err != nil || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("bad RSA exponent")
		}
		var exp int
		for _, b := range e {
			exp = exp<<8 | int(b)
		}
		if exp < 3 {
			return nil, errors.New("bad RSA exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}, nil
	case "EC":
		var (
			curve elliptic.Curve
			size  int
		)
		switch k.Crv {
		case "P-256":
			curve, size = elliptic.P256(), 32
		case "P-384":
			curve, size = elliptic.P384(), 48
		case "P-521":
			curve, size = elliptic.P521(), 66
		default:
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err1 := b64(k.X)
		y, err2 := b64(k.Y)
		if err1 != nil || err2 != nil || len(x) != size || len(y) != size {
			return nil, errors.New("bad EC point")
		}
		point := append(append([]byte{4}, x...), y...)
		pk, err := ecdsa.ParseUncompressedPublicKey(curve, point)
		if err != nil {
			return nil, errors.New("EC point not on curve")
		}
		return pk, nil
	default:
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
}

// keyFor returns the verification key with kid from jwksURI, refetching the
// set (rate-limited) when the kid is unknown. An empty kid matches only when
// the set holds exactly one key.
func (c *cache) keyFor(ctx context.Context, hc *http.Client, jwksURI, kid string, now time.Time) (crypto.PublicKey, error) {
	lookup := func(set cachedKeys) (crypto.PublicKey, bool) {
		if kid == "" {
			if len(set.keys) == 1 {
				for _, k := range set.keys {
					return k, true
				}
			}
			return nil, false
		}
		k, ok := set.keys[kid]
		return k, ok
	}
	c.mu.Lock()
	set, cached := c.keys[jwksURI]
	c.mu.Unlock()
	if cached && now.Sub(set.fetched) < discoveryTTL {
		if k, ok := lookup(set); ok {
			return k, nil
		}
		if now.Sub(set.fetched) < jwksMinRefresh {
			return nil, errors.New("unknown signing key")
		}
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := getJSON(ctx, hc, jwksURI, nil, &doc); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	set = cachedKeys{keys: map[string]crypto.PublicKey{}, fetched: now}
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pk, err := k.publicKey()
		if err != nil {
			continue
		}
		set.keys[k.Kid] = pk
	}
	c.mu.Lock()
	c.keys[jwksURI] = set
	c.mu.Unlock()
	if k, ok := lookup(set); ok {
		return k, nil
	}
	return nil, errors.New("unknown signing key")
}
