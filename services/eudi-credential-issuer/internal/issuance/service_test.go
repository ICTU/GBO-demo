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

// fakeSource returns one document of the asked configuration for every
// subject.
type fakeSource struct{ calls int }

func (s *fakeSource) Fetch(_ context.Context, configurationID string, _ map[string]any, _ map[string]string) ([]issuance.SourceDocument, error) {
	s.calls++
	typ := vct
	if configurationID == "death" {
		typ = vct + "/death"
	}
	return []issuance.SourceDocument{{AttestationType: typ, Format: "dc+sd-jwt",
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

// validConfig returns a configuration New accepts: providers "tax" (an
// income statement) and "civil" (a death certificate), an nl and an eudi
// profile, and the wallet provider both profiles trust.
func validConfig(t *testing.T) (issuance.Config, testwallet.Provider) {
	t.Helper()
	provider := testwallet.NewProvider(t, "provider")
	anchors := []x5c.Anchors{provider.Anchors("provider")}
	claims := []issuance.Claim{{Path: []string{"year"}, Mandatory: true}, {Path: []string{"address", "city"}}}
	return issuance.Config{
		PublicURL: publicURL,
		Providers: []issuance.Provider{testProvider(t, "tax", vct), testProvider(t, "civil", vct+"/death")},
		Profiles: []issuance.Profile{
			{Name: "nl", Kind: issuance.KindNL, WalletProviders: anchors},
			{Name: "eudi", Kind: issuance.KindEUDI, WalletProviders: anchors, KeyAttestationProviders: anchors,
				KeyStorage: []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"}},
		},
		Configurations: []issuance.CredentialConfiguration{
			{ID: "income", Provider: "tax", VCT: vct, Scope: "income", ValidDays: 365, StatusList: "income",
				Display: []issuance.Display{{Name: "Income", Locale: "en"}}, Claims: claims},
			{ID: "death", Provider: "civil", VCT: vct + "/death", Scope: "death", ValidDays: 365, StatusList: "death",
				Display: []issuance.Display{{Name: "Death certificate", Locale: "en"}}, Claims: claims},
		},
	}, provider
}

// testProvider returns a provider with its own access, issuer and status
// certificates and a registration certificate for vcts.
func testProvider(t *testing.T, name string, vcts ...string) issuance.Provider {
	t.Helper()
	subject := pkix.Name{CommonName: name, Organization: []string{name}}
	root := testpki.NewRoot(t, name+" issuer root")
	issuer := root.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{{1, 0, 18013, 5, 1, 2}}})
	status := root.Issue(t, testpki.Options{Subject: subject})
	access := testpki.NewRoot(t, name+" access root").Issue(t, testpki.Options{Subject: testpki.OrganizationSubject(name, name, orgID(name))})
	return issuance.Provider{Name: name, AccessKey: access.Key, AccessChain: access.Chain,
		RegistrationCertificate: testwallet.RegistrationCertificate(t, registrar(t), orgID(name), vcts...),
		IssuerKey:               issuer.Key, IssuerChain: issuer.Chain, StatusKey: status.Key, StatusChain: status.Chain}
}

// orgID is the organizationIdentifier of a test provider.
func orgID(name string) string { return "NTRNL-" + name }

func registrar(t *testing.T) testpki.Cert {
	t.Helper()
	return testpki.NewRoot(t, "registrar root").Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "registrar"}})
}

// tax is the target of the tax provider under a profile.
func tax(profile string) issuance.Target { return issuance.Target{Provider: "tax", Profile: profile} }

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
	return tokenAt(t, e, tax(profile), tax(profile), c)
}

