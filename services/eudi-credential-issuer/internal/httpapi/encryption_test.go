package httpapi_test

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// E1-E9: encrypted credential requests and responses (OpenID4VCI 1.0
// sections 8.2 and 10), published and supported on the eudi profile only.

// encryptionSetup is a fixture with an eudi wallet and the tax provider's
// request encryption key.
type encryptionSetup struct {
	f         *fixture
	eudi      *wallet
	issuerKey jose.JSONWebKey
}

func newEncryptionSetup(t *testing.T) encryptionSetup {
	f := newFixture(t)
	return encryptionSetup{f: f, eudi: f.wallet("eudi"), issuerKey: f.requestKey(t, "tax")}
}

// token returns a fresh access token and its credential identifier.
func (s encryptionSetup) token() (string, string) {
	tok := s.eudi.token(s.f.preAuthCode("eudi"))
	return tok.AccessToken, tok.AuthorizationDetails[0].CredentialIdentifiers[0]
}

// attestation returns an attestation proof for one key with a fresh nonce.
func (s encryptionSetup) attestation(t *testing.T) *oid4vci.Proofs {
	ka := s.f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, s.eudi.nonce(), time.Now().Add(time.Hour))
	return &oid4vci.Proofs{Attestation: []string{ka}}
}

func TestEncryptionMetadata(t *testing.T) {
	s := newEncryptionSetup(t)
	f, issuerKey := s.f, s.issuerKey
	t.Run("E1 metadata", func(t *testing.T) {
		md := f.metadata(t, "tax", "eudi")
		reqEnc, _ := md["credential_request_encryption"].(map[string]any)
		resEnc, _ := md["credential_response_encryption"].(map[string]any)
		if reqEnc == nil || resEnc == nil || reqEnc["encryption_required"] != false || resEnc["encryption_required"] != false {
			t.Fatalf("both encryption objects, not required: %v", md)
		}
		if issuerKey.KeyID == "" || issuerKey.Algorithm != "ECDH-ES" || issuerKey.Use != "enc" || !issuerKey.IsPublic() {
			t.Fatalf("published key %+v", issuerKey)
		}
		for _, o := range []map[string]any{reqEnc, resEnc} {
			if encs, _ := json.Marshal(o["enc_values_supported"]); string(encs) != `["A128GCM","A256GCM"]` {
				t.Fatalf("enc_values_supported %s", encs)
			}
			if _, zip := o["zip_values_supported"]; zip {
				t.Fatal("compression must not be published")
			}
		}
		if algs, _ := json.Marshal(resEnc["alg_values_supported"]); string(algs) != `["ECDH-ES"]` {
			t.Fatalf("alg_values_supported %s", algs)
		}
		if civil := f.requestKey(t, "civil"); civil.KeyID == issuerKey.KeyID {
			t.Fatal("each provider has its own request encryption key")
		}
	})
	t.Run("E1 signed metadata", func(t *testing.T) {
		// The EUDI wallet asks for the signed form.
		_, body, _ := get(t, f.public.URL+"/.well-known/openid-credential-issuer/tax/eudi", "application/jwt")
		parts := strings.Split(body, ".")
		if len(parts) != 3 {
			t.Fatalf("not a JWT: %s", body)
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		var md struct {
			RequestEncryption struct {
				JWKS jose.JSONWebKeySet `json:"jwks"`
			} `json:"credential_request_encryption"`
			ResponseEncryption map[string]any `json:"credential_response_encryption"`
		}
		if err := json.Unmarshal(payload, &md); err != nil || len(md.RequestEncryption.JWKS.Keys) != 1 ||
			md.RequestEncryption.JWKS.Keys[0].KeyID != issuerKey.KeyID || md.ResponseEncryption == nil {
			t.Fatalf("signed metadata: %s %v", payload, err)
		}
	})
	t.Run("E9 nl publishes no encryption", func(t *testing.T) {
		md := f.metadata(t, "tax", "nl")
		if _, ok := md["credential_request_encryption"]; ok {
			t.Fatalf("nl: %v", md)
		}
		if _, ok := md["credential_response_encryption"]; ok {
			t.Fatalf("nl: %v", md)
		}
	})
}

func TestEncryptedIssuance(t *testing.T) {
	s := newEncryptionSetup(t)
	eudi, issuerKey := s.eudi, s.issuerKey
	for _, enc := range []string{"A128GCM", "A256GCM"} {
		t.Run("E2 encrypted request and response "+enc, func(t *testing.T) {
			at, credID := s.token()
			walletKey, params := testwallet.ResponseEncryption(t, enc)
			status, ctype, body := eudi.encryptedCredential(t, at, issuerKey, jose.A256GCM, oid4vci.CredentialRequest{
				CredentialIdentifier: credID, Proofs: s.attestation(t), ResponseEncryption: &params,
			})
			if status != http.StatusOK || ctype != "application/jwt" {
				t.Fatalf("%d %s %s", status, ctype, body)
			}
			h, plain := testwallet.DecryptResponse(t, body, walletKey)
			var resp oid4vci.CredentialResponse
			if err := json.Unmarshal(plain, &resp); err != nil || len(resp.Credentials) != 1 {
				t.Fatalf("%s %v", plain, err)
			}
			if h.KeyID != "wallet-enc" || h.ExtraHeaders["enc"] != enc {
				t.Fatalf("the JWE must use the wallet's kid and enc: %+v", h)
			}
		})
	}
	t.Run("E2 wallet key without kid, media type with parameter", func(t *testing.T) {
		at, credID := s.token()
		walletKey := testpki.NewKey(t)
		jwk, _ := json.Marshal(jose.JSONWebKey{Key: &walletKey.PublicKey, Algorithm: "ECDH-ES"})
		raw, _ := json.Marshal(oid4vci.CredentialRequest{CredentialIdentifier: credID, Proofs: s.attestation(t),
			ResponseEncryption: &oid4vci.ResponseEncryption{JWK: jwk, Enc: "A256GCM"}})
		status, ctype, body := eudi.postJWTAs(t, at, "application/jwt; charset=utf-8", testwallet.EncryptRequest(t, raw, issuerKey, jose.A256GCM, nil))
		if status != http.StatusOK || ctype != "application/jwt" {
			t.Fatalf("%d %s %s", status, ctype, body)
		}
		if h, _ := testwallet.DecryptResponse(t, body, walletKey); h.KeyID != "" {
			t.Fatalf("no kid when the wallet key has none: %+v", h)
		}
	})
	t.Run("E4 encrypted request, plain response", func(t *testing.T) {
		at, credID := s.token()
		status, ctype, body := eudi.encryptedCredential(t, at, issuerKey, jose.A128GCM, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: s.attestation(t),
		})
		if status != http.StatusOK || ctype != "application/json" || !strings.Contains(body, `"credentials"`) {
			t.Fatalf("%d %s %s", status, ctype, body)
		}
	})
}

