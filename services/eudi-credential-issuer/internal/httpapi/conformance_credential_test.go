package httpapi_test

// Conformance of the credential endpoint (OpenID4VCI 1.0 sections 8.2,
// 8.3, Appendices A.3, D.1 and F; HAIP 1.0 section 6.1): the request
// parameters, the key proofs and key attestations, the response members
// and the algorithms in the metadata. The case IDs are those of
// conformance/oid4vci-1.0.yaml and conformance/haip-1.0.yaml.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

// credentialProfiles are the profiles with the wallets they serve; the
// combined profile serves both.
var credentialProfiles = []struct{ name, profile, as string }{
	{"nl", "nl", "nl"},
	{"eudi", "eudi", "eudi"},
	{"wallet as nl", "wallet", "nl"},
	{"wallet as eudi", "wallet", "eudi"},
}

// credentialCase is a fresh offer redeemed by a wallet of kind as on a
// profile: the access token and its credential identifier.
type credentialCase struct {
	f      *fixture
	w      *wallet
	as     string
	at     string
	credID string
}

func newCredentialCase(t *testing.T, f *fixture, profile, as string) *credentialCase {
	t.Helper()
	w := f.walletOn(profile, as)
	tok := w.token(f.preAuthCode(profile))
	return &credentialCase{f: f, w: w, as: as, at: tok.AccessToken, credID: tok.AuthorizationDetails[0].CredentialIdentifiers[0]}
}

// aud is the credential issuer identifier the case's proofs are for.
func (c *credentialCase) aud() string { return c.f.idOf(c.w.issuer, c.w.profile) }

// proofs returns valid proofs for one fresh key with a fresh c_nonce: a
// plain jwt proof from the NL Wallet, an attestation proof from an EUDI
// wallet.
func (c *credentialCase) proofs(t *testing.T) *oid4vci.Proofs {
	t.Helper()
	if c.as == "eudi" {
		return &oid4vci.Proofs{Attestation: []string{c.keyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, c.w.nonce())}}
	}
	return &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), c.aud(), c.w.nonce(), "")}}
}

// keyAttestation is a valid key attestation from the EUDI wallet provider
// at the levels the profiles require.
func (c *credentialCase) keyAttestation(t *testing.T, keys []*ecdsa.PrivateKey, nonce string) string {
	t.Helper()
	return c.f.eudiWallet.KeyAttestation(t, keys, nonce, time.Now().Add(time.Hour))
}

func (c *credentialCase) request(req oid4vci.CredentialRequest) (int, map[string]any) {
	return c.w.credentialRequest(c.at, c.w.dpopKey, req)
}

// requestJSON posts body as it is, for requests the CredentialRequest type
// cannot express.
func (c *credentialCase) requestJSON(body map[string]any) (int, map[string]any) {
	raw, err := json.Marshal(body)
	if err != nil {
		c.f.t.Fatal(err)
	}
	return c.w.post("/credential", bytes.NewReader(raw), map[string]string{
		"Authorization": "DPoP " + c.at,
		"DPoP":          testwallet.DPoP(c.f.t, c.w.dpopKey, "POST", c.aud()+"/credential", c.at),
	}, "Content-Type", "application/json")
}

// issue asks for the credential with valid proofs and wants it issued.
func (c *credentialCase) issue(t *testing.T) map[string]any {
	t.Helper()
	status, body := c.request(oid4vci.CredentialRequest{CredentialIdentifier: c.credID, Proofs: c.proofs(t)})
	if status != http.StatusOK || len(credentialsOf(body)) == 0 {
		t.Fatalf("a valid request: %d %v", status, body)
	}
	return body
}

func credentialsOf(body map[string]any) []string {
	list, _ := body["credentials"].([]any)
	var out []string
	for _, c := range list {
		if m, ok := c.(map[string]any); ok {
			s, _ := m["credential"].(string)
			out = append(out, s)
		}
	}
	return out
}

func expectCredentialError(t *testing.T, what string, status int, body map[string]any, code string) {
	t.Helper()
	if status != http.StatusBadRequest || body["error"] != code {
		t.Errorf("%s: %d %v, want 400 %s", what, status, body, code)
	}
}

