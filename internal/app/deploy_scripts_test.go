package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bropines/rdpanel/internal/crypto"
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

// TestHBBSAuthzEnv sources the helper the hbbs run script uses to point hbbs
// at /internal/hbbs/authorize and checks what it exports for typical
// RDPANEL_LISTEN forms, operator overrides and the off switches.
func TestHBBSAuthzEnv(t *testing.T) {
	// The image runs busybox sh; check every POSIX shell at hand.
	var shells [][]string
	for _, sh := range [][]string{{"sh"}, {"dash"}, {"bash", "--posix"}, {"busybox", "sh"}} {
		if p, err := exec.LookPath(sh[0]); err == nil {
			shells = append(shells, append([]string{p}, sh[1:]...))
		}
	}
	if len(shells) == 0 {
		t.Skip("no sh")
	}
	script, err := filepath.Abs(filepath.Join(rootfs, "usr/libexec/rdpanel/hbbs-authz-env"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := os.ReadFile(filepath.Join(rootfs, "etc/s6-overlay/s6-rc.d/hbbs/run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(run), "\n. /usr/libexec/rdpanel/hbbs-authz-env\n") {
		t.Fatal("the hbbs run script does not source hbbs-authz-env")
	}
	const (
		def    = "http://127.0.0.1:21114/internal/hbbs/authorize"
		defKey = "/data/panel/hbbs-authz.key"
	)
	for _, c := range []struct {
		env      map[string]string
		url, key string
	}{
		{nil, def, defKey},
		{map[string]string{"RDPANEL_LISTEN": ":21114"}, def, defKey},
		{map[string]string{"RDPANEL_LISTEN": "0.0.0.0:21114"}, def, defKey},
		{map[string]string{"RDPANEL_LISTEN": "127.0.0.1:31114"}, "http://127.0.0.1:31114/internal/hbbs/authorize", defKey},
		{map[string]string{"RDPANEL_LISTEN": "[::1]:21114"}, "http://[::1]:21114/internal/hbbs/authorize", defKey},
		{map[string]string{"RDPANEL_LISTEN": "[::]:8080"}, "http://127.0.0.1:8080/internal/hbbs/authorize", defKey},
		{map[string]string{"RDPANEL_LISTEN": "localhost:21114"}, def, defKey},
		{map[string]string{"RDPANEL_LISTEN": "10.0.0.5:21114"}, "unset", defKey},
		{map[string]string{"RDPANEL_LISTEN": "127.0.0.1"}, "unset", defKey},
		{map[string]string{"RDPANEL_DATA_DIR": "/srv/panel"}, def, "/srv/panel/hbbs-authz.key"},
		{map[string]string{"RDPANEL_HBBS_AUTHZ_SECRET_FILE": "/run/secrets/authz", "RDPANEL_DATA_DIR": "/srv/panel"}, def, "/run/secrets/authz"},
		{map[string]string{"AUTHZ_URL": "http://127.0.0.1:9/x", "AUTHZ_SECRET_FILE": "/etc/authz"}, "http://127.0.0.1:9/x", "/etc/authz"},
		{map[string]string{"AUTHZ_URL": "http://127.0.0.1:9/x", "RDPANEL_LISTEN": "10.0.0.5:1"}, "http://127.0.0.1:9/x", defKey},
		{map[string]string{"RDPANEL_HBBS_AUTHZ": "true"}, def, defKey},
		{map[string]string{"RDPANEL_HBBS_AUTHZ": "false"}, "unset", "unset"},
		{map[string]string{"RDPANEL_HBBS_AUTHZ": "0"}, "unset", "unset"},
		{map[string]string{"RDPANEL_HBBS_AUTHZ": "False"}, "unset", "unset"},
		{map[string]string{"RDPANEL_MODE": "agent"}, "unset", "unset"},
	} {
		for _, sh := range shells {
			// The values must be exported (seen by a child process), and the
			// helper's own names must not leak into hbbs' environment.
			args := append(append([]string{}, sh[1:]...), "-c",
				`. "$1"; command -v rdpanel_hbbs_authz_env >/dev/null && echo leaked-function; shift; exec "$@"`, "x", script)
			args = append(append(args, sh...), "-c",
				`printf '%s|%s|%s\n' "${AUTHZ_URL-unset}" "${AUTHZ_SECRET_FILE-unset}" "${_rdp_listen-}"`)
			cmd := exec.Command(sh[0], args...)
			cmd.Env = []string{}
			for k, v := range c.env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v %v: %v\n%s", sh, c.env, err, out)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if got, want := lines[len(lines)-1], c.url+"|"+c.key+"|"; got != want {
				t.Errorf("%v %v:\n got %q\nwant %q\n%s", sh, c.env, got, want, out)
			}
			if strings.Contains(string(out), "leaked-function") {
				t.Errorf("%v %v: helper function left defined", sh, c.env)
			}
		}
	}
}

// TestHBBSAuthzKey runs the key-secret helper that creates the hbbs
// authorization secret before hbbs starts.
func TestHBBSAuthzKey(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	script := filepath.Join(rootfs, "usr/libexec/rdpanel/hbbs-authz-key")
	keyRe := regexp.MustCompile(`^[0-9a-f]{64}\n$`)
	path := filepath.Join(t.TempDir(), "panel", "hbbs-authz.key")
	gen := func() string {
		t.Helper()
		out, err := exec.Command(sh, script, path).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := gen(); got != "generated" {
		t.Fatalf("first run: %q", got)
	}
	first, err := os.ReadFile(path)
	if err != nil || !keyRe.Match(first) {
		t.Fatalf("content %q %v", first, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if got := gen(); got != "exists" {
		t.Fatalf("second run: %q", got)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) {
		t.Fatal("existing secret replaced")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	// The panel reads the very same file.
	if s, src, err := crypto.LoadOrCreateHexSecret(path, hbbsAuthzSecretBytes); err != nil || src != crypto.KeyFromFile ||
		s+"\n" != string(first) {
		t.Fatalf("panel read %q %s %v", s, src, err)
	}
	// An empty file (e.g. a failed earlier write) is replaced.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gen(); got != "generated" {
		t.Fatalf("empty file: %q", got)
	}
	if b, _ := os.ReadFile(path); !keyRe.Match(b) || string(b) == string(first) {
		t.Fatalf("regenerated content %q", b)
	}
	key, err := os.ReadFile(filepath.Join(rootfs, "usr/libexec/rdpanel/key-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(key), "/usr/libexec/rdpanel/hbbs-authz-key \"$AUTHZ_FILE\"") ||
		!strings.Contains(string(key), "AUTHZ_FILE=${RDPANEL_HBBS_AUTHZ_SECRET_FILE:-$PANEL_DATA/hbbs-authz.key}") {
		t.Fatal("key-secret does not create the hbbs authorization secret")
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
