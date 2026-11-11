package oid4vp_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const wrprc = "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"

func (f *fixture) request(enc oid4vp.RegistrationEncoding) oid4vp.Request {
	return oid4vp.Request{
		Session: f.session, ResponseURI: "https://issuer.test/vp/response/s1",
		RegistrationCertificate: wrprc, Encoding: enc, IssuedAt: f.now, Expires: f.now.Add(5 * time.Minute),
	}
}

// V1: the request object the wallets require.
func TestRequestObject(t *testing.T) {
	f := newFixture(t)
	for enc, wantData := range map[oid4vp.RegistrationEncoding]string{
		oid4vp.RegistrationBase64URL: base64.RawURLEncoding.EncodeToString([]byte(wrprc)),
		oid4vp.RegistrationJWT:       wrprc,
	} {
		signed, err := f.signer.Sign(f.request(enc))
		if err != nil {
			t.Fatal(err)
		}
		tok, err := jwt.ParseSigned(signed, []jose.SignatureAlgorithm{jose.ES256})
		if err != nil {
			t.Fatal(err)
		}
		h := tok.Headers[0]
		if h.ExtraHeaders[jose.HeaderType] != "oauth-authz-req+jwt" {
			t.Fatalf("typ %v", h.ExtraHeaders[jose.HeaderType])
		}
		var raw struct {
			X5C []string `json:"x5c"`
		}
		header, _ := base64.RawURLEncoding.DecodeString(strings.Split(signed, ".")[0])
		if err := json.Unmarshal(header, &raw); err != nil || len(raw.X5C) != 1 || raw.X5C[0] != base64.StdEncoding.EncodeToString(f.wrpac.Cert.Raw) {
			t.Fatalf("x5c must hold the WRPAC chain without the root: %s", header)
		}
		var c map[string]any
		if err := tok.Claims(&f.wrpac.Key.PublicKey, &c); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(f.wrpac.Cert.Raw)
		want := map[string]any{
			"client_id": "x509_hash:" + base64.RawURLEncoding.EncodeToString(sum[:]), "aud": "https://self-issued.me/v2",
			"response_type": "vp_token", "response_mode": "direct_post.jwt", "response_uri": "https://issuer.test/vp/response/s1",
			"nonce": "nonce-1", "state": "state-1",
		}
		for k, v := range want {
			if c[k] != v {
				t.Errorf("%s: %v, want %v", k, c[k], v)
			}
		}
		if c["client_id"] != f.session.ClientID {
			t.Error("the session must carry the signer's client_id")
		}
		dcql := c["dcql_query"].(map[string]any)["credentials"].([]any)[0].(map[string]any)
		if dcql["id"] != "pid" || dcql["format"] != "dc+sd-jwt" || dcql["claims"].([]any)[0].(map[string]any)["path"].([]any)[0] != "bsn" ||
			dcql["meta"].(map[string]any)["vct_values"].([]any)[0] != pidType {
			t.Errorf("dcql_query %v", dcql)
		}
		if c["iat"] != float64(f.now.Unix()) || c["exp"] != float64(f.now.Add(5*time.Minute).Unix()) {
			t.Errorf("iat %v exp %v", c["iat"], c["exp"])
		}
		meta := c["client_metadata"].(map[string]any)
		jwk := meta["jwks"].(map[string]any)["keys"].([]any)[0].(map[string]any)
		if jwk["kid"] != f.session.Key.KeyID || jwk["alg"] != "ECDH-ES" || jwk["use"] != "enc" || jwk["d"] != nil {
			t.Errorf("jwks %v", jwk)
		}
		if encs := meta["encrypted_response_enc_values_supported"].([]any); len(encs) != 2 || encs[0] != "A128GCM" || encs[1] != "A256GCM" {
			t.Errorf("enc values %v", encs)
		}
		formats, _ := json.Marshal(meta["vp_formats_supported"])
		if string(formats) != `{"dc+sd-jwt":{"kb-jwt_alg_values":["ES256"],"sd-jwt_alg_values":["ES256"]}}` {
			t.Errorf("vp_formats_supported %s", formats)
		}
		info := c["verifier_info"].([]any)
		if len(info) != 1 || info[0].(map[string]any)["format"] != "registration_cert" || info[0].(map[string]any)["data"] != wantData {
			t.Errorf("verifier_info %v", info)
		}
	}
}

