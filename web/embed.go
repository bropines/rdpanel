// Package web embeds the built React SPA (web/dist, produced by `npm run build`).
//
// A fresh checkout only contains web/dist/.gitkeep, so the Go module always
// compiles and tests run without Node; the server then serves a placeholder
// page explaining how to build the UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built SPA rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // static path
	}
	return sub
}