// credentialJWS signs claims as a compact JWS with alg and key, with
// exactly the given header parameters besides alg. Alg "none" gives an
// unsecured JWS (RFC 7515 Appendix A.5).
func credentialJWS(t *testing.T, alg jose.SignatureAlgorithm, key any, header map[jose.HeaderKey]any, claims map[string]any) string {
	t.Helper()
	if alg == "none" {
		h := map[string]any{"alg": "none"}
		for k, v := range header {
			h[string(k)] = v
		}
		hj, _ := json.Marshal(h)
		cj, _ := json.Marshal(claims)
		return base64.RawURLEncoding.EncodeToString(hj) + "." + base64.RawURLEncoding.EncodeToString(cj) + "."
	}
	opts := &jose.SignerOptions{}
	for k, v := range header {
		opts = opts.WithHeader(k, v)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// HAIP-6.1-02: an issued SD-JWT VC has a limited validity: exp is nbf, the
// start of the day of the offer, plus the configuration's valid_days (365
// in the fixture).
func TestIssuedCredentialValidity(t *testing.T) {
	f := newFixture(t)
	anchors := []x5c.Anchors{x5c.NewAnchors("issuer", f.issuerRoot.Cert)}
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			c := newCredentialCase(t, f, p.profile, p.as)
			for _, cred := range credentialsOf(c.issue(t)) {
				parsed, err := sdjwtvc.Parse(cred, anchors, mdlIssuerEKU, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				exp, okExp := parsed.Claims["exp"].(float64)
				nbf, okNbf := parsed.Claims["nbf"].(float64)
				if !okExp || !okNbf {
					t.Fatalf("exp and nbf are required: %v", parsed.Claims)
				}
				if int64(nbf)%86400 != 0 || int64(exp-nbf) != 365*86400 || time.Unix(int64(exp), 0).Before(time.Now()) {
					t.Fatalf("nbf %v, exp %v: want exp 365 days after the start of the day", nbf, exp)
				}
				if parsed.Claims["status"] == nil {
					t.Fatal("a status claim is required")
				}
			}
		})
	}
}

// OID4VCI-8.2-01, 8.2-03, 8.2-05, 8.2-07: the token response returns
// authorization_details with credential_identifiers, so a credential
// request must name a credential_identifier and must not carry a
// credential_configuration_id, alone or with the identifier. The refusals
// use up neither the credential nor the c_nonce of the proofs.
func TestCredentialRequestIdentifiers(t *testing.T) {
	f := newFixture(t)
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			c := newCredentialCase(t, f, p.profile, p.as)
			proofs := c.proofs(t)
			for what, req := range map[string]oid4vci.CredentialRequest{
				"8.2-01 no credential_identifier":               {Proofs: proofs},
				"8.2-05 credential_configuration_id instead":    {CredentialConfigurationID: incomeConfig, Proofs: proofs},
				"8.2-03 8.2-07 credential_configuration_id too": {CredentialIdentifier: c.credID, CredentialConfigurationID: incomeConfig, Proofs: proofs},
			} {
				status, body := c.request(req)
				expectCredentialError(t, what, status, body, oid4vci.ErrInvalidCredentialRequest)
			}
			if status, body := c.request(oid4vci.CredentialRequest{CredentialIdentifier: c.credID, Proofs: proofs}); status != http.StatusOK {
				t.Fatalf("the same proofs with credential_identifier alone: %d %v", status, body)
			}
		})
	}
}

