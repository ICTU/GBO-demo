// Package stubregister is a stub of the national register of wallet-relying
// parties (CIR (EU) 2025/848; D9 in #336): entries from a JSON file, looked
// up by registryURI. It implements authorization.Register.
//
// The national register and its API (ETSI/EUDI TS5) are not available to this
// project. The entries use a subset of the TS5 data model: identifiers, legal
// name and entitlements as URIs.
package stubregister

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Register holds the entries by registryURI.
type Register struct {
	entries map[string]authorization.RegisterEntry
}

// New returns a register with the given entries, by registryURI.
func New(entries map[string]authorization.RegisterEntry) *Register {
	return &Register{entries: entries}
}

func (r *Register) Lookup(_ context.Context, registryURI string) (authorization.RegisterEntry, error) {
	e, ok := r.entries[registryURI]
	if !ok {
		return authorization.RegisterEntry{}, authorization.ErrNotRegistered
	}
	return e, nil
}

// Len is the number of entries.
func (r *Register) Len() int { return len(r.entries) }

// Load reads the register file.
func Load(path string) (*Register, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read register: %w", err)
	}
	var doc struct {
		Entries []struct {
			RegistryURI  string   `json:"registryURI"`
			Identifiers  []string `json:"identifiers"`
			LegalName    string   `json:"legalName"`
			Entitlements []string `json:"entitlements"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse register: %w", err)
	}
	entries := map[string]authorization.RegisterEntry{}
	for _, e := range doc.Entries {
		if e.RegistryURI == "" || len(e.Identifiers) == 0 {
			return nil, fmt.Errorf("register entry %q: registryURI and identifiers are required", e.RegistryURI)
		}
		entries[e.RegistryURI] = authorization.RegisterEntry{Identifiers: e.Identifiers, LegalName: e.LegalName, Entitlements: e.Entitlements}
	}
	return New(entries), nil
}
