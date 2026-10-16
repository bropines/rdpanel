package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bropines/rdpanel/internal/logs"
)

const rootfs = "../../deploy/rootfs"

// TestDeployScriptsParse runs `sh -n` over the image's shell scripts.
func TestDeployScriptsParse(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	var scripts []string
	for _, pat := range []string{"usr/libexec/rdpanel/*", "usr/local/bin/*", "etc/s6-overlay/s6-rc.d/*/run", "etc/s6-overlay/s6-rc.d/*/up"} {
		m, _ := filepath.Glob(filepath.Join(rootfs, pat))
		scripts = append(scripts, m...)
	}
	if len(scripts) < 10 {
		t.Fatalf("found only %d scripts", len(scripts))
	}
	for _, s := range scripts {
		if out, err := exec.Command(sh, "-n", s).CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", s, err, out)
		}
	}
}

// TestLogPipelinesFeedTheLogsPage checks the s6 wiring behind Server -> Logs:
// every service has a "<name>-log" consumer running prefix-log <name>, the
// sources prefix-log copies to s6-log are exactly logs.FileSources, and the
// image points rdpanel at the same directory.
func TestLogPipelinesFeedTheLogsPage(t *testing.T) {
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(rootfs, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, svc := range []string{"hbbs", "hbbr", "panel", "agent"} {
		if got := strings.TrimSpace(read("etc/s6-overlay/s6-rc.d/" + svc + "/producer-for")); got != svc+"-log" {
			t.Errorf("%s producer-for = %q", svc, got)
		}
		if run := read("etc/s6-overlay/s6-rc.d/" + svc + "-log/run"); !strings.Contains(run, "prefix-log "+svc) {
			t.Errorf("%s-log/run does not call prefix-log %s:\n%s", svc, svc, run)
		}
	}
	pl := read("usr/libexec/rdpanel/prefix-log")
	if !strings.HasPrefix(pl, "#!/command/with-contenv sh") {
		t.Error("prefix-log needs with-contenv to see RDPANEL_LOGS_*")
	}
	want := `case "$name" in ` + strings.Join(logs.FileSources, " | ") + `) capture=1 ;; esac`
	if !strings.Contains(pl, want) {
		t.Errorf("prefix-log capture list differs from logs.FileSources; want %q", want)
	}
	if !strings.Contains(pl, "RDPANEL_LOGS_DIR-/run/rdpanel/logs") {
		t.Error("prefix-log default dir changed")
	}
	df, err := os.ReadFile("../../deploy/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), "RDPANEL_LOGS_DIR=/run/rdpanel/logs") {
		t.Error("Dockerfile must set RDPANEL_LOGS_DIR=/run/rdpanel/logs")
	}
}
