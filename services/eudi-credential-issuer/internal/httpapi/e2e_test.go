package httpapi_test

import (
	"crypto/ecdsa"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

// O1-O5: creating offers through the internal API.
func TestOffers(t *testing.T) {
	f := newFixture(t)

	t.Run("O1 links per profile", func(t *testing.T) {
		status, body := f.offer(map[string]any{
			"profile": "nl", "credential_configuration_ids": []string{incomeConfig},
			"subject": map[string]any{"bsn": "999990019"}, "parameters": map[string]string{"jaar": "2025"},
		})
		if status != http.StatusCreated {
			t.Fatalf("status %d %v", status, body)
		}
		links := body["links"].(map[string]any)
		if !strings.HasPrefix(links["universal_link"].(string), "https://app.example.test/deeplink/credential-offer?credential_offer=") ||
			!strings.HasPrefix(links["openid-credential-offer"].(string), "openid-credential-offer://?credential_offer=") || links["haip-vci"] != nil {
			t.Fatalf("unexpected nl links %v", links)
		}
		if got := f.lastSourceCall().Get("jaar"); got != "2025" {
			t.Fatalf("offer parameters must reach the source, got %q", got)
		}
		_, body = f.offer(map[string]any{"profile": "eudi", "credential_configuration_ids": []string{incomeConfig}, "subject": map[string]any{"bsn": "1"}})
		links = body["links"].(map[string]any)
		if links["haip-vci"] == nil || links["universal_link"] != nil {
			t.Fatalf("unexpected eudi links %v", links)
		}
		if body["credential_offer"].(map[string]any)["credential_issuer"] != f.id("eudi") {
			t.Fatalf("offer must name the profile's issuer identifier: %v", body)
		}
	})
	t.Run("O2 unknown configuration", func(t *testing.T) {
		status, _ := f.offer(map[string]any{"profile": "nl", "credential_configuration_ids": []string{"nope"}, "subject": map[string]any{"bsn": "1"}})
		if status != http.StatusBadRequest {
			t.Fatalf("status %d", status)
		}
	})
	t.Run("O3 tx_code refused for nl", func(t *testing.T) {
		status, body := f.offer(map[string]any{"profile": "nl", "credential_configuration_ids": []string{incomeConfig}, "subject": map[string]any{"bsn": "1"}, "tx_code": true})
		if status != http.StatusBadRequest || body["error"] != "tx_code_unsupported" {
			t.Fatalf("status %d %v", status, body)
		}
	})
	t.Run("O4 source has no data", func(t *testing.T) {
		f.setSource(http.StatusNotFound, nil)
		defer f.setSource(http.StatusOK, nil)
		status, body := f.offer(map[string]any{"profile": "nl", "credential_configuration_ids": []string{incomeConfig}, "subject": map[string]any{"bsn": "1"}})
		if status != http.StatusNotFound || body["error"] != "no_data" {
			t.Fatalf("status %d %v", status, body)
		}
	})
	t.Run("O5 source unavailable", func(t *testing.T) {
		f.setSource(http.StatusBadGateway, nil)
		defer f.setSource(http.StatusOK, nil)
		status, body := f.offer(map[string]any{"profile": "nl", "credential_configuration_ids": []string{incomeConfig}, "subject": map[string]any{"bsn": "1"}})
		if status != http.StatusServiceUnavailable || body["error"] != "source_unavailable" {
			t.Fatalf("status %d %v", status, body)
		}
	})
	badData := func(t *testing.T, profile string, attributes map[string]any, wantRefused bool) {
		t.Helper()
		saved := f.sourceDocs
		defer f.setSource(http.StatusOK, saved)
		f.setSource(http.StatusOK, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": incomeVCT, "attributes": attributes}})
		status, body := f.offer(map[string]any{"profile": profile, "credential_configuration_ids": []string{incomeConfig}, "subject": map[string]any{"bsn": "1"}})
		if refused := status == http.StatusBadGateway && body["error"] == "bad_source_data"; refused != wantRefused {
			t.Fatalf("status %d %v, want refused %v", status, body, wantRefused)
		}
	}
	t.Run("source data without claim metadata is refused", func(t *testing.T) {
		badData(t, "nl", map[string]any{"year": "2025", "secret": "x"}, true)
	})
	t.Run("a reserved attribute name is refused", func(t *testing.T) {
		badData(t, "eudi", map[string]any{"year": "2025", "vct": "x"}, true)
	})
	t.Run("a decimal number is refused for nl only", func(t *testing.T) {
		badData(t, "nl", map[string]any{"year": "2025", "income": 41000.5}, true)
		badData(t, "eudi", map[string]any{"year": "2025", "income": 41000.5}, false)
	})
	t.Run("a missing profile is named", func(t *testing.T) {
		status, body := f.offer(map[string]any{"credential_configuration_ids": []string{incomeConfig}, "subject": map[string]any{"bsn": "1"}})
		if status != http.StatusBadRequest || body["error"] != "unknown_profile" {
			t.Fatalf("status %d %v", status, body)
		}
	})
}

// M1-M4 and P2: signed issuer metadata and authorization server metadata.
func TestMetadata(t *testing.T) {
	f := newFixture(t)
	anchors := []x5c.Anchors{x5c.NewAnchors("access", f.accessRoot.Cert)}
	for _, profile := range []string{"nl", "eudi"} {
		t.Run(profile, func(t *testing.T) {
			status, body, hdr := get(t, f.public.URL+"/.well-known/openid-credential-issuer/tax/"+profile, "application/jwt")
			if status != http.StatusOK || hdr.Get("Content-Type") != "application/jwt" {
				t.Fatalf("M1: status %d", status)
			}
			tok, err := jwt.ParseSigned(body, []jose.SignatureAlgorithm{jose.ES256})
			if err != nil {
				t.Fatal(err)
			}
			if typ := tok.Headers[0].ExtraHeaders[jose.HeaderType]; typ != "openidvci-issuer-metadata+jwt" {
				t.Fatalf("M1: typ %v", typ)
			}
			leaf, _, err := x5c.Verify(tok.Headers[0], anchors, time.Now(), nil)
			if err != nil {
				t.Fatalf("M1: %v", err)
			}
			var md map[string]any
			if err := tok.Claims(leaf.PublicKey, &md); err != nil {
				t.Fatal(err)
			}
			if md["sub"] != f.id(profile) || md["credential_issuer"] != f.id(profile) || md["nonce_endpoint"] != f.id(profile)+"/nonce" {
				t.Fatalf("M1: %v", md)
			}
			cfg := md["credential_configurations_supported"].(map[string]any)[incomeConfig].(map[string]any)
			jwtProof := cfg["proof_types_supported"].(map[string]any)["jwt"].(map[string]any)
			_, requiresKA := jwtProof["key_attestations_required"]
			_, preview := md["credential_preview_endpoint"]
			_, info := md["issuer_info"]
			if profile == "nl" && (requiresKA || !preview || !info) {
				t.Fatalf("M3/P1: nl must not require key attestation and must have a preview endpoint and issuer_info: %v", md)
			}
			if profile == "eudi" && (!requiresKA || preview ||
				jwtProof["key_attestations_required"].(map[string]any)["key_storage"].([]any)[0] != "iso_18045_high") {
				t.Fatalf("M3/P2: eudi must require key attestation and have no preview endpoint: %v", md)
			}

			// M2 (changed after review): OpenID4VCI section 12.2.2 requires the
			// unsigned form too.
			status, plain, hdr := get(t, f.public.URL+"/.well-known/openid-credential-issuer/tax/"+profile, "application/json")
			if status != http.StatusOK || hdr.Get("Content-Type") != "application/json" || !strings.Contains(plain, `"credential_issuer":"`+f.id(profile)+`"`) ||
				strings.Contains(plain, `"sub"`) {
				t.Fatalf("M2: unsigned metadata: %d %s", status, plain)
			}
			if !strings.Contains(plain, `"scope":"income"`) {
				t.Fatalf("HAIP 4.1: every configuration has a scope: %s", plain)
			}

			status, body, _ = get(t, f.public.URL+"/.well-known/oauth-authorization-server/tax/"+profile, "")
			if status != http.StatusOK || !strings.Contains(body, `"attest_jwt_client_auth"`) || !strings.Contains(body, `"challenge_endpoint":"`+f.id(profile)+`/challenge"`) {
				t.Fatalf("M4: %d %s", status, body)
			}
		})
	}
}

// W1-W5 and T1-T3: the token endpoint.
func TestToken(t *testing.T) {
	f := newFixture(t)
	nl := f.wallet("nl")

	t.Run("W1 T1 valid attestation and code", func(t *testing.T) {
		resp := nl.token(f.preAuthCode("nl"))
		if resp.TokenType != "DPoP" || resp.AccessToken == "" || len(resp.AuthorizationDetails) != 1 ||
			resp.AuthorizationDetails[0].CredentialConfigurationID != incomeConfig || len(resp.AuthorizationDetails[0].CredentialIdentifiers) != 1 {
			t.Fatalf("unexpected token response %+v", resp)
		}
	})

	refused := func(t *testing.T, status int, body map[string]any, wantStatus int, wantCode string) {
		t.Helper()
		if status != wantStatus || body["error"] != wantCode {
			t.Fatalf("got %d %v, want %d %s", status, body, wantStatus, wantCode)
		}
	}
	t.Run("W2 unknown wallet provider", func(t *testing.T) {
		other := testwallet.NewProvider(t, "unknown provider")
		status, body := nl.tokenRequest(f.preAuthCode("nl"), map[string]string{"OAuth-Client-Attestation": other.WIA(t, nlClient, nl.wiaKey, time.Now().Add(time.Hour))})
		refused(t, status, body, http.StatusUnauthorized, oid4vci.ErrInvalidClientAttestation)
	})
	t.Run("W3 expired attestation", func(t *testing.T) {
		status, body := nl.tokenRequest(f.preAuthCode("nl"), map[string]string{"OAuth-Client-Attestation": f.nlWallet.WIA(t, nlClient, nl.wiaKey, time.Now().Add(-time.Second))})
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrUseFreshAttestation)
	})
	t.Run("W4 reused challenge", func(t *testing.T) {
		c := nl.challenge()
		code := f.preAuthCode("nl")
		pop := func() string { return testwallet.PoP(t, nl.wiaKey, nlClient, f.id("nl"), c) }
		if status, body := nl.tokenRequest(code, map[string]string{"OAuth-Client-Attestation-PoP": pop()}); status != http.StatusOK {
			t.Fatalf("first use: %d %v", status, body)
		}
		code = f.preAuthCode("nl")
		status, body := nl.tokenRequest(code, map[string]string{"OAuth-Client-Attestation-PoP": pop()})
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrUseAttestationChallenge)
		// The refusal carries a fresh challenge, which works.
		fresh := nl.header.Get("OAuth-Client-Attestation-Challenge")
		if fresh == "" || fresh == c {
			t.Fatalf("want a fresh challenge in the header, got %q", fresh)
		}
		if status, body := nl.tokenRequest(code, map[string]string{"OAuth-Client-Attestation-PoP": testwallet.PoP(t, nl.wiaKey, nlClient, f.id("nl"), fresh)}); status != http.StatusOK {
			t.Fatalf("with the fresh challenge: %d %v", status, body)
		}
	})
	t.Run("W4 PoP without challenge", func(t *testing.T) {
		status, body := nl.tokenRequest(f.preAuthCode("nl"), map[string]string{"OAuth-Client-Attestation-PoP": testwallet.PoP(t, nl.wiaKey, nlClient, f.id("nl"), "")})
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrUseAttestationChallenge)
		if nl.header.Get("OAuth-Client-Attestation-Challenge") == "" {
			t.Fatal("want a fresh challenge in the header")
		}
	})
	t.Run("W5 attestation of another profile", func(t *testing.T) {
		eudi := f.wallet("eudi")
		eudi.profile = "nl" // an EUDI wallet presenting itself at /nl
		status, body := eudi.tokenRequest(f.preAuthCode("nl"), nil)
		refused(t, status, body, http.StatusUnauthorized, oid4vci.ErrInvalidClientAttestation)
	})
	t.Run("T2 code used twice", func(t *testing.T) {
		code := f.preAuthCode("nl")
		nl.token(code)
		status, body := nl.tokenRequest(code, nil)
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrInvalidGrant)
	})
	t.Run("T2 expired code", func(t *testing.T) {
		// The offer was made 6 minutes ago; offers live 5 minutes.
		f.clock.Advance(-6 * time.Minute)
		code := f.preAuthCode("nl")
		f.clock.Advance(6 * time.Minute)
		status, body := nl.tokenRequest(code, nil)
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrInvalidGrant)
	})
	t.Run("T2 code of another profile", func(t *testing.T) {
		code := f.preAuthCode("eudi")
		status, body := nl.tokenRequest(code, nil)
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrInvalidGrant)
		// The refused request did not burn the code for its own profile.
		f.wallet("eudi").token(code)
	})
	t.Run("client_id must match the attestation", func(t *testing.T) {
		status, body := nl.tokenRequestForm(f.preAuthCode("nl"), url.Values{"client_id": {"someone-else"}})
		refused(t, status, body, http.StatusUnauthorized, oid4vci.ErrInvalidClientAttestation)
	})
	t.Run("T3 no DPoP proof", func(t *testing.T) {
		code := f.preAuthCode("nl")
		status, body := nl.tokenRequest(code, map[string]string{"DPoP": ""})
		refused(t, status, body, http.StatusBadRequest, oid4vci.ErrInvalidDPoPProof)
		// The refused request did not burn the code.
		nl.token(code)
	})
}

