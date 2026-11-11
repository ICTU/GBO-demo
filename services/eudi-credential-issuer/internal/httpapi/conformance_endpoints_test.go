package httpapi_test

// Conformance cases the matrices named as untested (conformance/
// oid4vci-1.0.yaml, conformance/haip-1.0.yaml), through the wallet-facing
// endpoints: metadata, the authorization and token endpoints, the nonce,
// deferred and notification endpoints, the request objects of
// disclosure-based issuance, and what issued credentials and the log carry.

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

// OID4VCI-12.2.4-05, -18, -19, -22, -24, -25; HAIP-4-09: the unsigned
// metadata of every profile.
func TestConformanceIssuerMetadata(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"nl", "eudi", "wallet"} {
		md := f.metadata(t, "tax", profile)
		// 12.2.4-05: credential_endpoint is REQUIRED.
		if md["credential_endpoint"] != f.id(profile)+"/credential" {
			t.Errorf("%s: credential_endpoint = %v", profile, md["credential_endpoint"])
		}
		// 12.2.4-18, 12.2.4-19, HAIP-4-09: batch issuance is supported, so
		// batch_credential_issuance is there with a batch_size of 2 or more.
		batch, _ := md["batch_credential_issuance"].(map[string]any)
		if size, _ := batch["batch_size"].(float64); size != issuance.DefaultBatchSize {
			t.Errorf("%s: batch_credential_issuance = %v, want batch_size %d", profile, md["batch_credential_issuance"], issuance.DefaultBatchSize)
		}
		configs, _ := md["credential_configurations_supported"].(map[string]any)
		if len(configs) == 0 {
			t.Fatalf("%s: no credential configurations", profile)
		}
		for id, raw := range configs {
			c := raw.(map[string]any)
			// 12.2.4-22: format is REQUIRED.
			if c["format"] != "dc+sd-jwt" {
				t.Errorf("%s/%s: format = %v", profile, id, c["format"])
			}
			// 12.2.4-24: every credential is key-bound, so the binding
			// methods are there.
			if methods, _ := c["cryptographic_binding_methods_supported"].([]any); len(methods) != 1 || methods[0] != "jwk" {
				t.Errorf("%s/%s: cryptographic_binding_methods_supported = %v", profile, id, c["cryptographic_binding_methods_supported"])
			}
			// 12.2.4-25: proof_signing_alg_values_supported is REQUIRED per
			// proof type.
			proofs, _ := c["proof_types_supported"].(map[string]any)
			if len(proofs) == 0 {
				t.Errorf("%s/%s: no proof types", profile, id)
			}
			for name, p := range proofs {
				if algs, _ := p.(map[string]any)["proof_signing_alg_values_supported"].([]any); !containsAny(algs, "ES256") {
					t.Errorf("%s/%s: proof type %s has proof_signing_alg_values_supported %v", profile, id, name, algs)
				}
			}
		}
	}
}

// OID4VCI-12.2.4-24: the credentials are key-bound, as the metadata says:
// each carries the holder key in cnf.
func TestConformanceCredentialsAreKeyBound(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"nl", "eudi", "wallet"} {
		cred, holder := issueOne(t, f, profile)
		var cnf struct {
			JWK jose.JSONWebKey `json:"jwk"`
		}
		remarshal(t, parseCredential(t, f, cred)["cnf"], &cnf)
		if k, ok := cnf.JWK.Key.(*ecdsa.PublicKey); !ok || !k.Equal(&holder.PublicKey) {
			t.Errorf("%s: cnf = %v, want the holder key", profile, cnf)
		}
	}
}

