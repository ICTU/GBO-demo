package main

import (
	_ "embed"
	"net/http"
	"net/url"
	"strings"
)

// The playground page. Embedded rather than mounted so the image stays a
// binary plus mockdata, and so `go test` and `go run` serve the same bytes as
// the container does.
//
//go:embed playground.html
var playgroundHTML []byte

func playgroundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is baked into the binary and changes only with a deploy, but
	// it names exact CDN versions -- serving a stale copy from cache after an
	// upgrade would mean SRI hashes that no longer match their assets.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(playgroundHTML)
}

// wantsPlayground reports whether this /graphql request is a browser opening
// the endpoint rather than a client calling it. Mirrors the condition
// graphql-go/handler used to decide between rendering its own playground and
// executing the query, so flipping to our page keeps every machine path --
// POST from the FSC-Inway, and `GET ?raw` -- returning GraphQL results.
func wantsPlayground(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if _, raw := r.URL.Query()["raw"]; raw {
		return false
	}
	accept := r.Header.Get("Accept")
	return !strings.Contains(accept, "application/json") && strings.Contains(accept, "text/html")
}

// playgroundRedirect sends a browser from /graphql to /playground, carrying
// the query string along so the ?query= deep link survives. Bookmarks and
// older dev-portal builds still point at /graphql.
func playgroundRedirect(w http.ResponseWriter, r *http.Request) {
	target := url.URL{Path: "/playground", RawQuery: r.URL.RawQuery}
	http.Redirect(w, r, target.String(), http.StatusFound)
}
