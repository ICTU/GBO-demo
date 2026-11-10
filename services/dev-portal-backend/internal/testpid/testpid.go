// Package testpid lets a tester put a test PID in an EUDI wallet: it offers
// the BRP mock persons and asks the own credential issuer for a
// pre-authorized offer of the test PID for one of them.
//
// Demo shortcut: anyone who reaches the portal gets a test PID for any of
// these persons. The service is only wired when the deployment sets the
// issuer's offer URL, which the local demo does and a shared environment
// does not. A real deployment issues no PID on request at all.
package testpid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The test PID provider of the own credential issuer, on the profile the
// EUDI wallets use. The NL Wallet takes its PID from its own issuer.
const (
	Profile         = "eudi"
	ConfigurationID = "test-pid-pid"
	offerLinkKind   = "openid-credential-offer"
)

// Person is a test person a PID can be offered for.
type Person struct {
	BSN  string `json:"bsn"`
	Name string `json:"name"`
}

// ParsePersons reads the BRP mock persons, the same file the issuer's
// test-pid-source maps to PIDs, so the list cannot drift from what the
// issuer can issue.
func ParsePersons(data []byte) ([]Person, error) {
	var raw []struct {
		BSN           string `json:"bsn"`
		Voornamen     string `json:"voornamen"`
		Voorvoegsel   string `json:"voorvoegsel"`
		Geslachtsnaam string `json:"geslachtsnaam"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("persons: %w", err)
	}
	seen := map[string]bool{}
	persons := make([]Person, 0, len(raw))
	for i, p := range raw {
		if p.BSN == "" {
			return nil, fmt.Errorf("persons: entry %d has no bsn", i)
		}
		if seen[p.BSN] {
			return nil, fmt.Errorf("persons: bsn %s appears twice", p.BSN)
		}
		seen[p.BSN] = true
		name := strings.Join(strings.Fields(p.Voornamen+" "+p.Voorvoegsel+" "+p.Geslachtsnaam), " ")
		persons = append(persons, Person{BSN: p.BSN, Name: name})
	}
	return persons, nil
}

// OfferRequest is what the issuer needs for a pre-authorized offer.
type OfferRequest struct {
	Profile          string
	ConfigurationIDs []string
	BSN              string
}

// Issuer makes pre-authorized offers at the own credential issuer. It
// returns the offer's links by kind, ErrUnreachable when the issuer does
// not answer, and a *Refusal when it answers with an error.
type Issuer interface {
	Offer(ctx context.Context, req OfferRequest) (links map[string]string, err error)
}

// ErrUnreachable is the issuer not answering.
var ErrUnreachable = errors.New("issuer unreachable")

// Refusal is the issuer's error answer. Code is its error code, empty when
// the answer carried none.
type Refusal struct {
	Status int
	Code   string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("issuer refused the offer: %d %s", r.Status, r.Code)
}

// Error is why no offer was made. Its value is the code the portal shows a
// message for.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrUnknownPerson     Error = "unknown_person"
	ErrNotConfigured     Error = "test_pid_not_configured"
	ErrNoData            Error = "no_data"
	ErrSourceUnavailable Error = "source_unavailable"
	ErrBadSourceData     Error = "bad_source_data"
	ErrIssuerUnavailable Error = "issuer_unavailable"
	ErrIssuer            Error = "issuer_error"
)

// Service offers test PIDs for the test persons.
type Service struct {
	persons []Person
	known   map[string]bool
	issuer  Issuer
}

func New(persons []Person, issuer Issuer) *Service {
	known := make(map[string]bool, len(persons))
	for _, p := range persons {
		known[p.BSN] = true
	}
	return &Service{persons: persons, known: known, issuer: issuer}
}

// Persons are the test persons, in the order of the mock.
func (s *Service) Persons() []Person { return s.persons }

// Offer returns the link of a pre-authorized offer of the test PID for the
// person with this BSN. Only test persons get one: any other BSN is refused
// before the issuer is asked.
func (s *Service) Offer(ctx context.Context, bsn string) (string, error) {
	if !s.known[bsn] {
		return "", ErrUnknownPerson
	}
	links, err := s.issuer.Offer(ctx, OfferRequest{Profile: Profile, ConfigurationIDs: []string{ConfigurationID}, BSN: bsn})
	if err != nil {
		return "", reason(err)
	}
	link := links[offerLinkKind]
	if link == "" {
		return "", fmt.Errorf("%w: the offer has no %s link", ErrIssuer, offerLinkKind)
	}
	return link, nil
}

// reason translates the issuer's failure into why no offer was made, and
// keeps the issuer's own error for the log.
func reason(err error) error {
	if errors.Is(err, ErrUnreachable) {
		return fmt.Errorf("%w: %w", ErrIssuerUnavailable, err)
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		return fmt.Errorf("%w: %w", ErrIssuer, err)
	}
	var why Error
	switch refusal.Code {
	case "unknown_configuration", "unknown_profile":
		why = ErrNotConfigured
	case "no_data":
		why = ErrNoData
	case "source_unavailable":
		why = ErrSourceUnavailable
	case "bad_source_data":
		why = ErrBadSourceData
	default:
		why = ErrIssuer
	}
	return fmt.Errorf("%w: %w", why, err)
}