func TestEncryptionRefusals(t *testing.T) {
	s := newEncryptionSetup(t)
	f, eudi, issuerKey := s.f, s.eudi, s.issuerKey
	// retry checks that a refusal used up neither the c_nonce of proofs nor
	// the credential: the same proofs still work.
	retry := func(t *testing.T, at, credID string, proofs *oid4vci.Proofs) {
		t.Helper()
		if status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: proofs,
		}); status != http.StatusOK {
			t.Fatalf("retry with the same proofs: %d %v", status, body)
		}
	}
	t.Run("E5 response key in a plain request", func(t *testing.T) {
		at, credID := s.token()
		proofs := s.attestation(t)
		_, params := testwallet.ResponseEncryption(t, "A256GCM")
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: proofs, ResponseEncryption: &params,
		})
		if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidEncryptionParameters {
			t.Fatalf("%d %v", status, body)
		}
		retry(t, at, credID, proofs)
	})
	t.Run("E6 unsupported response parameters", func(t *testing.T) {
		at, credID := s.token()
		proofs := s.attestation(t)
		_, a192 := testwallet.ResponseEncryption(t, "A192GCM")
		_, zipped := testwallet.ResponseEncryption(t, "A256GCM")
		zipped.Zip = "DEF"
		signing, _ := json.Marshal(jose.JSONWebKey{Key: &testpki.NewKey(t).PublicKey, Algorithm: "ECDH-ES", Use: "sig"})
		for name, p := range map[string]oid4vci.ResponseEncryption{
			"A192GCM": a192, "zip": zipped, "signing key": {JWK: signing, Enc: "A256GCM"},
		} {
			status, _, body := eudi.encryptedCredential(t, at, issuerKey, jose.A256GCM, oid4vci.CredentialRequest{
				CredentialIdentifier: credID, Proofs: proofs, ResponseEncryption: &p,
			})
			if status != http.StatusBadRequest || !strings.Contains(body, oid4vci.ErrInvalidEncryptionParameters) {
				t.Fatalf("%s: %d %s", name, status, body)
			}
		}
		retry(t, at, credID, proofs)
	})
	t.Run("E7 undecryptable request", func(t *testing.T) {
		at, credID := s.token()
		proofs := s.attestation(t)
		otherKey := oid4vci.RequestKey{KeyID: issuerKey.KeyID, Key: testpki.NewKey(t)}.PublicJWK()
		raw, _ := json.Marshal(oid4vci.CredentialRequest{CredentialIdentifier: credID, Proofs: proofs})
		for name, body := range map[string]string{
			"another key":   testwallet.EncryptRequest(t, raw, otherKey, jose.A256GCM, nil),
			"compressed":    testwallet.EncryptRequest(t, raw, issuerKey, jose.A256GCM, &jose.EncrypterOptions{Compression: jose.DEFLATE}),
			"not a JWE":     testwallet.JWTProof(t, testpki.NewKey(t), "aud", "n", ""),
			"empty":         " ",
			"not a request": testwallet.EncryptRequest(t, []byte(`[1]`), issuerKey, jose.A256GCM, nil),
		} {
			status, _, resp := eudi.postJWT(t, at, body)
			if status != http.StatusBadRequest || !strings.Contains(resp, oid4vci.ErrInvalidCredentialRequest) {
				t.Fatalf("%s: %d %s", name, status, resp)
			}
		}
		retry(t, at, credID, proofs)
	})
	t.Run("E8 errors are not encrypted", func(t *testing.T) {
		at, credID := s.token()
		_, params := testwallet.ResponseEncryption(t, "A256GCM")
		status, ctype, body := eudi.encryptedCredential(t, at, issuerKey, jose.A256GCM, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, ResponseEncryption: &params,
			Proofs: &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.id("eudi"), eudi.nonce(), "")}},
		})
		if status != http.StatusBadRequest || ctype != "application/json" || !strings.Contains(body, oid4vci.ErrInvalidProof) {
			t.Fatalf("%d %s %s", status, ctype, body)
		}
	})
	t.Run("E9 nl refuses encryption", func(t *testing.T) {
		nl := f.wallet("nl")
		tok := nl.token(f.preAuthCode("nl"))
		credID := tok.AuthorizationDetails[0].CredentialIdentifiers[0]
		raw, _ := json.Marshal(oid4vci.CredentialRequest{CredentialIdentifier: credID})
		status, _, body := nl.postJWT(t, tok.AccessToken, testwallet.EncryptRequest(t, raw, issuerKey, jose.A256GCM, nil))
		if status != http.StatusBadRequest || !strings.Contains(body, oid4vci.ErrInvalidCredentialRequest) {
			t.Fatalf("encrypted request: %d %s", status, body)
		}
		_, params := testwallet.ResponseEncryption(t, "A256GCM")
		status, resp := nl.credentialRequest(tok.AccessToken, nl.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, ResponseEncryption: &params,
		})
		if status != http.StatusBadRequest || resp["error"] != oid4vci.ErrInvalidCredentialRequest {
			t.Fatalf("response key: %d %v", status, resp)
		}
	})
}

