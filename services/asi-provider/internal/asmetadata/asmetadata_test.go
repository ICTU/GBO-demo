package asmetadata

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/asi-provider/internal/accesstoken"
)

const testKeyID = "test-as-key"

// The ASIP takes the keys from the jwks_uri in the metadata of the
// authorization server (RFC 8414, RFC 9068 section 4).
func TestKeysFromAuthorizationServerMetadata(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: testKeyID, Algorithm: "ES256", Use: "sig"}}}
	var as *httptest.Server
	as = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/oauth-authorization-server"):
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": as.URL, "jwks_uri": as.URL + "/jwks.json"})
		case r.URL.Path == "/jwks.json":
			_ = json.NewEncoder(w).Encode(jwks)
		default:
			http.NotFound(w, r)
		}
	}))
	defer as.Close()
	keys, err := New(as.URL, "").Keys(t.Context(), testKeyID)
	if err != nil || len(keys) != 1 || keys[0].KeyID != testKeyID {
		t.Fatalf("keys = %v, %v; want the key of the authorization server", keys, err)
	}
	// RFC 8414 section 3.3: the issuer in the metadata must be the issuer the
	// ASIP is configured with.
	if _, err := New(as.URL+"/other", "").Keys(t.Context(), testKeyID); !errors.Is(err, accesstoken.ErrKeysUnavailable) {
		t.Fatalf("metadata of another issuer was accepted: %v", err)
	}
}

// In compose the issuer is a host address the ASIP container cannot reach:
// it fetches metadata and keys on the internal address, and still requires
// the metadata to name the public issuer.
func TestKeysThroughTheInternalAddress(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "http://public.as.test:9425"
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: testKeyID, Algorithm: "ES256", Use: "sig"}}}
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/jwks.json"})
		case "/jwks.json":
			_ = json.NewEncoder(w).Encode(jwks)
		default:
			http.NotFound(w, r)
		}
	}))
	defer internal.Close()
	keys, err := New(issuer, internal.URL).Keys(t.Context(), testKeyID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v; want the key through the internal address", keys, err)
	}
	if _, err := New("http://other.as.test", internal.URL).Keys(t.Context(), testKeyID); !errors.Is(err, accesstoken.ErrKeysUnavailable) {
		t.Fatalf("metadata naming another issuer was accepted through the internal address: %v", err)
	}
}

// RFC 8414 section 3.1: the well-known suffix goes between the host and the
// path of the issuer.
func TestMetadataURL(t *testing.T) {
	for issuer, want := range map[string]string{
		"https://as.test":         "https://as.test/.well-known/oauth-authorization-server",
		"https://as.test/tenant":  "https://as.test/.well-known/oauth-authorization-server/tenant",
		"https://as.test/tenant/": "https://as.test/.well-known/oauth-authorization-server/tenant",
	} {
		if got, err := MetadataURL(issuer); err != nil || got != want {
			t.Errorf("MetadataURL(%q) = %q, %v; want %q", issuer, got, err, want)
		}
	}
	for _, issuer := range []string{"as.test", "https://as.test?x=1", "https://as.test#f"} {
		if _, err := MetadataURL(issuer); err == nil {
			t.Errorf("MetadataURL(%q) accepted an issuer that is not a URL without query or fragment", issuer)
		}
	}
}

// An issuer with a path: the ASIP fetches the metadata at the RFC 8414
// location, directly and through the internal address.
func TestKeysOfAnIssuerWithAPath(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: testKeyID, Algorithm: "ES256", Use: "sig"}}}
	var issuer string
	as := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server/tenant":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/jwks.json"})
		case "/tenant/jwks.json":
			_ = json.NewEncoder(w).Encode(jwks)
		default:
			http.NotFound(w, r)
		}
	}))
	defer as.Close()

	issuer = as.URL + "/tenant"
	if keys, err := New(issuer, "").Keys(t.Context(), testKeyID); err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v; want the key of the issuer with a path", keys, err)
	}
	issuer = "http://public.as.test:9425/tenant"
	if keys, err := New(issuer, as.URL+"/tenant").Keys(t.Context(), testKeyID); err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v; want the key through the internal address", keys, err)
	}
}
