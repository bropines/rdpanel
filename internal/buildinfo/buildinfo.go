// Package buildinfo exposes version metadata injected at link time:
//
//	go build -ldflags "-X github.com/bropines/rustdesk-panel/internal/buildinfo.Version=v1.2.3 \
//	  -X github.com/bropines/rustdesk-panel/internal/buildinfo.Commit=abc123 \
//	  -X github.com/bropines/rustdesk-panel/internal/buildinfo.Date=2026-10-15T00:00:00Z"
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Link-time variables.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is the resolved build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
}

// Get returns build metadata, falling back to VCS info embedded by the Go
// toolchain when ldflags were not set.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version()}
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
	return fmt.Sprintf("%s %s", s, i.GoVersion)
}