// OID4VCI-12.2.3-07: signed metadata carries iat (and exp, an hour later).
func TestConformanceSignedMetadataIssuedAt(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"nl", "eudi", "wallet"} {
		status, body, _ := get(t, f.public.URL+"/.well-known/openid-credential-issuer/tax/"+profile, "application/jwt")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", profile, status)
		}
		tok, err := jwt.ParseSigned(body, []jose.SignatureAlgorithm{jose.ES256})
		if err != nil {
			t.Fatal(err)
		}
		leaf, _, err := x5c.Verify(tok.Headers[0], []x5c.Anchors{x5c.NewAnchors("access", f.accessRoot.Cert)}, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var claims struct {
			IAT *int64 `json:"iat"`
			EXP *int64 `json:"exp"`
		}
		if err := tok.Claims(leaf.PublicKey, &claims); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		if claims.IAT == nil || *claims.IAT < now-60 || *claims.IAT > now+60 {
			t.Fatalf("%s: iat = %v, want the time of signing", profile, claims.IAT)
		}
		if claims.EXP == nil || *claims.EXP != *claims.IAT+3600 {
			t.Errorf("%s: exp = %v, want iat plus an hour", profile, claims.EXP)
		}
	}
}

// OID4VCI-7.2-02, OID4VCI-7.2-03: every c_nonce is new and 256 bits long,
// and the nonce response is uncacheable.
func TestConformanceNonceEndpoint(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"nl", "eudi", "wallet"} {
		w := f.walletOn(profile, "eudi")
		seen := map[string]bool{}
		for range 20 {
			status, body := w.post("/nonce", nil, nil)
			if status != http.StatusOK {
				t.Fatalf("%s: nonce = %d %v", profile, status, body)
			}
			// 7.2-03: Cache-Control with no-store.
			if cc := w.header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Errorf("%s: Cache-Control = %q, want no-store", profile, cc)
			}
			// 7.2-02: unpredictable: 32 random bytes, never repeated.
			n, _ := body["c_nonce"].(string)
			if raw, err := base64.RawURLEncoding.DecodeString(n); err != nil || len(raw) != 32 {
				t.Errorf("%s: c_nonce %q is not 32 bytes", profile, n)
			}
			if seen[n] {
				t.Fatalf("%s: c_nonce %q handed out twice", profile, n)
			}
			seen[n] = true
		}
	}
}

// conformanceToken posts form to the token endpoint with a valid client
// attestation and DPoP proof.
func (w *wallet) conformanceToken(form url.Values) (int, map[string]any) {
	w.f.t.Helper()
	id := w.f.idOf(w.issuer, w.profile)
	return w.post("/token", strings.NewReader(form.Encode()), map[string]string{
		"OAuth-Client-Attestation":     w.wia,
		"OAuth-Client-Attestation-PoP": testwallet.PoP(w.f.t, w.wiaKey, w.clientID, id, w.challenge()),
		"DPoP":                         testwallet.DPoP(w.f.t, w.dpopKey, "POST", id+"/token", ""),
	}, "Content-Type", "application/x-www-form-urlencoded")
}

// OID4VCI-6.1-01, OID4VCI-6.1-09: the pre-authorized code grant needs its
// code, and the token endpoint ignores parameters it does not know.
func TestConformanceTokenParameters(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ profile, as string }{{"nl", "nl"}, {"eudi", "eudi"}, {"wallet", "nl"}, {"wallet", "eudi"}} {
		w := f.walletOn(tc.profile, tc.as)
		// 6.1-01: pre-authorized_code is REQUIRED with this grant.
		for name, form := range map[string]url.Values{
			"no pre-authorized_code":    {"grant_type": {oid4vci.GrantPreAuthorizedCode}},
			"empty pre-authorized_code": {"grant_type": {oid4vci.GrantPreAuthorizedCode}, "pre-authorized_code": {""}},
		} {
			if status, body := w.conformanceToken(form); status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidRequest {
				t.Errorf("%s as %s, %s: token = %d %v, want invalid_request", tc.profile, tc.as, name, status, body)
			}
		}
		// 6.1-09: unrecognized parameters are ignored.
		status, body := w.tokenRequestForm(f.preAuthCode(tc.profile), url.Values{
			"unknown_parameter": {"x"}, "x-vendor-extension": {`{"a":1}`},
		})
		if status != http.StatusOK || body["access_token"] == nil {
			t.Errorf("%s as %s: token with unknown parameters = %d %v, want a token", tc.profile, tc.as, status, body)
		}
	}
}

