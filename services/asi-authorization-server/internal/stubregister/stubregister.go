// Package stubregister is a stub of the national register of wallet-relying
// parties (CIR (EU) 2025/848; D9 in #336): entries from a JSON file, looked
// up by registryURI. It implements authorization.Register.
//
// The national register and its API (ETSI/EUDI TS5) are not available to this
// project. The entries use a subset of the TS5 data model: identifiers, legal
// name and entitlements as URIs.
//
// The file is read again on every lookup, so an entitlement withdrawn from
// the file is seen by the next periodic re-verification without a restart.
// A file that cannot be read or parsed counts as an unavailable register.
package stubregister

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Register looks entries up by registryURI, from a file or from a fixed set.
type Register struct {
	path    string
	entries map[string]authorization.RegisterEntry
}

// New returns a register with a fixed set of entries, by registryURI.
func New(entries map[string]authorization.RegisterEntry) *Register {
	return &Register{entries: entries}
}

// Load returns a register backed by the file at path. The file is read now,
// so a wrong file fails at start, and again on every lookup.
func Load(path string) (*Register, error) {
	entries, err := read(path)
	if err != nil {
		return nil, err
	}
	return &Register{path: path, entries: entries}, nil
}

func (r *Register) Lookup(_ context.Context, registryURI string) (authorization.RegisterEntry, error) {
	entries := r.entries
	if r.path != "" {
		var err error
		if entries, err = read(r.path); err != nil {
			return authorization.RegisterEntry{}, fmt.Errorf("%w: %v", authorization.ErrRegisterUnavailable, err)
		}
	}
	e, ok := entries[registryURI]
	if !ok {
		return authorization.RegisterEntry{}, authorization.ErrNotRegistered
	}
	return e, nil
}

// Len is the number of entries when the register was loaded.
func (r *Register) Len() int { return len(r.entries) }

func read(path string) (map[string]authorization.RegisterEntry, error) {
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
	return entries, nil
}
