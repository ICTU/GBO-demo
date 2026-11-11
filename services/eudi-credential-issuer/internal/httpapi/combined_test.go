package httpapi_test

import (
	"crypto/ecdsa"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// A1: one identifier for both wallets: jwt without key attestation for the
// NL Wallet, attestation with it for the EUDI wallets, encryption and the
// NL preview endpoint.
func TestCombinedMetadata(t *testing.T) {
	f := newFixture(t)
	md := f.metadata(t, "tax", "wallet")
	proofs := md["credential_configurations_supported"].(map[string]any)[incomeConfig].(map[string]any)["proof_types_supported"].(map[string]any)
	if _, ok := proofs["jwt"].(map[string]any)["key_attestations_required"]; ok {
		t.Error("the jwt proof type must not require key attestation")
	}
	required, ok := proofs["attestation"].(map[string]any)["key_attestations_required"].(map[string]any)
	if !ok || required["key_storage"].([]any)[0] != "iso_18045_high" {
		t.Errorf("attestation proof type: %v", proofs["attestation"])
	}
	for _, name := range []string{"credential_request_encryption", "credential_response_encryption", "credential_preview_endpoint", "issuer_info"} {
		if md[name] == nil {
			t.Errorf("%s is missing", name)
		}
	}
}

// combinedToken redeems a fresh offer on the combined profile as a wallet
// of kind as.
func combinedToken(t *testing.T, f *fixture, as string) (*wallet, string, string) {
	t.Helper()
	w := f.walletOn("wallet", as)
	tok := w.token(f.preAuthCode("wallet"))
	return w, tok.AccessToken, tok.AuthorizationDetails[0].CredentialIdentifiers[0]
}

// A2: both wallet providers are trusted, no other.
func TestCombinedToken(t *testing.T) {
	f := newFixture(t)
	for _, as := range []string{"nl", "eudi"} {
		combinedToken(t, f, as)
	}
	stranger := f.walletOn("wallet", "eudi")
	stranger.provider = testwallet.NewProvider(t, "unknown wallet provider")
	stranger.wia = stranger.provider.WIA(t, stranger.clientID, stranger.wiaKey, time.Now().Add(time.Hour))
	if status, body := stranger.tokenRequest(f.preAuthCode("wallet"), nil); status != http.StatusUnauthorized || body["error"] != oid4vci.ErrInvalidClientAttestation {
		t.Fatalf("another wallet provider: %d %v", status, body)
	}
}

// A3: an EUDI wallet must attest its keys on the combined profile.
func TestCombinedEUDIMustAttest(t *testing.T) {
	f := newFixture(t)
	request := func(proofs func(w *wallet) *oid4vci.Proofs) (int, map[string]any) {
		w, at, credID := combinedToken(t, f, "eudi")
		return w.credentialRequest(at, w.dpopKey, oid4vci.CredentialRequest{CredentialIdentifier: credID, Proofs: proofs(w)})
	}
	if status, body := request(func(w *wallet) *oid4vci.Proofs {
		ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
		return &oid4vci.Proofs{Attestation: []string{ka}}
	}); status != http.StatusOK {
		t.Errorf("attestation proof: %d %v", status, body)
	}
	if status, body := request(func(w *wallet) *oid4vci.Proofs {
		key, nonce := testpki.NewKey(t), w.nonce()
		ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{key}, nonce, time.Now().Add(time.Hour))
		return &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, key, f.id("wallet"), nonce, ka)}}
	}); status != http.StatusOK {
		t.Errorf("jwt proof with key attestation: %d %v", status, body)
	}
	status, body := request(func(w *wallet) *oid4vci.Proofs {
		return &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.id("wallet"), w.nonce(), "")}}
	})
	if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidProof || !strings.Contains(body["error_description"].(string), "key attestation is required") {
		t.Errorf("jwt proof without key attestation: %d %v", status, body)
	}
}

// A4: an NL Wallet may prove possession without key attestation, and sees
// its preview first.
func TestCombinedNLPlainJWT(t *testing.T) {
	f := newFixture(t)
	w, at, credID := combinedToken(t, f, "nl")
	checkPreview(t, w, at, credID)
	nonce := w.nonce()
	status, body := w.credentialRequest(at, w.dpopKey, oid4vci.CredentialRequest{CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{JWT: []string{
		testwallet.JWTProof(t, testpki.NewKey(t), f.id("wallet"), nonce, ""),
		testwallet.JWTProof(t, testpki.NewKey(t), f.id("wallet"), nonce, ""),
	}}})
	if status != http.StatusOK || len(body["credentials"].([]any)) != 2 {
		t.Fatalf("plain jwt proofs: %d %v", status, body)
	}
}

// A5: the combined profile accepts encrypted requests.
func TestCombinedEncryption(t *testing.T) {
	f := newFixture(t)
	w, at, credID := combinedToken(t, f, "eudi")
	ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
	status, mediaType, body := w.encryptedCredential(t, at, f.requestKey(t, "tax"), jose.A128GCM,
		oid4vci.CredentialRequest{CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}}})
	if status != http.StatusOK {
		t.Fatalf("encrypted request: %d %s %s", status, mediaType, body)
	}
}

// A6 and A8: the offer carries the one link both wallets read; disclosure
// offers are not served on the combined profile.
func TestCombinedOfferLinks(t *testing.T) {
	f := newFixture(t)
	status, body := f.offer(map[string]any{
		"profile": "wallet", "credential_configuration_ids": []string{incomeConfig},
		"subject": map[string]any{"bsn": "999990019"}, "parameters": map[string]string{"jaar": "2025"},
	})
	links, _ := body["links"].(map[string]any)
	if status != http.StatusCreated || !strings.HasPrefix(links["openid-credential-offer"].(string), "openid-credential-offer://?") ||
		!strings.HasPrefix(links["haip-vci"].(string), "haip-vci://?") ||
		!strings.HasPrefix(links["universal_link"].(string), "https://app.example.test/deeplink/credential-offer?") {
		t.Fatalf("%d %v", status, body)
	}
	if status, _ := f.fetchRequest(t, "tax", "wallet", incomeOffer); status != http.StatusNotFound {
		t.Errorf("disclosure request on the combined profile: %d", status)
	}
}

// The preview is the NL Wallet's: an EUDI wallet's token stays bound to
// DPoP on the combined profile.
func TestCombinedPreviewOnlyForNL(t *testing.T) {
	f := newFixture(t)
	w, at, _ := combinedToken(t, f, "eudi")
	if status, body := w.post("/credential_preview", nil, map[string]string{"Authorization": "Bearer " + at}); status != http.StatusUnauthorized || body["error"] != oid4vci.ErrInvalidToken {
		t.Fatalf("%d %v", status, body)
	}
}

// An NL Wallet that attests its keys, as later versions may, is served too.
func TestCombinedNLWithKeyAttestation(t *testing.T) {
	f := newFixture(t)
	w, at, credID := combinedToken(t, f, "nl")
	ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
	if status, body := w.credentialRequest(at, w.dpopKey, oid4vci.CredentialRequest{CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}}}); status != http.StatusOK {
		t.Fatalf("%d %v", status, body)
	}
}
