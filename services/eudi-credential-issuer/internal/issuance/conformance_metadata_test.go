package issuance_test

// Conformance cases the matrices named as untested (conformance/
// oid4vci-1.0.yaml, conformance/haip-1.0.yaml): the batch size in the
// metadata, and what the core hands to its store about an issued credential
// (OpenID4VCI 1.0 sections 15.3 and 15.4.1).

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
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
	"gbo-demo/oid4vc-kit/x5c"
)

// HAIP-4-09, OID4VCI-12.2.4-18, OID4VCI-12.2.4-19: batch issuance is
// published with a batch_size of 2 or more, and left out below 2.
func TestConformanceBatchSizeInMetadata(t *testing.T) {
	for name, tc := range map[string]struct {
		configured int
		want       int // 0: no batch_credential_issuance
	}{
		"the default batch size": {0, issuance.DefaultBatchSize},
		"batch size 1":           {1, 0},
		"batch size 2":           {2, 2},
		"batch size 10":          {10, 10},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := validConfig(t)
			cfg.BatchSize = tc.configured
			store := memstore.New()
			svc, err := issuance.New(cfg, &fakeSource{}, store, store, store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			for _, profile := range []string{"nl", "eudi"} {
				md, err := svc.IssuerMetadataJSON(tax(profile))
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(md)
				var got struct {
					Batch *struct {
						BatchSize *int `json:"batch_size"`
					} `json:"batch_credential_issuance"`
				}
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				switch {
				case tc.want == 0 && got.Batch != nil:
					t.Errorf("%s: batch_credential_issuance %s published for batch size %d", profile, raw, tc.configured)
				case tc.want != 0 && (got.Batch == nil || got.Batch.BatchSize == nil || *got.Batch.BatchSize != tc.want):
					t.Errorf("%s: batch_credential_issuance = %+v, want batch_size %d", profile, got.Batch, tc.want)
				}
			}
		})
	}
}

// recordingStore is a memstore that records everything the core hands to
// it, and which pending authorizations are still held.
type recordingStore struct {
	*memstore.Store
	mu      sync.Mutex
	written []string
	held    map[string]string // kind/id -> payload of a pending authorization not taken
}

func newRecordingStore() *recordingStore {
	return &recordingStore{Store: memstore.New(), held: map[string]string{}}
}

func (s *recordingStore) record(v ...any) {
	raw, _ := json.Marshal(v)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, string(raw))
}

// everWritten reports whether any value handed to the store contains secret.
func (s *recordingStore) everWritten(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.written {
		if strings.Contains(w, secret) {
			return true
		}
	}
	return false
}

// stillHeld reports whether a pending authorization the store still holds
// contains secret.
func (s *recordingStore) stillHeld(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.held {
		if strings.Contains(p, secret) {
			return true
		}
	}
	return false
}

func (s *recordingStore) CreateSession(ctx context.Context, sess issuance.Session) error {
	s.record("CreateSession", sess)
	return s.Store.CreateSession(ctx, sess)
}

func (s *recordingStore) SaveToken(ctx context.Context, t issuance.AccessToken) error {
	s.record("SaveToken", t)
	return s.Store.SaveToken(ctx, t)
}

func (s *recordingStore) CompleteDocument(ctx context.Context, id string, doc issuance.Document) error {
	s.record("CompleteDocument", id, doc)
	return s.Store.CompleteDocument(ctx, id, doc)
}

func (s *recordingStore) ReserveIssue(ctx context.Context, sessionID, credentialID, owner string, now, until time.Time) (bool, error) {
	s.record("ReserveIssue", sessionID, credentialID, owner)
	return s.Store.ReserveIssue(ctx, sessionID, credentialID, owner, now, until)
}

func (s *recordingStore) CompleteIssue(ctx context.Context, sessionID, credentialID, owner string) (bool, error) {
	s.record("CompleteIssue", sessionID, credentialID, owner)
	return s.Store.CompleteIssue(ctx, sessionID, credentialID, owner)
}

func (s *recordingStore) PutPending(ctx context.Context, p issuance.PendingAuthorization) error {
	// The payload is JSON; record it as text, not as base64.
	s.record("PutPending", p.Kind, p.ID, string(p.Payload))
	s.mu.Lock()
	s.held[p.Kind+"/"+p.ID] = string(p.Payload)
	s.mu.Unlock()
	return s.Store.PutPending(ctx, p)
}

func (s *recordingStore) TakePending(ctx context.Context, kind, id string, now time.Time) (issuance.PendingAuthorization, error) {
	p, err := s.Store.TakePending(ctx, kind, id, now)
	if err == nil {
		s.mu.Lock()
		delete(s.held, kind+"/"+id)
		s.mu.Unlock()
	}
	return p, err
}

func (s *recordingStore) CreateDisclosure(ctx context.Context, d issuance.DisclosureSession) error {
	s.record("CreateDisclosure", d)
	return s.Store.CreateDisclosure(ctx, d)
}

