package clientapi

import (
	"context"
	"testing"

	"github.com/bropines/rustdesk-panel/internal/store"
)

func countRows(t *testing.T, e *env, table string) int {
	t.Helper()
	var n int
	if err := e.st.Querier().QueryRowContext(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuditConn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.device("123456789", "dXVpZA==")
	e.user("ctl", "pw-ctl-12345", nil)
	tok := e.login("ctl", "pw-ctl-12345", "", "")

	// 1. new connection (session_id 0), 2. authorized (u64 session id, no action), 3. close.
	newEv := `{"action":"new","ip":"203.0.113.7","conn_audit_ref":"ref-1","id":"123456789","uuid":"dXVpZA==","conn_id":3,"session_id":0,"nonce":"n-1"}`
	e.empty("POST", "/api/audit/conn", newEv, "")
	e.empty("POST", "/api/audit/conn", newEv, "") // client retry: deduped by nonce
	if n := countRows(t, e, "connection_audits"); n != 1 {
		t.Fatalf("rows after retry = %d", n)
	}
	e.empty("POST", "/api/audit/conn", `{"peer":["987654321","Bob"],"type":0,"primary_auth":2,"two_factor":1,
		"id":"123456789","uuid":"dXVpZA==","conn_id":3,"session_id":18446744073709551615,"nonce":"n-2"}`, "")

	// Controller side: conn/active returns the guid as a JSON string.
	e.golden("audit_conn_active", e.do("GET", "/api/audit/conn/active?id=123456789&session_id=18446744073709551615&conn_type=0", "", tok), 200)
	e.golden("audit_conn_active_pending", e.do("GET", "/api/audit/conn/active?id=123456789&session_id=42&conn_type=0", "", tok), 200)
	e.golden("invalid_request", e.do("GET", "/api/audit/conn/active?id=123456789&session_id=1.5e3", "", tok), 400)
	e.golden("unauthorized", e.do("GET", "/api/audit/conn/active?id=123456789&session_id=1", "", ""), 401)

	e.empty("POST", "/api/audit/conn", `{"action":"close","id":"123456789","uuid":"dXVpZA==","conn_id":3,"session_id":18446744073709551615,"nonce":"n-3"}`, "")
	a, err := e.st.ConnectionAudits.GetByGUID(ctx, "guid-1")
	if err != nil || a.SessionID != "18446744073709551615" || a.ControllerRustdeskID != "987654321" || a.ControllerName != "Bob" ||
		a.ControllerIP != "203.0.113.7" || *a.PrimaryAuth != 2 || *a.TwoFactor != 1 || a.ConnAuditRef != "ref-1" || a.EndedAt == nil {
		t.Fatalf("audit row %+v %v", a, err)
	}

	// Controller note via /api/audit/conn (no uuid, no auth) and via PUT /api/audit.
	e.empty("POST", "/api/audit/conn", `{"id":"123456789","session_id":18446744073709551615,"note":"from session"}`, "")
	a, _ = e.st.ConnectionAudits.GetByGUID(ctx, "guid-1")
	if a.Note != "from session" {
		t.Fatalf("note %q", a.Note)
	}
	e.empty("PUT", "/api/audit", `{"guid":"guid-1","note":"final note"}`, tok)
	a, _ = e.st.ConnectionAudits.GetByGUID(ctx, "guid-1")
	if a.Note != "final note" {
		t.Fatalf("note %q", a.Note)
	}
	e.golden("audit_not_found", e.do("PUT", "/api/audit", `{"guid":"nope","note":"x"}`, tok), 404)
	e.golden("unauthorized", e.do("PUT", "/api/audit", `{"guid":"guid-1","note":"x"}`, ""), 401)

	// session_id as a string is accepted too.
	e.empty("POST", "/api/audit/conn", `{"action":"new","id":"123456789","uuid":"dXVpZA==","conn_id":4,"session_id":"12","nonce":"n-4"}`, "")

	// The reporting device must exist with exactly that uuid (dropped as 4xx).
	e.golden("audit_device", e.do("POST", "/api/audit/conn", `{"action":"new","id":"123456789","uuid":"ZXZpbA==","conn_id":5,"nonce":"n-5"}`, ""), 403)
	e.golden("audit_device", e.do("POST", "/api/audit/conn", `{"action":"new","id":"999","uuid":"dXVpZA==","conn_id":5}`, ""), 403)
	e.golden("invalid_request", e.do("POST", "/api/audit/conn", `{"session_id":-1}`, ""), 400)
	if n := countRows(t, e, "connection_audits"); n != 2 {
		t.Fatalf("rows = %d", n)
	}
}

func TestAuditFileAndAlarm(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.device("123456789", "dXVpZA==")
	file := `{"id":"123456789","uuid":"dXVpZA==","peer_id":"987654321","conn_id":3,"type":1,"path":"C:\\dir","is_file":false,
		"info":"{\"ip\":\"203.0.113.7\",\"name\":\"Bob\",\"num\":2,\"files\":[[\"a.txt\",12],[\"b.txt\",5]]}","nonce":"f-1"}`
	e.empty("POST", "/api/audit/file", file, "")
	e.empty("POST", "/api/audit/file", file, "")
	files, _ := e.st.FileTransferAudits.List(ctx, store.ClientAuditFilter{})
	if len(files) != 1 || files[0].ControllerName != "Bob" || files[0].ControllerIP != "203.0.113.7" || files[0].FileCount != 2 ||
		files[0].Direction != 1 || files[0].Path != `C:\dir` || files[0].ControllerRustdeskID != "987654321" {
		t.Fatalf("file audit %+v", files)
	}
	e.golden("audit_device", e.do("POST", "/api/audit/file", `{"id":"123456789","uuid":"bad","nonce":"f-2"}`, ""), 403)

	alarm := `{"id":"123456789","uuid":"dXVpZA==","typ":1,"info":"{\"ip\":\"1.2.3.4\",\"id\":\"x\",\"name\":\"y\"}","conn_id":3,"nonce":"a-1"}`
	e.empty("POST", "/api/audit/alarm", alarm, "")
	e.empty("POST", "/api/audit/alarm", alarm, "")
	e.empty("POST", "/api/audit/alarm", `{"id":"123456789","uuid":"dXVpZA==","typ":0,"info":"not json","conn_audit_ref":"r","nonce":"a-2"}`, "")
	alarms, _ := e.st.AlarmAudits.List(ctx, store.ClientAuditFilter{})
	if len(alarms) != 2 || alarms[1].AlarmType != 1 || alarms[1].Info != `{"ip":"1.2.3.4","id":"x","name":"y"}` ||
		alarms[0].Info != `{"raw":"not json"}` || alarms[0].ConnAuditRef != "r" {
		t.Fatalf("alarms %+v", alarms)
	}
	// Clients < 1.5.0 send no nonce: stored every time.
	e.empty("POST", "/api/audit/alarm", `{"id":"123456789","uuid":"dXVpZA==","typ":2}`, "")
	e.empty("POST", "/api/audit/alarm", `{"id":"123456789","uuid":"dXVpZA==","typ":2}`, "")
	if n := countRows(t, e, "alarm_audits"); n != 4 {
		t.Fatalf("alarms = %d", n)
	}
	e.golden("invalid_nonce", e.do("POST", "/api/audit/alarm",
		`{"id":"123456789","uuid":"dXVpZA==","typ":2,"nonce":"0123456789012345678901234567890123456789012345678901234567890123456789"}`, ""), 400)
}
