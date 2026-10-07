package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The source serves GraphQL only in the form the PDP judged: POST with a JSON
// body on one path. Anything else is refused here, before anything is
// decrypted or forwarded.
func TestOnlyTheTransportSubsetIsForwarded(t *testing.T) {
	const body = `{"query":"{ __typename }"}`
	cases := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		want    int
	}{
		{"json", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json"}, http.StatusOK},
		{"json with charset", http.MethodPost, "/graphql", map[string]string{"Content-Type": "Application/JSON; Charset=UTF-8"}, http.StatusOK},

		{"get with a query string", http.MethodGet, "/graphql?query=%7B__typename%7D", nil, http.StatusMethodNotAllowed},
		{"get", http.MethodGet, "/graphql", nil, http.StatusMethodNotAllowed},

		{"application/graphql", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/graphql"}, http.StatusUnsupportedMediaType},
		{"form body", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"other charset", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json; charset=latin1"}, http.StatusUnsupportedMediaType},
		{"other parameter", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json; profile=x"}, http.StatusUnsupportedMediaType},
		{"no content type", http.MethodPost, "/graphql", nil, http.StatusUnsupportedMediaType},
		// A general media-type parser reads these as charset=utf-8; the PDP's
		// mapper refuses them, so the source must too.
		{"RFC 2231 charset", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json; charset*=us-ascii''utf-8"}, http.StatusUnsupportedMediaType},
		{"RFC 2231 continuation", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json; charset*0=utf; charset*1=-8"}, http.StatusUnsupportedMediaType},
		{"trailing semicolon", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json;"}, http.StatusUnsupportedMediaType},
		{"space around =", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json ; charset = utf-8"}, http.StatusUnsupportedMediaType},

		{"post with a query string", http.MethodPost, "/graphql?query=%7B__typename%7D", map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest},
		{"post with a bare question mark", http.MethodPost, "/graphql?", map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest},

		{"trailing slash", http.MethodPost, "/graphql/", map[string]string{"Content-Type": "application/json"}, http.StatusNotFound},
		{"other case", http.MethodPost, "/GraphQL", map[string]string{"Content-Type": "application/json"}, http.StatusNotFound},
		{"double slash", http.MethodPost, "//graphql", map[string]string{"Content-Type": "application/json"}, http.StatusNotFound},
		{"percent-encoded slash", http.MethodPost, "/%2Fgraphql", map[string]string{"Content-Type": "application/json"}, http.StatusNotFound},
		{"percent-encoded letter", http.MethodPost, "/gr%61phql", map[string]string{"Content-Type": "application/json"}, http.StatusNotFound},

		{"upgrade", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json", "Upgrade": "websocket"}, http.StatusBadRequest},
		{"content encoding", http.MethodPost, "/graphql", map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forwarded := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"__typename":"Query"}}`))
			}))
			defer upstream.Close()
			cfg := config{UpstreamURL: upstream.URL, DecryptionURL: "http://unused.invalid", PseudonymVars: "bsn", GraphQLPath: "/graphql"}
			handler := mustMux(t, cfg, &http.Client{Timeout: 5 * time.Second}, nil)

			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(body))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body)
			}
			if wantForward := tc.want == http.StatusOK; (forwarded == 1) != wantForward {
				t.Fatalf("forwarded %d times, want forwarded=%v", forwarded, wantForward)
			}
		})
	}
}

// A refused request is not decrypted: the consent token is never read.
func TestARefusedRequestDecryptsNothing(t *testing.T) {
	decryptions := 0
	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decryptions++
		http.Error(w, "not expected", http.StatusInternalServerError)
	}))
	defer component.Close()
	cfg := config{UpstreamURL: "http://unused.invalid", DecryptionURL: component.URL, PseudonymVars: "bsn", GraphQLPath: "/graphql"}
	handler := mustMux(t, cfg, &http.Client{Timeout: 5 * time.Second}, nil)

	req := httptest.NewRequest(http.MethodGet, "/graphql?query=x", nil)
	req.Header.Set(consentTokenHeader, "a.b.c")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || decryptions != 0 {
		t.Fatalf("status = %d, decryptions = %d; want 405 and none", rec.Code, decryptions)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
}

// The path is configurable per source; the default is /graphql.
func TestTheGraphQLPathIsConfigurable(t *testing.T) {
	if got := loadConfig().GraphQLPath; got != "/graphql" {
		t.Fatalf("default GraphQLPath = %q, want /graphql", got)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	cfg := config{UpstreamURL: upstream.URL, DecryptionURL: "http://unused.invalid", PseudonymVars: "bsn", GraphQLPath: "/api/graphql"}
	handler := mustMux(t, cfg, &http.Client{Timeout: 5 * time.Second}, nil)

	for target, want := range map[string]int{"/api/graphql": http.StatusOK, "/graphql": http.StatusNotFound} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s: status = %d, want %d", target, rec.Code, want)
		}
	}
}
