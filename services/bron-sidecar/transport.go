package main

import (
	"mime"
	"net/http"
	"strings"
)

// The source accepts GraphQL only in the form the PDP judged (FTV GraphQL
// profile, Section 5.1 and guarantee H1): POST with a JSON body on one path,
// no query string, no Upgrade or Content-Encoding. The PDP already refuses
// anything else; refusing it here as well means a request the PDP never
// judged cannot run, whatever reaches the sidecar.

const defaultGraphQLPath = "/graphql"

// refuseTransport returns the status and reason for a request outside the
// transport subset, or 0 when it may be forwarded. The path is compared on
// the raw request target, byte for byte: no cleaning, no decoding.
func refuseTransport(r *http.Request, graphQLPath string) (int, string) {
	path, _, hasQuery := strings.Cut(r.RequestURI, "?")
	switch {
	case path != graphQLPath:
		return http.StatusNotFound, "GraphQL is served on " + graphQLPath + " only"
	case r.Method != http.MethodPost:
		return http.StatusMethodNotAllowed, "only POST is accepted"
	case hasQuery:
		return http.StatusBadRequest, "no query string is accepted"
	case !jsonContentType(r.Header.Values("Content-Type")):
		return http.StatusUnsupportedMediaType, "only application/json is accepted"
	case len(r.Header.Values("Upgrade")) > 0, len(r.Header.Values("Content-Encoding")) > 0:
		return http.StatusBadRequest, "no Upgrade or Content-Encoding is accepted"
	}
	return 0, ""
}

// jsonContentType holds for exactly one application/json value, with no
// parameter other than charset=utf-8, compared case-insensitively.
func jsonContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

// transportOnly forwards a request to next only when it is in the transport
// subset.
func transportOnly(graphQLPath string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status, reason := refuseTransport(r, graphQLPath); status != 0 {
			if status == http.StatusMethodNotAllowed {
				w.Header().Set("Allow", http.MethodPost)
			}
			http.Error(w, reason, status)
			return
		}
		next.ServeHTTP(w, r)
	})
}
