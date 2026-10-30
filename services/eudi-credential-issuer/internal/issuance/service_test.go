package issuance_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const (
	publicURL = "https://issuer.test"
	vct       = "https://source.test/types/income"
	client    = "https://wallet.test"
)

// fakeSource returns one income document for every subject.
type fakeSource struct{ calls int }

func (s *fakeSource) Fetch(context.Context, string, map[string]any, map[string]string) ([]issuance.SourceDocument, error) {
	s.calls++
	return []issuance.SourceDocument{{AttestationType: vct, Format: "dc+sd-jwt",
		Attributes: map[string]any{"year": "2025", "address": map[string]any{"city": "Utrecht"}}}}, nil
}

// countingStore counts status list allocations.
type countingStore struct {
	*memstore.Store
	mu          sync.Mutex
	allocations int
}

func (s *countingStore) Allocate(ctx context.Context, list string, size int, reference string) (int, error) {
	s.mu.Lock()
	s.allocations++
	s.mu.Unlock()
	return s.Store.Allocate(ctx, list, size, reference)
}

func (s *countingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allocations
}

type env struct {
	svc      *issuance.Service
	store    *countingStore
	source   *fakeSource
	provider testwallet.Provider
}

// validConfig returns a configuration New accepts, with one nl profile and
// one eudi profile, and the wallet provider both trust.
func validConfig(t *testing.T) (issuance.Config, testwallet.Provider) {
	t.Helper()
	subject := pkix.Name{CommonName: "Issuer", Organization: []string{"Demo"}}
	root := testpki.NewRoot(t, "issuer root")
	issuer := root.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{{1, 0, 18013, 5, 1, 2}}})
	status := root.Issue(t, testpki.Options{Subject: subject})
	access := testpki.NewRoot(t, "access root").Issue(t, testpki.Options{})
	provider := testwallet.NewProvider(t, "provider")
	anchors := []x5c.Anchors{provider.Anchors("provider")}
	return issuance.Config{
		PublicURL: publicURL,
		Profiles: []issuance.Profile{
			{Name: "nl", Kind: issuance.KindNL, WalletProviders: anchors, AccessKey: access.Key, AccessChain: access.Chain,
				RegistrationCertificate: []byte("a.b.c")},
			{Name: "eudi", Kind: issuance.KindEUDI, WalletProviders: anchors, KeyAttestationProviders: anchors,
				KeyStorage: []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"},
				AccessKey: access.Key, AccessChain: access.Chain},
		},
		Configurations: []issuance.CredentialConfiguration{{
			ID: "income", VCT: vct, Scope: "income", ValidDays: 365, StatusList: "income",
			Display: []issuance.Display{{Name: "Income", Locale: "en"}},
			Claims:  []issuance.Claim{{Path: []string{"year"}, Mandatory: true}, {Path: []string{"address", "city"}}},
		}},
		IssuerKey: issuer.Key, IssuerChain: issuer.Chain, StatusKey: status.Key, StatusChain: status.Chain,
	}, provider
}

