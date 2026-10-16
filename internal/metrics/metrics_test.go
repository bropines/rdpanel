package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bropines/rdpanel/internal/buildinfo"
)

func TestNilPanelIsNoop(t *testing.T) {
	var p *Panel
	p.Login(SurfacePanel, MethodPassword, ResultSuccess)
	p.TokenIssued(MethodOIDC)
	p.Audit(AuditConn, AuditStored)
	p.APITokenAuth(ResultSuccess)
}

func TestCountersAndLabelSanitising(t *testing.T) {
	reg := prometheus.NewRegistry()
	p := New(reg)
	p.Login(SurfaceClient, MethodTOTP, ResultFailure)
	p.Login("bogus", "magic", "weird") // folded into client/other/failure
	p.TokenIssued(MethodPassword)
	p.Audit(AuditAlarm, AuditRejected)

	if v := testutil.ToFloat64(p.logins.WithLabelValues(SurfaceClient, MethodTOTP, ResultFailure)); v != 1 {
		t.Fatalf("totp failures = %v", v)
	}
	if v := testutil.ToFloat64(p.logins.WithLabelValues(SurfaceClient, MethodOther, ResultFailure)); v != 1 {
		t.Fatalf("sanitised = %v", v)
	}
	if v := testutil.ToFloat64(p.tokens.WithLabelValues(MethodPassword)); v != 1 {
		t.Fatalf("tokens = %v", v)
	}
	if v := testutil.ToFloat64(p.audits.WithLabelValues(AuditAlarm, AuditRejected)); v != 1 {
		t.Fatalf("audits = %v", v)
	}
	// Pre-initialised series exist before any event.
	if v := testutil.ToFloat64(p.logins.WithLabelValues(SurfacePanel, MethodOIDC, ResultSuccess)); v != 0 {
		t.Fatalf("panel oidc = %v", v)
	}
	p.Login(SurfacePanel, MethodPasskey, ResultSuccess)
	if v := testutil.ToFloat64(p.logins.WithLabelValues(SurfacePanel, MethodPasskey, ResultSuccess)); v != 1 {
		t.Fatalf("panel passkey = %v", v)
	}
}

func TestBuildInfo(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterBuildInfo(reg, buildinfo.Info{Version: "v1.2.3", GoVersion: "go1.23"}, "")
	want := `
# HELP rdpanel_build_info Build metadata of rdpanel (constant 1): version, commit, Go version and the hbbs/hbbr source ref.
# TYPE rdpanel_build_info gauge
rdpanel_build_info{commit="unknown",go_version="go1.23",hbb_ref="unknown",version="v1.2.3"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "rdpanel_build_info"); err != nil {
		t.Fatal(err)
	}
}