// OID4VCI-8.2-12, F-01, F-03: every configuration publishes
// proof_types_supported, so proofs is required; a jwt proof type needs a
// non-empty array, the attestation proof type exactly one attestation.
func TestCredentialRequestProofs(t *testing.T) {
	f := newFixture(t)
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			cfg := f.metadata(t, "tax", p.profile)["credential_configurations_supported"].(map[string]any)[incomeConfig].(map[string]any)
			if types, _ := cfg["proof_types_supported"].(map[string]any); len(types) == 0 {
				t.Fatalf("proof_types_supported must be published: %v", cfg)
			}
			c := newCredentialCase(t, f, p.profile, p.as)
			nonce := c.w.nonce()
			two := []string{
				c.keyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, nonce),
				c.keyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, nonce),
			}
			for what, body := range map[string]map[string]any{
				"8.2-12 no proofs":                   {"credential_identifier": c.credID},
				"8.2-12 empty proofs":                {"credential_identifier": c.credID, "proofs": map[string]any{}},
				"F-01 empty jwt array":               {"credential_identifier": c.credID, "proofs": map[string]any{"jwt": []string{}}},
				"F-03 empty attestation array":       {"credential_identifier": c.credID, "proofs": map[string]any{"attestation": []string{}}},
				"F-03 two attestations in one array": {"credential_identifier": c.credID, "proofs": map[string]any{"attestation": two}},
			} {
				status, resp := c.requestJSON(body)
				expectCredentialError(t, what, status, resp, oid4vci.ErrInvalidProof)
			}
			c.issue(t)
		})
	}
}

// OID4VCI-8.2-13: unknown parameters of a credential request are ignored,
// in a plain request and, where the profile takes them, in an encrypted
// one.
func TestCredentialRequestUnknownParameters(t *testing.T) {
	f := newFixture(t)
	withUnknown := func(t *testing.T, c *credentialCase) []byte {
		raw, _ := json.Marshal(oid4vci.CredentialRequest{CredentialIdentifier: c.credID, Proofs: c.proofs(t)})
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		body["an_unknown_parameter"] = "ignored"
		body["an_unknown_object"] = map[string]any{"nested": []int{1, 2}}
		raw, _ = json.Marshal(body)
		return raw
	}
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			c := newCredentialCase(t, f, p.profile, p.as)
			var body map[string]any
			_ = json.Unmarshal(withUnknown(t, c), &body)
			if status, resp := c.requestJSON(body); status != http.StatusOK || len(credentialsOf(resp)) != 1 {
				t.Fatalf("plain request: %d %v", status, resp)
			}
			if p.profile == "nl" {
				return
			}
			c = newCredentialCase(t, f, p.profile, p.as)
			status, _, resp := c.w.postJWT(t, c.at, testwallet.EncryptRequest(t, withUnknown(t, c), f.requestKey(t, "tax"), jose.A256GCM, nil))
			if status != http.StatusOK || !strings.Contains(resp, `"credentials"`) {
				t.Fatalf("encrypted request: %d %s", status, resp)
			}
		})
	}
}

// OID4VCI-8.2-10: the wallet's credential_response_encryption has no zip,
// so the encrypted response is not compressed. Only the profiles that
// publish encryption; nl refuses it (E9).
func TestEncryptedResponseIsNotCompressed(t *testing.T) {
	f := newFixture(t)
	for _, p := range credentialProfiles {
		if p.profile == "nl" {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			c := newCredentialCase(t, f, p.profile, p.as)
			walletKey, params := testwallet.ResponseEncryption(t, "A256GCM")
			status, ctype, body := c.w.encryptedCredential(t, c.at, f.requestKey(t, "tax"), jose.A256GCM, oid4vci.CredentialRequest{
				CredentialIdentifier: c.credID, Proofs: c.proofs(t), ResponseEncryption: &params,
			})
			if status != http.StatusOK || ctype != "application/jwt" {
				t.Fatalf("%d %s %s", status, ctype, body)
			}
			raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(body, ".", 2)[0])
			if err != nil {
				t.Fatal(err)
			}
			var header map[string]any
			if err := json.Unmarshal(raw, &header); err != nil {
				t.Fatal(err)
			}
			if _, zip := header["zip"]; zip {
				t.Fatalf("the response is compressed: %s", raw)
			}
			_, plain := testwallet.DecryptResponse(t, body, walletKey)
			var resp oid4vci.CredentialResponse
			if err := json.Unmarshal(plain, &resp); err != nil || len(resp.Credentials) != 1 {
				t.Fatalf("%s %v", plain, err)
			}
		})
	}
}

