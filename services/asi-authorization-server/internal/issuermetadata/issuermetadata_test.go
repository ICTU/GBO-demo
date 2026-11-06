package issuermetadata_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/issuermetadata"
)

const metadata = `{"credential_configurations_supported": {
  "tax-income_2025": {"credential_metadata": {
    "display": [{"name": "Income statement 2025", "locale": "en"}, {"name": "Inkomensverklaring 2025", "locale": "nl"}],
    "claims": [
      {"path": ["jaar"], "display": [{"name": "Year", "locale": "en"}, {"name": "Jaar", "locale": "nl"}]},
      {"path": ["inkomen", "bedrag"], "display": [{"name": "Amount", "locale": "en"}]},
      {"path": ["kenmerk"]}
    ]}},
  "tax-plain": {"credential_metadata": {"display": [{"name": "Plain"}]}}
}}`

// The consent page reads the credential's name and its claims' names from
// the issuer's unsigned metadata at its well-known URL (OpenID4VCI 1.0
// section 12.2.2), in Dutch where the metadata has it.
func TestFetch(t *testing.T) {
	var path, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, accept = r.URL.Path, r.Header.Get("Accept")
		_, _ = w.Write([]byte(metadata))
	}))
	defer srv.Close()
	fetch := issuermetadata.Fetcher(srv.Client())
	d, err := fetch(context.Background(), srv.URL+"/tax/eudi", "tax-income_2025")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/.well-known/openid-credential-issuer/tax/eudi" || accept != "application/json" {
		t.Errorf("fetched %s with Accept %q", path, accept)
	}
	if d.Name != "Inkomensverklaring 2025" || !slices.Equal(d.Claims, []string{"Jaar", "Amount", "kenmerk"}) {
		t.Errorf("display = %+v", d)
	}
	if d, err := fetch(context.Background(), srv.URL+"/tax/eudi", "tax-plain"); err != nil || d.Name != "Plain" || len(d.Claims) != 0 {
		t.Errorf("a display without locale: %+v %v", d, err)
	}
	if _, err := fetch(context.Background(), srv.URL+"/tax/eudi", "tax-nope"); !errors.Is(err, authorization.ErrUnknownCredentialConfiguration) {
		t.Errorf("unknown configuration: %v", err)
	}
}

func TestFetchFails(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"not JSON": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			_, err := issuermetadata.Fetcher(srv.Client())(context.Background(), srv.URL+"/tax/eudi", "tax-income_2025")
			if err == nil || errors.Is(err, authorization.ErrUnknownCredentialConfiguration) {
				t.Fatalf("got %v, want another error", err)
			}
		})
	}
}
