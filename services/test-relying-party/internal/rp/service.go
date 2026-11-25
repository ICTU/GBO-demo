// Package rp is a test relying party for the demo: OpenID4VP requests on
// behalf of a few registered relying parties, for an attestation type and
// claims chosen in the developer portal. It records which copy of an
// attestation a wallet presented to whom, by the fingerprint of the holder
// key, and keeps no attribute values.
package rp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// Kinds of events.
const (
	// EventPresented: a wallet presented a credential that verified; its
	// status says whether it is valid, revoked or expired.
	EventPresented = "presented"
	// EventDeclined: the wallet answered with an error, for example
	// because the user declined.
	EventDeclined = "declined"
	// EventRejected: the presentation did not verify.
	EventRejected = "rejected"
)

// Defaults.
const (
	// RequestTTL is how long a request can be answered.
	RequestTTL = 5 * time.Minute
	// Retention is how long the timeline keeps an event.
	Retention = 50 * time.Minute
)

// queryID is the id of the one credential query of a request.
const queryID = "attestation"

// Errors.
var (
	ErrUnknownParty      = errors.New("unknown relying party")
	ErrUnknownCredential = errors.New("unknown attestation type")
	ErrInvalidClaims     = errors.New("the claims must be one or more registered claims of the attestation type, each once")
	ErrUnknownSession    = errors.New("the request is unknown, answered or expired")
	ErrRejected          = errors.New("the presentation was rejected")
)

// Event is what happened to one request. It holds no attribute values,
// no tokens and no full keys: the holder key is named by the first 16
// characters of its JWK thumbprint (RFC 7638), as in the issuer's timeline.
type Event struct {
	At               time.Time `json:"at"`
	Kind             string    `json:"kind"`
	Session          string    `json:"session"`
	RelyingParty     string    `json:"relying_party"`
	RelyingPartyName string    `json:"relying_party_name"`
	Configuration    string    `json:"configuration"`
	VCT              string    `json:"vct"`
	// Claims are the paths asked for, dotted.
	Claims           []string `json:"claims"`
	KeyFingerprint   string   `json:"key_fingerprint,omitempty"`
	Status           string   `json:"status,omitempty"`
	Error            string   `json:"error,omitempty"`
	ErrorDescription string   `json:"error_description,omitempty"`
}

// Service keeps the open requests and the events.
type Service struct {
	cfg      Config
	parties  map[string]Party
	creds    map[string]Credential
	verifier oid4vp.Verifier
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	events   []Event
}

type session struct {
	id      string
	party   Party
	cred    Credential
	claims  [][]string
	oid     oid4vp.Session
	expires time.Time
}

// New returns the service. fetch reads the status lists of presented
// credentials.
func New(cfg Config, fetch oid4vp.StatusFetcher, now func() time.Time) *Service {
	s := &Service{cfg: cfg, parties: map[string]Party{}, creds: map[string]Credential{}, now: now,
		verifier: oid4vp.Verifier{Anchors: cfg.Anchors, FetchStatus: fetch, ReportStatus: true},
		sessions: map[string]*session{}}
	for _, p := range cfg.Parties {
		s.parties[p.ID] = p
	}
	for _, c := range cfg.Credentials {
		s.creds[c.ID] = c
	}
	return s
}

// PartyView and Catalog are what the portal shows in its form.
type PartyView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ClientID string `json:"client_id"`
}

type Catalog struct {
	RelyingParties []PartyView  `json:"relying_parties"`
	Credentials    []Credential `json:"credentials"`
}

// Catalog returns the relying parties and the attestation types and
// claims they are registered for.
func (s *Service) Catalog() Catalog {
	c := Catalog{RelyingParties: []PartyView{}, Credentials: append([]Credential{}, s.cfg.Credentials...)}
	for _, p := range s.cfg.Parties {
		c.RelyingParties = append(c.RelyingParties, PartyView{ID: p.ID, Name: p.Name, ClientID: p.Signer.ClientID()})
	}
	return c
}

// PresentationRequest is what the portal asks for.
type PresentationRequest struct {
	RelyingParty string     `json:"relying_party"`
	Credential   string     `json:"credential"`
	Claims       [][]string `json:"claims"`
}

