// Package clientconfig is the client registry from a configuration file. It
// implements authorization.Clients. Dynamic client registration follows in
// the second pull request of #503.
package clientconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Registry holds the registered clients.
type Registry struct {
	clients map[string]*authorization.Client
}

// New returns a registry of the given clients.
func New(clients ...*authorization.Client) *Registry {
	r := &Registry{clients: map[string]*authorization.Client{}}
	for _, c := range clients {
		r.clients[c.ID] = c
	}
	return r
}

func (r *Registry) Client(id string) (*authorization.Client, bool) {
	c, ok := r.clients[id]
	return c, ok
}

// Len is the number of registered clients.
func (r *Registry) Len() int { return len(r.clients) }

// Load reads the registered clients. Without a file there are none.
func Load(path string) (*Registry, error) {
	if path == "" {
		return New(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clients: %w", err)
	}
	var doc struct {
		Clients []struct {
			ClientID string `json:"client_id"`
			// ClientName is the legal name the user sees (RFC 7591).
			ClientName   string             `json:"client_name"`
			RedirectURIs []string           `json:"redirect_uris"`
			Scope        string             `json:"scope"`
			JWKS         jose.JSONWebKeySet `json:"jwks"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse clients: %w", err)
	}
	var clients []*authorization.Client
	seen := map[string]bool{}
	for _, c := range doc.Clients {
		if c.ClientID == "" || len(c.RedirectURIs) == 0 || len(c.JWKS.Keys) == 0 {
			return nil, fmt.Errorf("client %q: client_id, redirect_uris and jwks are required", c.ClientID)
		}
		if seen[c.ClientID] {
			return nil, fmt.Errorf("client %q: client_id is used more than once", c.ClientID)
		}
		seen[c.ClientID] = true
		for _, k := range c.JWKS.Keys {
			if !k.IsPublic() {
				return nil, fmt.Errorf("client %q: jwks must contain public keys only", c.ClientID)
			}
		}
		scopes := map[string]bool{}
		for _, sc := range strings.Fields(c.Scope) {
			if !slices.Contains(authorization.OperationScopes, sc) {
				return nil, fmt.Errorf("client %q: scope %q is not verify or retrieve", c.ClientID, sc)
			}
			scopes[sc] = true
		}
		clients = append(clients, &authorization.Client{ID: c.ClientID, Name: c.ClientName, RedirectURIs: c.RedirectURIs, Scopes: scopes, Keys: c.JWKS})
	}
	return New(clients...), nil
}
