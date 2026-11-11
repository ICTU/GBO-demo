// Package testlogin is the demo test-person login behind the issuer's Login
// port. Choosing a fixture is not authentication and is never used for a
// source provider's real login.
package testlogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

type Person struct {
	BSN  string `json:"bsn"`
	Name string `json:"name"`
}
type Persons interface {
	Load(context.Context) ([]Person, error)
}
type Pending interface {
	PutPending(context.Context, issuance.PendingAuthorization) error
	Pending(context.Context, string, string, time.Time) (issuance.PendingAuthorization, error)
	TakePending(context.Context, string, string, time.Time) (issuance.PendingAuthorization, error)
}

var ErrUnknownPerson = errors.New("unknown test person")

const selection = "test-person-selection"
const exchange = "test-person-code"

type request struct{ State, Challenge, BSN string }
type Service struct {
	provider, resource string
	persons            Persons
	pending            Pending
	now                func() time.Time
}

func New(provider, resource string, persons Persons, pending Pending, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{provider: provider, resource: resource, persons: persons, pending: pending, now: now}
}
func (s *Service) Path() string { u, _ := url.Parse(s.resource); return u.Path + "/test-persons" }
func (s *Service) AuthorizationURL(ctx context.Context, provider, resource string, _ []string, state, challenge string) (string, error) {
	if provider != s.provider || resource != s.resource || state == "" || len(challenge) != 43 {
		return "", issuance.ErrInvalidAuthorization
	}
	persons, err := s.persons.Load(ctx)
	if err != nil || len(persons) == 0 {
		return "", issuance.ErrLoginUnavailable
	}
	ticket, err := secret()
	if err != nil {
		return "", err
	}
	if err = s.put(ctx, selection, ticket, request{State: state, Challenge: challenge}, s.now().Add(issuance.LoginLifetime)); err != nil {
		return "", err
	}
	return s.resource + "/test-persons?" + url.Values{"ticket": {ticket}}.Encode(), nil
}
func (s *Service) Choices(ctx context.Context, ticket string) ([]Person, error) {
	if _, _, err := s.get(ctx, selection, ticket, false); err != nil {
		return nil, err
	}
	people, err := s.persons.Load(ctx)
	if err != nil || len(people) == 0 {
		return nil, issuance.ErrLoginUnavailable
	}
	return people, nil
}
func (s *Service) Choose(ctx context.Context, ticket, bsn string) (string, error) {
	people, err := s.Choices(ctx, ticket)
	if err != nil {
		return "", err
	}
	found := false
	for _, p := range people {
		if p.BSN == bsn {
			found = true
			break
		}
	}
	if !found {
		return "", ErrUnknownPerson
	}
	req, expiry, err := s.get(ctx, selection, ticket, true)
	if err != nil {
		return "", err
	}
	code, err := secret()
	if err != nil {
		return "", err
	}
	req.BSN = bsn
	until := s.now().Add(issuance.CodeLifetime)
	if expiry.Before(until) {
		until = expiry
	}
	if err = s.put(ctx, exchange, code, req, until); err != nil {
		return "", err
	}
	return s.resource + "/callback?" + url.Values{"state": {req.State}, "code": {code}}.Encode(), nil
}
func (s *Service) Exchange(ctx context.Context, provider, resource, code, verifier string) (string, error) {
	if provider != s.provider || resource != s.resource {
		return "", issuance.ErrInvalidAuthorization
	}
	req, _, err := s.get(ctx, exchange, code, true)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	if len(verifier) < 43 || len(verifier) > 128 || base64.RawURLEncoding.EncodeToString(sum[:]) != req.Challenge {
		return "", issuance.ErrInvalidAuthorization
	}
	return req.BSN, nil
}
func (s *Service) put(ctx context.Context, kind, secret string, req request, until time.Time) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return s.pending.PutPending(ctx, issuance.PendingAuthorization{Kind: kind, ID: hash(s.resource + "\x00" + secret), Payload: raw, ExpiresAt: until})
}
func (s *Service) get(ctx context.Context, kind, secret string, take bool) (request, time.Time, error) {
	if secret == "" {
		return request{}, time.Time{}, issuance.ErrInvalidAuthorization
	}
	lookup := s.pending.Pending
	if take {
		lookup = s.pending.TakePending
	}
	p, err := lookup(ctx, kind, hash(s.resource+"\x00"+secret), s.now())
	if errors.Is(err, issuance.ErrNotFound) {
		return request{}, time.Time{}, issuance.ErrInvalidAuthorization
	}
	if err != nil {
		return request{}, time.Time{}, err
	}
	var req request
	err = json.Unmarshal(p.Payload, &req)
	return req, p.ExpiresAt, err
}
func secret() (string, error) {
	var raw [32]byte
	_, err := rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), err
}
func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