// OID4VCI-5.1.3-02: the PAR and authorization endpoints ignore parameters
// they do not know (eudi only: the other profiles have no authorization
// code flow).
func TestConformanceAuthorizationIgnoresUnknownParameters(t *testing.T) {
	f := newFixture(t)
	w := f.acfWallet()
	form := w.parForm()
	form.Set("unknown_parameter", "x")
	form.Set("x-vendor-extension", `{"a":1}`)
	status, body := w.par(form, nil)
	if status != http.StatusCreated {
		t.Fatalf("PAR with unknown parameters = %d %v, want 201", status, body)
	}
	q := url.Values{"client_id": {w.clientID}, "request_uri": {body["request_uri"].(string)}, "unknown_parameter": {"x"}}
	status, location, page := browse(t, f.id("eudi")+"/authorize?"+q.Encode())
	if status != http.StatusFound || !strings.HasPrefix(location, f.loginBase) {
		t.Fatalf("authorize with an unknown parameter = %d %q %s, want the login", status, location, page)
	}
	// The flow goes on as without them.
	if status, location, _ := w.login(location); status != http.StatusFound || !strings.HasPrefix(location, walletRedirect+"?") {
		t.Fatalf("callback = %d %q", status, location)
	}
}

// OID4VCI-5.1.1-02, OID4VCI-5.1.1-03: an authorization detail needs type
// openid_credential. One without type, or with another, is refused with
// invalid_authorization_details (RFC 9396 section 5); eudi only.
func TestConformanceAuthorizationDetailsType(t *testing.T) {
	f := newFixture(t)
	w := f.acfWallet()
	par := func(details string) (int, map[string]any) {
		form := w.parForm()
		form.Del("issuer_state")
		form.Del("scope")
		form.Set("authorization_details", details)
		return w.par(form, nil)
	}
	valid := `{"type":"openid_credential","credential_configuration_id":"` + incomeConfig + `"}`
	for name, details := range map[string]string{
		"5.1.1-02: no type":                    `[{"credential_configuration_id":"` + incomeConfig + `"}]`,
		"5.1.1-02: an empty type":              `[{"type":"","credential_configuration_id":"` + incomeConfig + `"}]`,
		"5.1.1-03: another type":               `[{"type":"openid_credentials","credential_configuration_id":"` + incomeConfig + `"}]`,
		"5.1.1-03: one of two of another type": `[` + valid + `,{"type":"payment_initiation","credential_configuration_id":"` + incomeConfig + `"}]`,
	} {
		if status, body := par(details); status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidAuthorizationDetails {
			t.Errorf("%s: PAR = %d %v, want invalid_authorization_details", name, status, body)
		}
	}
	if status, body := par(`[` + valid + `]`); status != http.StatusCreated {
		t.Fatalf("type openid_credential: PAR = %d %v, want 201", status, body)
	}
}

// OID4VCI-9.1-03: unknown members of a deferred credential request, plain
// or encrypted, are ignored (eudi only: the other profiles defer nothing).
func TestConformanceDeferredRequestIgnoresUnknownMembers(t *testing.T) {
	f := newFixture(t)
	plain, plainTok, _, plainCred := deferredStart(t, f)
	encrypted, encTok, _, encCred := deferredStart(t, f)
	f.setSource(http.StatusOK, nil)

	status, body := plain.deferred(plainTok.AccessToken, nil, map[string]any{
		"transaction_id": plainCred["transaction_id"], "unknown_member": "x", "x-vendor-extension": map[string]any{"a": 1},
	})
	if status != http.StatusOK || body["credentials"] == nil {
		t.Errorf("plain request with unknown members = %d %v, want the credential", status, body)
	}

	raw, _ := json.Marshal(map[string]any{"transaction_id": encCred["transaction_id"], "unknown_member": "x"})
	jwe := testwallet.EncryptRequest(t, raw, f.requestKey(t, "tax"), jose.A256GCM, nil)
	status, _, out := encrypted.postJWTTo(t, "/deferred_credential", encTok.AccessToken, jwe)
	if status != http.StatusOK || !strings.Contains(out, `"credentials"`) {
		t.Errorf("encrypted request with unknown members = %d %s, want the credential", status, out)
	}
}

