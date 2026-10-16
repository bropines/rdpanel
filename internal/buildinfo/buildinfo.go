// Package buildinfo exposes version metadata injected at link time:
//
//	go build -ldflags "-X github.com/bropines/rdpanel/internal/buildinfo.Version=v1.2.3 \
//	  -X github.com/bropines/rdpanel/internal/buildinfo.Commit=abc123 \
//	  -X github.com/bropines/rdpanel/internal/buildinfo.Date=2026-10-15T00:00:00Z"
//
// The container image also sets HBBRepo and HBBRef to the source of the bundled
// hbbs/hbbr binaries (deploy/Dockerfile build args HBB_REPO and HBB_REF).
package buildinfo

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// Link-time variables.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
	// HBBRepo and HBBRef identify the bundled hbbs/hbbr sources; empty when the
	// binary was built without them (e.g. `make build` outside the image).
	HBBRepo = ""
	HBBRef  = ""
)

// HBBRefEnv names the environment variable used as a fallback for HBBRef when
// it was not set at link time. The all-in-one image sets it from its HBB_REF
// build argument.
const HBBRefEnv = "RDPANEL_HBB_REF"

// Info is the resolved build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	HBBRepo   string `json:"hbb_repo,omitempty"`
	HBBRef    string `json:"hbb_ref,omitempty"`
}

// Get returns build metadata, falling back to VCS info embedded by the Go
// toolchain when ldflags were not set.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version(), HBBRepo: HBBRepo, HBBRef: HBBRef}
	if info.HBBRef == "" {
		info.HBBRef = strings.TrimSpace(os.Getenv(HBBRefEnv))
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			}
		}
	}
	if len(info.Commit) > 12 {
		info.Commit = info.Commit[:12]
	}
	return info
}

// String renders a one-line version string.
func (i Info) String() string {
	s := "rdpanel " + i.Version
	if i.Commit != "" {
		s += " (" + i.Commit + ")"
	}
	if i.Date != "" {
		s += " built " + i.Date
	}
	s = fmt.Sprintf("%s %s", s, i.GoVersion)
	if i.HBBRef != "" {
		s += "; hbbs/hbbr " + i.HBBRef
		if i.HBBRepo != "" {
			s += " from " + i.HBBRepo
		}
	}
	return s
}
