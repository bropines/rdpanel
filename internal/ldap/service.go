package ldap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	ldapv3 "github.com/go-ldap/ldap/v3"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/store"
)

var (
	ErrDisabled           = errors.New("ldap is disabled")
	ErrInvalidCredentials = errors.New("invalid ldap credentials")
	ErrUserNotFound       = errors.New("ldap user not found")
	ErrAmbiguousUser      = errors.New("multiple ldap users match the query")
	ErrAutoRegisterOff    = errors.New("ldap auto registration is disabled")
)

// TestRequest configures a test connection and/or lookup attempt.
type TestRequest struct {
	Settings        *store.LDAPSettings `json:"settings,omitempty"`
	RawBindPassword *string             `json:"raw_bind_password,omitempty"`
	TestUsername    string              `json:"test_username,omitempty"`
	TestPassword    string              `json:"test_password,omitempty"`
}

// TestResult reports the outcome of the test.
type TestResult struct {
	OK            bool              `json:"ok"`
	Message       string            `json:"message"`
	Connected     bool              `json:"connected"`
	Bound         bool              `json:"bound"`
	UserFound     bool              `json:"user_found"`
	UserDN        string            `json:"user_dn,omitempty"`
	UserBound     bool              `json:"user_bound"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Groups        []string          `json:"groups,omitempty"`
	MappedGroupID *int64            `json:"mapped_group_id,omitempty"`
}

// SyncResult reports the outcome of a manual or scheduled directory sync.
type SyncResult struct {
	SyncedCount int    `json:"synced_count"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

// Service provides LDAP authentication, directory sync, and configuration testing.
type Service struct {
	store  *store.Store
	aead   *crypto.AEAD
	dialer Dialer
	logger *slog.Logger
	now    func() time.Time
	syncMu sync.Mutex
}

// NewService constructs an LDAP service.
func NewService(st *store.Store, aead *crypto.AEAD, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  st,
		aead:   aead,
		dialer: defaultDialer,
		logger: logger,
		now:    time.Now,
	}
}

// SetDialer allows overriding the LDAP dialer for testing.
func (s *Service) SetDialer(d Dialer) {
	s.dialer = d
}

// GetSettings retrieves the current LDAP configuration.
func (s *Service) GetSettings(ctx context.Context) (*store.LDAPSettings, error) {
	return s.store.LDAPSettings.Get(ctx)
}

// UpdateSettings updates the LDAP configuration and seals the bind password if supplied.
func (s *Service) UpdateSettings(ctx context.Context, settings *store.LDAPSettings, rawBindPassword *string) error {
	if rawBindPassword != nil {
		if *rawBindPassword == "" {
			settings.BindPasswordEnc = []byte{}
		} else {
			if s.aead == nil {
				return errors.New("aead not configured for password encryption")
			}
			enc, err := s.aead.SealString(*rawBindPassword, crypto.LDAPBindPasswordAD())
			if err != nil {
				return fmt.Errorf("seal bind password: %w", err)
			}
			settings.BindPasswordEnc = enc
		}
	}
	return s.store.LDAPSettings.Update(ctx, settings)
}

func (s *Service) decryptBindPassword(cfg *store.LDAPSettings) (string, error) {
	if len(cfg.BindPasswordEnc) == 0 {
		return "", nil
	}
	if s.aead == nil {
		return "", errors.New("aead not configured for password decryption")
	}
	return s.aead.OpenString(cfg.BindPasswordEnc, crypto.LDAPBindPasswordAD())
}

