// Package testpersons is the identification stub: the test persons the
// chooser offers, with their BSN and PID attributes. It implements
// authorization.Identifier.
//
// It is NOT conformant user identification: the user picks a test person and
// nothing is checked. It exists so the OAuth flow can be built and tested
// before the OpenID4VP PID disclosure of slice 7 of #336.
package testpersons

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Person is a test person: an identifier for the chooser and the identity.
type Person struct {
	ID       string
	Identity authorization.Identity
}

// Stub holds the test persons in the order of the file.
type Stub struct {
	persons []Person
}

// New returns a stub with the given test persons. It does not validate them.
func New(persons ...Person) Stub { return Stub{persons: persons} }

func (s Stub) Persons() []authorization.TestPerson {
	out := make([]authorization.TestPerson, len(s.persons))
	for i, p := range s.persons {
		out[i] = authorization.TestPerson{ID: p.ID, Name: p.Identity.GivenName + " " + p.Identity.FamilyName}
	}
	return out
}

func (s Stub) Identify(personID string) (authorization.Identity, bool) {
	for _, p := range s.persons {
		if p.ID == personID {
			return p.Identity, true
		}
	}
	return authorization.Identity{}, false
}

// Len is the number of test persons.
func (s Stub) Len() int { return len(s.persons) }

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

// Load reads the test persons file. Every person needs a unique id, a BSN and
// the five mandatory PID attributes; otherwise the file is refused.
func Load(path string) (Stub, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Stub{}, fmt.Errorf("read test persons: %w", err)
	}
	var doc struct {
		Persons []struct {
			ID            string                     `json:"id"`
			BSN           string                     `json:"bsn"`
			FamilyName    string                     `json:"family_name"`
			GivenName     string                     `json:"given_name"`
			Birthdate     string                     `json:"birthdate"`
			PlaceOfBirth  authorization.PlaceOfBirth `json:"place_of_birth"`
			Nationalities []string                   `json:"nationalities"`
		} `json:"persons"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Stub{}, fmt.Errorf("parse test persons: %w", err)
	}
	var persons []Person
	seen := map[string]bool{}
	for _, p := range doc.Persons {
		invalid := func(reason string) (Stub, error) {
			return Stub{}, fmt.Errorf("test person %q: %s", p.ID, reason)
		}
		switch {
		case p.ID == "" || p.BSN == "":
			return invalid("id and bsn are required")
		case seen[p.ID]:
			return invalid("id is used more than once")
		case p.FamilyName == "" || p.GivenName == "":
			return invalid("family_name and given_name are required")
		}
		if _, err := time.Parse(time.DateOnly, p.Birthdate); err != nil {
			return invalid("birthdate must be YYYY-MM-DD")
		}
		pob := p.PlaceOfBirth
		if pob.Country == "" && pob.Region == "" && pob.Locality == "" {
			return invalid("place_of_birth needs country, region or locality")
		}
		if pob.Country != "" && !countryCode.MatchString(pob.Country) {
			return invalid("place_of_birth.country must be an ISO 3166-1 alpha-2 code")
		}
		if len(p.Nationalities) == 0 {
			return invalid("nationalities is required")
		}
		for _, n := range p.Nationalities {
			if !countryCode.MatchString(n) {
				return invalid("nationalities must be ISO 3166-1 alpha-2 codes")
			}
		}
		seen[p.ID] = true
		persons = append(persons, Person{ID: p.ID, Identity: authorization.Identity{
			BSN: p.BSN, FamilyName: p.FamilyName, GivenName: p.GivenName, Birthdate: p.Birthdate,
			PlaceOfBirth: pob, Nationalities: p.Nationalities,
		}})
	}
	return New(persons...), nil
}