// tokenAt redeems code c at target, with a client attestation challenge
// from challengeFrom.
func tokenAt(t *testing.T, e env, target, challengeFrom issuance.Target, c string) (oid4vci.TokenResponse, *ecdsa.PrivateKey, error) {
	t.Helper()
	ctx := context.Background()
	wiaKey, dpopKey := testpki.NewKey(t), testpki.NewKey(t)
	challenge, err := e.svc.Challenge(ctx, challengeFrom)
	if err != nil {
		t.Fatal(err)
	}
	id := e.svc.Identifier(target)
	resp, err := e.svc.Token(ctx, issuance.TokenRequest{
		Target: target, GrantType: oid4vci.GrantPreAuthorizedCode, PreAuthorizedCode: c,
		ClientAttestation:    e.provider.WIA(t, client, wiaKey, time.Now().Add(time.Hour)),
		ClientAttestationPoP: testwallet.PoP(t, wiaKey, client, id, challenge),
		DPoP:                 []string{testwallet.DPoP(t, dpopKey, "POST", id+"/token", "")},
	})
	return resp, dpopKey, err
}

func credential(t *testing.T, e env, tok oid4vci.TokenResponse, dpopKey *ecdsa.PrivateKey, proofs *oid4vci.Proofs) (oid4vci.CredentialResponse, error) {
	t.Helper()
	return credentialAt(t, e, tax("nl"), tok, dpopKey, proofs)
}

func credentialAt(t *testing.T, e env, target issuance.Target, tok oid4vci.TokenResponse, dpopKey *ecdsa.PrivateKey, proofs *oid4vci.Proofs) (oid4vci.CredentialResponse, error) {
	t.Helper()
	id := e.svc.Identifier(target)
	return e.svc.Credential(context.Background(), issuance.CredentialCall{
		Target:        target,
		Authorization: "DPoP " + tok.AccessToken,
		DPoP:          []string{testwallet.DPoP(t, dpopKey, "POST", id+"/credential", tok.AccessToken)},
		Request:       oid4vci.CredentialRequest{CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: proofs},
	})
}

func jwtProof(t *testing.T, e env) *oid4vci.Proofs {
	t.Helper()
	return jwtProofAt(t, e, tax("nl"), tax("nl"))
}

// jwtProofAt returns a proof for target that carries a c_nonce from
// nonceFrom.
func jwtProofAt(t *testing.T, e env, target, nonceFrom issuance.Target) *oid4vci.Proofs {
	t.Helper()
	nonce, err := e.svc.Nonce(context.Background(), nonceFrom)
	if err != nil {
		t.Fatal(err)
	}
	return &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), e.svc.Identifier(target), nonce, "")}}
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
		"nl without registration cert": func(c *issuance.Config) { c.Providers[0].RegistrationCertificate = nil },
		"registration cert with newline": func(c *issuance.Config) {
			c.Providers[0].RegistrationCertificate = append(c.Providers[0].RegistrationCertificate, '\n')
		},
		"registration cert not a JWT": func(c *issuance.Config) { c.Providers[0].RegistrationCertificate = []byte("a.b") },
		"registration cert of another organization": func(c *issuance.Config) {
			c.Providers[0].RegistrationCertificate = testwallet.RegistrationCertificate(t, registrar(t), orgID("civil"), vct)
		},
		"registration cert without the provider's vct": func(c *issuance.Config) {
			c.Providers[0].RegistrationCertificate = testwallet.RegistrationCertificate(t, registrar(t), orgID("tax"), vct+"/other")
		},
		"access cert without organizationIdentifier": func(c *issuance.Config) {
			access := testpki.NewRoot(t, "plain access root").Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "tax"}})
			c.Providers[0].AccessKey, c.Providers[0].AccessChain = access.Key, access.Chain
		},
		"P-5 provider without configurations": func(c *issuance.Config) {
			c.Providers = append(c.Providers, testProvider(t, "idle"))
		},
		"provider name with upper case":     func(c *issuance.Config) { c.Providers[1].Name = "Civil" },
		"status list name with a slash":     func(c *issuance.Config) { c.Configurations[0].StatusList = "a/b" },
		"eudi without key levels":           func(c *issuance.Config) { c.Profiles[1].KeyStorage = nil },
		"P-5 provider without issuer key":   func(c *issuance.Config) { c.Providers[0].IssuerKey = nil },
		"P-5 provider without access chain": func(c *issuance.Config) { c.Providers[1].AccessChain = nil },
		"P-5 configuration of no provider":  func(c *issuance.Config) { c.Configurations[1].Provider = "nobody" },
		"P-5 duplicate provider names":      func(c *issuance.Config) { c.Providers[1].Name = "tax" },
		"P-5 provider named status":         func(c *issuance.Config) { c.Providers[1].Name = "status" },
		"duplicate profile names":           func(c *issuance.Config) { c.Profiles[1].Name = "nl" },
		"negative batch size":               func(c *issuance.Config) { c.BatchSize = -1 },
		"negative status list size":         func(c *issuance.Config) { c.StatusListSize = -1 },
		"P-384 issuer key":                  func(c *issuance.Config) { c.Providers[0].IssuerKey = p384 },
		"self-signed non-CA access cert": func(c *issuance.Config) {
			c.Providers[0].AccessKey, c.Providers[0].AccessChain = selfSigned.Key, selfSigned.Chain
		},
		"status subject differs": func(c *issuance.Config) {
			c.Providers[0].StatusChain = c.Providers[0].AccessChain
			c.Providers[0].StatusKey = c.Providers[0].AccessKey
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
	proofs := &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), publicURL+"/tax/nl", "", "")}}
	_, err = credential(t, e, tok, dpopKey, proofs)
	refusedWith(t, err, oid4vci.ErrInvalidProof)
}

