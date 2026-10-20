// Package issuance binds an accepted ASI result to the citizen who was
// logged in at the reference QTSP and exchanges it for a test attestation
// when that citizen discloses the same PID attributes to the issuance server.
package issuance

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"gbo-demo/qtsp-reference/internal/login"
)

const (
	Usecase         = "qtsp-attestation"
	AttestationType = "https://gbo.ictu.nl/credentials/qtsp-attestation/v1"
	Qualification   = "TEST — not a qualified electronic attestation of attributes"
	IssuerName      = "GBO demo test QTSP"
	Lifetime        = 10 * time.Minute
)

// Evidence is one attribute result accepted from the ASIP. Verify evidence
// has an Outcome; retrieve evidence has the Value returned by the source.
type Evidence struct {
	Name    string          `json:"name"`
	Outcome string          `json:"outcome,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

// Request is an accepted ASI result waiting for the same citizen's wallet.
type Request struct {
	Identity   login.Identity
	Operation  string
	Source     string
	VerifiedAt time.Time
	Evidence   []Evidence
	// ExpectedAttributes is the number of attributes in the ASI request.
	ExpectedAttributes int
}

type pending struct {
	Request
	id      uint64
	expires time.Time
	timer   *time.Timer
}

// Document is the response shape understood by nl-wallet's issuance server.
type Document struct {
	AttestationType string         `json:"attestation_type"`
	Attributes      map[string]any `json:"attributes"`
	ID              string         `json:"id"`
}

// Store keeps only short-lived, not-yet-issued results in memory.
type Store struct {
	mu      sync.Mutex
	pending []pending
	nextID  uint64
	now     func() time.Time
}

func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now}
}

// Add records only a complete successful result. It deliberately returns a
// boolean so the result page shows a wallet action only when issuance exists.
func (s *Store) Add(request Request) bool {
	if !issuable(request) {
		return false
	}
	now := s.now().UTC()
	if request.VerifiedAt.IsZero() {
		request.VerifiedAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	// A static issuance link cannot distinguish two pending results for the
	// same identity. Keep the most recent successful request so a quick second
	// demo scenario does not make both requests unusable.
	s.pending = slices.DeleteFunc(s.pending, func(candidate pending) bool {
		replaced := sameIdentity(candidate.Identity, request.Identity)
		if replaced {
			candidate.timer.Stop()
		}
		return replaced
	})
	s.nextID++
	candidate := pending{Request: request, id: s.nextID, expires: now.Add(Lifetime)}
	id := candidate.id
	candidate.timer = time.AfterFunc(Lifetime, func() { s.remove(id) })
	s.pending = append(s.pending, candidate)
	return true
}

func issuable(request Request) bool {
	if strings.TrimSpace(request.Source) == "" || request.ExpectedAttributes <= 0 || len(request.Evidence) != request.ExpectedAttributes {
		return false
	}
	switch request.Operation {
	case "verify":
		for _, evidence := range request.Evidence {
			if evidence.Name == "" || (evidence.Outcome != "Match" && evidence.Outcome != "MatchWithVariation") || !nonNullJSON(evidence.Value) {
				return false
			}
		}
	case "retrieve":
		for _, evidence := range request.Evidence {
			if evidence.Name == "" || !nonNullJSON(evidence.Value) {
				return false
			}
		}
	default:
		return false
	}
	return completeIdentity(request.Identity)
}

func nonNullJSON(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var value any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func completeIdentity(identity login.Identity) bool {
	return identity.FamilyName != "" && identity.GivenName != "" && identity.Birthdate != "" && len(identity.Nationalities) > 0
}

// Take atomically exchanges the only unambiguous, unexpired result belonging
// to identity for an issuable document. A result can therefore be issued once.
func (s *Store) Take(identity login.Identity) *Document {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	var matches []int
	for index, candidate := range s.pending {
		if sameIdentity(candidate.Identity, identity) {
			matches = append(matches, index)
		}
	}
	// Defensive refusal if state was populated outside Add's replacement rule.
	if len(matches) != 1 {
		return nil
	}
	index := matches[0]
	candidate := s.pending[index]
	candidate.timer.Stop()
	s.pending = slices.Delete(s.pending, index, index+1)
	return document(candidate.Request)
}

func (s *Store) prune(now time.Time) {
	s.pending = slices.DeleteFunc(s.pending, func(candidate pending) bool {
		expired := !now.Before(candidate.expires)
		if expired {
			candidate.timer.Stop()
		}
		return expired
	})
}

func (s *Store) remove(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = slices.DeleteFunc(s.pending, func(candidate pending) bool { return candidate.id == id })
}

func sameIdentity(left, right login.Identity) bool {
	if left.FamilyName != right.FamilyName || left.GivenName != right.GivenName || left.Birthdate != right.Birthdate {
		return false
	}
	return slices.Equal(sortedUnique(left.Nationalities), sortedUnique(right.Nationalities))
}

func sortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	return slices.Compact(result)
}

func document(request Request) *Document {
	evidence := make([]string, 0, len(request.Evidence))
	for _, item := range request.Evidence {
		var compact bytes.Buffer
		_ = json.Compact(&compact, item.Value)
		if item.Outcome != "" {
			evidence = append(evidence, fmt.Sprintf("%s: %s — QTSP value %s", item.Name, item.Outcome, compact.String()))
		} else {
			evidence = append(evidence, fmt.Sprintf("%s: source value %s", item.Name, compact.String()))
		}
	}
	return &Document{
		AttestationType: AttestationType,
		ID:              randomID(),
		Attributes: map[string]any{
			"subject_family_name": request.Identity.FamilyName,
			"subject_given_name":  request.Identity.GivenName,
			"subject_birthdate":   request.Identity.Birthdate,
			"attribute_evidence":  strings.Join(evidence, "\n"),
			"source_legal_name":   request.Source,
			"issuer_name":         IssuerName,
			"verification_date":   request.VerifiedAt.UTC().Format(time.RFC3339),
			"qualification":       Qualification,
		},
	}
}

func randomID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
