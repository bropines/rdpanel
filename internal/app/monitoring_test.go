package app

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// monitoringFiles are the shipped Prometheus rules and Grafana dashboard; the
// Helm chart carries byte-identical copies under files/.
var monitoringFiles = map[string]string{
	"../../deploy/monitoring/prometheus/alerts.yaml":      "../../deploy/helm/rdpanel/files/alerts.yaml",
	"../../deploy/monitoring/grafana/rustdesk-panel.json": "../../deploy/helm/rdpanel/files/rustdesk-panel.json",
}

// vecsWithoutChildren are label-vector metrics that only appear in /metrics
// after their first observation (reconciler runs, published events), which a
// unit test app never makes.
var vecsWithoutChildren = map[string]bool{
	"rdpanel_hbb_admin_up":                      true,
	"rdpanel_hbb_admin_reconcile_total":         true,
	"rdpanel_hbb_admin_drift_corrections_total": true,
	"rdpanel_events_published_total":            true,
}

func TestMonitoringFilesMatchMetrics(t *testing.T) {
	// RDPANEL_METRICS_PER_DEVICE also checks that the opt-in collector registers.
	a := newTestApp(t, map[string]string{"RDPANEL_METRICS_PER_DEVICE": "true"})
	h := a.Handler()
	// The first scrape creates the HTTP series; the second one sees them.
	do(t, h, http.MethodGet, "/metrics", "127.0.0.1:1234", nil)
	rec := do(t, h, http.MethodGet, "/metrics", "127.0.0.1:1234", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics: %d", rec.Code)
	}
	exposed := map[string]bool{}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "# TYPE ") {
			name := strings.Fields(line)[2]
			exposed[name] = true
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				exposed[name+suffix] = true
			}
		}
	}
	re := regexp.MustCompile(`\brdpanel_[a-z0-9_]+`)
	for src, cp := range monitoringFiles {
		body, err := os.ReadFile(filepath.FromSlash(src))
		if err != nil {
			t.Fatal(err)
		}
		copyBody, err := os.ReadFile(filepath.FromSlash(cp))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, copyBody) {
			t.Errorf("%s differs from %s: copy it over", cp, src)
		}
		var missing []string
		seen := map[string]bool{}
		for _, name := range re.FindAllString(string(body), -1) {
			if seen[name] || exposed[name] || vecsWithoutChildren[name] {
				continue
			}
			seen[name] = true
			missing = append(missing, name)
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s references metrics rdpanel does not export: %v", src, missing)
		}
	}
}
