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
	keys, err := New(as.URL).Keys(t.Context(), testKeyID)
	if err != nil || len(keys) != 1 || keys[0].KeyID != testKeyID {
		t.Fatalf("keys = %v, %v; want the key of the authorization server", keys, err)
	}
	// RFC 8414 section 3.3: the issuer in the metadata must be the issuer the
	// ASIP is configured with.
	if _, err := New(as.URL+"/other").Keys(t.Context(), testKeyID); !errors.Is(err, accesstoken.ErrKeysUnavailable) {
		t.Fatalf("metadata of another issuer was accepted: %v", err)
	}
}