func (s *recordingStore) Put(ctx context.Context, kind, value string, expiry time.Time) error {
	s.record("Put", kind, value)
	return s.Store.Put(ctx, kind, value, expiry)
}

func (s *recordingStore) FirstUse(ctx context.Context, kind, id string, expiry, now time.Time) (bool, error) {
	s.record("FirstUse", kind, id)
	return s.Store.FirstUse(ctx, kind, id, expiry, now)
}

func (s *recordingStore) Allocate(ctx context.Context, list string, size int, reference string) (int, error) {
	s.record("Allocate", list, reference)
	return s.Store.Allocate(ctx, list, size, reference)
}

// holderKeyValues returns the x and y coordinates of key as a JWK carries
// them.
func holderKeyValues(t *testing.T, key *ecdsa.PrivateKey) []string {
	t.Helper()
	raw, err := jose.JSONWebKey{Key: &key.PublicKey}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var jwk struct{ X, Y string }
	if err := json.Unmarshal(raw, &jwk); err != nil {
		t.Fatal(err)
	}
	return []string{jwk.X, jwk.Y}
}

// issuerSignature is the signature of the issuer-signed JWT of an SD-JWT.
func issuerSignature(t *testing.T, credential string) string {
	t.Helper()
	jws, _, _ := strings.Cut(credential, "~")
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		t.Fatalf("credential %q is not an SD-JWT", credential)
	}
	return parts[2]
}

// OID4VCI-15.3-02, OID4VCI-15.3-03, OID4VCI-15.4.1-03: after issuance the
// store has been handed neither the credential, nor the issuer's signature,
// nor the key the credential is bound to; the codes and tokens only as
// hashes.
func TestConformanceIssuedCredentialsAreNotStored(t *testing.T) {
	for _, profile := range []string{"nl", "eudi"} {
		t.Run(profile, func(t *testing.T) {
			cfg, provider := validConfig(t)
			store := newRecordingStore()
			svc, err := issuance.New(cfg, &fakeSource{}, store, store, store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			e := env{svc: svc, provider: provider}
			_, c := code(t, e, profile, "")
			tok, dpopKey, err := token(t, e, profile, c)
			if err != nil {
				t.Fatal(err)
			}
			holder := testpki.NewKey(t)
			nonce, err := svc.Nonce(context.Background(), tax(profile))
			if err != nil {
				t.Fatal(err)
			}
			proofs := &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, holder, svc.Identifier(tax(profile)), nonce, "")}}
			if profile == "eudi" {
				ka := provider.KeyAttestation(t, []*ecdsa.PrivateKey{holder}, nonce, time.Now().Add(time.Hour))
				proofs = &oid4vci.Proofs{Attestation: []string{ka}}
			}
			resp, err := credentialAt(t, e, tax(profile), tok, dpopKey, proofs)
			if err != nil || len(resp.Credentials) != 1 {
				t.Fatalf("credential = %+v, %v", resp, err)
			}
			cred := resp.Credentials[0].Credential
			secrets := map[string]string{
				"the credential":              cred,
				"the issuer's signature":      issuerSignature(t, cred),
				"the pre-authorized code":     c,
				"the access token":            tok.AccessToken,
				"the holder key x coordinate": holderKeyValues(t, holder)[0],
				"the holder key y coordinate": holderKeyValues(t, holder)[1],
			}
			if tok.RefreshToken != "" {
				secrets["the refresh token"] = tok.RefreshToken
			}
			// The recorder sees what the core stores: the c_nonce is kept
			// as a single-use value.
			if !store.everWritten(nonce) {
				t.Fatal("the recording store missed the c_nonce")
			}
			for name, secret := range secrets {
				if store.everWritten(secret) {
					t.Errorf("the store was handed %s", name)
				}
			}
		})
	}
}

// deferredSource is unavailable until available is set.
type deferredSource struct {
	fakeSource
	mu        sync.Mutex
	available bool
}

func (s *deferredSource) FetchLogin(ctx context.Context, configurationID, bsn string, parameters map[string]string) ([]issuance.SourceDocument, error) {
	s.mu.Lock()
	available := s.available
	s.mu.Unlock()
	if !available {
		return nil, issuance.ErrSourceUnavailable
	}
	return s.fakeSource.FetchLogin(ctx, configurationID, bsn, parameters)
}

// conformanceLogin stands in for GBO's authorization server: it accepts
// every code and returns one BSN.
type conformanceLogin struct{ bsn string }

func (l conformanceLogin) AuthorizationURL(_ context.Context, _, _, state, _ string) (string, error) {
	return "https://login.test/authorize?" + url.Values{"state": {state}}.Encode(), nil
}

func (l conformanceLogin) Exchange(context.Context, string, string, string, string) (string, error) {
	return l.bsn, nil
}