// OID4VCI-11.1-06: unknown members of a notification request are ignored
// (eudi only: the other profiles have no notification endpoint).
func TestConformanceNotificationIgnoresUnknownMembers(t *testing.T) {
	f := newFixture(t)
	w, at, body := issuedOnEUDI(t, f)
	status, resp := w.notify(at, map[string]any{
		"notification_id": body["notification_id"], "event": "credential_accepted",
		"unknown_member": "x", "x-vendor-extension": map[string]any{"a": 1},
	})
	if status != http.StatusNoContent {
		t.Fatalf("notification with unknown members = %d %v, want 204", status, resp)
	}
}

// issueOne issues one credential on profile and returns it with its holder
// key; on the combined profile the wallet is an NL Wallet.
func issueOne(t *testing.T, f *fixture, profile string) (string, *ecdsa.PrivateKey) {
	t.Helper()
	w := f.wallet(profile)
	tok := w.token(f.preAuthCode(profile))
	return issueFor(t, w, tok)
}

// issueFor asks for the first credential of tok with a fresh holder key.
func issueFor(t *testing.T, w *wallet, tok oid4vci.TokenResponse) (string, *ecdsa.PrivateKey) {
	t.Helper()
	holder := testpki.NewKey(t)
	nonce := w.nonce()
	proofs := &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, holder, w.f.idOf(w.issuer, w.profile), nonce, "")}}
	if w.profile == "eudi" {
		proofs = &oid4vci.Proofs{Attestation: []string{w.f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{holder}, nonce, time.Now().Add(time.Hour))}}
	}
	status, body := w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: proofs,
	})
	if status != http.StatusOK {
		t.Fatalf("credential = %d %v", status, body)
	}
	return body["credentials"].([]any)[0].(map[string]any)["credential"].(string), holder
}

// parseCredential verifies a credential of the tax provider and returns
// its claims.
func parseCredential(t *testing.T, f *fixture, cred string) map[string]any {
	t.Helper()
	parsed, err := sdjwtvc.Parse(cred, []x5c.Anchors{x5c.NewAnchors("issuer", f.issuerRoot.Cert)}, mdlIssuerEKU, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Claims
}

// revoked reports whether the status list lists cred as revoked.
func revoked(t *testing.T, f *fixture, cred string) bool {
	t.Helper()
	ref := parseCredential(t, f, cred)["status"].(map[string]any)["status_list"].(map[string]any)
	return statuslist.Revoked(fetchStatus(t, f, ref["uri"].(string)), int(ref["idx"].(float64)))
}

// OID4VCI-14.3-01: a later credential request, or a re-issuance with a
// refresh token (eudi), revokes nothing issued before.
func TestConformanceLaterRequestsRevokeNothing(t *testing.T) {
	f := newFixture(t)
	t.Run("eudi", func(t *testing.T) {
		w := f.wallet("eudi")
		first := w.token(f.preAuthCode("eudi"))
		earlier, _ := issueFor(t, w, first)
		// A later credential request, for a new offer of the same data.
		issueFor(t, w, w.token(f.preAuthCode("eudi")))
		if revoked(t, f, earlier) {
			t.Fatal("a later credential request revoked an earlier credential")
		}
		// A re-issuance with the refresh token of the first.
		status, body := w.refresh(first.RefreshToken, nil)
		if status != http.StatusOK {
			t.Fatalf("refresh = %d %v", status, body)
		}
		var again oid4vci.TokenResponse
		remarshal(t, body, &again)
		issueFor(t, w, again)
		if revoked(t, f, earlier) {
			t.Fatal("a re-issuance revoked the earlier credential")
		}
	})
	for _, profile := range []string{"nl", "wallet"} {
		t.Run(profile, func(t *testing.T) {
			w := f.wallet(profile)
			earlier, _ := issueOne(t, f, profile)
			issueFor(t, w, w.token(f.preAuthCode(profile)))
			if revoked(t, f, earlier) {
				t.Fatal("a later credential request revoked an earlier credential")
			}
		})
	}
}

// OID4VCI-15.3-02: neither issued credentials nor their claims reach the
// log.
func TestConformanceCredentialsAreNotLogged(t *testing.T) {
	f := newFixture(t)
	var creds []string
	for _, profile := range []string{"nl", "eudi", "wallet"} {
		cred, _ := issueOne(t, f, profile)
		creds = append(creds, cred)
	}
	// And after a deferred issuance (eudi, after a login).
	w, tok, _, cred := deferredStart(t, f)
	f.setSource(http.StatusOK, nil)
	status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]})
	if status != http.StatusOK {
		t.Fatalf("deferred = %d %v", status, body)
	}
	creds = append(creds, body["credentials"].([]any)[0].(map[string]any)["credential"].(string))

	logs := f.logs.String()
	for _, c := range creds {
		jws, disclosures, _ := strings.Cut(c, "~")
		parts := strings.Split(jws, ".")
		for name, secret := range map[string]string{
			"credential payload": parts[1],
			"issuer signature":   parts[2],
			"a disclosure":       strings.Split(disclosures, "~")[0],
		} {
			if secret != "" && strings.Contains(logs, secret) {
				t.Errorf("the log holds the %s", name)
			}
		}
	}
	// The claims of the fake source.
	for _, claim := range []string{"Utrecht", "41000"} {
		if strings.Contains(logs, claim) {
			t.Errorf("the log holds the claim value %q", claim)
		}
	}
}

