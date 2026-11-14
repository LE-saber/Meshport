// Package console embeds the web console (a dependency-free single-page app)
// that meshportd serves at "/" unless web_dir points at another build.
package console

import (
	"embed"
	"io/fs"
)

//go:embed web
var files embed.FS

// FS returns the console files rooted at the web directory.
func FS() fs.FS {
	sub, err := fs.Sub(files, "web")
	if err != nil {
		panic(err)
	}
	return sub
}