// OID4VCI-8.3-08, 8.3-12, 8.3-14: a response with credentials carries no
// transaction_id and no interval; a deferred response (202, eudi only)
// carries no notification_id. Profiles without a notification endpoint
// carry none at all.
func TestCredentialResponseMembers(t *testing.T) {
	f := newFixture(t)
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			body := newCredentialCase(t, f, p.profile, p.as).issue(t)
			if _, ok := body["transaction_id"]; ok {
				t.Errorf("8.3-08: transaction_id with credentials: %v", body)
			}
			if _, ok := body["interval"]; ok {
				t.Errorf("8.3-12: interval with credentials: %v", body)
			}
			if _, ok := body["notification_id"]; ok != (p.profile == "eudi") {
				t.Errorf("notification_id only on eudi, which has a notification endpoint: %v", body)
			}
		})
	}
	t.Run("eudi deferred", func(t *testing.T) {
		f := newFixture(t)
		w, tok, status, cred := deferredStart(t, f)
		if status != http.StatusAccepted || cred["transaction_id"] == nil {
			t.Fatalf("want a deferred response: %d %v", status, cred)
		}
		if _, ok := cred["notification_id"]; ok {
			t.Errorf("8.3-14: notification_id in a deferred response: %v", cred)
		}
		status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]})
		if _, ok := body["notification_id"]; status != http.StatusAccepted || ok {
			t.Errorf("8.3-14: deferred credential response without credentials: %d %v", status, body)
		}
		f.setSource(http.StatusOK, f.sourceDocs)
		status, body = w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]})
		_, txID := body["transaction_id"]
		_, interval := body["interval"]
		if status != http.StatusOK || len(credentialsOf(body)) == 0 || txID || interval {
			t.Errorf("8.3-08, 8.3-12: deferred credential response with credentials: %d %v", status, body)
		}
	})
}

// OID4VCI-A.3.2-01, F.1-21, F.3-02: the signing algorithms in the metadata
// are the JWS algorithm name ES256, and the credential is signed with it.
func TestCredentialAlgorithmMetadata(t *testing.T) {
	f := newFixture(t)
	es256 := func(v any) bool { raw, _ := json.Marshal(v); return string(raw) == `["ES256"]` }
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			configs := f.metadata(t, "tax", p.profile)["credential_configurations_supported"].(map[string]any)
			for id, raw := range configs {
				cfg := raw.(map[string]any)
				if !es256(cfg["credential_signing_alg_values_supported"]) {
					t.Errorf("A.3.2-01 %s: credential_signing_alg_values_supported %v", id, cfg["credential_signing_alg_values_supported"])
				}
				types := cfg["proof_types_supported"].(map[string]any)
				if jwtType, _ := types["jwt"].(map[string]any); !es256(jwtType["proof_signing_alg_values_supported"]) {
					t.Errorf("F.1-21 %s: jwt proof_signing_alg_values_supported %v", id, types["jwt"])
				}
				attestation, ok := types["attestation"].(map[string]any)
				if ok != (p.profile != "nl") || (ok && !es256(attestation["proof_signing_alg_values_supported"])) {
					t.Errorf("F.3-02 %s: attestation proof type %v", id, types["attestation"])
				}
			}
			cred := credentialsOf(newCredentialCase(t, f, p.profile, p.as).issue(t))[0]
			raw, _ := base64.RawURLEncoding.DecodeString(strings.SplitN(cred, ".", 2)[0])
			var header map[string]any
			if err := json.Unmarshal(raw, &header); err != nil || header["alg"] != "ES256" {
				t.Errorf("A.3.2-01: the credential's header %s", raw)
			}
		})
	}
}

