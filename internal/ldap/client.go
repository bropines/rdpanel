package ldap

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	ldapv3 "github.com/go-ldap/ldap/v3"

	"github.com/bropines/rdpanel/internal/store"
)

// Client is the interface subset of ldapv3.Conn used by the service.
type Client interface {
	Bind(username, password string) error
	Search(searchRequest *ldapv3.SearchRequest) (*ldapv3.SearchResult, error)
	Close() error
}

// Dialer establishes an LDAP connection and, when cfg.BindDN is set, binds
// as that service account with bindPassword. go-ldap refuses empty-password
// binds, so a connection for a user's own bind must not ask for the service
// bind (see Service.dialUnbound).
type Dialer func(cfg *store.LDAPSettings, bindPassword string) (Client, error)

func defaultDialer(cfg *store.LDAPSettings, bindPassword string) (Client, error) {
	if cfg.ServerURL == "" {
		return nil, errors.New("ldap server_url is empty")
	}
	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("parse server_url: %w", err)
	}

	host := u.Hostname()
	tlsConfig := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	var conn *ldapv3.Conn
	switch strings.ToLower(u.Scheme) {
	case "ldaps":
		conn, err = ldapv3.DialURL(cfg.ServerURL, ldapv3.DialWithTLSConfig(tlsConfig))
	case "ldap":
		conn, err = ldapv3.DialURL(cfg.ServerURL)
		if err == nil && cfg.StartTLS {
			if err = conn.StartTLS(tlsConfig); err != nil {
				conn.Close()
				return nil, fmt.Errorf("ldap starttls: %w", err)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported ldap scheme %q (expected ldap or ldaps)", u.Scheme)
	}
	if err != nil {
		return nil, fmt.Errorf("dial ldap: %w", err)
	}

	conn.SetTimeout(10 * time.Second)

	if cfg.BindDN != "" {
		if err := conn.Bind(cfg.BindDN, bindPassword); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap bind failed: %w", err)
		}
	}
	return conn, nil
}

// extractCN extracts the Common Name from a Distinguished Name.
// e.g. "CN=DevOps,OU=Groups,DC=example,DC=com" -> "DevOps"
func extractCN(dn string) string {
	parts := strings.Split(dn, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "cn=") {
			return strings.TrimSpace(part[3:])
		}
	}
	return ""
}

// resolveGroupID matches user's LDAP group memberships against configured mappings.
// If multiple groups match, the first matching mapped group is returned.
// If none match, defaultGroupID is returned.
func resolveGroupID(userGroups []string, mappings map[string]int64, defaultGroupID *int64) *int64 {
	if len(mappings) > 0 {
		for _, g := range userGroups {
			gTrim := strings.TrimSpace(g)
			cn := extractCN(gTrim)
			for pattern, groupID := range mappings {
				pTrim := strings.TrimSpace(pattern)
				if strings.EqualFold(pTrim, gTrim) || (cn != "" && strings.EqualFold(pTrim, cn)) {
					id := groupID
					return &id
				}
			}
		}
	}
	return defaultGroupID
}

// formatUserFilter formats the configured user filter with the escaped username.
func formatUserFilter(filterTemplate, username string) string {
	if filterTemplate == "" {
		filterTemplate = "(&(objectClass=user)(sAMAccountName=%s))"
	}
	escaped := ldapv3.EscapeFilter(username)
	if strings.Contains(filterTemplate, "%s") {
		return fmt.Sprintf(filterTemplate, escaped)
	}
	return filterTemplate
}

// formatSyncFilter generates a filter that matches all users for directory sync.
func formatSyncFilter(filterTemplate string) string {
	if filterTemplate == "" {
		return "(&(objectClass=user)(sAMAccountName=*))"
	}
	if strings.Contains(filterTemplate, "%s") {
		return strings.ReplaceAll(filterTemplate, "%s", "*")
	}
	return filterTemplate
}
