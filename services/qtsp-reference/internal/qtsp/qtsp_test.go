package qtsp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

const (
	idFamilyName = "https://gbo.example/attributes/family_name/1"
	idDiploma    = "https://gbo.example/attributes/education_qualification/1"
)

// world is a fake DIP, contract and authorization server metadata: endpoint
// per attribute, and edits to break the chain.
type world struct {
	srv       *httptest.Server
	endpoints map[string][]string
	// tenant puts the authorization server under a path: issuer
	// <srv>/<tenant>, metadata at /.well-known/oauth-authorization-server/<tenant>.
	tenant    string
	server    string // the contract's server; empty means the endpoint
	issuer    string // the metadata's issuer; empty means the server's URL
	registers atomic.Int32
	register  func(w http.ResponseWriter)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{endpoints: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /retrieve", func(rw http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("attributeIdentifier")
		records := []map[string]any{}
		for _, ep := range w.endpoints[id] {
			records = append(records, map[string]any{"attributeIdentifier": id, "endpointURI": w.srv.URL + ep,
				"endpointDescription": w.srv.URL + ep + "/openapi.json", "provider": map[string]any{"legalName": "ASIP"}})
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"dataServices": records})
	})
	mux.HandleFunc("GET /{ds}/authsrc-api/openapi.json", func(rw http.ResponseWriter, r *http.Request) {
		server := w.srv.URL + "/" + r.PathValue("ds") + "/authsrc-api"
		if w.server != "" {
			server = w.server
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"servers": []any{map[string]any{"url": server}},
			"components": map[string]any{"securitySchemes": map[string]any{"oauth2": map[string]any{
				"x-oauth-authorization-server": w.srv.URL + "/.well-known/oauth-authorization-server" + w.tenantPath()}}}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/{tenant...}", func(rw http.ResponseWriter, r *http.Request) {
		issuer := w.srv.URL + w.tenantPath()
		if w.issuer != "" {
			issuer = w.issuer
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"issuer": issuer, "registration_endpoint": w.srv.URL + "/register",
			"authorization_endpoint": w.srv.URL + "/authorize", "token_endpoint": w.srv.URL + "/token"})
	})
	mux.HandleFunc("POST /register", func(rw http.ResponseWriter, _ *http.Request) {
		w.registers.Add(1)
		if w.register != nil {
			w.register(rw)
			return
		}
		rw.WriteHeader(http.StatusCreated)
		_, _ = rw.Write([]byte(`{"client_id":"c1","jwks":{"keys":[{"kty":"EC","crv":"P-256","kid":"k1","x":"WbbaSStuffgbOqpMb3Jtiv7oMHO_C2ODY4Yeal5XEnc","y":"2qhNgQsdMlNYYwoE1Ndri1jTvk8QwRhb9KZl1ZAXPpY"}]}}`))
	})
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *world) tenantPath() string {
	if w.tenant == "" {
		return ""
	}
	return "/" + w.tenant
}

func (w *world) client(t *testing.T) *Client {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c, err := New(Config{DIP: w.srv.URL, Key: key, RegistryURI: "https://register.test/qtsp", RedirectURI: "https://qtsp.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPlanFollowsTheRecords(t *testing.T) {
	w := newWorld(t)
	w.endpoints[idFamilyName] = []string{"/brp/authsrc-api"}
	p, err := w.client(t).Plan(context.Background(), []string{idFamilyName})
	if err != nil || p.EndpointURI != w.srv.URL+"/brp/authsrc-api" || p.Server.TokenEndpoint != w.srv.URL+"/token" {
		t.Fatalf("Plan = %+v, %v", p, err)
	}
	u, _ := url.Parse(w.client(t).AuthorizationURL(p, Registration{ClientID: "c1"}, "verify", "s", "ch"))
	if q := u.Query(); q.Get("resource") != p.EndpointURI || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "verify" || q.Get("state") != "s" {
		t.Errorf("authorization URL = %s", u)
	}
}

func TestPlanRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		edit  func(*world)
		ids   []string
		check func(error) bool
	}{
		"no data service (Q-C6)": {func(*world) {}, []string{idFamilyName, "https://gbo.example/attributes/shoe_size/1"},
			func(err error) bool { var e *NoDataServiceError; return errors.As(err, &e) }},
		"two data services (Q-C7)": {func(w *world) { w.endpoints[idDiploma] = []string{"/duo/authsrc-api"} }, []string{idFamilyName, idDiploma},
			func(err error) bool { return errors.Is(err, ErrNoCommonDataService) }},
		"contract for another endpoint": {func(w *world) { w.server = "https://elsewhere.test/authsrc-api" }, []string{idFamilyName},
			func(err error) bool { return err != nil }},
		"metadata of another issuer": {func(w *world) { w.issuer = "https://elsewhere.test" }, []string{idFamilyName},
			func(err error) bool { return err != nil }},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.endpoints[idFamilyName] = []string{"/brp/authsrc-api"}
			tc.edit(w)
			if _, err := w.client(t).Plan(context.Background(), tc.ids); !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			if w.registers.Load() != 0 {
				t.Fatal("registered before the plan was complete")
			}
		})
	}
}

