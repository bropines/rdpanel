package store

import (
	"context"
	"testing"
	"time"
)

func TestLDAPSettingsAndUsers(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	// Initial settings should exist with defaults
	cfg, err := s.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatalf("LDAPSettings.Get: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("default enabled = true, want false")
	}
	if cfg.UserFilter == "" {
		t.Errorf("default user_filter empty")
	}
	if cfg.HasBindPassword {
		t.Errorf("default has_bind_password = true, want false")
	}

	// Create user group for mapping
	grp := &UserGroup{Name: "IT Admins"}
	if err := s.UserGroups.Create(ctx, grp); err != nil {
		t.Fatalf("UserGroups.Create: %v", err)
	}

	// Update settings
	cfg.Enabled = true
	cfg.ServerURL = "ldaps://ldap.example.com:636"
	cfg.StartTLS = false
	cfg.InsecureSkipVerify = true
	cfg.BindDN = "cn=admin,dc=example,dc=com"
	cfg.BindPasswordEnc = []byte("encrypted-secret")
	cfg.BaseDN = "dc=example,dc=com"
	cfg.UserSearchBase = "ou=users,dc=example,dc=com"
	cfg.UserFilter = "(&(objectClass=person)(uid=%s))"
	cfg.UsernameAttribute = "uid"
	cfg.DisplayNameAttribute = "cn"
	cfg.EmailAttribute = "mail"
	cfg.GroupAttribute = "memberOf"
	cfg.GroupMappings = map[string]int64{
		"cn=admins,ou=groups,dc=example,dc=com": grp.ID,
	}
	cfg.DefaultUserGroupID = &grp.ID
	cfg.AutoRegister = true
	cfg.SyncIntervalMins = 30

	if err := s.LDAPSettings.Update(ctx, cfg); err != nil {
		t.Fatalf("LDAPSettings.Update: %v", err)
	}

	// Verify updated
	got, err := s.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatalf("LDAPSettings.Get after update: %v", err)
	}
	if !got.Enabled || got.ServerURL != "ldaps://ldap.example.com:636" {
		t.Errorf("got enabled=%v, url=%q", got.Enabled, got.ServerURL)
	}
	if !got.HasBindPassword || string(got.BindPasswordEnc) != "encrypted-secret" {
		t.Errorf("got bind pw enc=%q, has=%v", got.BindPasswordEnc, got.HasBindPassword)
	}
	if got.DefaultUserGroupID == nil || *got.DefaultUserGroupID != grp.ID {
		t.Errorf("got default group %v, want %d", got.DefaultUserGroupID, grp.ID)
	}
	if got.DefaultUserGroupName != "IT Admins" {
		t.Errorf("got default group name %q, want 'IT Admins'", got.DefaultUserGroupName)
	}
	if got.GroupMappings["cn=admins,ou=groups,dc=example,dc=com"] != grp.ID {
		t.Errorf("group mapping mismatch: %v", got.GroupMappings)
	}

	// Update without bind password keeps existing password
	cfg.BindPasswordEnc = nil
	cfg.ServerURL = "ldap://ldap.internal:389"
	if err := s.LDAPSettings.Update(ctx, cfg); err != nil {
		t.Fatalf("LDAPSettings.Update without pw: %v", err)
	}
	got2, err := s.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatalf("LDAPSettings.Get: %v", err)
	}
	if got2.ServerURL != "ldap://ldap.internal:389" {
		t.Errorf("got server url %q", got2.ServerURL)
	}
	if string(got2.BindPasswordEnc) != "encrypted-secret" {
		t.Errorf("bind password was overwritten: %q", got2.BindPasswordEnc)
	}

	// Update sync result
	now := time.Now().Truncate(time.Second)
	if err := s.LDAPSettings.UpdateSyncResult(ctx, now, "success", 42); err != nil {
		t.Fatalf("UpdateSyncResult: %v", err)
	}
	got3, err := s.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatalf("LDAPSettings.Get: %v", err)
	}
	if got3.LastSyncStatus != "success" || got3.LastSyncCount != 42 || got3.LastSyncAt == nil {
		t.Errorf("unexpected sync result: %+v", got3)
	}

	// Test user with LDAP fields
	u := &User{
		Username:    "ldapuser",
		DisplayName: "LDAP User",
		Email:       "ldapuser@example.com",
		Role:        RoleUser,
		Status:      UserActive,
		IsLDAP:      true,
		LDAPDN:      "uid=ldapuser,ou=users,dc=example,dc=com",
	}
	if err := s.Users.Create(ctx, u); err != nil {
		t.Fatalf("Users.Create: %v", err)
	}

	byDN, err := s.Users.GetByLDAPDN(ctx, "uid=ldapuser,ou=users,dc=example,dc=com")
	if err != nil {
		t.Fatalf("Users.GetByLDAPDN: %v", err)
	}
	if byDN.ID != u.ID || !byDN.IsLDAP || byDN.LDAPDN != u.LDAPDN {
		t.Errorf("GetByLDAPDN mismatch: %+v", byDN)
	}

	// Search user row
	row, err := s.Users.GetRow(ctx, u.ID)
	if err != nil {
		t.Fatalf("Users.GetRow: %v", err)
	}
	if !row.IsLDAP || row.LDAPDN != u.LDAPDN {
		t.Errorf("UserRow LDAP fields mismatch: %+v", row)
	}
}
