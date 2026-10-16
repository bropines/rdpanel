package clientapi

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bropines/rustdesk-panel/internal/metrics"
)

func TestLoginAndAuditMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	tel := metrics.New(reg)
	e := newEnv(t, func(d *Deps) { d.Telemetry = tel })
	e.user("alice", "correct horse", nil)

	e.mustDo("POST", "/api/login", loginBody("alice", "correct horse"), "", 200)
	e.do("POST", "/api/login", loginBody("alice", "wrong"), "")
	e.do("POST", "/api/audit/alarm", `{"id":"123456789","uuid":"nope","typ":1}`, "")

	want := map[string]float64{
		`rdpanel_auth_login_attempts_total{method="password",result="success",surface="client"}`: 1,
		`rdpanel_auth_login_attempts_total{method="password",result="failure",surface="client"}`: 1,
		`rdpanel_auth_client_tokens_issued_total{method="password"}`:                             1,
		`rdpanel_audit_events_total{result="rejected",type="alarm"}`:                             1,
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, l.GetName()+`="`+l.GetValue()+`"`)
			}
			got[mf.GetName()+"{"+strings.Join(labels, ",")+"}"] = m.GetCounter().GetValue()
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if n := testutil.CollectAndCount(reg, "rdpanel_auth_login_attempts_total"); n == 0 {
		t.Fatal("no login series")
	}
}
