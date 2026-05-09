// Package web embeds the built dashboard SPA. The Vite build emits files
// into ./dist; the placeholder index.html committed alongside this file is
// replaced by `make web`.
package web

import "embed"

//go:embed all:dist
var FS embed.FS
