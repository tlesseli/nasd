// Package web embeds the built UI (web/dist) into the nasd binary, so the
// whole NAS software is a single file to copy. Run `make ui` before
// `go build`, otherwise the UI will be empty.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
