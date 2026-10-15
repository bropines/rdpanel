package app

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bropines/rustdesk-panel/internal/adminapi"
	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
	"github.com/bropines/rustdesk-panel/internal/hbbs/hbbstest"
	"github.com/bropines/rustdesk-panel/internal/store"
)

func TestCollectPunchRequests(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st := store.New(d)
	fake := hbbstest.NewFakeAdmin(t)
	ctl, err := control.New(control.Config{HBBSAddr: fake.Addr(), HBBRAddr: fake.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	ctl.SetAudit(adminapi.NewCommandAudit(st, nil))

	n, err := collectPunchRequests(ctx, ctl, st)
	if err != nil || n != 2 {
		t.Fatalf("collect: %d %v", n, err)
	}
	cmds := fake.Commands()
	if len(cmds) != 2 || cmds[0] != "punch-requests 0 1000" || cmds[1] != "punch-requests -" {
		t.Fatalf("commands: %q", cmds)
	}
	rows, total, err := st.PunchRequests.ListPanel(ctx, store.AuditQuery{})
	if err != nil || total != 2 || rows[0].ToRustdeskID != "abcdef" || rows[0].ToIP != "::2" || rows[1].FromIP != "1.2.3.4" {
		t.Fatalf("stored: %+v %d %v", rows, total, err)
	}
	// The collector's housekeeping clear is not written to admin_audit.
	if audit, _, _ := st.AdminAudit.ListPanel(ctx, store.AdminAuditQuery{}); len(audit) != 0 {
		t.Fatalf("collector audited: %+v", audit)
	}

	// Nothing recorded: no clear.
	fake.SetOverride(func(cmd string) (string, bool) { return "", cmd == "punch-requests 0 1000" })
	fake.ResetCommands()
	if n, err := collectPunchRequests(ctx, ctl, st); err != nil || n != 0 {
		t.Fatalf("empty collect: %d %v", n, err)
	}
	if cmds := fake.Commands(); len(cmds) != 1 {
		t.Fatalf("empty collect sent %q", cmds)
	}
}

func TestB2bRoutesWired(t *testing.T) {
	a := newTestApp(t, nil)
	h := a.Handler()
	for _, p := range []string{"/api/v1/admin/address-books", "/api/v1/admin/audit/connections",
		"/api/v1/admin/server/health", "/api/v1/admin/server/console/commands"} {
		if rec := do(t, h, http.MethodGet, p, "127.0.0.1:1", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d (want 401 from the mounted route)", p, rec.Code)
		}
	}
	var d adminapi.Deps
	a.b2bDeps(&d)
	if d.Presence == nil || d.Control == nil || d.Reconciler == nil {
		t.Fatalf("hbbs deps not wired: %+v", d)
	}
	off := newTestApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false"})
	var d2 adminapi.Deps
	off.b2bDeps(&d2)
	if d2.Presence != nil || d2.Control != nil || d2.Reconciler != nil {
		t.Fatal("disabled integration must leave nil interfaces (no typed nils)")
	}
}
