package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// identifier identifies the user during the authorization request
// (REQ-AZSP-6.1.3.1-02) and returns the BSN. It is the seam where slice 3
// puts the test-person chooser and slice 7 the OpenID4VP PID disclosure.
type identifier interface {
	identify(r *http.Request) (bsn string, ok bool)
}

// stubIdentifier is NOT conformant user identification: the client names a
// test person in login_hint and no user is involved at all. It exists so the
// OAuth flow can be built and tested before slice 3.
type stubIdentifier struct {
	persons map[string]string // login_hint -> BSN
}

func (s stubIdentifier) identify(r *http.Request) (string, bool) {
	bsn, ok := s.persons[r.URL.Query().Get("login_hint")]
	return bsn, ok
}

func loadTestPersons(path string) (stubIdentifier, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return stubIdentifier{}, fmt.Errorf("read test persons: %w", err)
	}
	var doc struct {
		Persons []struct {
			LoginHint string `json:"login_hint"`
			BSN       string `json:"bsn"`
		} `json:"persons"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return stubIdentifier{}, fmt.Errorf("parse test persons: %w", err)
	}
	persons := map[string]string{}
	for _, p := range doc.Persons {
		if p.LoginHint == "" || p.BSN == "" {
			return stubIdentifier{}, fmt.Errorf("parse test persons: login_hint and bsn are required")
		}
		persons[p.LoginHint] = p.BSN
	}
	return stubIdentifier{persons: persons}, nil
}