// Created is a request a wallet can answer.
type Created struct {
	// Link is the openid4vp:// link for a QR code or the same device.
	Link       string    `json:"link"`
	RequestURI string    `json:"request_uri"`
	Session    string    `json:"session"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Create starts a request.
func (s *Service) Create(req PresentationRequest) (Created, error) {
	party, ok := s.parties[req.RelyingParty]
	if !ok {
		return Created{}, ErrUnknownParty
	}
	cred, ok := s.creds[req.Credential]
	if !ok {
		return Created{}, ErrUnknownCredential
	}
	if len(req.Claims) == 0 {
		return Created{}, ErrInvalidClaims
	}
	var asked [][]string
	for _, p := range req.Claims {
		if !containsPath(cred.Claims, p) || containsPath(asked, p) {
			return Created{}, ErrInvalidClaims
		}
		asked = append(asked, p)
	}
	key, err := oid4vp.NewResponseKey()
	if err != nil {
		return Created{}, err
	}
	var id, nonce, state string
	for _, v := range []*string{&id, &nonce, &state} {
		if *v, err = randomToken(); err != nil {
			return Created{}, err
		}
	}
	claims := make([]oid4vp.ClaimQuery, len(asked))
	for i, p := range asked {
		claims[i] = oid4vp.ClaimQuery{Path: p}
	}
	now := s.now()
	ses := &session{id: id, party: party, cred: cred, claims: asked, expires: now.Add(RequestTTL),
		oid: oid4vp.Session{ClientID: party.Signer.ClientID(), Nonce: nonce, State: state, Key: key,
			Query: oid4vp.Query{Credentials: []oid4vp.CredentialQuery{{
				ID: queryID, Format: sdjwtvc.Type, Meta: oid4vp.Meta{VCTValues: []string{cred.VCT}}, Claims: claims,
			}}}}}
	s.mu.Lock()
	for k, v := range s.sessions {
		if !now.Before(v.expires) {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = ses
	s.mu.Unlock()
	requestURI := s.cfg.PublicURL + "/request/" + id
	link := "openid4vp://?client_id=" + url.QueryEscape(ses.oid.ClientID) + "&request_uri=" + url.QueryEscape(requestURI)
	return Created{Link: link, RequestURI: requestURI, Session: label(id), ExpiresAt: ses.expires}, nil
}

// RequestObject returns the signed request object of an open request.
func (s *Service) RequestObject(id string) (string, error) {
	ses, ok := s.open(id, false)
	if !ok {
		return "", ErrUnknownSession
	}
	return ses.party.Signer.Sign(oid4vp.Request{
		Session:                 ses.oid,
		ResponseURI:             s.cfg.PublicURL + "/response/" + id,
		RegistrationCertificate: ses.party.Registration,
		Encoding:                oid4vp.RegistrationJWT,
		IssuedAt:                s.now(),
		Expires:                 ses.expires,
	})
}

// Response is what a wallet posts to the response_uri (direct_post.jwt):
// the encrypted response, or an error.
type Response struct {
	Response         string
	Error            string
	ErrorDescription string
}

// Respond ends a request with the wallet's answer and records it. A
// presentation that does not verify is ErrRejected; an unknown, answered
// or expired request is ErrUnknownSession and records nothing.
func (s *Service) Respond(ctx context.Context, id string, r Response) error {
	ses, ok := s.open(id, true)
	if !ok {
		return ErrUnknownSession
	}
	e := Event{Kind: EventPresented, Session: label(id), RelyingParty: ses.party.ID, RelyingPartyName: ses.party.Name,
		Configuration: ses.cred.ID, VCT: ses.cred.VCT}
	for _, p := range ses.claims {
		e.Claims = append(e.Claims, strings.Join(p, "."))
	}
	if r.Error != "" {
		e.Kind, e.Error, e.ErrorDescription = EventDeclined, r.Error, r.ErrorDescription
		s.record(e)
		return nil
	}
	d, err := s.verifier.VerifyResponse(ctx, r.Response, ses.oid, s.now())
	if err != nil {
		e.Kind, e.Error, e.ErrorDescription = EventRejected, "invalid_presentation", err.Error()
		s.record(e)
		return ErrRejected
	}
	// The disclosed values are dropped here: only the status and the
	// fingerprint of the holder key are kept.
	e.Status, e.KeyFingerprint = d.Status, holderFingerprint(d.Claims)
	s.record(e)
	return nil
}

// Events returns the events of the last Retention, oldest first.
func (s *Service) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	return append([]Event{}, s.events...)
}

func (s *Service) record(e Event) {
	e.At = s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	s.purge()
}

func (s *Service) purge() {
	cutoff := s.now().Add(-Retention)
	kept := s.events[:0]
	for _, e := range s.events {
		if !e.At.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	s.events = kept
}

// open returns an open request; take removes it, so it is answered once.
func (s *Service) open(id string, take bool) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	if !s.now().Before(ses.expires) {
		delete(s.sessions, id)
		return nil, false
	}
	if take {
		delete(s.sessions, id)
	}
	return ses, true
}

// holderFingerprint is the shortened JWK thumbprint of cnf.jwk.
func holderFingerprint(claims map[string]any) string {
	cnf, _ := claims["cnf"].(map[string]any)
	raw, err := json.Marshal(cnf["jwk"])
	if err != nil {
		return ""
	}
	var k jose.JSONWebKey
	if err := k.UnmarshalJSON(raw); err != nil {
		return ""
	}
	tp, err := k.Thumbprint(crypto.SHA256)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(tp)[:16]
}

func containsPath(paths [][]string, p []string) bool {
	return slices.ContainsFunc(paths, func(q []string) bool { return slices.Equal(q, p) })
}

// label names a request in events without giving away its id.
func label(id string) string {
	sum := sha256.Sum256([]byte("request label " + id))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:12]
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