func TestRequestObjectRefuses(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]func(r *oid4vp.Request){
		"another client_id": func(r *oid4vp.Request) { r.Session.ClientID = "x509_hash:other" },
		"two credentials": func(r *oid4vp.Request) {
			r.Session.Query.Credentials = append(r.Session.Query.Credentials, r.Session.Query.Credentials[0])
		},
		"no claims":          func(r *oid4vp.Request) { r.Session.Query = query() },
		"bad query id":       func(r *oid4vp.Request) { r.Session.Query.Credentials[0].ID = "p i d" },
		"no nonce":           func(r *oid4vp.Request) { r.Session.Nonce = "" },
		"no response key":    func(r *oid4vp.Request) { r.Session.Key = oid4vp.ResponseKey{} },
		"no WRPRC":           func(r *oid4vp.Request) { r.RegistrationCertificate = "" },
		"no validity period": func(r *oid4vp.Request) { r.Expires = r.IssuedAt },
	} {
		r := f.request(oid4vp.RegistrationJWT)
		r.Session.Query = query([]string{"bsn"})
		mutate(&r)
		if _, err := f.signer.Sign(r); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// V2: a valid response gives the disclosed claims and the anchor.
func TestValidResponse(t *testing.T) {
	f := newFixture(t)
	for _, enc := range []jose.ContentEncryption{jose.A128GCM, jose.A256GCM} {
		d, err := f.verify(f.respondWith(t, f.body(f.present(t, f.pid(t, pidOptions{}), "bsn")), f.session.Key.PublicJWK(), enc))
		if err != nil {
			t.Fatalf("%s: %v", enc, err)
		}
		if d.Claims["bsn"] != "999991772" || d.Anchor != "nl" || d.Issuer == nil {
			t.Fatalf("%s: %+v", enc, d)
		}
		if _, ok := d.Claims["given_name"]; ok {
			t.Fatal("an undisclosed claim must not appear")
		}
	}
	t.Run("the test PID", func(t *testing.T) {
		issuer, status := pidSigners(t, f.testRoot)
		f.tokens[statusURI] = f.statusToken(t, status, nil)
		d, err := f.verify(f.respond(t, f.body(f.present(t, f.pid(t, pidOptions{issuer: &issuer}), "bsn"))))
		if err != nil || d.Anchor != "test-pid" {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("a nested claim with its parent", func(t *testing.T) {
		f := newFixture(t)
		f.session.Query = query([]string{"address", "locality"})
		d, err := f.verify(f.respond(t, f.body(f.present(t, f.pid(t, pidOptions{}), "address", "locality"))))
		if err != nil || d.Claims["address"].(map[string]any)["locality"] != "Rotterdam" {
			t.Fatalf("%+v %v", d, err)
		}
	})
}

// V3: a response that cannot be decrypted.
func TestResponseDoesNotDecrypt(t *testing.T) {
	f := newFixture(t)
	body := f.body(f.present(t, f.pid(t, pidOptions{}), "bsn"))
	other, err := oid4vp.NewResponseKey()
	if err != nil {
		t.Fatal(err)
	}
	otherSameKid := other.PublicJWK()
	otherSameKid.KeyID = f.session.Key.KeyID
	unknownKid := f.session.Key.PublicJWK()
	unknownKid.KeyID = "other"
	f.mustRefuse(t, "another key", "not encrypted to the session's key", f.respondWith(t, body, other.PublicJWK(), jose.A128GCM))
	f.mustRefuse(t, "another key with the session's kid", "does not decrypt", f.respondWith(t, body, otherSameKid, jose.A128GCM))
	f.mustRefuse(t, "an unknown kid", "not encrypted to the session's key", f.respondWith(t, body, unknownKid, jose.A128GCM))
	keyWrap := testwallet.EncryptWith(t, []byte(`{}`), f.session.Key.PublicJWK(), jose.ECDH_ES_A128KW, jose.A128GCM)
	f.mustRefuse(t, "alg not ECDH-ES", "not a JWE", keyWrap)
	f.mustRefuse(t, "enc not offered", "not a JWE", f.respondWith(t, body, f.session.Key.PublicJWK(), jose.A192GCM))
	f.mustRefuse(t, "not a JWE", "not a JWE", testwallet.Sign(t, f.holder, "jwt", nil, false, body))
	zipped := testwallet.EncryptRequest(t, []byte(`{}`), f.session.Key.PublicJWK(), jose.A128GCM, (&jose.EncrypterOptions{Compression: jose.DEFLATE}))
	f.mustRefuse(t, "compressed", "compression", zipped)
}

// V4: vp_token and state must match the session's query.
func TestResponseShape(t *testing.T) {
	f := newFixture(t)
	pres := f.present(t, f.pid(t, pidOptions{}), "bsn")
	f.mustRefuse(t, "another query id", "vp_token must hold", f.respond(t, map[string]any{"vp_token": map[string]any{"other": []string{pres}}, "state": "state-1"}))
	f.mustRefuse(t, "an extra query id", "vp_token must hold", f.respond(t, map[string]any{"vp_token": map[string]any{"pid": []string{pres}, "other": []string{pres}}, "state": "state-1"}))
	f.mustRefuse(t, "two presentations", "vp_token must hold", f.respond(t, f.body(pres, pres)))
	f.mustRefuse(t, "no presentation", "vp_token must hold", f.respond(t, f.body()))
	f.mustRefuse(t, "another state", "state does not match", f.respond(t, map[string]any{"vp_token": map[string]any{"pid": []string{pres}}, "state": "state-2"}))
	f.mustRefuse(t, "no vp_token", "vp_token must map", f.respond(t, map[string]any{"state": "state-1"}))
}

// V5: the PID itself.
func TestResponsePID(t *testing.T) {
	f := newFixture(t)
	untrusted, _ := pidSigners(t, testpki.NewRoot(t, "untrusted"))
	noEKU := f.pidRoot.Issue(t, testpki.Options{Subject: pidName})
	for name, tc := range map[string]struct {
		o    pidOptions
		want string
	}{
		"an untrusted issuer":          {pidOptions{issuer: &untrusted}, "chain does not verify"},
		"a signer without the mDL EKU": {pidOptions{issuer: &noEKU}, "extended key usage"},
		"another vct":                  {pidOptions{vct: "urn:eudi:pid:1"}, "is not asked for"},
		"expired":                      {pidOptions{expires: f.now.Add(-oid4vp.ClockSkew - time.Second)}, "has expired"},
		"not yet valid":                {pidOptions{notBefore: f.now.Add(oid4vp.ClockSkew + time.Second)}, "not yet valid"},
	} {
		f.mustRefuse(t, name, tc.want, f.respond(t, f.body(f.present(t, f.pid(t, tc.o), "bsn"))))
	}
	for name, o := range map[string]pidOptions{
		"expired within the clock skew":       {expires: f.now.Add(-oid4vp.ClockSkew + time.Second)},
		"not yet valid within the clock skew": {notBefore: f.now.Add(oid4vp.ClockSkew - time.Second)},
	} {
		if _, err := f.verify(f.respond(t, f.body(f.present(t, f.pid(t, o), "bsn")))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.mustRefuse(t, "no exp", "has no exp", f.respond(t, f.body(f.present(t, f.pidWithoutExp(t), "bsn"))))
	if _, err := f.verify(f.respond(t, f.body(f.present(t, f.pidWithoutSdAlg(t), "bsn")))); err != nil {
		t.Errorf("a PID without _sd_alg (SHA-256 by default): %v", err)
	}

	// Replace a character of the issuer's signature by another valid one.
	tampered := []byte(f.present(t, f.pid(t, pidOptions{}), "bsn"))
	i := strings.Index(string(tampered), "~") - 5
	if tampered[i] == 'A' {
		tampered[i] = 'B'
	} else {
		tampered[i] = 'A'
	}
	f.mustRefuse(t, "a tampered PID", "signature does not verify", f.respond(t, f.body(string(tampered))))
}

// V6: the key binding.
func TestResponseKeyBinding(t *testing.T) {
	f := newFixture(t)
	sdjwt := testwallet.Select(t, f.pid(t, pidOptions{}), "bsn")
	kb := func(key any, typ string, claims map[string]any) string {
		c := map[string]any{"aud": f.session.ClientID, "nonce": f.session.Nonce, "iat": f.now.Unix(), "sd_hash": testwallet.SDHash(sdjwt)}
		for k, v := range claims {
			c[k] = v
		}
		holder := f.holder
		if key != nil {
			holder = testpki.NewKey(t)
		}
		return sdjwt + testwallet.Sign(t, holder, typ, nil, false, c)
	}
	f.mustRefuse(t, "no key binding", "no key binding", f.respond(t, f.body(sdjwt)))
	f.mustRefuse(t, "another typ", "typ must be", f.respond(t, f.body(kb(nil, "jwt", nil))))
	f.mustRefuse(t, "another key binding key", "not signed with the credential's key", f.respond(t, f.body(kb(true, "kb+jwt", nil))))
	f.mustRefuse(t, "aud without prefix", "aud is not the client_id", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"aud": f.session.ClientID[len("x509_hash:"):]}))))
	f.mustRefuse(t, "aud as an array", "aud is not the client_id", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"aud": []string{f.session.ClientID}}))))
	f.mustRefuse(t, "another nonce", "nonce is not the session's", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"nonce": "nonce-2"}))))
	f.mustRefuse(t, "another sd_hash", "sd_hash does not match", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"sd_hash": testwallet.SDHash(sdjwt + "x")}))))
	f.mustRefuse(t, "no iat", "has no iat", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"iat": nil}))))
	tooOld := f.now.Add(-oid4vp.KeyBindingMaxAge - oid4vp.ClockSkew - time.Second)
	f.mustRefuse(t, "iat too old", "outside the allowed window", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"iat": tooOld.Unix()}))))
	f.mustRefuse(t, "iat in the future", "outside the allowed window", f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"iat": f.now.Add(oid4vp.ClockSkew + time.Second).Unix()}))))
	oldest := f.now.Add(-oid4vp.KeyBindingMaxAge - oid4vp.ClockSkew + time.Second)
	if _, err := f.verify(f.respond(t, f.body(kb(nil, "kb+jwt", map[string]any{"iat": oldest.Unix()})))); err != nil {
		t.Errorf("iat at the edge of the window: %v", err)
	}
}

