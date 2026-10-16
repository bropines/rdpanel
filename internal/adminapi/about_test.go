package adminapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/buildinfo"
	"github.com/bropines/rustdesk-panel/internal/store"
	"github.com/bropines/rustdesk-panel/internal/updatecheck"
)

type fakeUpdates struct{ res updatecheck.Result }

func (f fakeUpdates) Latest(context.Context) updatecheck.Result { return f.res }

func setBuildVars(t *testing.T, version, repo, ref string) {
	t.Helper()
	oldV, oldRepo, oldRef := buildinfo.Version, buildinfo.HBBRepo, buildinfo.HBBRef
	buildinfo.Version, buildinfo.HBBRepo, buildinfo.HBBRef = version, repo, ref
	t.Cleanup(func() { buildinfo.Version, buildinfo.HBBRepo, buildinfo.HBBRef = oldV, oldRepo, oldRef })
}

func TestAbout(t *testing.T) {
	setBuildVars(t, "v0.1.0", "https://github.com/bropines/rustdesk-server", "8b6f490")
	checked := time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC)
	e := newB2Env(t, b2opts{updates: fakeUpdates{updatecheck.Result{CheckedAt: checked,
		Latest: &updatecheck.Release{Version: "v0.2.0", URL: "https://github.com/bropines/rustdesk-panel/releases/tag/v0.2.0",
			PublishedAt: checked.Add(-time.Hour)}}}})
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)

	code, m, _ := e.client().do(http.MethodGet, "/about", nil)
	wantErr(t, "anon about", code, 401, m, "unauthorized")

	code, m, _ = user.do(http.MethodGet, "/about", nil)
	want(t, "user about", code, 200, m)
	ver, _ := m["version"].(map[string]any)
	hbb, _ := m["hbb"].(map[string]any)
	upd, _ := m["update"].(map[string]any)
	if m["name"] != AppName || ver["panel"] != "v0.1.0" || hbb["ref"] != "8b6f490" ||
		hbb["repo"] != "https://github.com/bropines/rustdesk-server" {
		t.Fatalf("user about: %v", m)
	}
	if upd["status"] != "update_available" || upd["latest"] != "v0.2.0" || upd["latest_url"] == nil || upd["checked_at"] == nil {
		t.Fatalf("update: %v", upd)
	}
	if m["server"] != nil {
		t.Fatalf("server details leaked to a user: %v", m["server"])
	}

	code, m, _ = admin.do(http.MethodGet, "/about", nil)
	want(t, "admin about", code, 200, m)
	srv, _ := m["server"].(map[string]any)
	health, _ := srv["health"].(map[string]any)
	if srv == nil || b2bNum(srv["uptime_s"]) != 5400 || b2bNum(srv["schema_version"]) < 7 ||
		b2bNum(srv["schema_latest"]) != b2bNum(srv["schema_version"]) || srv["os"] == "" || health["hbbs"] == nil {
		t.Fatalf("admin server: %v", srv)
	}
}

func TestAboutUpdateCheckStates(t *testing.T) {
	setBuildVars(t, "v0.2.0", "", "")
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("root", store.RoleAdmin)
	code, m, _ := admin.do(http.MethodGet, "/about", nil)
	want(t, "about", code, 200, m)
	upd, _ := m["update"].(map[string]any)
	hbb, _ := m["hbb"].(map[string]any)
	if upd["status"] != "disabled" || hbb["ref"] != nil || hbb["repo"] != nil {
		t.Fatalf("disabled check / no hbb: %v", m)
	}

	failing := fakeUpdates{updatecheck.Result{CheckedAt: time.Now(), Err: "update check: GitHub answered 403 Forbidden"}}
	e = newB2Env(t, b2opts{updates: failing})
	admin, _ = e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)
	_, m, _ = admin.do(http.MethodGet, "/about", nil)
	if upd, _ = m["update"].(map[string]any); upd["status"] != "error" || upd["error"] == nil {
		t.Fatalf("admin error: %v", upd)
	}
	_, m, _ = user.do(http.MethodGet, "/about", nil)
	if upd, _ = m["update"].(map[string]any); upd["status"] != "error" || upd["error"] != nil {
		t.Fatalf("user error must not carry details: %v", upd)
	}
}
