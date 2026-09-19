package web

import (
	"embed"
	"io/fs"
)

// fsSub narrows an embedded filesystem to a subdirectory.
func fsSub(f embed.FS, dir string) (fs.FS, error) {
	return fs.Sub(f, dir)
}