// C1, S1, S2 and P1 for the nl profile: jwt proofs without key
// attestation, the preview, the credential and its status.
func TestIssueNL(t *testing.T) {
	f := newFixture(t)
	nl := f.wallet("nl")
	tok := nl.token(f.preAuthCode("nl"))
	credID := tok.AuthorizationDetails[0].CredentialIdentifiers[0]

	payload := checkPreview(t, nl, tok.AccessToken, credID)
	// Two proofs with one key are refused: one credential per proof.
	dup := testpki.NewKey(t)
	dupNonce := nl.nonce()
	if status, body := nl.credentialRequest(tok.AccessToken, nl.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: credID,
		Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, dup, f.id("nl"), dupNonce, ""), testwallet.JWTProof(t, dup, f.id("nl"), dupNonce, "")}},
	}); status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidProof {
		t.Fatalf("duplicate proof keys: %d %v", status, body)
	}

	keys := []*ecdsa.PrivateKey{testpki.NewKey(t), testpki.NewKey(t)}
	nonce := nl.nonce()
	status, body := nl.credentialRequest(tok.AccessToken, nl.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: credID,
		Proofs: &oid4vci.Proofs{JWT: []string{
			testwallet.JWTProof(t, keys[0], f.id("nl"), nonce, ""),
			testwallet.JWTProof(t, keys[1], f.id("nl"), nonce, ""),
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("C1: %d %v", status, body)
	}
	creds := body["credentials"].([]any)
	if len(creds) != 2 {
		t.Fatalf("C1: want one credential per proof, got %d", len(creds))
	}
	anchors := []x5c.Anchors{x5c.NewAnchors("issuer", f.issuerRoot.Cert)}
	for i, c := range creds {
		parsed, err := sdjwtvc.Parse(c.(map[string]any)["credential"].(string), anchors, mdlIssuerEKU, time.Now())
		if err != nil {
			t.Fatalf("S1: %v", err)
		}
		claims := parsed.Claims
		if claims["vct"] != incomeVCT || claims["year"] != "2025" || claims["income"] != float64(41000) || claims["iat"] == nil {
			t.Fatalf("S1: %v", claims)
		}
		// iat is rounded to the day, so copies of a batch share it with
		// everything issued that day (RFC 9901 section 10.1).
		if int64(claims["iat"].(float64))%86400 != 0 {
			t.Fatalf("iat %v is not rounded to the day", claims["iat"])
		}
		if _, ok := claims["iss"]; ok {
			t.Fatal("S1: no iss")
		}
		// P1: what was previewed is what is issued.
		if claims["nbf"] != payload["nbf"] || claims["exp"] != payload["exp"] {
			t.Fatalf("P1: preview nbf/exp %v/%v, credential %v/%v", payload["nbf"], payload["exp"], claims["nbf"], claims["exp"])
		}
		// C1: bound to the key of the proof at the same position.
		var cnf struct {
			JWK jose.JSONWebKey `json:"jwk"`
		}
		remarshal(t, claims["cnf"], &cnf)
		if !cnf.JWK.Key.(*ecdsa.PublicKey).Equal(&keys[i].PublicKey) {
			t.Fatalf("C1: credential %d is not bound to the key of proof %d", i, i)
		}
	}

	// C2: the credential identifier is used up.
	status, body = nl.credentialRequest(tok.AccessToken, nl.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: credID,
		Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, keys[0], f.id("nl"), nl.nonce(), "")}},
	})
	if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidCredentialRequest {
		t.Fatalf("C2: %d %v", status, body)
	}

	// C2: the nonce is used up, also for another credential.
	tok2 := nl.token(f.preAuthCode("nl"))
	status, body = nl.credentialRequest(tok2.AccessToken, nl.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: tok2.AuthorizationDetails[0].CredentialIdentifiers[0],
		Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.id("nl"), nonce, "")}},
	})
	if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidNonce {
		t.Fatalf("C2 nonce: %d %v", status, body)
	}

	// S2: the status list lists the credential as valid, until revoked.
	first := creds[0].(map[string]any)["credential"].(string)
	parsed, _ := sdjwtvc.Parse(first, anchors, nil, time.Now())
	ref := parsed.Claims["status"].(map[string]any)["status_list"].(map[string]any)
	uri, idx := ref["uri"].(string), int(ref["idx"].(float64))
	if bits := fetchStatus(t, f, uri); statuslist.Revoked(bits, idx) {
		t.Fatal("S2: a fresh credential must be valid")
	}
	raw := `{"provider":"tax","list":"income","index":` + strconv.Itoa(idx) + `}`
	resp, err := http.Post(f.internal.URL+"/revocations", "application/json", strings.NewReader(raw))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("S2: revoke: %v %v", err, resp)
	}
	if bits := fetchStatus(t, f, uri); !statuslist.Revoked(bits, idx) {
		t.Fatal("S2: a revoked credential must be listed as revoked")
	}
}

