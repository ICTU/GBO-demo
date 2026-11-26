package refresh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const (
	clientPeer = "99999999900000000300"
	otherPeer  = "99999999900000001000"
)

var testNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// fakeStore is the register's store with one map, keyed by token hash.
type fakeStore struct {
	mu        sync.Mutex
	consents  map[string]Consent
	forgotten []string
	failGet   error
}

func newFakeStore(token string, consent Consent) *fakeStore {
	return &fakeStore{consents: map[string]Consent{string(HashToken(token)): consent}}
}

func (s *fakeStore) ByRefreshToken(_ context.Context, hash []byte) (Consent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet != nil {
		return Consent{}, false, s.failGet
	}
	consent, ok := s.consents[string(hash)]
	return consent, ok, nil
}

func (s *fakeStore) Forget(_ context.Context, consentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, consentID)
	for hash, consent := range s.consents {
		if consent.ID == consentID {
			delete(s.consents, hash)
		}
	}
	return nil
}

// fakeSigner numbers its proofs, so two refreshes give two proofs.
type fakeSigner struct {
	mu     sync.Mutex
	signed []Consent
}

func (s *fakeSigner) Sign(_ context.Context, consent Consent) (Proof, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signed = append(s.signed, consent)
	return Proof{Token: fmt.Sprintf("proof-%d", len(s.signed)), ExpiresAt: testNow.Add(5 * time.Minute)}, nil
}

type fakeLogbook struct {
	mu       sync.Mutex
	recorded []Consent
	fail     error
}

func (l *fakeLogbook) RecordRefresh(_ context.Context, consent Consent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		return l.fail
	}
	l.recorded = append(l.recorded, consent)
	return nil
}

func activeConsent() Consent {
	return Consent{
		ID:                "c-1",
		Status:            StatusActive,
		SubjectRef:        "EP-3f9a1c77b2",
		DienstverlenerOIN: clientPeer,
		ClientPeer:        clientPeer,
		Scopes:            []string{"inkomen:read", "woz:read"},
		ScopeEntries:      []ScopeEntry{{Bronhouder: "MinBZK", ScopeID: "inkomen:read", ConsentedFields: []string{"verzamelinkomen"}}},
		ValidUntil:        testNow.Add(90 * 24 * time.Hour),
		EncryptedSubject:  []byte(`{"99999999900000000200":{"pseudonym":{"key_set_version":1,"value":"cHNldWRv"}}}`),
	}
}

type harness struct {
	store   *fakeStore
	signer  *fakeSigner
	logbook *fakeLogbook
	service Service
}

func newHarness(token string, consent Consent) *harness {
	h := &harness{store: newFakeStore(token, consent), signer: &fakeSigner{}, logbook: &fakeLogbook{}}
	h.service = Service{Store: h.store, Signer: h.signer, Logbook: h.logbook, Now: func() time.Time { return testNow }}
	return h
}

func TestARefreshSignsTheSameConsentAndValuesAgain(t *testing.T) {
	consent := activeConsent()
	h := newHarness("token", consent)

	result, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.ConsentID != consent.ID || result.Proof.Token != "proof-1" || len(result.Scopes) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !result.Proof.ExpiresAt.Equal(testNow.Add(5 * time.Minute)) {
		t.Fatalf("expires_at = %s, want the signer's", result.Proof.ExpiresAt)
	}
	if len(h.signer.signed) != 1 {
		t.Fatalf("signed %d proofs, want 1", len(h.signer.signed))
	}
	signed := h.signer.signed[0]
	if signed.ID != consent.ID || !bytes.Equal(signed.EncryptedSubject, consent.EncryptedSubject) ||
		len(signed.ScopeEntries) != 1 || signed.DienstverlenerOIN != consent.DienstverlenerOIN {
		t.Fatalf("signed %+v, want the kept consent and values", signed)
	}
	if len(h.logbook.recorded) != 1 || h.logbook.recorded[0].ClientPeer != clientPeer {
		t.Fatalf("recorded %+v, want one record for the client peer", h.logbook.recorded)
	}
}

// The token is not rotated: using it again is normal use.
func TestTheSameTokenWorksAgain(t *testing.T) {
	h := newHarness("token", activeConsent())
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer})
		if err != nil {
			t.Fatalf("refresh %d: %v", attempt, err)
		}
		if want := fmt.Sprintf("proof-%d", attempt); result.Proof.Token != want {
			t.Fatalf("refresh %d gave %q, want a new proof %q", attempt, result.Proof.Token, want)
		}
	}
}

