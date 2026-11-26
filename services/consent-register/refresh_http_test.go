package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"consent-register/refresh"
	"consent-register/uses"

	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"

	"github.com/golang-jwt/jwt/v5"
)

// testClientPeer is the FSC peer that pushed the request and refreshes.
const (
	testClientPeer = "99999999900000000300"
	testOtherPeer  = "99999999900000001000"
)

// refreshRegister is a register with a proof lifetime, over a store the test
// can reach, and its issuer, so the test can check signatures.
type refreshRegister struct {
	url     string
	store   *Store
	issuer  *ConsentIssuer
	client  *registerLogbook
	logbook *ldvtest.Logbook
}

func newRefreshRegister(t *testing.T, store *Store, lifetime time.Duration) *refreshRegister {
	t.Helper()
	issuer, err := NewConsentIssuer(config{TokenIssuer: "test-issuer", TokenAudience: "test-audience", ProofLifetime: lifetime})
	if err != nil {
		t.Fatalf("consent issuer: %v", err)
	}
	logbook := ldvtest.New(t, append(allGBOActivities(), consentProofRefreshActivity)...)
	client := newRegisterLogbook(logbook.Client(t, "consent-register"))
	server := httptest.NewServer(newMux(store, issuer, client, uses.Service{}))
	t.Cleanup(server.Close)
	return &refreshRegister{url: server.URL, store: store, issuer: issuer, client: client, logbook: logbook}
}

