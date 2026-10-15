package store

import (
	"context"
	"testing"
	"time"
)

func TestRetentionDaysParsing(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	days, err := s.Settings.RetentionDays(ctx)
	if err != nil || days[RetentionAdminAudit] != DefaultRetentionDays || len(days) != 6 {
		t.Fatalf("default: %v %v", days, err)
	}
	if err := s.Settings.Set(ctx, SettingRetentionDays, 30, nil); err != nil {
		t.Fatal(err)
	}
	if days, _ = s.Settings.RetentionDays(ctx); days[RetentionLoginEvents] != 30 || days[RetentionPunchRequests] != 30 {
		t.Fatalf("number: %v", days)
	}
	if err := s.Settings.Set(ctx, SettingRetentionDays, map[string]int{"login_events_days": 7, "admin_audit_days": 0}, nil); err != nil {
		t.Fatal(err)
	}
	days, _ = s.Settings.RetentionDays(ctx)
	if days[RetentionLoginEvents] != 7 || days[RetentionAdminAudit] != 0 || days[RetentionAlarmAudits] != DefaultRetentionDays {
		t.Fatalf("object: %v", days)
	}
}

func TestPurgeRetention(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	old := clk.now().Add(-40 * 24 * time.Hour)
	veryOld := clk.now().Add(-70 * 24 * time.Hour)
	ended := old.Add(time.Hour)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Settings.Set(ctx, SettingRetentionDays, map[string]int{"login_events_days": 30, "connection_audits_days": 30,
		"file_transfer_audits_days": 30, "alarm_audits_days": 30, "admin_audit_days": 0, "punch_requests_days": 30}, nil))
	must(s.LoginEvents.Record(ctx, &LoginEvent{Method: LoginMethodPassword, CreatedAt: old}))
	must(s.LoginEvents.Record(ctx, &LoginEvent{Method: LoginMethodPassword}))
	must(s.ConnectionAudits.Create(ctx, &ConnectionAudit{DeviceID: "1", StartedAt: old, EndedAt: &ended}))
	must(s.ConnectionAudits.Create(ctx, &ConnectionAudit{DeviceID: "2", StartedAt: old}))     // open: kept (< 2x)
	must(s.ConnectionAudits.Create(ctx, &ConnectionAudit{DeviceID: "3", StartedAt: veryOld})) // open but > 2x
	must(s.FileTransferAudits.Create(ctx, &FileTransferAudit{DeviceID: "1", CreatedAt: old}))
	must(s.AlarmAudits.Create(ctx, &AlarmAudit{DeviceID: "1", CreatedAt: old}))
	must(s.AdminAudit.Record(ctx, &AuditEntry{Action: "x.y", CreatedAt: old}))
	must(s.PunchRequests.Insert(ctx, []PunchRequest{{At: old, FromIP: "1.1.1.1", ToRustdeskID: "1"}, {At: clk.now(), FromIP: "1.1.1.1", ToRustdeskID: "2"}}))

	n, err := s.PurgeRetention(ctx, clk.now())
	if err != nil {
		t.Fatal(err)
	}
	wantN := map[string]int64{RetentionLoginEvents: 1, RetentionConnectionAudits: 2, RetentionFileTransferAudits: 1,
		RetentionAlarmAudits: 1, RetentionPunchRequests: 1}
	for k, v := range wantN {
		if n[k] != v {
			t.Fatalf("%s purged %d, want %d (%v)", k, n[k], v, n)
		}
	}
	if _, ok := n[RetentionAdminAudit]; ok {
		t.Fatalf("0 days must keep forever: %v", n)
	}
	if rows, total, _ := s.PunchRequests.ListPanel(ctx, AuditQuery{}); total != 1 || rows[0].ToRustdeskID != "2" {
		t.Fatalf("punch requests left: %v", rows)
	}
}

func TestBookNameUniquenessAndShares(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	u := &User{Username: "o"}
	if err := s.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	a := &AddressBook{OwnerUserID: u.ID, Name: "Team"}
	if err := s.AddressBooks.CreateShared(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddressBooks.CreateShared(ctx, &AddressBook{OwnerUserID: u.ID, Name: "TEAM"}); err == nil {
		t.Fatal("duplicate shared name accepted")
	}
	g := &UserGroup{Name: "g"}
	if err := s.UserGroups.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	sh, err := s.AddressBooks.CreateShare(ctx, a.ID, nil, &g.ID, ABRead)
	if err != nil || sh.GranteeName != "g" || sh.Permission != ABRead {
		t.Fatalf("share: %+v %v", sh, err)
	}
	if _, err := s.AddressBooks.CreateShare(ctx, a.ID, nil, &g.ID, ABFull); err == nil {
		t.Fatal("duplicate share accepted")
	}
	// The SQL rule (ListBooks) agrees with Permission for a group member.
	m := &User{Username: "m", UserGroupID: &g.ID}
	if err := s.Users.Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.AddressBooks.ListBooks(ctx, BookListFilter{ViewerID: m.ID})
	perm, _ := s.AddressBooks.Permission(ctx, a, m.ID)
	if err != nil || total != 1 || items[0].Rule != perm || perm != ABRead || items[0].ShareCount != 1 {
		t.Fatalf("list %+v total %d perm %v err %v", items, total, perm, err)
	}
}