// V7 and V9: requested claims must be disclosed, and nothing else.
func TestResponseDisclosures(t *testing.T) {
	f := newFixture(t)
	cred := f.pid(t, pidOptions{})
	f.mustRefuse(t, "bsn not disclosed", "[bsn] is not disclosed", f.respond(t, f.body(f.present(t, cred, "given_name"))))
	f.mustRefuse(t, "an extra claim", "[given_name] is disclosed but not asked for", f.respond(t, f.body(f.present(t, cred, "bsn", "given_name"))))
	f.session.Query = query([]string{"address", "locality"})
	f.mustRefuse(t, "an extra nested claim", "[address street_address] is disclosed but not asked for", f.respond(t, f.body(f.present(t, cred, "address", "locality", "street_address"))))
	f.mustRefuse(t, "a parent without the claim", "[address locality] is not disclosed", f.respond(t, f.body(f.present(t, cred, "address"))))
	f.session.Query = query([]string{"address"})
	if _, err := f.verify(f.respond(t, f.body(f.present(t, cred, "address", "locality", "street_address")))); err != nil {
		t.Errorf("the parts of a requested object: %v", err)
	}
}

// V8 and V10: the status list.
func TestResponseStatus(t *testing.T) {
	f := newFixture(t)
	respond := func() string { return f.respond(t, f.body(f.present(t, f.pid(t, pidOptions{}), "bsn"))) }

	f.tokens[statusURI] = f.statusToken(t, f.status, []int{pidIndex})
	if _, err := f.verify(respond()); !errors.Is(err, oid4vp.ErrRevoked) {
		t.Errorf("revoked: %v", err)
	}
	// Two bits per entry, entry 3 set to 2 (suspended): bit 7 of byte 0.
	lst, err := statuslist.Encode(8, []int{7})
	if err != nil {
		t.Fatal(err)
	}
	f.tokens[statusURI] = testwallet.Sign(t, f.status.Key, statuslist.Type, map[jose.HeaderKey]any{"x5c": x5c.Header(f.status.Chain)}, false, map[string]any{
		"sub": statusURI, "iat": f.now.Unix(), "status_list": map[string]any{"bits": 2, "lst": lst},
	})
	if _, err := f.verify(respond()); !errors.Is(err, oid4vp.ErrRevoked) {
		t.Errorf("suspended: %v", err)
	}
	delete(f.tokens, statusURI)
	if _, err := f.verify(respond()); !errors.Is(err, oid4vp.ErrStatusUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
	_, otherStatus := pidSigners(t, f.testRoot)
	f.tokens[statusURI] = f.statusToken(t, otherStatus, nil)
	f.mustRefuse(t, "a status list under the other anchor", "chain does not verify", respond())
	f.tokens[statusURI] = f.statusToken(t, f.pidRoot.Issue(t, testpki.Options{Subject: pidName}), nil)
	f.mustRefuse(t, "a status list signer without its EKU", "extended key usage", respond())
	f.tokens[statusURI] = f.statusToken(t, f.pidRoot.Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "other"}, EKU: []asn1.ObjectIdentifier{statuslist.SignerEKU}}), nil)
	f.mustRefuse(t, "a status list under another name", "not signed under the credential signer's name", respond())
	f.tokens[statusURI] = "not a token"
	f.mustRefuse(t, "an invalid status list", "statuslist:", respond())

	f.tokens[statusURI] = f.statusToken(t, f.status, nil)
	f.mustRefuse(t, "an index beyond int32", "no status list reference", f.respond(t, f.body(f.present(t, f.pid(t, pidOptions{index: 1 << 40}), "bsn"))))
	f.mustRefuse(t, "no status reference", "no status list reference", f.respond(t, f.body(f.present(t, f.pid(t, pidOptions{noStatus: true}), "bsn"))))
}