// OID4VCI-15.4.1-03 (deferred issuance): the holder key is kept while the
// issuance waits for its source and discarded once the credential is
// issued; the BSN of the login is never handed to the store in clear, and
// neither is the credential or its signature.
func TestConformanceDeferredIssuanceDiscardsTheHolderKey(t *testing.T) {
	const (
		redirect = "https://wallet.test/cb"
		bsn      = "999991772"
	)
	ctx := context.Background()
	cfg := disclosureConfig(t)
	cfg.Login = conformanceLogin{bsn: bsn}
	cfg.Profiles[1].WalletRedirectURIs = []string{redirect}
	provider := testwallet.NewProvider(t, "provider")
	anchors := provider.Anchors("provider")
	cfg.Profiles[1].WalletProviders = []x5c.Anchors{anchors}
	cfg.Profiles[1].KeyAttestationProviders = []x5c.Anchors{anchors}
	store := newRecordingStore()
	src := &deferredSource{}
	svc, err := issuance.New(cfg, src, store, store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	target := tax("eudi")
	id := svc.Identifier(target)
	wiaKey, dpopKey := testpki.NewKey(t), testpki.NewKey(t)
	wia := provider.WIA(t, client, wiaKey, time.Now().Add(time.Hour))
	pop := func() string {
		c, err := svc.Challenge(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		return testwallet.PoP(t, wiaKey, client, id, c)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))

	par, err := svc.PushAuthorization(ctx, issuance.PushedAuthorizationRequest{
		Target: target, ResponseType: "code", ClientID: client, RedirectURI: redirect, State: "s",
		CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), CodeChallengeMethod: "S256", IssuerState: "income-2025",
		ClientAttestation: wia, ClientAttestationPoP: pop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	loginURL, _, err := svc.Authorize(ctx, target, client, par.RequestURI)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loginURL)
	ret, err := svc.Callback(ctx, target, issuance.LoginReturn{Code: "login-code", State: u.Query().Get("state")})
	if err != nil || ret.Page != issuance.ReturnRedirect {
		t.Fatalf("callback = %+v, %v", ret, err)
	}
	back, _ := url.Parse(ret.Location)
	tok, err := svc.Token(ctx, issuance.TokenRequest{
		Target: target, GrantType: issuance.GrantAuthorizationCode, Code: back.Query().Get("code"), CodeVerifier: verifier,
		RedirectURI: redirect, ClientID: client, ClientAttestation: wia, ClientAttestationPoP: pop(),
		DPoP: []string{testwallet.DPoP(t, dpopKey, "POST", id+"/token", "")},
	})
	if err != nil {
		t.Fatal(err)
	}

	holder := testpki.NewKey(t)
	nonce, err := svc.Nonce(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	ka := provider.KeyAttestation(t, []*ecdsa.PrivateKey{holder}, nonce, time.Now().Add(time.Hour))
	res, err := svc.Credential(ctx, issuance.CredentialCall{
		Target: target, Authorization: "DPoP " + tok.AccessToken,
		DPoP: []string{testwallet.DPoP(t, dpopKey, "POST", id+"/credential", tok.AccessToken)},
		Request: oid4vci.CredentialRequest{
			CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0],
			Proofs:               &oid4vci.Proofs{Attestation: []string{ka}},
		},
	})
	if err != nil || !res.Deferred || res.Response.TransactionID == "" {
		t.Fatalf("credential = %+v, %v; want a deferred issuance", res, err)
	}
	txID := res.Response.TransactionID
	key := holderKeyValues(t, holder)
	if !store.stillHeld(key[0]) {
		t.Fatal("while the issuance waits, the holder key must be kept for it")
	}

	src.mu.Lock()
	src.available = true
	src.mu.Unlock()
	res, err = svc.Deferred(ctx, issuance.DeferredCall{
		Target: target, Authorization: "DPoP " + tok.AccessToken,
		DPoP:    []string{testwallet.DPoP(t, dpopKey, "POST", id+"/deferred_credential", tok.AccessToken)},
		Request: oid4vci.DeferredRequest{TransactionID: txID},
	})
	if err != nil || len(res.Response.Credentials) != 1 {
		t.Fatalf("deferred = %+v, %v; want the credential", res, err)
	}
	for _, k := range key {
		if store.stillHeld(k) {
			t.Error("the holder key is still kept after the credential was issued")
		}
	}
	cred := res.Response.Credentials[0].Credential
	for name, secret := range map[string]string{
		"the credential":         cred,
		"the issuer's signature": issuerSignature(t, cred),
		"the BSN in clear":       bsn,
	} {
		if store.everWritten(secret) {
			t.Errorf("the store was handed %s", name)
		}
	}
	// The transaction is over: the holder key is gone with it.
	_, err = svc.Deferred(ctx, issuance.DeferredCall{
		Target: target, Authorization: "DPoP " + tok.AccessToken,
		DPoP:    []string{testwallet.DPoP(t, dpopKey, "POST", id+"/deferred_credential", tok.AccessToken)},
		Request: oid4vci.DeferredRequest{TransactionID: txID},
	})
	refusedWith(t, err, oid4vci.ErrInvalidTransactionID)
}
