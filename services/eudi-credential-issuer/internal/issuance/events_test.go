package issuance_test

// The events of a wallet session that the core hands to its Events port,
// for the timeline in the developer portal. Cases C1 to C3 and C5 of the
// batch-size slice of the credential lifecycle (#745); C4, how long they
// are kept, belongs to the store.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// recordedEvents is an Events port that keeps what it is handed.
type recordedEvents struct {
	mu     sync.Mutex
	events []issuance.Event
}

func (r *recordedEvents) Record(_ context.Context, e issuance.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordedEvents) all() []issuance.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]issuance.Event(nil), r.events...)
}

// ofKind returns the recorded events of one kind, in order.
func (r *recordedEvents) ofKind(kind string) []issuance.Event {
	var out []issuance.Event
	for _, e := range r.all() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func eventsEnv(t *testing.T) (env, *fakeSettings, *recordedEvents) {
	t.Helper()
	cfg, provider := validConfig(t)
	settings := newFakeSettings()
	events := &recordedEvents{}
	cfg.Settings, cfg.Events = settings, events
	store := &countingStore{Store: memstore.New()}
	src := &fakeSource{}
	svc, err := issuance.New(cfg, src, store, store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return env{svc: svc, store: store, source: src, provider: provider}, settings, events
}

// fingerprint is the shortened JWK thumbprint (RFC 7638) of a holder key.
func fingerprint(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	jwk := jose.JSONWebKey{Key: &key.PublicKey}
	tp, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(tp)[:16]
}

// attestedProof returns an attestation proof for keys with a c_nonce of
// target.
func attestedProof(t *testing.T, e env, target issuance.Target, keys []*ecdsa.PrivateKey) *oid4vci.Proofs {
	t.Helper()
	nonce, err := e.svc.Nonce(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	return &oid4vci.Proofs{Attestation: []string{e.provider.KeyAttestation(t, keys, nonce, time.Now().Add(time.Hour))}}
}

func newKeys(t *testing.T, n int) []*ecdsa.PrivateKey {
	t.Helper()
	keys := make([]*ecdsa.PrivateKey, n)
	for i := range keys {
		keys[i] = testpki.NewKey(t)
	}
	return keys
}

func notify(t *testing.T, e env, target issuance.Target, tok oid4vci.TokenResponse, dpopKey *ecdsa.PrivateKey, id, event string) {
	t.Helper()
	err := e.svc.Notify(context.Background(), issuance.NotificationCall{
		Target:        target,
		Authorization: "DPoP " + tok.AccessToken,
		DPoP:          []string{testwallet.DPoP(t, dpopKey, "POST", e.svc.Identifier(target)+"/notification", tok.AccessToken)},
		Notification:  oid4vci.Notification{NotificationID: id, Event: event},
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
}

// eudiSession starts a session for income on the eudi profile with a
// once_only policy of batch 3, and redeems its code. It returns the session
// label of the session_started event.
func eudiSession(t *testing.T) (env, *recordedEvents, string, oid4vci.TokenResponse, *ecdsa.PrivateKey) {
	t.Helper()
	e, settings, events := eventsEnv(t)
	settings.setPolicy("income", onceOnly(3, 1))
	_, c := code(t, e, "eudi", "ref-1", "income")
	started := events.ofKind(issuance.EventSessionStarted)
	if len(started) != 1 {
		t.Fatalf("session_started events = %+v", started)
	}
	tok, dpopKey, err := token(t, e, "eudi", c)
	if err != nil {
		t.Fatal(err)
	}
	return e, events, started[0].Session, tok, dpopKey
}

// C1: the session starts with its configurations and flow, and the token
// names the grant and the wallet provider.
func TestSessionStartedAndTokenEvents(t *testing.T) {
	_, events, session, _, _ := eudiSession(t)
	s := events.ofKind(issuance.EventSessionStarted)[0]
	if session == "" || s.Provider != "tax" || s.Profile != "eudi" ||
		strings.Join(s.Configurations, ",") != "income" || s.Flow != issuance.FlowPreAuthorizedCode {
		t.Errorf("session_started = %+v", s)
	}
	tokens := events.ofKind(issuance.EventTokenIssued)
	if len(tokens) != 1 {
		t.Fatalf("token_issued events = %+v", tokens)
	}
	if tk := tokens[0]; tk.Session != session || tk.GrantType != oid4vci.GrantPreAuthorizedCode ||
		tk.WalletProvider != "provider" || tk.PreviousSession != "" {
		t.Errorf("token_issued = %+v, want the pre-authorized code grant in session %s", tk, session)
	}
}

// C1: a refused request names the keys offered and the batch size in force.
func TestCredentialRefusedEvent(t *testing.T) {
	e, events, session, tok, dpopKey := eudiSession(t)
	target := tax("eudi")
	_, err := credentialAt(t, e, target, tok, dpopKey, attestedProof(t, e, target, newKeys(t, 4)))
	refusedWith(t, err, oid4vci.ErrInvalidProof)
	refused := events.ofKind(issuance.EventCredentialRefused)
	if len(refused) != 1 {
		t.Fatalf("credential_refused events = %+v", refused)
	}
	r := refused[0]
	if r.Session != session || r.Error != oid4vci.ErrInvalidProof || r.ErrorDescription == "" || r.Credentials != 0 {
		t.Errorf("credential_refused = %+v", r)
	}
	if r.KeysOffered != 4 || r.BatchSize != 3 || r.BatchSizeFrom != issuance.BatchSizeFromReusePolicy ||
		r.Configuration != "income" || r.ProofType != issuance.ProofTypeAttestation {
		t.Errorf("credential_refused details = %+v", r)
	}
}

// C1: an issuance names a fingerprint per copy, and the wallet's
// notification lands in the same session.
func TestCredentialIssuedAndNotificationEvents(t *testing.T) {
	e, events, session, tok, dpopKey := eudiSession(t)
	target := tax("eudi")
	keys := newKeys(t, 2)
	resp, err := credentialAt(t, e, target, tok, dpopKey, attestedProof(t, e, target, keys))
	if err != nil {
		t.Fatal(err)
	}
	issued := events.ofKind(issuance.EventCredentialIssued)
	if len(issued) != 1 {
		t.Fatalf("credential_issued events = %+v", issued)
	}
	i := issued[0]
	want := []string{fingerprint(t, keys[0]), fingerprint(t, keys[1])}
	if i.Session != session || i.Configuration != "income" || i.ProofType != issuance.ProofTypeAttestation {
		t.Errorf("credential_issued = %+v", i)
	}
	if i.KeysOffered != 2 || i.Credentials != 2 || i.BatchSize != 3 || i.BatchSizeFrom != issuance.BatchSizeFromReusePolicy ||
		strings.Join(i.KeyFingerprints, ",") != strings.Join(want, ",") {
		t.Errorf("credential_issued = %+v, want fingerprints %v", i, want)
	}
	notify(t, e, target, tok, dpopKey, resp.NotificationID, oid4vci.EventCredentialAccepted)
	notes := events.ofKind(issuance.EventNotificationReceived)
	if len(notes) != 1 || notes[0].Session != session || notes[0].Notification != oid4vci.EventCredentialAccepted {
		t.Errorf("notification_received = %+v", notes)
	}
}

// C3: a refresh starts a session of its own, linked to the one it
// continues, and its issuance lands in the new session.
func TestRefreshEventsContinueTheSession(t *testing.T) {
	e, events, session, tok, dpopKey := eudiSession(t)
	target := tax("eudi")
	again := refresh(t, e, target, tok.RefreshToken, dpopKey)
	tokens := events.ofKind(issuance.EventTokenIssued)
	if len(tokens) != 2 {
		t.Fatalf("token_issued events = %+v", tokens)
	}
	next := tokens[1]
	if next.GrantType != oid4vci.GrantRefreshToken || next.PreviousSession != session || next.Session == "" || next.Session == session {
		t.Errorf("refresh token_issued = %+v, want a new session after %s", next, session)
	}
	if _, err := credentialAt(t, e, target, again, dpopKey, attestedProof(t, e, target, newKeys(t, 3))); err != nil {
		t.Fatal(err)
	}
	issued := events.ofKind(issuance.EventCredentialIssued)
	if len(issued) != 1 || issued[0].Session != next.Session || issued[0].Credentials != 3 {
		t.Errorf("re-issuance credential_issued = %+v, want 3 in session %s", issued, next.Session)
	}
}

// The batch size from the provider, for a configuration without a policy,
// and jwt proofs on the nl profile.
func TestCredentialEventWithTheProvidersBatchSize(t *testing.T) {
	e, settings, events := eventsEnv(t)
	settings.setBatch("tax", 5)
	_, c := code(t, e, "nl", "", "income")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentialAt(t, e, tax("nl"), tok, dpopKey, jwtProofs(t, e, tax("nl"), 2)); err != nil {
		t.Fatal(err)
	}
	issued := events.ofKind(issuance.EventCredentialIssued)
	if len(issued) != 1 || issued[0].BatchSize != 5 || issued[0].BatchSizeFrom != issuance.BatchSizeFromProvider ||
		issued[0].ProofType != issuance.ProofTypeJWT || issued[0].Credentials != 2 || len(issued[0].KeyFingerprints) != 2 {
		t.Errorf("credential_issued = %+v", issued)
	}
}

// C2: no attribute values, no BSN, no codes, tokens, session ids or holder
// keys in any event.
func TestEventsHoldNoSecretsOrPersonalData(t *testing.T) {
	e, _, events := eventsEnv(t)
	target := tax("eudi")
	_, c := code(t, e, "eudi", "", "income")
	tok, dpopKey, err := token(t, e, "eudi", c)
	if err != nil {
		t.Fatal(err)
	}
	keys := newKeys(t, 1)
	resp, err := credentialAt(t, e, target, tok, dpopKey, attestedProof(t, e, target, keys))
	if err != nil {
		t.Fatal(err)
	}
	notify(t, e, target, tok, dpopKey, resp.NotificationID, oid4vci.EventCredentialAccepted)
	refresh(t, e, target, tok.RefreshToken, dpopKey)

	raw, err := json.Marshal(events.all())
	if err != nil {
		t.Fatal(err)
	}
	all := string(raw)
	secrets := map[string]string{
		"an attribute value":         "Utrecht",
		"the pre-authorized code":    c,
		"the access token":           tok.AccessToken,
		"the refresh token":          tok.RefreshToken,
		"the notification id":        resp.NotificationID,
		"the credential":             resp.Credentials[0].Credential,
		"the credential identifier":  tok.AuthorizationDetails[0].CredentialIdentifiers[0],
		"the holder key x":           holderKeyValues(t, keys[0])[0],
		"the full holder thumbprint": fullThumbprint(t, keys[0]),
	}
	for name, secret := range secrets {
		if secret != "" && strings.Contains(all, secret) {
			t.Errorf("C2: the events contain %s", name)
		}
	}
	for _, ev := range events.all() {
		if ev.Session == "" || len(ev.Session) > 16 {
			t.Errorf("C2: session label %q, want a short label, not the session id", ev.Session)
		}
	}
}

func fullThumbprint(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	jwk := jose.JSONWebKey{Key: &key.PublicKey}
	tp, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(tp)
}

// C5: without an Events port the issuer issues as before and records
// nothing; the rest of the suite runs that way.
func TestNoEventsPort(t *testing.T) {
	e := newEnv(t)
	_, c := code(t, e, "eudi", "", "income")
	tok, dpopKey, err := token(t, e, "eudi", c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentialAt(t, e, tax("eudi"), tok, dpopKey, attestedProof(t, e, tax("eudi"), newKeys(t, 1))); err != nil {
		t.Fatal(err)
	}
}

// A deferred issuance ends in the timeline too: still waiting is
// credential_deferred again, the credential from the deferred request is
// credential_issued with the holder key's fingerprint, in the same
// session, and a request after it is refused.
func TestDeferredIssuanceEvents(t *testing.T) {
	events := &recordedEvents{}
	f := startDeferred(t, newRecordingStore(), events)
	deferred := events.ofKind(issuance.EventCredentialDeferred)
	if len(deferred) != 1 || deferred[0].Session == "" {
		t.Fatalf("credential_deferred events = %+v", deferred)
	}
	session := deferred[0].Session

	if res, err := f.deferredRequest(t); err != nil || !res.Deferred {
		t.Fatalf("still waiting: %+v, %v", res, err)
	}
	if n := len(events.ofKind(issuance.EventCredentialDeferred)); n != 2 {
		t.Errorf("credential_deferred events = %d, want 2", n)
	}

	f.src.mu.Lock()
	f.src.available = true
	f.src.mu.Unlock()
	if _, err := f.deferredRequest(t); err != nil {
		t.Fatal(err)
	}
	issued := events.ofKind(issuance.EventCredentialIssued)
	if len(issued) != 1 {
		t.Fatalf("credential_issued events = %+v", issued)
	}
	if i := issued[0]; i.Session != session || i.Credentials != 1 || i.Configuration == "" ||
		strings.Join(i.KeyFingerprints, ",") != fingerprint(t, f.holder) {
		t.Errorf("credential_issued = %+v, want one copy of %s in session %s", i, fingerprint(t, f.holder), session)
	}

	_, err := f.deferredRequest(t)
	refusedWith(t, err, oid4vci.ErrInvalidTransactionID)
	if r := events.ofKind(issuance.EventCredentialRefused); len(r) != 1 || r[0].Session != session || r[0].Error != oid4vci.ErrInvalidTransactionID {
		t.Errorf("credential_refused = %+v", r)
	}
}