// A verifier set up wrongly is refused before anything is checked.
func TestVerifierSetup(t *testing.T) {
	f := newFixture(t)
	compact := f.respond(t, f.body(f.present(t, f.pid(t, pidOptions{}), "bsn")))
	dup := f.verifier
	dup.Anchors = append(dup.Anchors, dup.Anchors[1])
	if _, err := dup.VerifyResponse(context.Background(), compact, f.session, f.now); err == nil || !strings.Contains(err.Error(), "occurs twice") {
		t.Errorf("duplicate anchor names: %v", err)
	}
	noFetch := f.verifier
	noFetch.FetchStatus = nil
	if _, err := noFetch.VerifyResponse(context.Background(), compact, f.session, f.now); err == nil {
		t.Error("a verifier without a status fetcher must be refused")
	}
	for name, mutate := range map[string]func(s *oid4vp.Session){
		"no query":     func(s *oid4vp.Session) { s.Query = oid4vp.Query{} },
		"no client_id": func(s *oid4vp.Session) { s.ClientID = "" },
		"no nonce":     func(s *oid4vp.Session) { s.Nonce = "" },
		"no state":     func(s *oid4vp.Session) { s.State = "" },
		"no key":       func(s *oid4vp.Session) { s.Key = oid4vp.ResponseKey{} },
	} {
		s := f.session
		mutate(&s)
		if _, err := f.verifier.VerifyResponse(context.Background(), compact, s, f.now); err == nil {
			t.Errorf("a session with %s must be refused", name)
		}
	}
}