// Authenticate verifies user credentials against LDAP and provisions/updates the local user.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*store.User, error) {
	cfg, err := s.store.LDAPSettings.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	bindPw, err := s.decryptBindPassword(cfg)
	if err != nil {
		s.logger.Error("decrypt ldap bind password failed", "err", err)
		return nil, fmt.Errorf("ldap bind password error: %w", err)
	}

	conn, err := s.dialer(cfg, bindPw)
	if err != nil {
		s.logger.Warn("ldap connection failed", "err", err)
		return nil, fmt.Errorf("ldap connect: %w", err)
	}
	defer conn.Close()

	searchBase := cfg.UserSearchBase
	if searchBase == "" {
		searchBase = cfg.BaseDN
	}
	userFilter := formatUserFilter(cfg.UserFilter, username)

	attrs := []string{"dn"}
	if cfg.UsernameAttribute != "" {
		attrs = append(attrs, cfg.UsernameAttribute)
	}
	if cfg.DisplayNameAttribute != "" {
		attrs = append(attrs, cfg.DisplayNameAttribute)
	}
	if cfg.EmailAttribute != "" {
		attrs = append(attrs, cfg.EmailAttribute)
	}
	if cfg.GroupAttribute != "" {
		attrs = append(attrs, cfg.GroupAttribute)
	}

	searchReq := ldapv3.NewSearchRequest(
		searchBase,
		ldapv3.ScopeWholeSubtree, ldapv3.NeverDerefAliases, 0, 10, false,
		userFilter,
		attrs,
		nil,
	)

	sr, err := conn.Search(searchReq)
	if err != nil {
		s.logger.Warn("ldap user search failed", "username", username, "err", err)
		return nil, fmt.Errorf("ldap search: %w", err)
	}
	if len(sr.Entries) == 0 {
		return nil, ErrUserNotFound
	}
	if len(sr.Entries) > 1 {
		s.logger.Warn("multiple ldap users found for filter", "filter", userFilter, "count", len(sr.Entries))
		return nil, ErrAmbiguousUser
	}

	entry := sr.Entries[0]
	userDN := entry.DN

	// Test user bind on a separate connection
	userConn, err := s.dialer(cfg, "")
	if err != nil {
		return nil, fmt.Errorf("ldap dial for user bind: %w", err)
	}
	defer userConn.Close()

	if err := userConn.Bind(userDN, password); err != nil {
		s.logger.Info("ldap user bind failed", "dn", userDN, "err", err)
		return nil, ErrInvalidCredentials
	}

	// Extract attributes
	entryUsername := username
	if cfg.UsernameAttribute != "" {
		if val := entry.GetAttributeValue(cfg.UsernameAttribute); val != "" {
			entryUsername = val
		}
	}
	displayName := entryUsername
	if cfg.DisplayNameAttribute != "" {
		if val := entry.GetAttributeValue(cfg.DisplayNameAttribute); val != "" {
			displayName = val
		}
	}
	email := ""
	if cfg.EmailAttribute != "" {
		email = entry.GetAttributeValue(cfg.EmailAttribute)
	}
	var groups []string
	if cfg.GroupAttribute != "" {
		groups = entry.GetAttributeValues(cfg.GroupAttribute)
	}
	mappedGroupID := resolveGroupID(groups, cfg.GroupMappings, cfg.DefaultUserGroupID)

	// Upsert user in local database
	existing, err := s.store.Users.GetByLDAPDN(ctx, userDN)
	if errors.Is(err, store.ErrNotFound) {
		existing, err = s.store.Users.GetByUsername(ctx, entryUsername)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	if existing != nil {
		existing.DisplayName = displayName
		if email != "" {
			existing.Email = email
		}
		existing.IsLDAP = true
		existing.LDAPDN = userDN
		if mappedGroupID != nil {
			existing.UserGroupID = mappedGroupID
		}
		if err := s.store.Users.Update(ctx, existing); err != nil {
			return nil, fmt.Errorf("update ldap user: %w", err)
		}
		return existing, nil
	}

	if !cfg.AutoRegister {
		return nil, ErrAutoRegisterOff
	}

	newUser := &store.User{
		Username:    entryUsername,
		DisplayName: displayName,
		Email:       email,
		Role:        store.RoleUser,
		Status:      store.UserActive,
		IsLDAP:      true,
		LDAPDN:      userDN,
		UserGroupID: mappedGroupID,
	}
	if err := s.store.Users.Create(ctx, newUser); err != nil {
		return nil, fmt.Errorf("create ldap user: %w", err)
	}
	return newUser, nil
}

