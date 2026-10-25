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

// maxFileName is the longest file name WindowsFileName will produce, ".exe"
// included. It is deliberately far below the 255 characters NTFS allows for a
// single name, because the name does not travel alone: the classic Windows
// MAX_PATH of 260 characters (terminating NUL included) limits the whole path,
// directory and name together, and Explorer, many browsers, messengers and
// archivers still enforce it. The arithmetic:
//
//	260  MAX_PATH
//	 -1  terminating NUL, so 259 usable characters
//	-56  the download directory: C:\Users\<30 characters>\Downloads\. A profile
//	     redirected to OneDrive is longer still, which the slack below covers
//	 -4  the " (1)" a browser inserts before ".exe" when the file already
//	     exists (RustDesk's own parser expects that form)
//	199  what is left for the name; 192 keeps a few characters of slack
//
// The number is a compromise, not a guarantee. Lower, and an ordinary
// deployment (a host, a relay, an https API URL and a 44-character key runs to
// about 185) loses the preconfigured installer altogether; higher, and names
// start to break on the machines that receive them.
//
// The settings live in the name itself, and the END of the name is the START of
// the JSON. When something shortens a name that is too long, the JSON no longer
// parses and RustDesk silently ignores it: the user ends up with a client that
// has no server. So a name that merely fits 255 is not safe. A name that does
// not fit here is refused, and the callers fall back to a share link or the
// manual settings instead of handing out a name that may be cut. Do not make it
// fit by dropping fields: without api the client derives http://<host>:21114,
// the wrong scheme and port for an HTTPS deployment.
const maxFileName = 192

// ErrFileName is returned when the settings cannot be put into a file name.
var ErrFileName = errors.New("server settings do not fit into a Windows file name")

// WindowsFileName returns the name under which the official Windows client
// applies s on its own when started (and keeps them when it installs):
// "rustdesk-licensed-<config string>.exe", the config string being RustDesk's
// reversed URL-safe base64 of the settings JSON. RustDesk splits the name at
// "--", which the encoding of non-ASCII JSON (an IDN host name) can contain;
// such encodings are avoided by shifting the base64 alignment with leading
// JSON whitespace, which RustDesk's parser skips. A shift that leaves "--" or
// makes the name longer than maxFileName is skipped, and every shift is tried
// before ErrFileName is returned: the result is never a cut name or one that
// does not read back as s.
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
