// Package urlmap maps the public addresses the records and contracts name to
// the addresses a container can reach, for transport only: the request keeps
// its URL for everything that is signed or checked (DPoP htu, audiences).
package urlmap

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Transport rewrites the scheme and host of a request whose URL starts with
// a mapped public base, and sends it on next.
type Transport struct {
	next  http.RoundTripper
	bases map[string]*url.URL
}

// New parses "public=internal,…" pairs, for example
// "http://localhost:9424=http://asi-provider:4020".
func New(spec string, next http.RoundTripper) (*Transport, error) {
	t := &Transport{next: next, bases: map[string]*url.URL{}}
	for _, pair := range strings.Split(spec, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		public, internal, ok := strings.Cut(strings.TrimSpace(pair), "=")
		u, err := url.Parse(internal)
		if !ok || err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("URL map: %q is not public=internal", pair)
		}
		t.bases[strings.TrimRight(public, "/")] = u
	}
	return t, nil
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := r.URL.Scheme + "://" + r.URL.Host
	if u, ok := t.bases[base]; ok {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host, r.Host = u.Scheme, u.Host, u.Host
	}
	return t.next.RoundTrip(r)
}
