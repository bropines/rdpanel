package relays

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// InstallScriptPath is where the panel serves the relay agent installer, on
// the public host next to /agent/v1/* (docs/relay-agents.md).
const InstallScriptPath = "/agent/install.sh"

//go:embed install.sh
var installScript []byte

// Placeholders in install.sh, replaced (single quotes included) by
// shell-quoted values.
const (
	phPanelURL = "'@@RDPANEL_PANEL_URL@@'"
	phImage    = "'@@RDPANEL_IMAGE@@'"
)

// Same character sets the script accepts for --panel and --image.
var (
	safePanelURL = regexp.MustCompile(`^https?://[][A-Za-z0-9._~:/%@+=-]+$`)
	safeImage    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
)

// shellQuote returns s as one single-quoted POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9._~:/%@+=,-]+$`)

// shellWord quotes s only when the shell would otherwise interpret it.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return shellQuote(s)
}

// installPanelURL is the default --panel of the script: the public URL when it
// is a plain http(s) URL the script would accept, otherwise empty (the script
// then requires --panel).
func installPanelURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	if !safePanelURL.MatchString(raw) {
		return ""
	}
	return raw
}

// renderInstallScript fills the panel URL and image into install.sh.
func renderInstallScript(panelURL, image string) []byte {
	if !safeImage.MatchString(image) {
		image = "ghcr.io/bropines/rdpanel:latest"
	}
	out := bytes.Replace(installScript, []byte(phPanelURL), []byte(shellQuote(installPanelURL(panelURL))), 1)
	return bytes.Replace(out, []byte(phImage), []byte(shellQuote(image)), 1)
}

// HandleInstallScript serves GET/HEAD /agent/install.sh: a static POSIX sh
// installer for relay hosts (no auth; it contains no secrets).
func (s *Service) HandleInstallScript(w http.ResponseWriter, r *http.Request) {
	body := s.installScript
	sum := sha256.Sum256(body)
	h := w.Header()
	h.Set("Content-Type", "text/x-shellscript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	h.Set("Content-Disposition", `inline; filename="install.sh"`)
	http.ServeContent(w, r, "install.sh", time.Time{}, bytes.NewReader(body))
}

// installCommand is the one-line install shown in the Add-agent dialog.
func installCommand(panelURL, token string) string {
	return "curl -fsSL " + shellWord(panelURL+InstallScriptPath) + " | sudo sh -s -- --token " + shellWord(token)
}

// dockerRunCommand is the plain `docker run` alternative to the installer
// (same settings as the compose file it writes).
func dockerRunCommand(panelURL, image, token string) string {
	return strings.Join([]string{
		"docker run -d --name rdpanel-relay --network host --restart unless-stopped",
		"  --ulimit nofile=1048576:1048576 --stop-timeout 20",
		"  --log-opt max-size=20m --log-opt max-file=5",
		"  -e RDPANEL_MODE=agent",
		"  -e RDPANEL_AGENT_PANEL_URL=" + shellWord(panelURL),
		"  -e RDPANEL_AGENT_ENROLL_TOKEN=" + shellWord(token),
		"  -v /opt/rdpanel-agent/data:/data",
		"  " + shellWord(image),
	}, " \\\n")
}