func TestRegister(t *testing.T) {
	w := newWorld(t)
	w.endpoints[idFamilyName] = []string{"/brp/authsrc-api"}
	c := w.client(t)
	p, err := c.Plan(context.Background(), []string{idFamilyName})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := c.Register(context.Background(), p.Server)
	if err != nil || reg.ClientID != "c1" || reg.KeyID != "k1" {
		t.Fatalf("Register = %+v, %v", reg, err)
	}
	if _, err := c.Register(context.Background(), p.Server); err != nil || w.registers.Load() != 1 {
		t.Fatalf("second Register: %v; %d registrations, want one per server", err, w.registers.Load())
	}

	refusing := newWorld(t)
	refusing.endpoints[idFamilyName] = []string{"/brp/authsrc-api"}
	refusing.register = func(rw http.ResponseWriter) {
		rw.WriteHeader(http.StatusBadRequest)
		_, _ = rw.Write([]byte(`{"error":"unapproved_software_statement","error_description":"no QEAA_Provider entitlement"}`))
	}
	c = refusing.client(t)
	p, _ = c.Plan(context.Background(), []string{idFamilyName})
	var refused *RegistrationError
	if _, err := c.Register(context.Background(), p.Server); !errors.As(err, &refused) || refused.Code != "unapproved_software_statement" {
		t.Fatalf("refused: err = %v", err)
	}

	unavailable := newWorld(t)
	unavailable.endpoints[idFamilyName] = []string{"/brp/authsrc-api"}
	unavailable.register = func(rw http.ResponseWriter) { rw.WriteHeader(http.StatusServiceUnavailable) }
	c = unavailable.client(t)
	p, _ = c.Plan(context.Background(), []string{idFamilyName})
	if _, err := c.Register(context.Background(), p.Server); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable: err = %v", err)
	}
}

// A directory with several ASIPs: the plan takes a data service that
// provides every attribute, the same one on every run.
func TestPlanChoosesACommonDataService(t *testing.T) {
	w := newWorld(t)
	w.endpoints[idFamilyName] = []string{"/b/authsrc-api", "/a/authsrc-api"}
	p, err := w.client(t).Plan(context.Background(), []string{idFamilyName})
	if err != nil || p.EndpointURI != w.srv.URL+"/a/authsrc-api" {
		t.Fatalf("one attribute, two data services: %+v, %v; want the first by endpoint", p.EndpointURI, err)
	}
	w.endpoints[idDiploma] = []string{"/b/authsrc-api"}
	p, err = w.client(t).Plan(context.Background(), []string{idFamilyName, idDiploma})
	if err != nil || p.EndpointURI != w.srv.URL+"/b/authsrc-api" {
		t.Fatalf("the one data service with both: %+v, %v", p.EndpointURI, err)
	}
}

// An authorization server under a path (RFC 8414 section 3.1).
func TestPlanAcceptsAnIssuerWithAPath(t *testing.T) {
	w := newWorld(t)
	w.endpoints[idFamilyName] = []string{"/brp/authsrc-api"}
	w.tenant = "issuer1"
	p, err := w.client(t).Plan(context.Background(), []string{idFamilyName})
	if err != nil || p.Server.Issuer != w.srv.URL+"/issuer1" {
		t.Fatalf("Plan = %+v, %v", p.Server, err)
	}
}

func TestMetadataURL(t *testing.T) {
	for issuer, want := range map[string]string{
		"https://example.com":         "https://example.com/.well-known/oauth-authorization-server",
		"https://example.com/":        "https://example.com/.well-known/oauth-authorization-server",
		"https://example.com/issuer1": "https://example.com/.well-known/oauth-authorization-server/issuer1",
		"https://example.com/a/b/":    "https://example.com/.well-known/oauth-authorization-server/a/b",
		"http://localhost:9425":       "http://localhost:9425/.well-known/oauth-authorization-server",
	} {
		if got, err := MetadataURL(issuer); err != nil || got != want {
			t.Errorf("MetadataURL(%q) = %q, %v; want %q", issuer, got, err, want)
		}
	}
	for _, bad := range []string{"", "example.com", "https://example.com?x=1", "https://example.com#f"} {
		if _, err := MetadataURL(bad); err == nil {
			t.Errorf("MetadataURL(%q): err = nil", bad)
		}
	}
}
