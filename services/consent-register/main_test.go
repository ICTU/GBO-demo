package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Happy-path integration test: create a consent, then fetch it by its
// generated consent_id. Verifies the POST/GET handlers share the same
// store while the encrypted subject only exists inside the signed token.
// assertSignedSubject checks the values the token carries for the two parties
// of TestCreateThenGetConsent: one on the BSN authorisation list, one not.
func assertSignedSubject(t *testing.T, subject map[string]EncryptedSubject) {
	t.Helper()
	listed, unlisted := subject["99999999900000000200"], subject["99999999900000000210"]
	if listed.Identity == nil || *listed.Identity != (EncryptedValue{KeySetVersion: 20260101, Value: "dmFsdWU="}) ||
		listed.Pseudonym == nil || *listed.Pseudonym != (EncryptedValue{KeySetVersion: 20260101, Value: "cHNldWRv"}) {
		t.Fatalf("signed subject of the listed party = %+v", listed)
	}
	// A party without an identity gets none in the token, not an empty one.
	if unlisted.Identity != nil || unlisted.Pseudonym == nil || unlisted.Pseudonym.Value != "cHNldWRvLTIxMA==" {
		t.Fatalf("signed subject of the unlisted party = %+v", unlisted)
	}
}

func TestCreateThenGetConsent(t *testing.T) {
	issuer, err := NewConsentIssuer(config{
		SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience",
	})
	if err != nil {
		t.Fatal(err)
	}
	issuer.now = func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }
	store := NewStore()
	srv := httptest.NewServer(newMux(store, issuer, nil))
	defer srv.Close()

	createBody := bytes.NewBufferString(`{
		"encrypted_subject": {
			"99999999900000000200": {"identity": {"key_set_version": 20260101, "value": "dmFsdWU="}, "pseudonym": {"key_set_version": 20260101, "value": "cHNldWRv"}},
			"99999999900000000210": {"pseudonym": {"key_set_version": 20260301, "value": "cHNldWRvLTIxMA=="}}
		},
		"subject_ref": "EP-portal-abc123",
		"dienstverlener_oin": "00000001234567890000",
		"scopes": ["bsn:read"],
		"use_case": "hypotheek-aanvraag"
	}`)
	createResp, err := http.Post(srv.URL+"/consents", "application/json", createBody)
	if err != nil {
		t.Fatalf("create consent: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", createResp.StatusCode)
	}
	var created struct {
		ConsentID    string `json:"consent_id"`
		Status       string `json:"status"`
		SubjectRef   string `json:"subject_ref"`
		ConsentToken string `json:"consent_token"`
	}
	createdBody, err := io.ReadAll(createResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	var createdRaw map[string]any
	_ = json.Unmarshal(createdBody, &createdRaw)
	if _, leaked := createdRaw["encrypted_subject"]; leaked {
		t.Fatalf("create response exposed the encrypted subject outside the token: %s", createdBody)
	}
	if created.ConsentID == "" {
		t.Fatalf("empty consent_id in create response: %+v", created)
	}
	if created.Status != "ACTIVE" {
		t.Fatalf("created status = %q, want ACTIVE", created.Status)
	}
	parsed, err := jwt.ParseWithClaims(created.ConsentToken, &ConsentClaims{}, func(_ *jwt.Token) (any, error) {
		return &issuer.key.PublicKey, nil
	}, jwt.WithAudience("test-audience"), jwt.WithIssuer("test-issuer"), jwt.WithTimeFunc(func() time.Time { return issuer.now() }))
	if err != nil || !parsed.Valid {
		t.Fatalf("issued consent token is invalid: %v", err)
	}
	claims := parsed.Claims.(*ConsentClaims)
	assertSignedSubject(t, claims.EncryptedSubject)
	if claims.DienstverlenrOIN != "00000001234567890000" {
		t.Fatalf("unexpected signed claims: %+v", claims)
	}
	if claims.ValidUntil == "" || claims.ID == "" || len(claims.Audience) != 1 {
		t.Fatalf("required signed claims missing: %+v", claims)
	}

	assertPublicJWKS(t, srv.URL)

	getResp, err := http.Get(srv.URL + "/consents/" + created.ConsentID)
	if err != nil {
		t.Fatalf("get consent: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", getResp.StatusCode)
	}
	fetchedBody, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var fetched struct {
		ConsentID  string `json:"consent_id"`
		Status     string `json:"status"`
		SubjectRef string `json:"subject_ref"`
	}
	if err := json.Unmarshal(fetchedBody, &fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.ConsentID != created.ConsentID {
		t.Fatalf("consent_id mismatch: got %q, want %q", fetched.ConsentID, created.ConsentID)
	}
	var raw map[string]any
	_ = json.Unmarshal(fetchedBody, &raw)
	if _, leaked := raw["encrypted_subject"]; leaked {
		t.Fatalf("persisted consent response leaked the encrypted subject: %s", fetchedBody)
	}
	if fetched.SubjectRef != "EP-portal-abc123" {
		t.Fatalf("subject_ref mismatch: %q", fetched.SubjectRef)
	}
	if fetched.Status != "ACTIVE" {
		t.Fatalf("fetched status = %q, want ACTIVE", fetched.Status)
	}

	statusResp := getStatus(t, statusUnderTest(t, store, nil), created.ConsentID, testPDPPeer)
	statusBody, _ := io.ReadAll(statusResp.Body)
	if statusResp.StatusCode != http.StatusOK || string(statusBody) == "" {
		t.Fatalf("status response = %d %s", statusResp.StatusCode, statusBody)
	}
	var statusRaw map[string]any
	_ = json.Unmarshal(statusBody, &statusRaw)
	if _, leaked := statusRaw["encrypted_subject"]; leaked {
		t.Fatalf("status response leaked the encrypted subject: %s", statusBody)
	}
	if len(statusRaw) != 2 {
		t.Fatalf("status response contains authorization details: %s", statusBody)
	}
}

func assertPublicJWKS(t *testing.T, baseURL string) {
	t.Helper()
	resp, err := http.Get(baseURL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 1 || jwks.Keys[0]["kid"] != "test-key" {
		t.Fatalf("unexpected JWKS: %#v", jwks)
	}
	if _, exposesPrivateKey := jwks.Keys[0]["d"]; exposesPrivateKey {
		t.Fatalf("JWKS exposes private key material: %#v", jwks.Keys[0])
	}
}

func TestConsentIssuerRejectsInvalidConfiguration(t *testing.T) {
	tests := []config{
		{SigningKeyPath: "/definitely/missing/consent-key.pem", SigningKeyID: "key", TokenIssuer: "issuer", TokenAudience: "audience"},
		{SigningKeyID: "", TokenIssuer: "issuer", TokenAudience: "audience"},
	}
	for _, cfg := range tests {
		if _, err := NewConsentIssuer(cfg); err == nil {
			t.Fatalf("NewConsentIssuer(%+v) unexpectedly succeeded", cfg)
		}
	}
}

func TestPersistentStoreRequiresStableSigningKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("CONSENT_SIGNING_KEY_PATH", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("persistent consent store accepted an ephemeral signing key")
	}
}

// A consent without a value for any party could never be answered by a
// source, and a plain identifier in its place is refused.
func TestCreateRefusesAConsentWithoutAnEncryptedSubject(t *testing.T) {
	issuer, err := NewConsentIssuer(config{
		SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(NewStore(), issuer, nil))
	defer srv.Close()

	for name, subject := range map[string]string{
		"none":                ``,
		"empty":               `"encrypted_subject": {},`,
		"a plain identifier":  `"pi": "PI-abc123",`,
		"party is no OIN":     `"encrypted_subject": {"belastingdienst": {"pseudonym": {"key_set_version": 20260101, "value": "cHNldWRv"}}},`,
		"no pseudonym":        `"encrypted_subject": {"99999999900000000200": {"identity": {"key_set_version": 20260101, "value": "dmFsdWU="}}},`,
		"the old shape":       `"encrypted_subject": {"99999999900000000200": {"identifier_type": "Identity", "key_set_version": 20260101, "value": "dmFsdWU="}},`,
		"no key set version":  `"encrypted_subject": {"99999999900000000200": {"pseudonym": {"value": "cHNldWRv"}}},`,
		"no value":            `"encrypted_subject": {"99999999900000000200": {"pseudonym": {"key_set_version": 20260101}}},`,
		"incomplete identity": `"encrypted_subject": {"99999999900000000200": {"identity": {"value": "dmFsdWU="}, "pseudonym": {"key_set_version": 20260101, "value": "cHNldWRv"}}},`,
	} {
		body := bytes.NewBufferString(`{` + subject + `
			"subject_ref": "EP-portal-abc123",
			"dienstverlener_oin": "00000001234567890000",
			"scopes": ["bsn:read"]
		}`)
		resp, err := http.Post(srv.URL+"/consents", "application/json", body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}
