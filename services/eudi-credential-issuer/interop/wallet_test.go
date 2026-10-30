package interop_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	fapi "github.com/idfoundry/fapigo"
	"github.com/idfoundry/fapigo/client"
	"github.com/idfoundry/fapigo/fapihttp"
	"github.com/idfoundry/fapigo/keys"
	"github.com/idfoundry/fapigo/storage"
	oid4vci "github.com/idfoundry/oid4vcgo"
	"github.com/idfoundry/oid4vcgo/wallet"

	"gbo-demo/oid4vc-kit/testpki"
)

// The same flow as walletflow_test.go, one OID4VCgo wallet-package call
// per protocol step, so a refusal points at one step, and so the
// attestation proof type (Appendix F.3) is exercised: walletflow only
// sends it to an issuer that takes no jwt proof.

// newCoreWallet is OID4VCgo's wallet role.
//
// Non-default options, and why:
//   - AssuranceDevelopment and Fetch.AllowLoopbackHosts: the issuer runs on
//     127.0.0.1 (httptest, over TLS).
//   - Dependencies.HTTP: a client trusting httptest's TLS certificate.
//   - IssuerMetadataRoots + RequireSignedIssuerMetadata: verify our signed
//     metadata against the test access root; refuse unsigned metadata.
func newCoreWallet(t *testing.T, is *issuer) *wallet.Wallet {
	t.Helper()
	w, err := wallet.New(wallet.Config{
		Assurance:                   wallet.AssuranceDevelopment,
		ProofSigningAlg:             oid4vci.ES256,
		Fetch:                       fapihttp.Config{MaxResponseBytes: 1 << 20, RequestTimeout: 10 * time.Second, AllowLoopbackHosts: true},
		IssuerMetadataRoots:         is.accessRoots(),
		RequireSignedIssuerMetadata: true,
	}, wallet.Dependencies{HTTP: is.httpClient(), Clock: wallet.ClockFunc(time.Now), Random: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// session is a redeemed offer: the metadata, the DPoP-bound access token
// and the credential identifier it grants.
type session struct {
	w        *wallet.Wallet
	issuerID string
	metadata oid4vci.Metadata
	resource wallet.ProtectedResourceClient
	credID   string
}

// redeem resolves a fresh offer of profile, fetches and verifies the signed
// metadata and the authorization server metadata, and redeems the
// pre-authorized code with a Wallet Attestation, a PoP over a fresh
// challenge and DPoP.
func redeem(t *testing.T, is *issuer, w *wallet.Wallet, profile, link string) session {
	t.Helper()
	ctx := context.Background()

	offer, err := w.ResolveCredentialOffer(ctx, is.offer(profile)[link])
	if err != nil {
		t.Fatalf("resolve offer: %v", err)
	}
	md, err := w.FetchCredentialIssuerMetadata(ctx, offer.CredentialIssuer)
	if err != nil {
		t.Fatalf("signed issuer metadata: %v", err)
	}
	plan, err := wallet.PlanPreAuthorizedCode(offer, md)
	if err != nil {
		t.Fatalf("plan pre-authorized code: %v", err)
	}
	as, err := w.FetchAuthorizationServerMetadata(ctx, plan.AuthorizationServer)
	if err != nil {
		t.Fatalf("authorization server metadata: %v", err)
	}
	if !as.SupportsGrantType(wallet.PreAuthorizedCodeGrantType) || !as.SupportsAttestationAuth(string(oid4vci.ES256)) || as.ChallengeEndpoint == "" {
		t.Fatalf("authorization server metadata does not offer what HAIP needs: %+v", as)
	}
	issuerURL, endpoints, err := as.TokenClientEndpoints()
	if err != nil {
		t.Fatalf("token client endpoints: %v", err)
	}
	challengeEndpoint, err := fapi.ParseEndpointURL(as.ChallengeEndpoint)
	if err != nil {
		t.Fatal(err)
	}

	// Client authentication as fapigo/client builds it: the WIA from the
	// OID4VCgo-side provider, and a PoP with a challenge fetched by
	// wallet.RequestAttestationChallenge.
	instanceKey, dpopKey := testpki.NewKey(t), testpki.NewKey(t)
	wia, err := is.provider.WalletAttestation(ctx, clientID, &instanceKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	km, err := keys.NewKeyManagerFromSigners([]keys.SignerSpec{
		{Purpose: keys.ClientAttestationPoPSigning, Algorithm: fapi.ES256, Signer: instanceKey},
		{Purpose: keys.DPoPProofSigning, Algorithm: fapi.ES256, Signer: dpopKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := client.New(client.Config{
		Issuer: issuerURL, ClientID: fapi.ClientID(clientID), Endpoints: endpoints,
		Profile: client.ProfileFAPISecurity, Assurance: client.AssuranceDevelopment,
		ClientAuthMethod: storage.ClientAuthMethodAttestation, OAuthOnly: true,
		// Required by client.New; unused, as no authorization is started.
		AuthorizationResponseIssPolicy: client.RequireAuthorizationResponseIss,
		Algorithms:                     client.Algorithms{DPoP: fapi.ES256, ClientAttestationPoP: fapi.ES256},
		Limits:                         client.Limits{SessionLifetime: 5 * time.Minute, MaxClockSkew: 10 * time.Second, HTTPTimeout: 10 * time.Second, MaxHTTPResponseBytes: 1 << 20, MaxJOSECompactBytes: 16 * 1024},
	}, client.Dependencies{
		Keys: km, HTTP: is.httpClient(), Clock: client.SystemClock{}, Random: rand.Reader,
		Attestation: challenged{AttestationSource: client.StaticAttestation(wia), w: w, endpoint: challengeEndpoint},
	})
	if err != nil {
		t.Fatalf("oauth client: %v", err)
	}

	tok, err := w.RequestPreAuthorizedCodeToken(ctx, endpoints.Token, wallet.PreAuthorizedCodeTokenRequest{
		PreAuthorizedCode: plan.PreAuthorizedCode, DPoPKey: dpopKey, ClientAttestation: oauth,
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok.TokenType != "DPoP" || len(tok.AuthorizationDetails) != 1 || len(tok.AuthorizationDetails[0].CredentialIdentifiers) != 1 {
		t.Fatalf("token response: type %q, authorization_details %+v", tok.TokenType, tok.AuthorizationDetails)
	}
	return session{
		w: w, issuerID: offer.CredentialIssuer, metadata: md,
		resource: w.DPoPResourceClient(tok.AccessToken, dpopKey),
		credID:   tok.AuthorizationDetails[0].CredentialIdentifiers[0],
	}
}

// challenged is the WIA source with a challenge from the authorization
// server's challenge endpoint, as walletflow builds it
// (client.ChallengeSource).
type challenged struct {
	client.AttestationSource
	w        *wallet.Wallet
	endpoint fapi.URL
}

func (c challenged) CurrentChallenge(ctx context.Context) (string, error) {
	return c.w.RequestAttestationChallenge(ctx, c.endpoint)
}

// nonce fetches a c_nonce.
func (s session) nonce(t *testing.T) string {
	t.Helper()
	n, err := s.w.RequestNonce(context.Background(), *s.metadata.NonceEndpoint)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return n.CNonce
}

// request sends a credential request for the session's identifier and
// verifies every credential with wallet.VerifyIssuedCredential, the i-th
// bound to holders[i].
func (s session) request(t *testing.T, is *issuer, req wallet.CredentialRequest, holders []*ecdsa.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	req.CredentialIdentifier = s.credID
	res, err := s.w.RequestCredential(ctx, s.resource, s.metadata.CredentialEndpoint, req)
	if err != nil {
		t.Fatalf("credential request: %v", err)
	}
	if len(res.Credentials) != len(holders) {
		t.Fatalf("want %d credentials, got %d", len(holders), len(res.Credentials))
	}
	conf := s.metadata.CredentialConfigurationsSupported[incomeConfig]
	for i, c := range res.Credentials {
		v, err := wallet.VerifyIssuedCredential(ctx, wallet.VerifyIssuedCredentialParams{
			Configuration: conf, Credential: c.Credential, HolderKey: &holders[i].PublicKey,
			IssuerRoots: is.issuerRoots(), CredentialIssuer: s.issuerID,
		})
		if err != nil {
			t.Fatalf("credential %d: %v", i, err)
		}
		if v.Claims["year"] != "2025" || v.StatusList == nil {
			t.Fatalf("credential %d: claims %v status %v", i, v.Claims, v.StatusList)
		}
	}
}

func publicKeys(keys []*ecdsa.PrivateKey) []*ecdsa.PublicKey {
	out := make([]*ecdsa.PublicKey, len(keys))
	for i, k := range keys {
		out[i] = &k.PublicKey
	}
	return out
}

func newKeys(t *testing.T, n int) []*ecdsa.PrivateKey {
	out := make([]*ecdsa.PrivateKey, n)
	for i := range out {
		out[i] = testpki.NewKey(t)
	}
	return out
}

func TestWalletEUDI(t *testing.T) {
	is := newIssuer(t)
	w := newCoreWallet(t, is)

	t.Run("jwt proof with key_attestation (Appendix D.1, F.1)", func(t *testing.T) {
		s := redeem(t, is, w, "eudi", "haip-vci")
		holders := newKeys(t, 3)
		nonce := s.nonce(t)
		ka, err := is.provider.KeyAttestation(context.Background(), publicKeys(holders), nonce)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := w.GenerateProofWithKeyAttestation(holders[0], s.issuerID, nonce, ka)
		if err != nil {
			t.Fatal(err)
		}
		s.request(t, is, wallet.CredentialRequest{JWTProofs: []string{proof}}, holders)
	})

	t.Run("attestation proof type (Appendix F.3)", func(t *testing.T) {
		s := redeem(t, is, w, "eudi", "haip-vci")
		holders := newKeys(t, 2)
		claims, err := is.provider.keyAttestationClaims(publicKeys(holders))
		if err != nil {
			t.Fatal(err)
		}
		ka, err := w.GenerateAttestationProof(is.provider.signer.Key, oid4vci.ES256, is.provider.header(), claims, s.nonce(t))
		if err != nil {
			t.Fatal(err)
		}
		s.request(t, is, wallet.CredentialRequest{Attestation: ka}, holders)
	})

	t.Run("plain jwt proofs are refused", func(t *testing.T) {
		s := redeem(t, is, w, "eudi", "haip-vci")
		holders := newKeys(t, 1)
		_, err := w.RequestCredential(context.Background(), s.resource, s.metadata.CredentialEndpoint, wallet.CredentialRequest{
			CredentialIdentifier: s.credID, Keys: []crypto.Signer{holders[0]}, CredentialIssuer: s.issuerID, Nonce: s.nonce(t),
		})
		var werr *wallet.Error
		if !errors.As(err, &werr) || werr.Code != "invalid_proof" {
			t.Fatalf("want invalid_proof, got %v", err)
		}
	})
}

// The nl profile: plain jwt proofs, one per key.
func TestWalletNL(t *testing.T) {
	is := newIssuer(t)
	w := newCoreWallet(t, is)
	s := redeem(t, is, w, "nl", "openid-credential-offer")
	holders := newKeys(t, 2)
	signers := []crypto.Signer{holders[0], holders[1]}
	s.request(t, is, wallet.CredentialRequest{Keys: signers, CredentialIssuer: s.issuerID, Nonce: s.nonce(t)}, holders)
}
