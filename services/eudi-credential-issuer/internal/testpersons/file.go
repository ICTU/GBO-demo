// Package testpersons reads the same demo person fixtures as the portal and
// test-PID source. No real identity verification is performed.
package testpersons

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"gbo-demo/eudi-credential-issuer/internal/testlogin"
)

type File string

func (f File) Load(context.Context) ([]testlogin.Person, error) {
	raw, err := os.ReadFile(string(f))
	if err != nil {
		return nil, err
	}
	var entries []struct {
		BSN    string `json:"bsn"`
		Name   string `json:"name"`
		Given  string `json:"voornamen"`
		Prefix string `json:"voorvoegsel"`
		Family string `json:"geslachtsnaam"`
	}
	if err = json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	people := make([]testlogin.Person, 0, len(entries))
	for _, p := range entries {
		if len(p.BSN) != 9 || strings.Trim(p.BSN, "0123456789") != "" || seen[p.BSN] {
			return nil, errors.New("invalid or duplicate test-person BSN")
		}
		seen[p.BSN] = true
		name := p.Name
		if name == "" {
			name = strings.Join(strings.Fields(p.Given+" "+p.Prefix+" "+p.Family), " ")
		}
		if name == "" {
			return nil, errors.New("test person has no name")
		}
		people = append(people, testlogin.Person{BSN: p.BSN, Name: name})
	}
	return people, nil
}