func TestChallengeRefusalCarriesFreshChallenge(t *testing.T) {
	e := newEnv(t)
	_, c := code(t, e, "nl", "")
	wiaKey, dpopKey := testpki.NewKey(t), testpki.NewKey(t)
	_, err := e.svc.Token(context.Background(), issuance.TokenRequest{
		Target: tax("nl"), GrantType: oid4vci.GrantPreAuthorizedCode, PreAuthorizedCode: c,
		ClientAttestation:    e.provider.WIA(t, client, wiaKey, time.Now().Add(time.Hour)),
		ClientAttestationPoP: testwallet.PoP(t, wiaKey, client, publicURL+"/tax/nl", "made-up"),
		DPoP:                 []string{testwallet.DPoP(t, dpopKey, "POST", publicURL+"/tax/nl/token", "")},
	})
	var pe *issuance.Error
	if !errors.As(err, &pe) || pe.Code != oid4vci.ErrUseAttestationChallenge || pe.Challenge == "" || strings.Contains(pe.Description, "made-up") {
		t.Fatalf("want a fresh challenge, got %+v", pe)
	}
}

// lossyStore hands every reservation to another request before the issuing
// request completes, as when a request outlives its reservation.
type lossyStore struct{ *countingStore }

func (s lossyStore) CompleteIssue(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func TestARequestThatLostItsReservationReturnsNothing(t *testing.T) {
	cfg, provider := validConfig(t)
	store := lossyStore{&countingStore{Store: memstore.New()}}
	svc, err := issuance.New(cfg, &fakeSource{}, store, store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	e := env{svc: svc, store: store.countingStore, provider: provider}
	_, c := code(t, e, "nl", "")
	tok, dpopKey, err := token(t, e, "nl", c)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := credential(t, e, tok, dpopKey, jwtProof(t, e))
	refusedWith(t, err, oid4vci.ErrInvalidCredentialRequest)
	if len(resp.Credentials) != 0 {
		t.Fatal("a request that lost its reservation must not return credentials")
	}
}

// P-3: one offer holds credentials of one provider.
func TestOfferWithTwoProvidersIsRefused(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.CreateOffer(context.Background(), issuance.OfferRequest{
		Profile: "nl", ConfigurationIDs: []string{"income", "death"}, Subject: map[string]any{"bsn": "1"},
	})
	if !errors.Is(err, issuance.ErrMixedProviders) {
		t.Fatalf("want ErrMixedProviders, got %v", err)
	}
	if e.source.calls != 0 {
		t.Fatal("the source must not be asked for a refused offer")
	}
}

// P-4: what one provider hands out works only at that provider.
func TestProvidersAreIsolated(t *testing.T) {
	civil := issuance.Target{Provider: "civil", Profile: "nl"}
	t.Run("a code of one provider does not redeem at another", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "", "death")
		_, _, err := tokenAt(t, e, tax("nl"), tax("nl"), c)
		refusedWith(t, err, oid4vci.ErrInvalidGrant)
		if _, _, err := tokenAt(t, e, civil, civil, c); err != nil {
			t.Fatalf("the refusal must not burn the code: %v", err)
		}
	})
	t.Run("a token of one provider is no token at another", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "", "death")
		tok, dpopKey, err := tokenAt(t, e, civil, civil, c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = credentialAt(t, e, tax("nl"), tok, dpopKey, jwtProofAt(t, e, tax("nl"), tax("nl")))
		refusedWith(t, err, oid4vci.ErrInvalidToken)
	})
	t.Run("a c_nonce of one provider is unknown at another", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "")
		tok, dpopKey, err := token(t, e, "nl", c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = credential(t, e, tok, dpopKey, jwtProofAt(t, e, tax("nl"), civil))
		refusedWith(t, err, oid4vci.ErrInvalidNonce)
	})
	t.Run("a c_nonce of one profile is unknown at another", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "")
		tok, dpopKey, err := token(t, e, "nl", c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = credential(t, e, tok, dpopKey, jwtProofAt(t, e, tax("nl"), tax("eudi")))
		refusedWith(t, err, oid4vci.ErrInvalidNonce)
	})
	t.Run("an attestation challenge of one provider is unknown at another", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "")
		_, _, err := tokenAt(t, e, tax("nl"), civil, c)
		refusedWith(t, err, oid4vci.ErrUseAttestationChallenge)
	})
	t.Run("a configuration moved to another provider is no longer offered", func(t *testing.T) {
		e := newEnv(t)
		_, c := code(t, e, "nl", "")
		tok, dpopKey, err := token(t, e, "nl", c)
		if err != nil {
			t.Fatal(err)
		}
		// The issuer restarts with income at civil and death at tax.
		cfg, _ := validConfig(t)
		cfg.Providers = []issuance.Provider{testProvider(t, "tax", vct+"/death"), testProvider(t, "civil", vct)}
		cfg.Configurations[0].Provider, cfg.Configurations[1].Provider = "civil", "tax"
		if e.svc, err = issuance.New(cfg, e.source, e.store, e.store, e.store, time.Now); err != nil {
			t.Fatal(err)
		}
		_, err = credential(t, e, tok, dpopKey, jwtProof(t, e))
		refusedWith(t, err, oid4vci.ErrUnknownCredentialID)
	})
}

// P-6: revoking a reference revokes its credentials at every provider.
func TestRevocationByReferenceSpansProviders(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	civil := issuance.Target{Provider: "civil", Profile: "nl"}
	for target, id := range map[issuance.Target]string{tax("nl"): "income", civil: "death"} {
		_, c := code(t, e, "nl", "case-77", id)
		tok, dpopKey, err := tokenAt(t, e, target, target, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := credentialAt(t, e, target, tok, dpopKey, jwtProofAt(t, e, target, target)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.svc.RevokeReference(ctx, "case-77")
	if err != nil || n != 2 {
		t.Fatalf("revoked %d, %v; want 2", n, err)
	}
	for _, list := range []string{"tax/income", "civil/death"} {
		if revoked, err := e.store.Revoked(ctx, list); err != nil || len(revoked) != 1 {
			t.Fatalf("%s: revoked %v, %v", list, revoked, err)
		}
	}
}