// Test checks connectivity, bind credentials, user search and optional user password verification.
func (s *Service) Test(ctx context.Context, req TestRequest) (*TestResult, error) {
	cfg := req.Settings
	if cfg == nil {
		var err error
		cfg, err = s.store.LDAPSettings.Get(ctx)
		if err != nil {
			return nil, err
		}
	}

	bindPw := ""
	if req.RawBindPassword != nil {
		bindPw = *req.RawBindPassword
	} else if len(cfg.BindPasswordEnc) > 0 {
		var err error
		bindPw, err = s.decryptBindPassword(cfg)
		if err != nil {
			return &TestResult{
				OK:      false,
				Message: fmt.Sprintf("failed to decrypt bind password: %v", err),
			}, nil
		}
	}

	res := &TestResult{
		Attributes: make(map[string]string),
	}

	conn, err := s.dialer(cfg, bindPw)
	if err != nil {
		res.OK = false
		res.Message = fmt.Sprintf("Connection failed: %v", err)
		return res, nil
	}
	defer conn.Close()

	res.Connected = true
	res.Bound = true

	testUser := strings.TrimSpace(req.TestUsername)
	if testUser == "" {
		res.OK = true
		res.Message = "Connected and authenticated to LDAP server successfully"
		return res, nil
	}

	searchBase := cfg.UserSearchBase
	if searchBase == "" {
		searchBase = cfg.BaseDN
	}
	userFilter := formatUserFilter(cfg.UserFilter, testUser)

	attrs := []string{"dn"}
	if cfg.UsernameAttribute != "" {
		attrs = append(attrs, cfg.UsernameAttribute)
	}
	if cfg.DisplayNameAttribute != "" {
		attrs = append(attrs, cfg.DisplayNameAttribute)
	}
	if cfg.EmailAttribute != "" {
		attrs = append(attrs, cfg.EmailAttribute)
	}
	if cfg.GroupAttribute != "" {
		attrs = append(attrs, cfg.GroupAttribute)
	}

	searchReq := ldapv3.NewSearchRequest(
		searchBase,
		ldapv3.ScopeWholeSubtree, ldapv3.NeverDerefAliases, 0, 10, false,
		userFilter,
		attrs,
		nil,
	)

	sr, err := conn.Search(searchReq)
	if err != nil {
		res.OK = false
		res.Message = fmt.Sprintf("User search query failed: %v", err)
		return res, nil
	}
	if len(sr.Entries) == 0 {
		res.OK = false
		res.Message = fmt.Sprintf("User %q not found in directory with filter %s", testUser, userFilter)
		return res, nil
	}
	if len(sr.Entries) > 1 {
		res.OK = false
		res.Message = fmt.Sprintf("Search returned %d entries; filter must match exactly one user", len(sr.Entries))
		return res, nil
	}

	entry := sr.Entries[0]
	res.UserFound = true
	res.UserDN = entry.DN

	if cfg.UsernameAttribute != "" {
		res.Attributes[cfg.UsernameAttribute] = entry.GetAttributeValue(cfg.UsernameAttribute)
	}
	if cfg.DisplayNameAttribute != "" {
		res.Attributes[cfg.DisplayNameAttribute] = entry.GetAttributeValue(cfg.DisplayNameAttribute)
	}
	if cfg.EmailAttribute != "" {
		res.Attributes[cfg.EmailAttribute] = entry.GetAttributeValue(cfg.EmailAttribute)
	}
	if cfg.GroupAttribute != "" {
		res.Groups = entry.GetAttributeValues(cfg.GroupAttribute)
	}
	res.MappedGroupID = resolveGroupID(res.Groups, cfg.GroupMappings, cfg.DefaultUserGroupID)

	if req.TestPassword != "" {
		userConn, err := s.dialer(cfg, "")
		if err != nil {
			res.OK = false
			res.Message = fmt.Sprintf("Dial for user bind failed: %v", err)
			return res, nil
		}
		defer userConn.Close()

		if err := userConn.Bind(entry.DN, req.TestPassword); err != nil {
			res.OK = false
			res.Message = fmt.Sprintf("User credentials invalid: %v", err)
			return res, nil
		}
		res.UserBound = true
	}

	res.OK = true
	res.Message = "LDAP test completed successfully"
	return res, nil
}