// C1, C3, C4 and C5 for the eudi profile: key attestations.
func TestIssueEUDI(t *testing.T) {
	f := newFixture(t)
	eudi := f.wallet("eudi")
	newToken := func() (string, string) {
		tok := eudi.token(f.preAuthCode("eudi"))
		return tok.AccessToken, tok.AuthorizationDetails[0].CredentialIdentifiers[0]
	}

	t.Run("C1 jwt proof with key attestation", func(t *testing.T) {
		at, credID := newToken()
		keys := []*ecdsa.PrivateKey{testpki.NewKey(t), testpki.NewKey(t), testpki.NewKey(t)}
		nonce := eudi.nonce()
		ka := f.eudiWallet.KeyAttestation(t, keys, nonce, time.Now().Add(time.Hour))
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID,
			Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, keys[0], f.id("eudi"), nonce, ka)}},
		})
		if status != http.StatusOK || len(body["credentials"].([]any)) != 3 {
			t.Fatalf("want one credential per attested key: %d %v", status, body)
		}
	})
	t.Run("C1 attestation proof", func(t *testing.T) {
		at, credID := newToken()
		keys := []*ecdsa.PrivateKey{testpki.NewKey(t), testpki.NewKey(t)}
		ka := f.eudiWallet.KeyAttestation(t, keys, eudi.nonce(), time.Now().Add(time.Hour))
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
		})
		if status != http.StatusOK || len(body["credentials"].([]any)) != 2 {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("C1 attestation proof without exp", func(t *testing.T) {
		at, credID := newToken()
		keys := []*ecdsa.PrivateKey{testpki.NewKey(t)}
		ka := f.eudiWallet.KeyAttestationClaims(t, keys, map[string]any{"nonce": eudi.nonce(),
			"key_storage": []string{"iso_18045_high"}, "user_authentication": []string{"iso_18045_high"}})
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
		})
		if status != http.StatusOK {
			t.Fatalf("exp is optional with the attestation proof type: %d %v", status, body)
		}
	})
	t.Run("key attestation below the required level", func(t *testing.T) {
		at, credID := newToken()
		keys := []*ecdsa.PrivateKey{testpki.NewKey(t)}
		ka := f.eudiWallet.KeyAttestationClaims(t, keys, map[string]any{"nonce": eudi.nonce(), "exp": time.Now().Add(time.Hour).Unix(),
			"key_storage": []string{"iso_18045_moderate"}, "user_authentication": []string{"iso_18045_high"}})
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
		})
		if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidProof {
			t.Fatalf("%d %v", status, body)
		}
		// The refusal left the credential issuable.
		keys = []*ecdsa.PrivateKey{testpki.NewKey(t)}
		ka = f.eudiWallet.KeyAttestation(t, keys, eudi.nonce(), time.Now().Add(time.Hour))
		if status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
		}); status != http.StatusOK {
			t.Fatalf("retry: %d %v", status, body)
		}
	})
	t.Run("C3 no key attestation", func(t *testing.T) {
		at, credID := newToken()
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID,
			Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.id("eudi"), eudi.nonce(), "")}},
		})
		if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidProof {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("C4 more keys than the batch size", func(t *testing.T) {
		at, credID := newToken()
		keys := make([]*ecdsa.PrivateKey, 5)
		for i := range keys {
			keys[i] = testpki.NewKey(t)
		}
		ka := f.eudiWallet.KeyAttestation(t, keys, eudi.nonce(), time.Now().Add(time.Hour))
		status, body := eudi.credentialRequest(at, eudi.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
		})
		if status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidProof {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("C5 DPoP key of another wallet", func(t *testing.T) {
		at, credID := newToken()
		keys := []*ecdsa.PrivateKey{testpki.NewKey(t)}
		ka := f.eudiWallet.KeyAttestation(t, keys, eudi.nonce(), time.Now().Add(time.Hour))
		status, body := eudi.credentialRequest(at, testpki.NewKey(t), oid4vci.CredentialRequest{
			CredentialIdentifier: credID, Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
		})
		if status != http.StatusUnauthorized || body["error"] != oid4vci.ErrInvalidDPoPProof {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("P2 no preview endpoint", func(t *testing.T) {
		at, _ := newToken()
		if status, _ := eudi.post("/credential_preview", nil, map[string]string{"Authorization": "Bearer " + at}); status != http.StatusNotFound {
			t.Fatalf("status %d", status)
		}
	})
}

func fetchStatus(t *testing.T, f *fixture, uri string) []byte {
	t.Helper()
	return fetchStatusFrom(t, f.issuerRoot, uri)
}

// fetchStatusFrom fetches a status list and verifies it against a
// provider's issuer root.
func fetchStatusFrom(t *testing.T, issuerRoot testpki.Cert, uri string) []byte {
	t.Helper()
	status, body, _ := get(t, uri, "application/statuslist+jwt")
	if status != http.StatusOK {
		t.Fatalf("status list: %d", status)
	}
	tok, err := jwt.ParseSigned(body, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Headers[0].ExtraHeaders[jose.HeaderType] != statuslist.Type {
		t.Fatal("status list typ")
	}
	leaf, _, err := x5c.Verify(tok.Headers[0], []x5c.Anchors{x5c.NewAnchors("issuer", issuerRoot.Cert)}, time.Now(), statusListEKU)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Sub        string `json:"sub"`
		StatusList struct {
			Bits int    `json:"bits"`
			Lst  string `json:"lst"`
		} `json:"status_list"`
	}
	if err := tok.Claims(leaf.PublicKey, &c); err != nil {
		t.Fatal(err)
	}
	if c.Sub != uri || c.StatusList.Bits != 1 {
		t.Fatalf("status list claims %+v", c)
	}
	bits, err := statuslist.Decode(c.StatusList.Lst)
	if err != nil {
		t.Fatal(err)
	}
	return bits
}

// Revocation through the internal API: by the offer caller's reference,
// which revokes every credential issued for the offer, or by entry.
func TestRevocation(t *testing.T) {
	f := newFixture(t)
	nl := f.wallet("nl")
	tok := nl.token(f.preAuthCodeRef("nl", "case-42"))
	keys := []*ecdsa.PrivateKey{testpki.NewKey(t), testpki.NewKey(t)}
	nonce := nl.nonce()
	status, body := nl.credentialRequest(tok.AccessToken, nl.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0],
		Proofs: &oid4vci.Proofs{JWT: []string{
			testwallet.JWTProof(t, keys[0], f.id("nl"), nonce, ""), testwallet.JWTProof(t, keys[1], f.id("nl"), nonce, ""),
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("issue: %d %v", status, body)
	}
	revoke := func(raw string) (int, map[string]any) {
		resp, err := http.Post(f.internal.URL+"/revocations", "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return decode(t, resp)
	}
	for _, bad := range []string{`{"provider":"tax","list":"income"}`, `{"list":"income","index":1}`, `{}`, `{"reference":"case-42","provider":"tax","list":"income","index":1}`} {
		if status, _ := revoke(bad); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400: a revocation is never guessed", bad, status)
		}
	}
	if status, body := revoke(`{"reference":"case-42"}`); status != http.StatusOK || body["revoked"] != float64(2) {
		t.Fatalf("revoke by reference: %d %v", status, body)
	}
	anchors := []x5c.Anchors{x5c.NewAnchors("issuer", f.issuerRoot.Cert)}
	for _, c := range body["credentials"].([]any) {
		parsed, err := sdjwtvc.Parse(c.(map[string]any)["credential"].(string), anchors, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ref := parsed.Claims["status"].(map[string]any)["status_list"].(map[string]any)
		if bits := fetchStatus(t, f, ref["uri"].(string)); !statuslist.Revoked(bits, int(ref["idx"].(float64))) {
			t.Fatal("every credential of the offer must be revoked")
		}
	}
}

// checkPreview fetches the NL preview (P1), with Bearer and without DPoP,
// checks its shape and that it answers once per token, and returns the
// payload.
func checkPreview(t *testing.T, nl *wallet, accessToken, credID string) map[string]any {
	t.Helper()
	status, preview := nl.post("/credential_preview", nil, map[string]string{"Authorization": "Bearer " + accessToken})
	if status != http.StatusOK {
		t.Fatalf("preview: %d %v", status, preview)
	}
	p := preview["credential_previews"].([]any)[0].(map[string]any)
	payload := p["credential_payload"].(map[string]any)
	if p["credential_id"] != credID || p["config_id"] != incomeConfig || p["format"] != "dc+sd-jwt" ||
		payload["year"].(map[string]any)["type"] != "text" || payload["address"].(map[string]any)["type"] != "object" ||
		payload["income"].(map[string]any)["type"] != "number" {
		t.Fatalf("preview: %v", p)
	}
	if status, body := nl.post("/credential_preview", nil, map[string]string{"Authorization": "Bearer " + accessToken}); status != http.StatusBadRequest {
		t.Fatalf("second preview: %d %v", status, body)
	}
	return payload
}

// P-1, P-2, P-4, P-6: each provider is its own Attestation Provider under
// the same issuer service.
func TestProviders(t *testing.T) {
	f := newFixture(t)

	t.Run("P-1 metadata of a provider", func(t *testing.T) {
		status, body, _ := get(t, f.public.URL+"/.well-known/openid-credential-issuer/civil/nl", "application/jwt")
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		tok, err := jwt.ParseSigned(body, []jose.SignatureAlgorithm{jose.ES256})
		if err != nil {
			t.Fatal(err)
		}
		// Signed with the civil provider's own access certificate.
		if _, _, err := x5c.Verify(tok.Headers[0], []x5c.Anchors{x5c.NewAnchors("tax", f.accessRoot.Cert)}, time.Now(), nil); err == nil {
			t.Fatal("the civil metadata must not verify against the tax access root")
		}
		leaf, _, err := x5c.Verify(tok.Headers[0], []x5c.Anchors{x5c.NewAnchors("civil", f.civilAccessRoot.Cert)}, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var md map[string]any
		if err := tok.Claims(leaf.PublicKey, &md); err != nil {
			t.Fatal(err)
		}
		configs := md["credential_configurations_supported"].(map[string]any)
		if _, ok := configs[deathConfig]; !ok || len(configs) != 1 || md["credential_issuer"] != f.idOf("civil", "nl") {
			t.Fatalf("civil metadata: %v", md)
		}
		info := md["issuer_info"].([]any)[0].(map[string]any)
		if info["data"] != base64.RawURLEncoding.EncodeToString(f.civilRegistration) {
			t.Fatalf("issuer_info must carry the civil registration certificate: %v", info)
		}
	})

	civilToken := func(t *testing.T, reference string) (*wallet, oid4vci.TokenResponse) {
		t.Helper()
		status, body := f.offer(map[string]any{"profile": "nl", "credential_configuration_ids": []string{deathConfig},
			"subject": map[string]any{"bsn": "1"}, "reference": reference})
		if status != http.StatusCreated || body["credential_offer"].(map[string]any)["credential_issuer"] != f.idOf("civil", "nl") {
			t.Fatalf("civil offer: %d %v", status, body)
		}
		code := body["credential_offer"].(map[string]any)["grants"].(map[string]any)[oid4vci.GrantPreAuthorizedCode].(map[string]any)["pre-authorized_code"].(string)
		w := f.wallet("nl")
		w.issuer = "civil"
		return w, w.token(code)
	}
	issue := func(t *testing.T, w *wallet, tok oid4vci.TokenResponse) string {
		t.Helper()
		status, body := w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0],
			Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.idOf(w.issuer, w.profile), w.nonce(), "")}},
		})
		if status != http.StatusOK {
			t.Fatalf("credential: %d %v", status, body)
		}
		return body["credentials"].([]any)[0].(map[string]any)["credential"].(string)
	}

	t.Run("P-2 credential signed by its provider", func(t *testing.T) {
		w, tok := civilToken(t, "")
		cred := issue(t, w, tok)
		if _, err := sdjwtvc.Parse(cred, []x5c.Anchors{x5c.NewAnchors("tax", f.issuerRoot.Cert)}, nil, time.Now()); err == nil {
			t.Fatal("a civil credential must not verify against the tax issuer root")
		}
		parsed, err := sdjwtvc.Parse(cred, []x5c.Anchors{x5c.NewAnchors("civil", f.civilIssuerRoot.Cert)}, mdlIssuerEKU, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		uri := parsed.Claims["status"].(map[string]any)["status_list"].(map[string]any)["uri"].(string)
		if uri != f.public.URL+"/status/civil/death" {
			t.Fatalf("status uri %s", uri)
		}
		fetchStatusFrom(t, f.civilIssuerRoot, uri)
	})

	t.Run("P-4 a code of one provider does not work at another", func(t *testing.T) {
		status, body := f.offer(map[string]any{"profile": "nl", "credential_configuration_ids": []string{deathConfig}, "subject": map[string]any{"bsn": "1"}})
		if status != http.StatusCreated {
			t.Fatal(body)
		}
		code := body["credential_offer"].(map[string]any)["grants"].(map[string]any)[oid4vci.GrantPreAuthorizedCode].(map[string]any)["pre-authorized_code"].(string)
		tax := f.wallet("nl") // talks to the tax provider
		if status, body := tax.tokenRequest(code, nil); status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidGrant {
			t.Fatalf("civil code at tax: %d %v", status, body)
		}
		civil := f.wallet("nl")
		civil.issuer = "civil"
		civil.token(code) // the refusal did not burn it
	})

	t.Run("P-4 a token of one provider does not work at another", func(t *testing.T) {
		w, tok := civilToken(t, "")
		w.issuer = "tax"
		status, body := w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
			CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0],
			Proofs:               &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.id("nl"), w.nonce(), "")}},
		})
		if status != http.StatusUnauthorized || body["error"] != oid4vci.ErrInvalidToken {
			t.Fatalf("civil token at tax: %d %v", status, body)
		}
	})

	t.Run("P-6 revocation by reference spans providers", func(t *testing.T) {
		w, tok := civilToken(t, "case-77")
		civilCred := issue(t, w, tok)
		taxWallet := f.wallet("nl")
		taxTok := taxWallet.token(f.preAuthCodeRef("nl", "case-77"))
		taxCred := issue(t, taxWallet, taxTok)
		resp, err := http.Post(f.internal.URL+"/revocations", "application/json", strings.NewReader(`{"reference":"case-77"}`))
		if err != nil {
			t.Fatal(err)
		}
		if status, body := decode(t, resp); status != http.StatusOK || body["revoked"] != float64(2) {
			t.Fatalf("revoke: %d %v", status, body)
		}
		for root, cred := range map[*testpki.Cert]string{&f.civilIssuerRoot: civilCred, &f.issuerRoot: taxCred} {
			parsed, err := sdjwtvc.Parse(cred, []x5c.Anchors{x5c.NewAnchors("p", root.Cert)}, nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ref := parsed.Claims["status"].(map[string]any)["status_list"].(map[string]any)
			if bits := fetchStatusFrom(t, *root, ref["uri"].(string)); !statuslist.Revoked(bits, int(ref["idx"].(float64))) {
				t.Fatalf("credential at %s is not revoked", ref["uri"])
			}
		}
	})
}
