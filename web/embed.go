package web

import (
	"embed"
	"io/fs"
)

// Build the frontend before compiling Go to include its assets in the binary.
//
//go:embed all:dist
var assets embed.FS

func Assets() fs.FS {
	files, _ := fs.Sub(assets, "dist")
	return files
}
