package authz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bropines/rdpanel/internal/store"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestHandler(t *testing.T) (*Handler, *fixture) {
	t.Helper()
	f := newFixture(t)
	h, err := NewHandler(f.svc, testSecret, "X-Forwarded-For", prometheus.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return h, f
}

func call(h http.Handler, method, remote, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, Path, strings.NewReader(body))
	req.RemoteAddr = remote
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bearer(s string) map[string]string { return map[string]string{"Authorization": "Bearer " + s} }

func TestHandlerRejectsWithNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	body := `{"target_id":"` + devOffice + `","token":"","conn_type":0,"controller_ip":"198.51.100.7","kind":"punch_hole"}`
	ok := bearer(testSecret)
	withProxy := func(k, v string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + testSecret, k: v}
	}
	for name, c := range map[string]struct {
		method, remote string
		hdr            map[string]string
	}{
		"remote peer":              {http.MethodPost, "203.0.113.9:40000", ok},
		"private peer":             {http.MethodPost, "10.0.0.5:40000", ok},
		"unparsable peer":          {http.MethodPost, "localhost", ok},
		"no secret":                {http.MethodPost, "127.0.0.1:40000", nil},
		"wrong secret":             {http.MethodPost, "127.0.0.1:40000", bearer(testSecret[:63] + "0")},
		"secret prefix":            {http.MethodPost, "127.0.0.1:40000", bearer(testSecret[:32])},
		"basic auth":               {http.MethodPost, "127.0.0.1:40000", map[string]string{"Authorization": "Basic " + testSecret}},
		"GET":                      {http.MethodGet, "127.0.0.1:40000", ok},
		"PUT":                      {http.MethodPut, "127.0.0.1:40000", ok},
		"proxied XFF":              {http.MethodPost, "127.0.0.1:40000", withProxy("X-Forwarded-For", "127.0.0.1")},
		"proxied X-Real-IP":        {http.MethodPost, "127.0.0.1:40000", withProxy("X-Real-IP", "127.0.0.1")},
		"proxied Forwarded":        {http.MethodPost, "[::1]:40000", withProxy("Forwarded", "for=127.0.0.1")},
		"proxied CF":               {http.MethodPost, "127.0.0.1:40000", withProxy("CF-Connecting-IP", "198.51.100.1")},
		"proxied X-Forwarded-Host": {http.MethodPost, "127.0.0.1:40000", withProxy("X-Forwarded-Host", "desk.example.test")},
	} {
		rec := call(h, c.method, c.remote, body, c.hdr)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Wrong secrets from loopback are counted (a misconfigured hbbs runs
	// without decisions); other rejections are plain 404s.
	if got := testutil.ToFloat64(h.total.WithLabelValues(ResultError)); got != 4 {
		t.Errorf("error count = %v, want 4 (no, wrong, prefix and basic-auth secret)", got)
	}
}

func TestHandlerDecides(t *testing.T) {
	h, f := newTestHandler(t)
	decode := func(t *testing.T, rec *httptest.ResponseRecorder) Response {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var raw map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		// The contract: allow (bool), permissions (decimal string), reason (string).
		if _, ok := raw["allow"].(bool); !ok {
			t.Fatalf("allow: %v", raw)
		}
		if _, ok := raw["permissions"].(string); !ok {
			t.Fatalf("permissions must be a string: %v", raw)
		}
		if _, ok := raw["reason"].(string); !ok {
			t.Fatalf("reason: %v", raw)
		}
		var r Response
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return r
	}
	ctx := context.Background()
	if err := f.st.ConnectionACLs.Create(ctx, &store.ConnectionACL{Name: "support office", Action: store.ACLActionAllow,
		Priority: 10, Enabled: true, UserGroupID: ptr(f.groups["support"]), DeviceGroupID: ptr(f.groups["office"]),
		ControlRoleID: &f.roles["Support (No Files)"].ID}); err != nil {
		t.Fatal(err)
	}

	// IPv4 and IPv6 loopback, IPv4-mapped loopback; unknown fields tolerated.
	for _, remote := range []string{"127.0.0.1:40000", "127.8.9.10:1", "[::1]:40000", "[::ffff:127.0.0.1]:40000"} {
		r := decode(t, call(h, http.MethodPost, remote,
			`{"target_id":"`+devOffice+`","token":"`+f.tokens["bob"]+`","conn_type":0,"controller_ip":"198.51.100.7","kind":"request_relay","future":{"x":1}}`,
			bearer(testSecret)))
		if !r.Allow || r.Permissions != permsSupport || r.Reason != "" {
			t.Fatalf("%s: %+v", remote, r)
		}
	}
	r := decode(t, call(h, http.MethodPost, "127.0.0.1:40000", `{"target_id":"`+devOffice+`","kind":"punch_hole"}`, bearer(testSecret)))
	if r.Allow || r.Permissions != "0" || r.Reason != ReasonSignIn {
		t.Fatalf("anonymous: %+v", r)
	}
	// The bearer scheme is case-insensitive.
	r = decode(t, call(h, http.MethodPost, "127.0.0.1:40000",
		`{"target_id":"`+devOffice+`","token":"`+f.tokens["bob"]+`","conn_type":1}`,
		map[string]string{"Authorization": "bearer " + testSecret}))
	if r.Allow || !strings.Contains(r.Reason, "File transfer") {
		t.Fatalf("file transfer: %+v", r)
	}
	if a, d := testutil.ToFloat64(h.total.WithLabelValues(ResultAllow)), testutil.ToFloat64(h.total.WithLabelValues(ResultDeny)); a != 4 || d != 2 {
		t.Fatalf("allow=%v deny=%v", a, d)
	}
}

func TestHandlerBadBodies(t *testing.T) {
	h, _ := newTestHandler(t)
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"not JSON":       {"target_id=1", http.StatusBadRequest},
		"empty":          {"", http.StatusBadRequest},
		"no target":      {`{"token":""}`, http.StatusBadRequest},
		"wrong type":     {`{"target_id":"1","conn_type":"file"}`, http.StatusBadRequest},
		"too large":      {`{"target_id":"` + devOffice + `","token":"` + strings.Repeat("a", MaxBody) + `"}`, http.StatusRequestEntityTooLarge},
		"padded too far": {`{"target_id":"` + devOffice + `"` + strings.Repeat(" ", MaxBody) + `}`, http.StatusRequestEntityTooLarge},
	} {
		rec := call(h, http.MethodPost, "127.0.0.1:40000", c.body, bearer(testSecret))
		if rec.Code != c.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if got := testutil.ToFloat64(h.total.WithLabelValues(ResultError)); got != 6 {
		t.Errorf("error count = %v", got)
	}
	// Just under the limit is fine.
	body := `{"target_id":"` + devUnknown + `","kind":"` + strings.Repeat("k", MaxBody-64) + `"}`
	if rec := call(h, http.MethodPost, "127.0.0.1:40000", body, bearer(testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("body of %d bytes: %d", len(body), rec.Code)
	}
}

func TestNewHandlerNeedsSecret(t *testing.T) {
	if _, err := NewHandler(&Service{}, "", "", nil, nil); err == nil {
		t.Fatal("empty secret accepted")
	}
	if _, err := NewHandler(nil, testSecret, "", nil, nil); err == nil {
		t.Fatal("nil service accepted")
	}
}