// OID4VCI-15.4.1-02: nbf and exp are rounded to the start of a day, as iat
// is, for issuance at once and deferred issuance.
func TestConformanceTimeClaimsAreRounded(t *testing.T) {
	f := newFixture(t)
	creds := map[string]string{}
	for _, profile := range []string{"nl", "eudi", "wallet"} {
		creds[profile], _ = issueOne(t, f, profile)
	}
	w, tok, _, cred := deferredStart(t, f)
	f.setSource(http.StatusOK, nil)
	status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]})
	if status != http.StatusOK {
		t.Fatalf("deferred = %d %v", status, body)
	}
	creds["eudi deferred"] = body["credentials"].([]any)[0].(map[string]any)["credential"].(string)

	for name, c := range creds {
		claims := parseCredential(t, f, c)
		for _, claim := range []string{"iat", "nbf", "exp"} {
			v, ok := claims[claim].(float64)
			if !ok || int64(v)%86400 != 0 {
				t.Errorf("%s: %s = %v, want the start of a day", name, claim, claims[claim])
			}
		}
		// exp is nbf plus the configured 365 days.
		if nbf, exp := claims["nbf"].(float64), claims["exp"].(float64); exp-nbf != 365*86400 {
			t.Errorf("%s: exp - nbf = %v s, want 365 days", name, exp-nbf)
		}
	}
}

// HAIP-5-14: every request object of disclosure-based issuance carries its
// own ephemeral P-256 encryption key in client_metadata (nl and eudi; the
// combined profile has no disclosure-based issuance).
func TestConformanceEphemeralResponseKeys(t *testing.T) {
	f := newFixture(t)
	seen := map[string]string{}
	for _, profile := range []string{"nl", "eudi"} {
		for i := range 3 {
			status, r := f.fetchRequest(t, "tax", profile, incomeOffer)
			if status != http.StatusOK {
				t.Fatalf("%s: request = %d", profile, status)
			}
			keys, _ := r.claims["client_metadata"].(map[string]any)["jwks"].(map[string]any)["keys"].([]any)
			if len(keys) != 1 {
				t.Fatalf("%s: client_metadata keys = %v, want one", profile, keys)
			}
			k := keys[0].(map[string]any)
			if k["kty"] != "EC" || k["crv"] != "P-256" || k["d"] != nil {
				t.Errorf("%s: key %v, want a public P-256 key", profile, k)
			}
			var jwk jose.JSONWebKey
			raw, _ := json.Marshal(k)
			if err := jwk.UnmarshalJSON(raw); err != nil {
				t.Fatal(err)
			}
			pub, _ := jwk.Key.(*ecdsa.PublicKey)
			if pub == nil {
				t.Fatalf("%s: key %v is not an EC public key", profile, k)
			}
			id := base64.RawURLEncoding.EncodeToString(pub.X.Bytes()) + "." + base64.RawURLEncoding.EncodeToString(pub.Y.Bytes())
			if prev, ok := seen[id]; ok {
				t.Fatalf("%s request %d: the key of %s is used again", profile, i, prev)
			}
			seen[id] = profile
		}
	}
}