// OID4VCI-F.1-01 to F.1-08, F.1-15, F.1-19, F.1-22, F.4-01: a jwt key proof
// with one element wrong is an invalid_proof. An EUDI wallet's proof
// carries a valid key attestation of its key, so only the proof is wrong.
// The library test TestJWTProofConformance shows which check refuses each.
func TestKeyProofRefusals(t *testing.T) {
	f := newFixture(t)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range credentialProfiles {
		t.Run(p.name, func(t *testing.T) {
			c := newCredentialCase(t, f, p.profile, p.as)
			// proof is a proof of signer (ES256 with key unless alg says
			// otherwise) for the key in the jwk header; change edits the
			// header, drop removes a claim.
			proof := func(alg jose.SignatureAlgorithm, signer any, key *ecdsa.PrivateKey, change map[jose.HeaderKey]any, drop string) string {
				nonce := c.w.nonce()
				h := map[jose.HeaderKey]any{jose.HeaderType: oid4vci.ProofType, "jwk": jose.JSONWebKey{Key: &key.PublicKey}}
				if c.as == "eudi" {
					h["key_attestation"] = c.keyAttestation(t, []*ecdsa.PrivateKey{key}, nonce)
				}
				for k, v := range change {
					if v == nil {
						delete(h, k)
					} else {
						h[k] = v
					}
				}
				claims := map[string]any{"aud": c.aud(), "iat": time.Now().Unix(), "nonce": nonce}
				delete(claims, drop)
				return credentialJWS(t, alg, signer, h, claims)
			}
			key := testpki.NewKey(t)
			for what, jwtProof := range map[string]string{
				"F.1-22 ES384":                 proof(jose.ES384, p384, p384, nil, ""),
				"F.1-03 MAC":                   proof(jose.HS256, []byte("a shared secret of thirty-two by"), key, nil, ""),
				"F.1-03 none":                  proof("none", nil, key, nil, ""),
				"F.1-04 no typ":                proof(jose.ES256, key, key, map[jose.HeaderKey]any{jose.HeaderType: nil}, ""),
				"F.1-05 typ jwt":               proof(jose.ES256, key, key, map[jose.HeaderKey]any{jose.HeaderType: "jwt"}, ""),
				"F.1-06 F.1-07 jwk and kid":    proof(jose.ES256, key, key, map[jose.HeaderKey]any{"kid": "key-1"}, ""),
				"F.1-19 signed by another key": proof(jose.ES256, testpki.NewKey(t), key, nil, ""),
				"F.1-15 no iat":                proof(jose.ES256, key, key, nil, "iat"),
				"F.1-01 no nonce":              proof(jose.ES256, key, key, nil, "nonce"),
				"F.1-01 no aud":                proof(jose.ES256, key, key, nil, "aud"),
			} {
				status, body := c.request(oid4vci.CredentialRequest{CredentialIdentifier: c.credID, Proofs: &oid4vci.Proofs{JWT: []string{jwtProof}}})
				expectCredentialError(t, what, status, body, oid4vci.ErrInvalidProof)
			}
			valid := proof(jose.ES256, key, key, nil, "")
			if status, body := c.request(oid4vci.CredentialRequest{CredentialIdentifier: c.credID, Proofs: &oid4vci.Proofs{JWT: []string{valid}}}); status != http.StatusOK {
				t.Fatalf("the valid proof the cases start from: %d %v", status, body)
			}
		})
	}
}