// Sync iterates directory users matching the sync filter and creates/updates local records.
func (s *Service) Sync(ctx context.Context) (*SyncResult, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	cfg, err := s.store.LDAPSettings.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrDisabled
	}

	bindPw, err := s.decryptBindPassword(cfg)
	if err != nil {
		errMsg := fmt.Sprintf("decrypt bind password: %v", err)
		_ = s.store.LDAPSettings.UpdateSyncResult(ctx, s.now(), "error: "+errMsg, 0)
		return &SyncResult{Status: "error", Error: errMsg}, nil
	}

	conn, err := s.dialer(cfg, bindPw)
	if err != nil {
		errMsg := fmt.Sprintf("connect: %v", err)
		_ = s.store.LDAPSettings.UpdateSyncResult(ctx, s.now(), "error: "+errMsg, 0)
		return &SyncResult{Status: "error", Error: errMsg}, nil
	}
	defer conn.Close()

	searchBase := cfg.UserSearchBase
	if searchBase == "" {
		searchBase = cfg.BaseDN
	}
	syncFilter := formatSyncFilter(cfg.UserFilter)

	attrs := []string{"dn"}
	if cfg.UsernameAttribute != "" {
		attrs = append(attrs, cfg.UsernameAttribute)
	}
	if cfg.DisplayNameAttribute != "" {
		attrs = append(attrs, cfg.DisplayNameAttribute)
	}
	if cfg.EmailAttribute != "" {
		attrs = append(attrs, cfg.EmailAttribute)
	}
	if cfg.GroupAttribute != "" {
		attrs = append(attrs, cfg.GroupAttribute)
	}

	searchReq := ldapv3.NewSearchRequest(
		searchBase,
		ldapv3.ScopeWholeSubtree, ldapv3.NeverDerefAliases, 5000, 30, false,
		syncFilter,
		attrs,
		nil,
	)

	sr, err := conn.Search(searchReq)
	if err != nil {
		errMsg := fmt.Sprintf("search: %v", err)
		_ = s.store.LDAPSettings.UpdateSyncResult(ctx, s.now(), "error: "+errMsg, 0)
		return &SyncResult{Status: "error", Error: errMsg}, nil
	}

	count := 0
	for _, entry := range sr.Entries {
		username := entry.GetAttributeValue(cfg.UsernameAttribute)
		if username == "" {
			continue
		}
		displayName := username
		if val := entry.GetAttributeValue(cfg.DisplayNameAttribute); val != "" {
			displayName = val
		}
		email := entry.GetAttributeValue(cfg.EmailAttribute)
		var groups []string
		if cfg.GroupAttribute != "" {
			groups = entry.GetAttributeValues(cfg.GroupAttribute)
		}
		mappedGroupID := resolveGroupID(groups, cfg.GroupMappings, cfg.DefaultUserGroupID)

		existing, err := s.store.Users.GetByLDAPDN(ctx, entry.DN)
		if errors.Is(err, store.ErrNotFound) {
			existing, _ = s.store.Users.GetByUsername(ctx, username)
		}

		if existing != nil {
			existing.DisplayName = displayName
			if email != "" {
				existing.Email = email
			}
			existing.IsLDAP = true
			existing.LDAPDN = entry.DN
			if mappedGroupID != nil {
				existing.UserGroupID = mappedGroupID
			}
			_ = s.store.Users.Update(ctx, existing)
			count++
		} else if cfg.AutoRegister {
			newUser := &store.User{
				Username:    username,
				DisplayName: displayName,
				Email:       email,
				Role:        store.RoleUser,
				Status:      store.UserActive,
				IsLDAP:      true,
				LDAPDN:      entry.DN,
				UserGroupID: mappedGroupID,
			}
			if err := s.store.Users.Create(ctx, newUser); err == nil {
				count++
			}
		}
	}

	_ = s.store.LDAPSettings.UpdateSyncResult(ctx, s.now(), "success", count)
	return &SyncResult{SyncedCount: count, Status: "success"}, nil
}

// Run executes the scheduled directory sync loop in the background.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg, err := s.store.LDAPSettings.Get(ctx)
			if err != nil || !cfg.Enabled || cfg.SyncIntervalMins <= 0 {
				continue
			}

			shouldSync := cfg.LastSyncAt == nil ||
				s.now().Sub(*cfg.LastSyncAt) >= time.Duration(cfg.SyncIntervalMins)*time.Minute

			if shouldSync {
				s.logger.Info("starting scheduled ldap directory sync")
				res, err := s.Sync(ctx)
				if err != nil {
					s.logger.Warn("scheduled ldap sync error", "err", err)
				} else {
					s.logger.Info("scheduled ldap sync completed", "status", res.Status, "count", res.SyncedCount)
				}
			}
		}
	}
}