func newEnv(t *testing.T) env {
	t.Helper()
	cfg, provider := validConfig(t)
	store := &countingStore{Store: memstore.New()}
	src := &fakeSource{}
	svc, err := issuance.New(cfg, src, store, store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return env{svc: svc, store: store, source: src, provider: provider}
}

func code(t *testing.T, e env, profile, reference string, ids ...string) (issuance.Offer, string) {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{"income"}
	}
	offer, err := e.svc.CreateOffer(context.Background(), issuance.OfferRequest{
		Profile: profile, ConfigurationIDs: ids, Subject: map[string]any{"bsn": "1"}, Reference: reference,
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := offer.CredentialOffer.Grants[oid4vci.GrantPreAuthorizedCode].(oid4vci.PreAuthorizedCodeGrant)
	return offer, grant.PreAuthorizedCode
}

// token redeems c on profile and returns the access token and the DPoP
// key it is bound to.
func token(t *testing.T, e env, profile, c string) (oid4vci.TokenResponse, *ecdsa.PrivateKey, error) {
	t.Helper()
	ctx := context.Background()
	wiaKey, dpopKey := testpki.NewKey(t), testpki.NewKey(t)
	challenge, err := e.svc.Challenge(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.svc.Token(ctx, issuance.TokenRequest{
		Profile: profile, GrantType: oid4vci.GrantPreAuthorizedCode, PreAuthorizedCode: c,
		ClientAttestation:    e.provider.WIA(t, client, wiaKey, time.Now().Add(time.Hour)),
		ClientAttestationPoP: testwallet.PoP(t, wiaKey, client, publicURL+"/"+profile, challenge),
		DPoP:                 []string{testwallet.DPoP(t, dpopKey, "POST", publicURL+"/"+profile+"/token", "")},
	})
	return resp, dpopKey, err
}

func credential(t *testing.T, e env, tok oid4vci.TokenResponse, dpopKey *ecdsa.PrivateKey, proofs *oid4vci.Proofs) (oid4vci.CredentialResponse, error) {
	t.Helper()
	return e.svc.Credential(context.Background(), issuance.CredentialCall{
		Profile:       "nl",
		Authorization: "DPoP " + tok.AccessToken,
		DPoP:          []string{testwallet.DPoP(t, dpopKey, "POST", publicURL+"/nl/credential", tok.AccessToken)},
		Request:       oid4vci.CredentialRequest{CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: proofs},
	})
}

func jwtProof(t *testing.T, e env) *oid4vci.Proofs {
	t.Helper()
	nonce, err := e.svc.Nonce(context.Background(), "nl")
	if err != nil {
		t.Fatal(err)
	}
	return &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), publicURL+"/nl", nonce, "")}}
}

func refusedWith(t *testing.T, err error, code string) {
	t.Helper()
	var pe *issuance.Error
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// Configurations a wallet would refuse are refused at startup.
func TestNewRefusesConfigurations(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	selfSigned := testpki.NewSelfSignedLeaf(t, "self")
	for name, mutate := range map[string]func(*issuance.Config){
		"public URL with a path": func(c *issuance.Config) { c.PublicURL = publicURL + "/issuer" },
		"no scope (HAIP 4.1)":    func(c *issuance.Config) { c.Configurations[0].Scope = "" },
		"no display":             func(c *issuance.Config) { c.Configurations[0].Display = nil },
		"empty claim path":       func(c *issuance.Config) { c.Configurations[0].Claims[1].Path = nil },
		"mandatory object claim": func(c *issuance.Config) {
			c.Configurations[0].Claims = append(c.Configurations[0].Claims, issuance.Claim{Path: []string{"address"}, Mandatory: true})
		},
		"duplicate scope": func(c *issuance.Config) {
			d := c.Configurations[0]
			d.ID = "other"
			c.Configurations = append(c.Configurations, d)
		},
		"nl without registration cert":   func(c *issuance.Config) { c.Profiles[0].RegistrationCertificate = nil },
		"registration cert with newline": func(c *issuance.Config) { c.Profiles[0].RegistrationCertificate = []byte("a.b.c\n") },
		"eudi without key levels":        func(c *issuance.Config) { c.Profiles[1].KeyStorage = nil },
		"duplicate profile names":        func(c *issuance.Config) { c.Profiles[1].Name = "nl" },
		"negative batch size":            func(c *issuance.Config) { c.BatchSize = -1 },
		"negative status list size":      func(c *issuance.Config) { c.StatusListSize = -1 },
		"P-384 issuer key":               func(c *issuance.Config) { c.IssuerKey = p384 },
		"self-signed non-CA access cert": func(c *issuance.Config) {
			c.Profiles[0].AccessKey, c.Profiles[0].AccessChain = selfSigned.Key, selfSigned.Chain
		},
		"status subject differs": func(c *issuance.Config) {
			c.StatusChain = c.Profiles[0].AccessChain
			c.StatusKey = c.Profiles[0].AccessKey
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := validConfig(t)
			mutate(&cfg)
			store := memstore.New()
			_, err := issuance.New(cfg, &fakeSource{}, store, store, store, time.Now)
			if err == nil {
				t.Fatal("expected refusal")
			}
			t.Log(err)
		})
	}
}

