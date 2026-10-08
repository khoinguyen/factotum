// Package web embeds the built shadcn/ui single-page app that `ft serve` hosts
// under /app. The app is built by Vite into web/dist and committed to the Go
// binary at compile time, so the dashboard ships as one artifact with no
// external asset directory.
//
// dist is committed with a placeholder .gitkeep so `go build` and `go test`
// always find something to embed on a fresh clone; `mise run build-web`
// overwrites it with the real bundle before the binary is built.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built frontend rooted at index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// embed guarantees the directory exists, so this cannot happen at
		// runtime; panic rather than serve a silently empty app.
		panic("web: embedded dist: " + err.Error())
	}
	return sub
}
