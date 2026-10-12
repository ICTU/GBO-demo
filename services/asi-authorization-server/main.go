// Command asi-authorization-server is the authorization server of the ASIP
// mock (ETSI TS 119 478 V1.1.1 clause 6.1.3, I4 Authorize). A QTSP obtains a
// DPoP-bound JWT access token here and presents it to services/asi-provider.
//
// The ASIP and the authorization server may be operated by one party, but
// they are separate roles (ASIP and AZSP), so they are separate services with
// separate keys.
//
// User identification is a stub: login_hint names a test person. It is not
// conformant identity proofing; slice 3 of #336 replaces it.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func main() {
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}

func serve() error {
	issuer := strings.TrimRight(env("AZSP_ISSUER", "http://localhost:4021"), "/")
	key, err := loadSigningKey(os.Getenv("AZSP_SIGNING_KEY_FILE"))
	if err != nil {
		return err
	}
	tokens, err := newTokenIssuer(issuer, key)
	if err != nil {
		return err
	}
	clients, err := loadClients(os.Getenv("AZSP_CLIENTS_FILE"))
	if err != nil {
		return err
	}
	persons, err := loadTestPersons(env("AZSP_TEST_PERSONS_FILE", "testdata/test-persons.json"))
	if err != nil {
		return err
	}
	handler := newServer(serverConfig{
		Issuer:     issuer,
		Resource:   strings.TrimRight(env("AZSP_RESOURCE", "http://localhost:4020/authsrc-api"), "/"),
		Clients:    clients,
		Identifier: persons,
		Tokens:     tokens,
	})
	addr := env("AZSP_ADDR", ":4021")
	log.Printf("asi-authorization-server listening on %s (issuer %s, %d clients); user identification is a stub (login_hint names a test person): not conformant", addr, issuer, len(clients))
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// loadSigningKey reads the ES256 token signing key. Without a file the server
// generates a key at start: tokens then do not survive a restart, which is
// acceptable for tokens that live ten minutes.
func loadSigningKey(path string) (*ecdsa.PrivateKey, error) {
	if path == "" {
		log.Printf("AZSP_SIGNING_KEY_FILE not set: using an ephemeral signing key")
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("signing key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return x509.ParseECPrivateKey(block.Bytes)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("signing key must be an ECDSA P-256 key")
	}
	return key, nil
}

// loadClients reads the registered clients. Without a file there are none;
// dynamic client registration follows in the second pull request of slice 2.
func loadClients(path string) (map[string]*client, error) {
	clients := map[string]*client{}
	if path == "" {
		return clients, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clients: %w", err)
	}
	var doc struct {
		Clients []struct {
			ClientID     string             `json:"client_id"`
			RedirectURIs []string           `json:"redirect_uris"`
			Scope        string             `json:"scope"`
			JWKS         jose.JSONWebKeySet `json:"jwks"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse clients: %w", err)
	}
	for _, c := range doc.Clients {
		if c.ClientID == "" || len(c.RedirectURIs) == 0 || len(c.JWKS.Keys) == 0 {
			return nil, fmt.Errorf("client %q: client_id, redirect_uris and jwks are required", c.ClientID)
		}
		for _, k := range c.JWKS.Keys {
			if !k.IsPublic() {
				return nil, fmt.Errorf("client %q: jwks must contain public keys only", c.ClientID)
			}
		}
		scopes := map[string]bool{}
		for _, sc := range strings.Fields(c.Scope) {
			if !slices.Contains(operationScopes, sc) {
				return nil, fmt.Errorf("client %q: scope %q is not verify or retrieve", c.ClientID, sc)
			}
			scopes[sc] = true
		}
		clients[c.ClientID] = &client{ID: c.ClientID, RedirectURIs: c.RedirectURIs, Scopes: scopes, Keys: c.JWKS}
	}
	return clients, nil
}
