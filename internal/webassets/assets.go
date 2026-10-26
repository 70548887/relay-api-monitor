package webassets

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

func Files() fs.FS {
	f, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return f
}
