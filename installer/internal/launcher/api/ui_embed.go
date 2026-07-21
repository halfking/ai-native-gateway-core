package api

import (
	"embed"
	"io/fs"
)

// web holds the embedded launcher UI static assets (HTML/CSS/JS).
// Files live in the web/ subdirectory; Task 9 fills in real content.
//
//go:embed web
var webFS embed.FS

// WebFiles returns the embedded web filesystem rooted at the web/ directory.
// Used by API.serveUI to serve /launcher/* static paths.
func WebFiles() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("launcher: embedded web/ missing: " + err.Error())
	}
	return sub
}
