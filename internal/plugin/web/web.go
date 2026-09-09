// Package web embeds the built management UI (a single inlined index.html)
// and serves it as a CPA plugin resource under
// /v0/resource/plugins/cpa-key-quota/index.html.
//
// dist/index.html is a build artifact produced by `npm run build` in ../../web.
// The committed copy is the real bundle (rebuilt by `make web-build` and CI), so
// a plain `go build` embeds the shipped UI without needing the frontend toolchain.
package web

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed dist/index.html
var indexHTML []byte

const contentType = "text/html; charset=utf-8"

// IndexPath is the resource path (relative to the plugin resource base) the UI
// is served at.
const IndexPath = "/index.html"

// Serve returns a management response for a plugin resource GET request. It
// only handles the index page; any other path yields 404.
func Serve(path string) (status int, headers http.Header, body []byte) {
	if strings.TrimRight(path, "/") != IndexPath {
		return http.StatusNotFound, http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, []byte("not found")
	}
	headers = http.Header{
		"Content-Type":            []string{contentType},
		"X-Frame-Options":         []string{"SAMEORIGIN"},
		"Content-Security-Policy": []string{"frame-ancestors 'self'"},
	}
	return http.StatusOK, headers, indexHTML
}