// metadata returns the unsigned issuer metadata of a provider and profile.
func (f *fixture) metadata(t *testing.T, provider, profile string) map[string]any {
	t.Helper()
	status, body, _ := get(t, f.public.URL+"/.well-known/openid-credential-issuer/"+provider+"/"+profile, "application/json")
	var md map[string]any
	if err := json.Unmarshal([]byte(body), &md); status != http.StatusOK || err != nil {
		t.Fatalf("metadata: %d %v", status, err)
	}
	return md
}

// requestKey returns the request encryption key a provider publishes on the
// eudi profile.
func (f *fixture) requestKey(t *testing.T, provider string) jose.JSONWebKey {
	t.Helper()
	var md struct {
		RequestEncryption struct {
			JWKS jose.JSONWebKeySet `json:"jwks"`
		} `json:"credential_request_encryption"`
	}
	remarshal(t, f.metadata(t, provider, "eudi"), &md)
	if len(md.RequestEncryption.JWKS.Keys) != 1 {
		t.Fatalf("want one request encryption key: %+v", md)
	}
	return md.RequestEncryption.JWKS.Keys[0]
}

// encryptedCredential posts req encrypted to key and returns the status,
// the media type and the raw body of the response.
func (w *wallet) encryptedCredential(t *testing.T, accessToken string, key jose.JSONWebKey, enc jose.ContentEncryption, req oid4vci.CredentialRequest) (int, string, string) {
	t.Helper()
	raw, _ := json.Marshal(req)
	return w.postJWT(t, accessToken, testwallet.EncryptRequest(t, raw, key, enc, nil))
}

// postJWT posts an application/jwt credential request.
func (w *wallet) postJWT(t *testing.T, accessToken, body string) (int, string, string) {
	t.Helper()
	return w.postJWTAs(t, accessToken, "application/jwt", body)
}

// postJWTAs posts a credential request with the given media type.
func (w *wallet) postJWTAs(t *testing.T, accessToken, mediaType, body string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.f.idOf(w.issuer, w.profile)+"/credential", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mediaType)
	req.Header.Set("Authorization", "DPoP "+accessToken)
	req.Header.Set("DPoP", testwallet.DPoP(t, w.dpopKey, "POST", w.f.idOf(w.issuer, w.profile)+"/credential", accessToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(out)
}

// M-4 (OpenID4VCI 1.0 section 12.2.4): a display without a locale is
// published without one, not with an empty language tag.
func TestADisplayWithoutLocale(t *testing.T) {
	f := newFixtureWith(t, func(_ *fixture, cfg *issuance.Config) {
		cfg.Configurations[0].Display = []issuance.Display{{Name: "Income statement"}}
	})
	configs, _ := f.metadata(t, "tax", "eudi")["credential_configurations_supported"].(map[string]any)
	c, _ := configs[incomeConfig].(map[string]any)
	cm, _ := c["credential_metadata"].(map[string]any)
	display, _ := cm["display"].([]any)
	if len(display) != 1 {
		t.Fatalf("credential_metadata = %v, want one display", cm)
	}
	if d, _ := display[0].(map[string]any); d["name"] != "Income statement" || d["locale"] != nil {
		t.Errorf("display = %v, want a name and no locale", d)
	}
}
