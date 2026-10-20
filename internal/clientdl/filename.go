package clientdl

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// ServerSettings are what a RustDesk client needs to use this server.
type ServerSettings struct {
	Host  string `json:"host,omitempty"`
	Relay string `json:"relay,omitempty"`
	Key   string `json:"key,omitempty"`
	API   string `json:"api,omitempty"`
}

// maxFileName is the longest file name Windows (NTFS) accepts.
const maxFileName = 255

// ErrFileName is returned when the settings cannot be put into a file name.
var ErrFileName = errors.New("server settings do not fit into a Windows file name")

// WindowsFileName returns the name under which the official Windows client
// applies s on its own when started (and keeps them when it installs):
// "rustdesk-licensed-<config string>.exe", the config string being RustDesk's
// reversed URL-safe base64 of the settings JSON. RustDesk splits the name at
// "--", which the encoding of non-ASCII JSON (an IDN host name) can contain;
// such encodings are avoided by shifting the base64 alignment with leading
// JSON whitespace, which RustDesk's parser skips.
func WindowsFileName(s ServerSettings) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	for pad := 0; pad < 6; pad++ {
		js := append([]byte(strings.Repeat(" ", pad)), raw...)
		cfg := reverse(base64.RawURLEncoding.EncodeToString(js))
		name := "rustdesk-licensed-" + cfg + ".exe"
		if strings.Contains(cfg, "--") || strings.HasPrefix(cfg, "-") || strings.HasSuffix(cfg, "-") || len(name) > maxFileName {
			continue
		}
		if got, ok := ParseWindowsFileName(name); ok && got == s {
			return name, nil
		}
	}
	return "", ErrFileName
}

// ParseWindowsFileName reads settings back from a file name the way the
// RustDesk client does (src/custom_server.rs, get_custom_server_from_string,
// "licensed" form, unsigned config). It exists to check WindowsFileName.
func ParseWindowsFileName(name string) (ServerSettings, bool) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".exe.exe"):
		name = name[:len(name)-8]
	case strings.HasSuffix(lower, ".exe"):
		name = name[:len(name)-4]
	}
	if strings.Contains(strings.ToLower(name), "host=") {
		return ServerSettings{}, false
	}
	name = strings.ReplaceAll(name, "-licensed---", "--")
	name = strings.ReplaceAll(name, "-licensed--", "--")
	name = strings.ReplaceAll(name, "-licensed-", "--")
	for _, part := range strings.Split(name, "--") {
		candidates := []string{strings.TrimSpace(part)}
		if strings.Contains(part, "(") {
			for _, p := range strings.Split(part, "(") {
				candidates = append(candidates, strings.TrimSpace(p))
			}
		}
		for _, c := range candidates {
			b, err := base64.RawURLEncoding.DecodeString(reverse(c))
			if err != nil {
				if b, err = base64.URLEncoding.DecodeString(reverse(c)); err != nil {
					continue
				}
			}
			var s ServerSettings
			if json.Unmarshal(b, &s) == nil {
				return s, true
			}
		}
	}
	return ServerSettings{}, false
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
