// Package testpersons is the identification stub: it implements
// authorization.Identifier with a file of test persons.
//
// It is NOT conformant user identification: the client names a test person
// in login_hint and no user is involved at all. It exists so the OAuth flow
// can be built and tested before slice 3 of #336.
package testpersons

import (
	"encoding/json"
	"fmt"
	"os"
)

// Stub maps a login_hint to the BSN of a test person.
type Stub struct {
	persons map[string]string
}

// New returns a stub for the given login_hint -> BSN pairs.
func New(persons map[string]string) Stub { return Stub{persons: persons} }

func (s Stub) Identify(loginHint string) (string, bool) {
	bsn, ok := s.persons[loginHint]
	return bsn, ok
}

// Len is the number of test persons.
func (s Stub) Len() int { return len(s.persons) }

// Load reads the test persons file.
func Load(path string) (Stub, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Stub{}, fmt.Errorf("read test persons: %w", err)
	}
	var doc struct {
		Persons []struct {
			LoginHint string `json:"login_hint"`
			BSN       string `json:"bsn"`
		} `json:"persons"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Stub{}, fmt.Errorf("parse test persons: %w", err)
	}
	persons := map[string]string{}
	for _, p := range doc.Persons {
		if p.LoginHint == "" || p.BSN == "" {
			return Stub{}, fmt.Errorf("parse test persons: login_hint and bsn are required")
		}
		persons[p.LoginHint] = p.BSN
	}
	return Stub{persons: persons}, nil
}
