package relays

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestInstallScriptHandler(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Image = "ghcr.io/bropines/rdpanel:1.4.2" })
	rec := httptest.NewRecorder()
	h.svc.HandleInstallScript(rec, httptest.NewRequest(http.MethodGet, InstallScriptPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	hd := rec.Header()
	if ct := hd.Get("Content-Type"); ct != "text/x-shellscript; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	if cc := hd.Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("cache-control %q", cc)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "#!/bin/sh\n") ||
		!strings.Contains(body, "\nDEFAULT_PANEL_URL='https://desk-api.example.com'\n") ||
		!strings.Contains(body, "\nDEFAULT_IMAGE='ghcr.io/bropines/rdpanel:1.4.2'\n") || strings.Contains(body, "'@@") {
		t.Fatalf("script not templated:\n%.600s", body)
	}

	etag := hd.Get("ETag")
	if etag == "" {
		t.Fatal("no etag")
	}
	req := httptest.NewRequest(http.MethodGet, InstallScriptPath, nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.svc.HandleInstallScript(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("conditional GET: %d", rec.Code)
	}
}

func TestRenderInstallScriptEscaping(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://desk-api.example.com/", "'https://desk-api.example.com'"},
		{"http://[2001:db8::1]:8080/rd", "'http://[2001:db8::1]:8080/rd'"},
		{"", "''"},
		{"ftp://desk.example.com", "''"},
		{"https://desk.example.com/'; rm -rf / #", "''"},
		{"https://desk.example.com/$(id)", "''"},
		{"https://desk.example.com/`id`", "''"},
		{"https://desk.example.com/a\nb", "''"},
		{"https://desk.example.com/?x=1", "''"},
	}
	for _, c := range cases {
		out := string(renderInstallScript(c.url, "ghcr.io/bropines/rdpanel:latest"))
		if !strings.Contains(out, "\nDEFAULT_PANEL_URL="+c.want+"\n") {
			t.Errorf("%q: want DEFAULT_PANEL_URL=%s", c.url, c.want)
		}
	}
	out := string(renderInstallScript("https://desk.example.com", "evil'; id; '"))
	if !strings.Contains(out, "\nDEFAULT_IMAGE='ghcr.io/bropines/rdpanel:latest'\n") {
		t.Error("unsafe image not replaced")
	}
	if got := shellQuote(`a'b`); got != `'a'\''b'` {
		t.Errorf("shellQuote = %s", got)
	}
	if got := installCommand("https://x.example.com", "tok_-"); got != "curl -fsSL https://x.example.com/agent/install.sh | sudo sh -s -- --token tok_-" {
		t.Errorf("install command = %s", got)
	}
	if got := installCommand("https://<panel public URL>", "t"); !strings.HasPrefix(got, "curl -fsSL 'https://<panel public URL>/agent/install.sh' |") {
		t.Errorf("placeholder URL not quoted: %s", got)
	}
}

// fakeDocker records its arguments and simulates the few docker commands the
// installer runs. DOCKER_STATE exists while the container "runs".
const fakeDocker = `#!/bin/sh
echo "$*" >>"$DOCKER_LOG"
case $1 in
info) exit 0 ;;
compose)
	for a in "$@"; do
		case $a in
		version) echo "Docker Compose version v2.29.7"; exit 0 ;;
		up) : >"$DOCKER_STATE" ;;
		down) rm -f "$DOCKER_STATE" ;;
		esac
	done
	exit 0 ;;
container) [ -f "$DOCKER_STATE" ] || exit 1; echo rdpanel-agent ;;
inspect) [ -f "$DOCKER_STATE" ] || exit 1; echo "${DOCKER_HEALTH:-healthy}/running" ;;
logs) echo "fake agent log" ;;
rm) rm -f "$DOCKER_STATE" ;;
esac
exit 0
`

type installEnv struct {
	t       *testing.T
	script  string
	bin     string
	dir     string
	log     string
	state   string
	noTools bool // PATH without docker (only the tools the script needs)
	health  string
}

func newInstallEnv(t *testing.T) *installEnv {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("installer test needs a Linux sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	tmp := t.TempDir()
	e := &installEnv{t: t, script: filepath.Join(tmp, "install.sh"), bin: filepath.Join(tmp, "bin"),
		dir: filepath.Join(tmp, "agent"), log: filepath.Join(tmp, "docker.log"), state: filepath.Join(tmp, "running")}
	if err := os.WriteFile(e.script, renderInstallScript("https://desk-api.example.com", "ghcr.io/bropines/rdpanel:1.4.2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.bin, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *installEnv) run(args ...string) (string, error) {
	e.t.Helper()
	path := e.bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if e.noTools {
		tools := filepath.Join(filepath.Dir(e.bin), "tools")
		_ = os.MkdirAll(tools, 0o755)
		for _, name := range []string{"sh", "grep", "sed", "head", "id", "cat", "chmod", "mv", "mkdir", "rm", "rmdir"} {
			p, err := exec.LookPath(name)
			if err != nil {
				e.t.Skipf("no %s", name)
			}
			_ = os.Symlink(p, filepath.Join(tools, name))
		}
		path = tools
	}
	cmd := exec.Command("sh", append([]string{e.script}, args...)...)
	cmd.Env = []string{"PATH=" + path, "HOME=" + filepath.Dir(e.bin), "RDPANEL_INSTALL_ALLOW_NONROOT=1",
		"DOCKER_LOG=" + e.log, "DOCKER_STATE=" + e.state, "DOCKER_HEALTH=" + e.health}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (e *installEnv) read(name string) string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dir, name))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

func (e *installEnv) dockerLog() string {
	raw, _ := os.ReadFile(e.log)
	_ = os.Remove(e.log)
	return string(raw)
}

type composeFile struct {
	Services map[string]struct {
		Image         string            `yaml:"image"`
		ContainerName string            `yaml:"container_name"`
		NetworkMode   string            `yaml:"network_mode"`
		Restart       string            `yaml:"restart"`
		Environment   map[string]string `yaml:"environment"`
		EnvFile       []string          `yaml:"env_file"`
		Volumes       []string          `yaml:"volumes"`
		Ulimits       map[string]struct {
			Soft int `yaml:"soft"`
			Hard int `yaml:"hard"`
		} `yaml:"ulimits"`
	} `yaml:"services"`
}

func (e *installEnv) compose() composeFile {
	e.t.Helper()
	var c composeFile
	if err := yaml.Unmarshal([]byte(e.read("compose.yml")), &c); err != nil {
		e.t.Fatalf("compose.yml: %v", err)
	}
	return c
}

const testToken = "Zm9vYmFyYmF6cXV4LXRva2VuLWZvci10ZXN0cw"

func TestInstallScriptInstallUpdateUninstall(t *testing.T) {
	e := newInstallEnv(t)

	// First install needs a token.
	if out, err := e.run("--dir", e.dir); err == nil || !strings.Contains(out, "--token is required") {
		t.Fatalf("no token: %v\n%s", err, out)
	}

	out, err := e.run("--token", testToken, "--dir", e.dir, "--port", "21118", "--name", "relay-de-1")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "relay agent is running") || !strings.Contains(out, "Server -> Relays") {
		t.Errorf("install output:\n%s", out)
	}
	if log := e.dockerLog(); !strings.Contains(log, "compose -f "+e.dir+"/compose.yml pull") ||
		!strings.Contains(log, "compose -f "+e.dir+"/compose.yml up -d") {
		t.Errorf("docker calls:\n%s", log)
	}
	c := e.compose()
	svc, ok := c.Services["relay"]
	if !ok {
		t.Fatalf("compose: %+v", c)
	}
	if svc.Image != "ghcr.io/bropines/rdpanel:1.4.2" || svc.NetworkMode != "host" || svc.Restart != "unless-stopped" ||
		svc.ContainerName != "rdpanel-relay" || svc.Ulimits["nofile"].Soft != 1048576 || svc.Ulimits["nofile"].Hard != 1048576 {
		t.Errorf("service: %+v", svc)
	}
	wantEnv := map[string]string{"RDPANEL_MODE": "agent", "RDPANEL_AGENT_PANEL_URL": "https://desk-api.example.com",
		"RDPANEL_AGENT_RELAY_PORT": "21118", "RDPANEL_AGENT_HOSTNAME": "relay-de-1"}
	for k, v := range wantEnv {
		if svc.Environment[k] != v {
			t.Errorf("environment %s = %q, want %q", k, svc.Environment[k], v)
		}
	}
	if len(svc.EnvFile) != 1 || svc.EnvFile[0] != e.dir+"/agent.env" || len(svc.Volumes) != 1 || svc.Volumes[0] != e.dir+"/data:/data" {
		t.Errorf("env_file %v volumes %v", svc.EnvFile, svc.Volumes)
	}
	if strings.Contains(e.read("compose.yml"), testToken) {
		t.Error("token written into compose.yml")
	}
	if got := e.read("agent.env"); got != "RDPANEL_AGENT_ENROLL_TOKEN="+testToken+"\n" {
		t.Errorf("agent.env = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(e.dir, "agent.env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("agent.env mode: %v %v", fi.Mode(), err)
	}

	// The agent enrolled: identity and state live in data/.
	for _, f := range []string{"agent.key", "agent-state.json"} {
		if err := os.WriteFile(filepath.Join(e.dir, "data", f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Re-run without a token: new image, saved port/name kept, data untouched.
	out, err = e.run("--dir", e.dir, "--image", "ghcr.io/bropines/rdpanel:1.5.0")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	svc = e.compose().Services["relay"]
	if svc.Image != "ghcr.io/bropines/rdpanel:1.5.0" || svc.Environment["RDPANEL_AGENT_RELAY_PORT"] != "21118" ||
		svc.Environment["RDPANEL_AGENT_HOSTNAME"] != "relay-de-1" {
		t.Errorf("after update: %+v", svc)
	}
	if got := e.read("agent.env"); !strings.Contains(got, testToken) {
		t.Errorf("agent.env lost the token: %q", got)
	}
	if e.read("data/agent.key") != "x" {
		t.Error("identity changed")
	}
	_ = e.dockerLog()

	// Unhealthy container: non-zero exit with logs.
	e.health = "unhealthy"
	if out, err := e.run("--dir", e.dir, "--wait", "0"); err == nil || !strings.Contains(out, "not healthy") || !strings.Contains(out, "fake agent log") {
		t.Errorf("unhealthy: %v\n%s", err, out)
	}
	e.health = ""

	// Uninstall keeps the files; --purge deletes them.
	if out, err := e.run("--dir", e.dir, "--uninstall"); err != nil || !strings.Contains(out, "is kept") {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if log := e.dockerLog(); !strings.Contains(log, "compose -f "+e.dir+"/compose.yml down") {
		t.Errorf("uninstall docker calls:\n%s", log)
	}
	if e.read("data/agent.key") != "x" {
		t.Error("uninstall removed the identity")
	}
	if out, err := e.run("--dir", e.dir, "--uninstall", "--purge"); err != nil {
		t.Fatalf("purge: %v\n%s", err, out)
	}
	if _, err := os.Stat(e.dir); !os.IsNotExist(err) {
		t.Errorf("dir still there after --purge: %v", err)
	}
}

func TestInstallScriptRejectsBadInput(t *testing.T) {
	e := newInstallEnv(t)
	cases := [][]string{
		{"--token", testToken, "--panel", "https://x.example.com/'$(id)"},
		{"--token", testToken, "--panel", "ftp://x.example.com"},
		{"--token", testToken, "--port", "70000"},
		{"--token", testToken, "--port", "21117;id"},
		{"--token", "short"},
		{"--token", testToken + "$(id)"},
		{"--token", testToken, "--name", "a b"},
		{"--token", testToken, "--image", "img; id"},
		{"--token", testToken, "--dir", "relative/dir"},
		{"--token", testToken, "--dir", "/opt/../etc"},
		{"--token", testToken, "--dir", "/etc"},
		{"--token", testToken, "--purge"},
		{"--token"},
		{"--bogus"},
	}
	for _, args := range cases {
		if !slices.Contains(args, "--dir") {
			args = append(args, "--dir", e.dir)
		}
		if out, err := e.run(args...); err == nil || !strings.Contains(out, "error:") {
			t.Errorf("%q: accepted (%v)\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(e.log); !os.IsNotExist(err) {
		t.Errorf("docker was called for invalid input:\n%s", e.dockerLog())
	}

	// Docker missing: explain, do not install unless asked.
	e.noTools = true
	out, err := e.run("--token", testToken, "--dir", e.dir)
	if err == nil || !strings.Contains(out, "--install-docker") || !strings.Contains(out, "https://get.docker.com") {
		t.Errorf("docker missing: %v\n%s", err, out)
	}
}
