package login

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// urlMap rewrites the scheme and host of a request whose URL starts with a
// mapped public base, for transport only: the request keeps its URL for what
// is signed or checked (DPoP htu, the assertion's audience). In the demo the
// authorization server's issuer is a host address the browser uses; the
// issuer's container reaches it on the internal network.
type urlMap struct {
	next  http.RoundTripper
	bases map[string]*url.URL
}

// NewURLMap parses "public=internal,..." pairs, for example
// "http://localhost:9425=http://asi-authorization-server:4021". An empty
// spec maps nothing.
func NewURLMap(spec string, next http.RoundTripper) (http.RoundTripper, error) {
	m := &urlMap{next: next, bases: map[string]*url.URL{}}
	for _, pair := range strings.Split(spec, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		public, internal, ok := strings.Cut(strings.TrimSpace(pair), "=")
		u, err := url.Parse(internal)
		if !ok || err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("URL map: %q is not public=internal", pair)
		}
		m.bases[strings.TrimRight(public, "/")] = u
	}
	return m, nil
}

func (m *urlMap) RoundTrip(r *http.Request) (*http.Response, error) {
	if u, ok := m.bases[r.URL.Scheme+"://"+r.URL.Host]; ok {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		r.Host = ""
	}
	return m.next.RoundTrip(r)
}