// OID4VCI-D.1-01 to D.1-05, D.1-08, F.1-09, F.3-01, F.3-04: key
// attestations, in a jwt proof and as the attestation proof type, on the
// profiles that take them from EUDI wallets. A key attestation with one
// element wrong is an invalid_proof; an unknown c_nonce is an
// invalid_nonce (section 8.3.1.2).
func TestKeyAttestationRefusals(t *testing.T) {
	f := newFixture(t)
	for _, p := range credentialProfiles {
		if p.as != "eudi" {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			c := newCredentialCase(t, f, p.profile, p.as)
			signer := f.eudiWallet.Signer
			// attestation is a key attestation of keys with nonce, signed
			// with alg by the wallet provider's key unless alg needs
			// another; typ nil leaves typ out, drop removes a claim.
			attestation := func(alg jose.SignatureAlgorithm, key any, typ any, keys []*ecdsa.PrivateKey, nonce, drop string) string {
				attested := make([]jose.JSONWebKey, len(keys))
				for i, k := range keys {
					attested[i] = jose.JSONWebKey{Key: &k.PublicKey}
				}
				h := map[jose.HeaderKey]any{"x5c": x5c.Header(signer.Chain)}
				if typ != nil {
					h[jose.HeaderType] = typ
				}
				claims := map[string]any{"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "attested_keys": attested, "nonce": nonce,
					"key_storage": []string{"iso_18045_high"}, "user_authentication": []string{"iso_18045_high"}}
				delete(claims, drop)
				return credentialJWS(t, alg, key, h, claims)
			}
			one := func() []*ecdsa.PrivateKey { return []*ecdsa.PrivateKey{testpki.NewKey(t)} }
			attestationProof := func(ka string) *oid4vci.Proofs { return &oid4vci.Proofs{Attestation: []string{ka}} }
			// jwtProof is a proof of key with nonce that carries ka.
			jwtProof := func(key *ecdsa.PrivateKey, nonce, ka string) *oid4vci.Proofs {
				return &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, key, c.aud(), nonce, ka)}}
			}
			cases := map[string]struct {
				proofs *oid4vci.Proofs
				code   string
			}{
				"D.1-02 F.3-04 MAC":                      {attestationProof(attestation(jose.HS256, []byte("a shared secret of thirty-two by"), "key-attestation+jwt", one(), c.w.nonce(), "")), oid4vci.ErrInvalidProof},
				"D.1-02 F.3-04 none":                     {attestationProof(attestation("none", nil, "key-attestation+jwt", one(), c.w.nonce(), "")), oid4vci.ErrInvalidProof},
				"D.1-03 no typ":                          {attestationProof(attestation(jose.ES256, signer.Key, nil, one(), c.w.nonce(), "")), oid4vci.ErrInvalidProof},
				"D.1-04 typ jwt":                         {attestationProof(attestation(jose.ES256, signer.Key, "jwt", one(), c.w.nonce(), "")), oid4vci.ErrInvalidProof},
				"D.1-05 no iat":                          {attestationProof(attestation(jose.ES256, signer.Key, "key-attestation+jwt", one(), c.w.nonce(), "iat")), oid4vci.ErrInvalidProof},
				"F.3-01 no nonce":                        {attestationProof(attestation(jose.ES256, signer.Key, "key-attestation+jwt", one(), "", "nonce")), oid4vci.ErrInvalidProof},
				"F.3-01 a nonce the issuer did not give": {attestationProof(attestation(jose.ES256, signer.Key, "key-attestation+jwt", one(), "not-a-c-nonce", "")), oid4vci.ErrInvalidNonce},
			}
			// D.1-08: the proof is signed by a key the attestation does
			// not hold.
			nonce := c.w.nonce()
			cases["D.1-08 proof key not attested"] = struct {
				proofs *oid4vci.Proofs
				code   string
			}{jwtProof(testpki.NewKey(t), nonce, c.keyAttestation(t, one(), nonce)), oid4vci.ErrInvalidProof}
			// F.1-09: the key attestation in a jwt proof carries the
			// proof's c_nonce.
			for what, kaNonce := range map[string]string{"F.1-09 another nonce than the proof": "not-a-c-nonce", "F.1-09 no nonce": ""} {
				key, nonce := testpki.NewKey(t), c.w.nonce()
				cases[what] = struct {
					proofs *oid4vci.Proofs
					code   string
				}{jwtProof(key, nonce, c.keyAttestation(t, []*ecdsa.PrivateKey{key}, kaNonce)), oid4vci.ErrInvalidProof}
			}
			for what, tc := range cases {
				status, body := c.request(oid4vci.CredentialRequest{CredentialIdentifier: c.credID, Proofs: tc.proofs})
				expectCredentialError(t, what, status, body, tc.code)
			}
			key, nonce := testpki.NewKey(t), c.w.nonce()
			if status, body := c.request(oid4vci.CredentialRequest{CredentialIdentifier: c.credID,
				Proofs: jwtProof(key, nonce, c.keyAttestation(t, []*ecdsa.PrivateKey{key}, nonce))}); status != http.StatusOK {
				t.Fatalf("a valid jwt proof with key attestation: %d %v", status, body)
			}
		})
	}
}