type createdConsent struct {
	ConsentID    string    `json:"consent_id"`
	ConsentToken string    `json:"consent_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	RefreshToken string    `json:"refresh_token"`
}

// create gives a consent, for clientPeer when it is not empty, and returns
// the answer and its raw body.
func (r *refreshRegister) create(t *testing.T, clientPeer string, validitySeconds int) (createdConsent, []byte) {
	t.Helper()
	request := map[string]any{
		"encrypted_subject":  testEncryptedSubject,
		"subject_ref":        testSubjectRef,
		"dienstverlener_oin": testClientPeer,
		"scope_entries": []map[string]any{
			{"bronhouder": "MinBZK", "scope_id": "bd:ib:2025", "consented_fields": []string{"verzamelinkomen"}},
		},
		"use_case": "hypotheek",
	}
	if clientPeer != "" {
		request["client_peer"] = clientPeer
	}
	if validitySeconds > 0 {
		request["validity_seconds"] = validitySeconds
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(r.url+"/consents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", response.StatusCode, raw)
	}
	var created createdConsent
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created, raw
}

type refreshAnswer struct {
	status       int
	Error        string    `json:"error"`
	ConsentID    string    `json:"consent_id"`
	ConsentToken string    `json:"consent_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes"`
	raw          []byte
}

func (r *refreshRegister) refresh(t *testing.T, token, peer, scope string) refreshAnswer {
	t.Helper()
	body, err := json.Marshal(map[string]string{"refresh_token": token, "client_peer": peer, "scope": scope})
	if err != nil {
		t.Error(err)
		return refreshAnswer{}
	}
	response, err := http.Post(r.url+"/proofs/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Errorf("refresh: %v", err)
		return refreshAnswer{}
	}
	defer func() { _ = response.Body.Close() }()
	var answer refreshAnswer
	answer.raw, _ = io.ReadAll(response.Body)
	answer.status = response.StatusCode
	if err := json.Unmarshal(answer.raw, &answer); err != nil {
		t.Errorf("decode %s: %v", answer.raw, err)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	return answer
}

// verify checks a proof's signature against an issuer and returns its claims
// and kid.
func verify(t *testing.T, issuer *ConsentIssuer, token string) (*ConsentClaims, string) {
	t.Helper()
	parsed, err := jwt.ParseWithClaims(token, &ConsentClaims{}, func(*jwt.Token) (any, error) {
		return &issuer.key.PublicKey, nil
	}, jwt.WithAudience("test-audience"), jwt.WithIssuer("test-issuer"))
	if err != nil || !parsed.Valid {
		t.Fatalf("proof does not verify: %v", err)
	}
	kid, _ := parsed.Header["kid"].(string)
	return parsed.Claims.(*ConsentClaims), kid
}

// A consent with a client peer gets a refresh token and expires_at; one
// without gets expires_at only. Neither answer, nor a later read, shows the
// kept values, the token hash or the peer.
func TestCreateReturnsExpiryAndARefreshTokenOnlyForAClient(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)

	withClient, raw := register.create(t, testClientPeer, 0)
	if len(withClient.RefreshToken) != 43 {
		t.Fatalf("refresh token = %q, want 32 bytes base64url", withClient.RefreshToken)
	}
	claims, _ := verify(t, register.issuer, withClient.ConsentToken)
	if !withClient.ExpiresAt.Equal(claims.ExpiresAt.Time) {
		t.Fatalf("expires_at %s is not the proof's exp %s", withClient.ExpiresAt, claims.ExpiresAt.Time)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 5*time.Minute {
		t.Fatalf("exp - iat = %s, want the lifetime", got)
	}
	assertNothingKeptIsShown(t, raw, withClient.RefreshToken)

	withoutClient, raw := register.create(t, "", 0)
	if withoutClient.RefreshToken != "" || withoutClient.ExpiresAt.IsZero() {
		t.Fatalf("without a client: %s", raw)
	}
	kept, _, _ := register.store.Get(t.Context(), withoutClient.ConsentID)
	if kept.EncryptedSubject != nil || kept.RefreshTokenHash != nil {
		t.Fatalf("a consent without a refresh token keeps %+v", kept)
	}

	for _, path := range []string{"/consents/" + withClient.ConsentID, "/consents?subject_ref=" + testSubjectRef} {
		response, err := http.Get(register.url + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		assertNothingKeptIsShown(t, body, withClient.RefreshToken)
	}
}

func assertNothingKeptIsShown(t *testing.T, body []byte, refreshToken string) {
	t.Helper()
	text := string(body)
	for _, leak := range []string{"encrypted_subject", testEncryptedValue, "refresh_token_hash", "client_peer"} {
		if strings.Contains(text, leak) {
			t.Fatalf("answer shows %q: %s", leak, text)
		}
	}
	// The create answer carries the token once, under refresh_token.
	if strings.Count(text, refreshToken) > 1 {
		t.Fatalf("answer repeats the refresh token: %s", text)
	}
}

func TestCreateRefusesAClientPeerThatIsNoPeerID(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 0)
	body, _ := json.Marshal(map[string]any{
		"encrypted_subject": testEncryptedSubject, "subject_ref": testSubjectRef,
		"dienstverlener_oin": testClientPeer, "scopes": []string{"bd:ib:2025"}, "client_peer": "not-a-peer",
	})
	response, err := http.Post(register.url+"/consents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
}

// A refresh signs a new proof of the same consent, with the same values and
// a new jti, bounded by the lifetime, and logs it for the client peer.
func TestARefreshGivesANewProofOfTheSameConsent(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)
	created, _ := register.create(t, testClientPeer, 0)
	first, _ := verify(t, register.issuer, created.ConsentToken)

	answer := register.refresh(t, created.RefreshToken, testClientPeer, "")
	if answer.status != http.StatusOK {
		t.Fatalf("refresh = %d: %s", answer.status, answer.raw)
	}
	if strings.Contains(string(answer.raw), "refresh_token") {
		t.Fatalf("the answer carries a refresh token; it is not rotated: %s", answer.raw)
	}
	second, _ := verify(t, register.issuer, answer.ConsentToken)
	if second.ConsentID != created.ConsentID || answer.ConsentID != created.ConsentID {
		t.Fatalf("consent_id = %q, want %q", second.ConsentID, created.ConsentID)
	}
	if second.ID == first.ID {
		t.Fatal("the refreshed proof has the same jti")
	}
	firstSubject, _ := json.Marshal(first.EncryptedSubject)
	secondSubject, _ := json.Marshal(second.EncryptedSubject)
	if string(firstSubject) != string(secondSubject) || !strings.Contains(string(secondSubject), testEncryptedValue) {
		t.Fatalf("values differ: %s vs %s", firstSubject, secondSubject)
	}
	if strings.Join(second.Scopes, ",") != "bd:ib:2025" || len(second.ScopeEntries) != 1 ||
		second.DienstverlenrOIN != testClientPeer || second.ValidUntil != first.ValidUntil {
		t.Fatalf("refreshed claims = %+v", second)
	}
	if !answer.ExpiresAt.Equal(second.ExpiresAt.Time) {
		t.Fatalf("expires_at %s is not exp %s", answer.ExpiresAt, second.ExpiresAt.Time)
	}
	if got := second.ExpiresAt.Sub(second.IssuedAt.Time); got != 5*time.Minute {
		t.Fatalf("exp - iat = %s, want 5m", got)
	}

	drain(t, register.client, register.store)
	refreshes := ldvtest.ByName(register.logbook.Written(), "dataverwerking.toestemmingsbewijs-vernieuwen")
	if len(refreshes) != 1 {
		t.Fatalf("want one refresh record, got %+v", register.logbook.Written())
	}
	record := refreshes[0]
	if got := record.Attributes[ldv.AttrProcessingActivityID]; got != consentProofRefreshActivity {
		t.Errorf("activity = %v", got)
	}
	if got, want := record.Attributes[ldv.AttrForeignOperationProcessor], ldv.DefaultPeerURIBase+"/"+testClientPeer; got != want {
		t.Errorf("processor = %v, want %s", got, want)
	}
	if record.Attributes[ldv.AttrDataSubjectID] != testSubjectRef ||
		record.Attributes["dpl.gbo.consentId"] != created.ConsentID ||
		record.Attributes["dpl.gbo.dienstverlenerOin"] != testClientPeer {
		t.Errorf("attributes = %#v", record.Attributes)
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), testEncryptedValue) || strings.Contains(string(encoded), created.RefreshToken) {
		t.Errorf("the record carries a value or the token: %s", encoded)
	}
}

// The end of the consent bounds a refreshed proof too.
func TestARefreshedProofEndsWithTheConsent(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), time.Hour)
	created, _ := register.create(t, testClientPeer, 120)
	answer := register.refresh(t, created.RefreshToken, testClientPeer, "")
	if answer.status != http.StatusOK {
		t.Fatalf("refresh = %d: %s", answer.status, answer.raw)
	}
	claims, _ := verify(t, register.issuer, answer.ConsentToken)
	validUntil, err := time.Parse(time.RFC3339, claims.ValidUntil)
	if err != nil {
		t.Fatal(err)
	}
	if !claims.ExpiresAt.Equal(validUntil) {
		t.Fatalf("exp %s, want valid_until %s", claims.ExpiresAt.Time, validUntil)
	}
}

// The token is not rotated: reused after a refresh, and by several at once,
// it gives a valid proof each time.
func TestTheRefreshTokenWorksAgainAndConcurrently(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)
	created, _ := register.create(t, testClientPeer, 0)
	for attempt := range 2 {
		if answer := register.refresh(t, created.RefreshToken, testClientPeer, ""); answer.status != http.StatusOK {
			t.Fatalf("refresh %d = %d: %s", attempt, answer.status, answer.raw)
		}
	}
	const parallel = 8
	var wait sync.WaitGroup
	answers := make(chan refreshAnswer, parallel)
	for range parallel {
		wait.Add(1)
		go func() {
			defer wait.Done()
			answers <- register.refresh(t, created.RefreshToken, testClientPeer, "")
		}()
	}
	wait.Wait()
	close(answers)
	jtis := map[string]bool{}
	for answer := range answers {
		if answer.status != http.StatusOK {
			t.Fatalf("concurrent refresh = %d: %s", answer.status, answer.raw)
		}
		claims, _ := verify(t, register.issuer, answer.ConsentToken)
		jtis[claims.ID] = true
	}
	if len(jtis) != parallel {
		t.Fatalf("%d distinct proofs from %d refreshes", len(jtis), parallel)
	}
}

func TestRefreshRefusals(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)
	created, _ := register.create(t, testClientPeer, 0)
	tests := []struct {
		name, token, peer, scope, want string
	}{
		{"unknown token", "not-a-token", testClientPeer, "", "invalid_grant"},
		{"another client peer", created.RefreshToken, testOtherPeer, "", "invalid_grant"},
		{"no token", "", testClientPeer, "", "invalid_request"},
		{"narrower scope", created.RefreshToken, testClientPeer, "bd:ib:2024", "invalid_scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answer := register.refresh(t, tt.token, tt.peer, tt.scope)
			if answer.status != http.StatusBadRequest || answer.Error != tt.want {
				t.Fatalf("answer = %d %s, want 400 %s", answer.status, answer.raw, tt.want)
			}
		})
	}
	// The whole consent as scope is accepted.
	if answer := register.refresh(t, created.RefreshToken, testClientPeer, "bd:ib:2025"); answer.status != http.StatusOK {
		t.Fatalf("whole scope = %d: %s", answer.status, answer.raw)
	}
	drain(t, register.client, register.store)
	if got := ldvtest.ByName(register.logbook.Written(), "dataverwerking.toestemmingsbewijs-vernieuwen"); len(got) != 1 {
		t.Fatalf("refusals wrote records: %d refresh records, want 1", len(got))
	}
}

// Revoking wipes the values and the token hash under the same lock as the
// status change and its record; the token is refused from then on.
func TestRevokingForgetsTheValuesAndRefusesTheToken(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)
	created, _ := register.create(t, testClientPeer, 0)

	var seen *Consent
	if _, ok, err := register.store.Revoke(t.Context(), created.ConsentID, func(revoked *Consent) ([]byte, error) {
		seen = revoked
		return nil, nil
	}); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if seen.Status != "REVOKED" || seen.EncryptedSubject != nil || seen.RefreshTokenHash != nil {
		t.Fatalf("the record was built from %+v, want the revoked and wiped consent", seen)
	}
	kept, _, _ := register.store.Get(t.Context(), created.ConsentID)
	if kept.EncryptedSubject != nil || kept.RefreshTokenHash != nil {
		t.Fatalf("revoked consent keeps %+v", kept)
	}
	if answer := register.refresh(t, created.RefreshToken, testClientPeer, ""); answer.Error != "invalid_grant" {
		t.Fatalf("refresh after revoke = %d %s", answer.status, answer.raw)
	}
}

// A revocation whose record cannot be built changes nothing: the consent
// stays active and keeps its values.
func TestAFailedRevocationKeepsTheValues(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)
	created, _ := register.create(t, testClientPeer, 0)
	if _, _, err := register.store.Revoke(t.Context(), created.ConsentID, func(*Consent) ([]byte, error) {
		return nil, errors.New("record failed")
	}); err == nil {
		t.Fatal("revoke without a record succeeded")
	}
	kept, _, _ := register.store.Get(t.Context(), created.ConsentID)
	if kept.Status != "ACTIVE" || kept.EncryptedSubject == nil || kept.RefreshTokenHash == nil {
		t.Fatalf("a failed revocation changed %+v", kept)
	}
	if answer := register.refresh(t, created.RefreshToken, testClientPeer, ""); answer.status != http.StatusOK {
		t.Fatalf("refresh = %d %s", answer.status, answer.raw)
	}
}

// After valid_until the first refused refresh wipes the values.
func TestARefreshAfterTheEndForgetsTheValues(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 5*time.Minute)
	created, _ := register.create(t, testClientPeer, 0)
	register.store.mu.Lock()
	register.store.consents[created.ConsentID].ValidUntil = time.Now().Add(-time.Second)
	register.store.mu.Unlock()

	if answer := register.refresh(t, created.RefreshToken, testClientPeer, ""); answer.Error != "invalid_grant" {
		t.Fatalf("refresh after the end = %d %s", answer.status, answer.raw)
	}
	kept, _, _ := register.store.Get(t.Context(), created.ConsentID)
	if kept.EncryptedSubject != nil || kept.RefreshTokenHash != nil {
		t.Fatalf("ended consent still keeps %+v", kept)
	}
	if _, found, _ := register.store.ByRefreshToken(t.Context(), refresh.HashToken(created.RefreshToken)); found {
		t.Fatal("the token is still known")
	}
}

// Fail closed: a refresh whose record cannot be stored gives no proof.
func TestNoRefreshedProofWithoutARecord(t *testing.T) {
	store := &refusingOutbox{Store: NewStore()}
	issuer, err := NewConsentIssuer(config{TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatal(err)
	}
	logbook := ldvtest.New(t, append(allGBOActivities(), consentProofRefreshActivity)...)
	server := httptest.NewServer(newMux(store, issuer, newRegisterLogbook(logbook.Client(t, "consent-register")), uses.Service{}))
	t.Cleanup(server.Close)
	register := &refreshRegister{url: server.URL, store: store.Store, issuer: issuer}
	created, _ := register.create(t, testClientPeer, 0)

	answer := register.refresh(t, created.RefreshToken, testClientPeer, "")
	if answer.status != http.StatusInternalServerError || answer.Error != "server_error" || answer.ConsentToken != "" {
		t.Fatalf("answer = %d %s, want 500 server_error without a proof", answer.status, answer.raw)
	}
}

// refusingOutbox stores consents but no record on its own.
type refusingOutbox struct{ *Store }

func (s *refusingOutbox) AppendRecord(context.Context, []byte) error {
	return errors.New("outbox unavailable")
}

// Key rotation without a rotation mechanism (a restart with a new key): a
// proof signed with key A, then a register with key B over the same store.
// The refresh is signed with B, and no proof outlives the proof lifetime, so
// no proof with A is valid longer than that after the rotation.
func TestAfterAKeyRotationRefreshesUseTheNewKey(t *testing.T) {
	const lifetime = 5 * time.Minute
	store := NewStore()
	before := newRefreshRegister(t, store, lifetime)
	created, _ := before.create(t, testClientPeer, 0)
	firstClaims, oldKid := verify(t, before.issuer, created.ConsentToken)

	after := newRefreshRegister(t, store, lifetime)
	if after.issuer.kid == oldKid {
		t.Fatal("the new key has the old kid")
	}
	answer := after.refresh(t, created.RefreshToken, testClientPeer, "")
	if answer.status != http.StatusOK {
		t.Fatalf("refresh after rotation = %d %s", answer.status, answer.raw)
	}
	claims, kid := verify(t, after.issuer, answer.ConsentToken)
	if kid != after.issuer.kid {
		t.Fatalf("refreshed proof kid = %q, want the new key's %q", kid, after.issuer.kid)
	}
	for name, proof := range map[string]*ConsentClaims{"first": firstClaims, "refreshed": claims} {
		if got := proof.ExpiresAt.Sub(proof.IssuedAt.Time); got > lifetime {
			t.Errorf("%s proof: exp - iat = %s, over the lifetime %s", name, got, lifetime)
		}
	}
}

func TestRefreshOnlyTakesPOST(t *testing.T) {
	register := newRefreshRegister(t, NewStore(), 0)
	response, err := http.Get(register.url + "/proofs/refresh")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", response.StatusCode)
	}
}