// Two refreshes at once each get a proof; neither invalidates the other.
func TestConcurrentRefreshesEachGetAProof(t *testing.T) {
	h := newHarness("token", activeConsent())
	const parallel = 8
	var wait sync.WaitGroup
	errs := make(chan error, parallel)
	for range parallel {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent refresh: %v", err)
		}
	}
	if len(h.signer.signed) != parallel || len(h.logbook.recorded) != parallel {
		t.Fatalf("signed %d, recorded %d; want %d each", len(h.signer.signed), len(h.logbook.recorded), parallel)
	}
}

func TestRefusals(t *testing.T) {
	revoked := activeConsent()
	revoked.Status = "REVOKED"
	ended := activeConsent()
	ended.ValidUntil = testNow
	noValues := activeConsent()
	noValues.EncryptedSubject = nil

	tests := []struct {
		name    string
		consent Consent
		request Request
		want    error
	}{
		{"unknown token", activeConsent(), Request{RefreshToken: "other", ClientPeer: clientPeer}, ErrInvalidGrant},
		{"another client peer", activeConsent(), Request{RefreshToken: "token", ClientPeer: otherPeer}, ErrInvalidGrant},
		{"revoked consent", revoked, Request{RefreshToken: "token", ClientPeer: clientPeer}, ErrInvalidGrant},
		{"ended consent", ended, Request{RefreshToken: "token", ClientPeer: clientPeer}, ErrInvalidGrant},
		{"no values kept", noValues, Request{RefreshToken: "token", ClientPeer: clientPeer}, ErrInvalidGrant},
		{"no token", activeConsent(), Request{ClientPeer: clientPeer}, ErrInvalidRequest},
		{"no client peer", activeConsent(), Request{RefreshToken: "token"}, ErrInvalidRequest},
		{"narrower scope", activeConsent(), Request{RefreshToken: "token", ClientPeer: clientPeer, Scopes: []string{"woz:read"}}, ErrInvalidScope},
		{"wider scope", activeConsent(), Request{RefreshToken: "token", ClientPeer: clientPeer, Scopes: []string{"woz:read", "inkomen:read", "brp:read"}}, ErrInvalidScope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness("token", tt.consent)
			_, err := h.service.Refresh(context.Background(), tt.request)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if len(h.signer.signed) != 0 || len(h.logbook.recorded) != 0 {
				t.Fatalf("a refusal signed %d proofs and wrote %d records", len(h.signer.signed), len(h.logbook.recorded))
			}
		})
	}
}

// The whole consent, in any order, is not a narrower scope.
func TestTheWholeScopeInAnyOrderIsAccepted(t *testing.T) {
	h := newHarness("token", activeConsent())
	if _, err := h.service.Refresh(context.Background(), Request{
		RefreshToken: "token", ClientPeer: clientPeer, Scopes: []string{"woz:read", "inkomen:read"},
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

// After valid_until the values and the token are of no use: the first
// refused refresh wipes them. Another peer cannot make that happen.
func TestARefreshAfterTheEndForgetsTheValues(t *testing.T) {
	ended := activeConsent()
	ended.ValidUntil = testNow.Add(-time.Second)
	h := newHarness("token", ended)

	if _, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: otherPeer}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("other peer: err = %v", err)
	}
	if len(h.store.forgotten) != 0 {
		t.Fatalf("another peer's refresh wiped %v", h.store.forgotten)
	}
	if _, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("err = %v, want invalid_grant", err)
	}
	if len(h.store.forgotten) != 1 || h.store.forgotten[0] != ended.ID {
		t.Fatalf("forgotten = %v, want the ended consent", h.store.forgotten)
	}
	// And the token is unknown from then on.
	if _, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("after forgetting: err = %v", err)
	}
}

// Fail closed: a proof the logbook did not record is not handed out.
func TestNoProofWithoutARecord(t *testing.T) {
	h := newHarness("token", activeConsent())
	h.logbook.fail = errors.New("outbox unavailable")
	result, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer})
	if err == nil || result.Proof.Token != "" {
		t.Fatalf("refresh without a record gave %+v, %v", result, err)
	}
	for _, refusal := range []error{ErrInvalidGrant, ErrInvalidRequest, ErrInvalidScope} {
		if errors.Is(err, refusal) {
			t.Fatalf("a logbook failure is the register's own failure, not %v", refusal)
		}
	}
}

func TestAStoreFailureIsNoRefusal(t *testing.T) {
	h := newHarness("token", activeConsent())
	h.store.failGet = errors.New("database down")
	_, err := h.service.Refresh(context.Background(), Request{RefreshToken: "token", ClientPeer: clientPeer})
	if err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("err = %v, want a failure that is not invalid_grant", err)
	}
}

func TestNewTokenIsOpaqueAndHashed(t *testing.T) {
	first, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || first == second {
		t.Fatalf("tokens %q and %q: want two different 32-byte base64url values", first, second)
	}
	if !bytes.Equal(hash, HashToken(first)) || len(hash) != 32 {
		t.Fatalf("hash is not the SHA-256 of the token")
	}
}
