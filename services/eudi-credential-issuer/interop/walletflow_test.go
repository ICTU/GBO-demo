package interop_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/idfoundry/oid4vcgo/walletflow"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// newWalletflow is OID4VCgo's wallet application (walletflow): it decides
// on its own, from the issuer's metadata, how to authenticate, which proof
// to send and what to check.
//
// Non-default options, and why:
//   - Development: the issuer runs on http://127.0.0.1 (httptest); without
//     it walletflow refuses loopback and plain http. It also skips the
//     revocation checkers production requires (no CRLs in this test PKI).
//   - IssuerMetadataRoots + RequireSignedIssuerMetadata: our issuer only
//     serves signed metadata; this makes the wallet verify it against our
//     test access root and refuse anything unsigned.
//   - IssuerRoots: our test issuer root, so it accepts the SD-JWT VCs.
//   - Provider: the OID4VCgo-side wallet provider of fixture_test.go.
//   - Dependencies.HTTP: a client trusting httptest's TLS certificate for
//     the issuer's public port (Development still requires https).
func newWalletflow(t *testing.T, is *issuer, profile walletflow.IssuanceProfile) *walletflow.Wallet {
	t.Helper()
	w, err := walletflow.New(walletflow.Config{
		ClientID:                    clientID,
		IssuerRoots:                 is.issuerRoots(),
		IssuerMetadataRoots:         is.accessRoots(),
		RequireSignedIssuerMetadata: true,
		IssuanceProfile:             profile,
		Development:                 true,
	}, walletflow.Dependencies{
		Keys:        walletflow.NewMemoryKeyStore(),
		Credentials: walletflow.NewMemoryCredentialStore(),
		Provider:    is.provider,
		HTTP:        is.httpClient(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// receive runs a whole issuance with walletflow from an offer link.
func receive(t *testing.T, w *walletflow.Wallet, link string) walletflow.IssuanceResult {
	t.Helper()
	ctx := context.Background()
	s, err := w.StartIssuance(ctx, link)
	if err != nil {
		t.Fatalf("start issuance (offer, signed metadata, AS metadata): %v", err)
	}
	defer func() { _ = s.Close(ctx) }()
	if s.Offer().Grant != walletflow.GrantPreAuthorizedCode {
		t.Fatalf("grant %v, want the pre-authorized code", s.Offer().Grant)
	}
	if err := s.RedeemPreAuthorizedCode(ctx, ""); err != nil {
		t.Fatalf("redeem pre-authorized code (challenge, WIA + PoP, DPoP): %v", err)
	}
	res, err := s.RequestCredentials(ctx)
	if err != nil {
		t.Fatalf("request credentials: %v", err)
	}
	for _, f := range res.Failed {
		t.Errorf("failed credential %s: %v", f.ConfigurationID, f.Err)
	}
	if len(res.Credentials) != 1 {
		t.Fatalf("want one credential, got %d (%d deferred, %d failed)", len(res.Credentials), len(res.Deferred), len(res.Failed))
	}
	return res
}

// checkSent checks what walletflow sent: signed metadata asked for, one
// token request with a challenged PoP and DPoP, and one credential request
// with DPoP and the given proofs.
func checkSent(t *testing.T, is *issuer, proofTypes []string, jwtProofs int, keyAttestation bool) {
	t.Helper()
	md := is.exchanges("")
	for _, e := range md {
		if strings.HasPrefix(e.Path, "/.well-known/openid-credential-issuer/") && !strings.Contains(e.Accept, "application/jwt") {
			t.Errorf("metadata fetched without Accept: application/jwt: %q", e.Accept)
		}
	}
	tokens := is.exchanges("/token")
	if len(tokens) != 1 || tokens[0].Status != 200 || !tokens[0].PoPChallenge || !tokens[0].DPoP {
		t.Errorf("token requests: %+v", tokens)
	}
	if n := len(is.exchanges("/challenge")); n != 1 {
		t.Errorf("want one challenge request, got %d", n)
	}
	creds := is.exchanges("/credential")
	if len(creds) != 1 {
		t.Fatalf("credential requests: %+v", creds)
	}
	c := creds[0]
	if !slices.Equal(c.ProofTypes, proofTypes) || c.JWTProofs != jwtProofs || c.JWTKeyAttestation != keyAttestation || !c.DPoP {
		t.Errorf("credential request: %+v", c)
	}
	t.Logf("wallet sent: %d token request(s) with challenged PoP; credential request proofs %v, %d jwt proof(s), key_attestation %v",
		len(tokens), c.ProofTypes, c.JWTProofs, c.JWTKeyAttestation)
}

func checkReceived(t *testing.T, is *issuer, profile string, c walletflow.StoredCredential, wantCopies int) {
	t.Helper()
	if c.CredentialIssuer != is.id(profile) || c.ConfigurationID != incomeConfig || c.VCT != incomeVCT || c.Format != "dc+sd-jwt" {
		t.Errorf("stored credential: issuer %q config %q vct %q format %q", c.CredentialIssuer, c.ConfigurationID, c.VCT, c.Format)
	}
	if c.Claims["year"] != "2025" || c.Claims["income"] != float64(41000) {
		t.Errorf("claims %v", c.Claims)
	}
	if addr, _ := c.Claims["address"].(map[string]any); addr["city"] != "Utrecht" {
		t.Errorf("nested claim address.city: %v", c.Claims["address"])
	}
	if len(c.Copies) != wantCopies {
		t.Errorf("want %d copies (batch), got %d", wantCopies, len(c.Copies))
	}
	if c.StatusList == nil {
		t.Error("no status list reference")
	}
}

// checkUnlinkable checks walletflow's own verdict on the batch: copies of
// one credential must not share anything a Verifier can link them by.
// RFC 9901 section 10.1: in batch issuance, "claims carrying time
// information, like iat, exp, and nbf, MUST either be randomized ... or
// rounded"; OpenID4VCI 1.0 section 15.4 says SHOULD.
func checkUnlinkable(t *testing.T, c walletflow.StoredCredential) {
	t.Run("batch copies unlinkable", func(t *testing.T) {
		for i, cp := range c.Copies {
			p := jwtPart(strings.SplitN(cp.Credential, "~", 2)[0], 1)
			t.Logf("copy %d: iat %v nbf %v exp %v status %v", i, p["iat"], p["nbf"], p["exp"], p["status"])
		}
		if c.CopiesCorrelated {
			t.Fatalf("walletflow finds the %d copies correlated (StoredCredential.CopiesCorrelated): "+
				"they share a non-day-aligned iat, so a Verifier shown two copies can link them", len(c.Copies))
		}
	})
}

// The eudi profile with walletflow held to HAIP 1.0 (ProfileHAIP): it must
// authenticate with a Wallet Attestation and a challenge, use DPoP, and
// send a key attestation (here: a jwt proof with key_attestation, since
// the issuer takes jwt proofs).
func TestWalletflowEUDIUnderHAIP(t *testing.T) {
	is := newIssuer(t)
	w := newWalletflow(t, is, walletflow.ProfileHAIP)
	res := receive(t, w, is.offer("eudi")["haip-vci"])
	// walletflow asks for min(DefaultBatchSize 5, batch_size 4) copies,
	// attested in one key attestation; our issuer issues one per key.
	checkReceived(t, is, "eudi", res.Credentials[0], 4)
	checkSent(t, is, []string{"jwt"}, 1, true)
	checkUnlinkable(t, res.Credentials[0])

	// walletflow accepts a status list only when it is signed with the
	// credential's own key, or by a certificate naming the credential's
	// iss (or a Status Issuer configured for that iss). Our credentials
	// have no iss and our status lists are signed with a separate status
	// certificate, so neither applies.
	t.Run("status list", func(t *testing.T) {
		// Known difference, decided: our status lists follow the NL Wallet
		// (no iss, a separate status certificate from the same CA, as Token
		// Status List section 11.3 recommends for a separate Status
		// Issuer); the spec leaves key resolution to the ecosystem.
		t.Skip("known difference: OID4VCgo resolves the status list signer through iss; see the issuer README")
		c, err := w.CheckStatus(context.Background(), res.Credentials[0].ID)
		if err != nil {
			t.Fatalf("check status: %v", err)
		}
		if c.Status.Value != walletflow.StatusValid {
			t.Fatalf("status %q", c.Status.Value)
		}
	})
}

// A credential configuration without a scope: a wallet held to HAIP 1.0
// refuses such an issuer ("The Credential Issuer metadata MUST include a
// scope for every Credential Configuration it supports", HAIP 1.0 section
// 4.1), so the issuer refuses to start with it.
func TestIssuerRefusesConfigurationWithoutScope(t *testing.T) {
	_, err := tryIssuer(t, func(c *issuance.Config) { c.Configurations[0].Scope = "" })
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("want the issuer to refuse a configuration without scope, got %v", err)
	}
}

// The eudi profile with walletflow's default profile (plain OpenID4VCI
// 1.0): it follows key_attestations_required from the metadata.
func TestWalletflowEUDIDefaultProfile(t *testing.T) {
	is := newIssuer(t)
	w := newWalletflow(t, is, walletflow.ProfileOpenID4VCI)
	res := receive(t, w, is.offer("eudi")["openid-credential-offer"])
	checkReceived(t, is, "eudi", res.Credentials[0], 4)
	checkSent(t, is, []string{"jwt"}, 1, true)
	checkUnlinkable(t, res.Credentials[0])
}

// The nl profile: no key attestation required, so walletflow sends plain
// jwt proofs, one per copy. Client authentication is still a Wallet
// Attestation, since the nl profile does not allow anonymous access.
func TestWalletflowNL(t *testing.T) {
	is := newIssuer(t)
	w := newWalletflow(t, is, walletflow.ProfileOpenID4VCI)
	res := receive(t, w, is.offer("nl")["openid-credential-offer"])
	checkReceived(t, is, "nl", res.Credentials[0], 4)
	checkSent(t, is, []string{"jwt"}, 4, false)
	checkUnlinkable(t, res.Credentials[0])
}

// The nl profile under ProfileHAIP: walletflow would send a key
// attestation wherever the issuer takes one; the nl profile takes none.
func TestWalletflowNLUnderHAIP(t *testing.T) {
	is := newIssuer(t)
	w := newWalletflow(t, is, walletflow.ProfileHAIP)
	res := receive(t, w, is.offer("nl")["openid-credential-offer"])
	checkReceived(t, is, "nl", res.Credentials[0], 4)
	checkSent(t, is, []string{"jwt"}, 4, false)
	checkUnlinkable(t, res.Credentials[0])
}