func TestOfferAsksEachConfigurationOnce(t *testing.T) {
	e := newEnv(t)
	offer, c := code(t, e, "nl", "", "income", "income")
	if e.source.calls != 1 || len(offer.CredentialOffer.CredentialConfigurationIDs) != 1 {
		t.Fatalf("source calls %d, ids %v", e.source.calls, offer.CredentialOffer.CredentialConfigurationIDs)
	}
	tok, _, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(tok.AuthorizationDetails[0].CredentialIdentifiers); n != 1 {
		t.Fatalf("want one credential identifier, got %d", n)
	}
}

func TestRepeatedRequestAllocatesNothing(t *testing.T) {
	e := newEnv(t)
	_, c := code(t, e, "nl", "")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credential(t, e, tok, dpopKey, jwtProof(t, e)); err != nil {
		t.Fatal(err)
	}
	before := e.store.count()
	for range 3 {
		_, err := credential(t, e, tok, dpopKey, jwtProof(t, e))
		refusedWith(t, err, oid4vci.ErrInvalidCredentialRequest)
	}
	if e.store.count() != before {
		t.Fatalf("repeated requests allocated %d status entries", e.store.count()-before)
	}
}

func TestConcurrentRequestsAllocateOnce(t *testing.T) {
	e := newEnv(t)
	_, c := code(t, e, "nl", "")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	proofs := make([]*oid4vci.Proofs, 8)
	for i := range proofs {
		proofs[i] = jwtProof(t, e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	issued := 0
	for _, p := range proofs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := credential(t, e, tok, dpopKey, p); err == nil {
				mu.Lock()
				issued++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if issued != 1 || e.store.count() != 1 {
		t.Fatalf("%d requests issued, %d status entries allocated; want 1 and 1", issued, e.store.count())
	}
}

func TestRevokedReferenceIssuesNothing(t *testing.T) {
	ctx := context.Background()
	t.Run("before the code is redeemed", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "case-1")
		if _, err := e.svc.RevokeReference(ctx, "case-1"); err != nil {
			t.Fatal(err)
		}
		_, _, err := token(t, e, "nl", c)
		refusedWith(t, err, oid4vci.ErrInvalidGrant)
	})
	t.Run("after the token, before the credential", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "case-2")
		tok, dpopKey, err := token(t, e, "nl", c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RevokeReference(ctx, "case-2"); err != nil {
			t.Fatal(err)
		}
		_, err = credential(t, e, tok, dpopKey, jwtProof(t, e))
		refusedWith(t, err, oid4vci.ErrCredentialRequestDenied)
	})
}

func TestProofWithoutNonceIsInvalidProof(t *testing.T) {
	e := newEnv(t)
	_, c := code(t, e, "nl", "")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	proofs := &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), publicURL+"/nl", "", "")}}
	_, err = credential(t, e, tok, dpopKey, proofs)
	refusedWith(t, err, oid4vci.ErrInvalidProof)
}

func TestChallengeRefusalCarriesFreshChallenge(t *testing.T) {
	e := newEnv(t)
	_, c := code(t, e, "nl", "")
	wiaKey, dpopKey := testpki.NewKey(t), testpki.NewKey(t)
	_, err := e.svc.Token(context.Background(), issuance.TokenRequest{
		Profile: "nl", GrantType: oid4vci.GrantPreAuthorizedCode, PreAuthorizedCode: c,
		ClientAttestation:    e.provider.WIA(t, client, wiaKey, time.Now().Add(time.Hour)),
		ClientAttestationPoP: testwallet.PoP(t, wiaKey, client, publicURL+"/nl", "made-up"),
		DPoP:                 []string{testwallet.DPoP(t, dpopKey, "POST", publicURL+"/nl/token", "")},
	})
	var pe *issuance.Error
	if !errors.As(err, &pe) || pe.Code != oid4vci.ErrUseAttestationChallenge || pe.Challenge == "" || strings.Contains(pe.Description, "made-up") {
		t.Fatalf("want a fresh challenge, got %+v", pe)
	}
}
