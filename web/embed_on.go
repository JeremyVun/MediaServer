//go:build embedweb

// Package webdist embeds the built web app (web/dist) into the binary.
// Build with -tags embedweb after `vite build` (make build does both);
// without the tag (plain `go build` during development) FS returns nil and
// the server serves a placeholder while the Vite dev server hosts the UI.
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // impossible: "dist" is the embedded directory itself
	}
	return sub
}
