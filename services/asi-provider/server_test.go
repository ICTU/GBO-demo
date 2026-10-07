package main

// Contract tests for the ASIP mock, slice 1 (ETSI TS 119 478 V1.1.1 clause
// 6.1.1 and 6.1.2). One test per agreed case; the numbers refer to the case
// table in the slice issue.

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	idFamilyName = "https://gbo.example/attributes/family_name/1"
	idGivenName  = "https://gbo.example/attributes/given_name/1"
	idAddress    = "https://gbo.example/attributes/address/1"
	idDiploma    = "https://gbo.example/attributes/education_qualification/1"
	idNotInCat   = "https://gbo.example/attributes/shoe_size/1"

	resultMatch     = "http://uri.etsi.org/19478/VerificationResult/Match"
	resultNoMatch   = "http://uri.etsi.org/19478/VerificationResult/NoMatch"
	resultVariation = "http://uri.etsi.org/19478/VerificationResult/MatchWithVariation"
	resultUnknown   = "http://uri.etsi.org/19478/VerificationResult/Unknown"

	tokenFrouke = "test-token-frouke"
	tokenTom    = "test-token-tom"
	tokenSanne  = "test-token-sanne"
)

type testEnv struct {
	handler http.Handler
	caPool  *x509.CertPool
}

// newTestServer wires the server with the test persons, the provisional
// catalogue, a seal certificate from an ephemeral test CA, and the stub
// tokens of slice 1.
func newTestServer(t *testing.T) testEnv {
	t.Helper()
	caKey, caCert := newTestCA(t)
	sealKey, sealCert := newTestSealCertificate(t, caKey, caCert)

	source, err := loadMemorySource("testdata/persons.json")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	handler, err := newServer(serverConfig{
		Source:    source,
		Catalogue: defaultCatalogue(),
		Sealer:    newSealer(sealKey, []*x509.Certificate{sealCert, caCert}),
		Tokens: map[string]string{
			tokenFrouke: "999991772",
			tokenTom:    "555555555",
			tokenSanne:  "987654321",
		},
		Provider:        provider{LegalName: "GBO demo ASIP (test)"},
		AuthenticSource: provider{LegalName: "Basisregistratie Personen (mock)"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return testEnv{handler: handler, caPool: pool}
}

// --- case 1-6: attribute verification ---------------------------------------

func TestVerifyMatch(t *testing.T) { // case 1
	env := newTestServer(t)
	res := verifyAttribute(t, env, tokenFrouke, idFamilyName, `{"family_name":"Jansen"}`)
	assertResult(t, res, idFamilyName, resultMatch)
	assertNoAttributeValue(t, res)
}

func TestVerifyMatchWithVariation(t *testing.T) { // case 2
	env := newTestServer(t)
	for _, claimed := range []string{
		`{"family_name":"JANSEN"}`,   // case
		`{"family_name":" Jansen "}`, // surrounding spaces
	} {
		res := verifyAttribute(t, env, tokenFrouke, idFamilyName, claimed)
		assertResult(t, res, idFamilyName, resultVariation)
		assertNoAttributeValue(t, res)
	}
	// diacritics: é is an admissible variation of e
	res := verifyAttribute(t, env, tokenFrouke, idGivenName, `{"given_name":"Froukë"}`)
	assertResult(t, res, idGivenName, resultVariation)
	// transliteration of a Latin letter that does not decompose (ICAO 9303):
	// the ligature ĳ is written ij, so "Meĳer" varies from "Meijer"
	res = verifyAttribute(t, env, tokenSanne, idFamilyName, `{"family_name":"Meĳer"}`)
	assertResult(t, res, idFamilyName, resultVariation)
}

func TestVerifyNoMatch(t *testing.T) { // case 3
	env := newTestServer(t)
	res := verifyAttribute(t, env, tokenFrouke, idFamilyName, `{"family_name":"Bakker"}`)
	assertResult(t, res, idFamilyName, resultNoMatch)
}

func TestVerifyUnknownWhenSourceHoldsNoValue(t *testing.T) { // case 4
	// ETSI REQ-ASIP-6.1.1.2-04: Unknown "indicates that no authentic source
	// data was available to determine a match for the attribute for the user".
	env := newTestServer(t)
	res := verifyAttribute(t, env, tokenTom, idAddress,
		`{"street":"Grote Markt","house_number":"1","postal_code":"1000","city":"Brussel","country":"BE"}`)
	assertResult(t, res, idAddress, resultUnknown)
}

func TestVerifyUnknownWhenNotTheAuthenticSource(t *testing.T) { // case 5
	env := newTestServer(t)
	res := verifyAttribute(t, env, tokenFrouke, idDiploma, `{"qualification":"MSc"}`)
	assertResult(t, res, idDiploma, resultUnknown)
}

func TestVerifyUnknownAttributeIdentifierIs404(t *testing.T) { // case 6
	env := newTestServer(t)
	rec := post(t, env, "/verify", tokenFrouke, verifyBody(idNotInCat, `{"size":43}`))
	assertProblem(t, rec, http.StatusNotFound)
}

// --- case 7-10: fragments, mandate, malformed requests, tokens ---------------

func TestVerifyFragment(t *testing.T) { // case 7
	env := newTestServer(t)
	body := `{"attributeFragments":[{"attributeIdentifier":"` + idAddress + `","location":"$.city","value":{"city":"Rotterdam"}}]}`
	rec := post(t, env, "/verify", tokenFrouke, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var resp struct {
		FragmentVerificationResults []struct {
			AttributeIdentifier        string `json:"attributeIdentifier"`
			FragmentVerificationResult string `json:"fragmentVerificationResult"`
		} `json:"fragmentVerificationResults"`
	}
	decode(t, rec, &resp)
	if len(resp.FragmentVerificationResults) != 1 ||
		resp.FragmentVerificationResults[0].AttributeIdentifier != idAddress ||
		resp.FragmentVerificationResults[0].FragmentVerificationResult != resultMatch {
		t.Fatalf("fragment results = %+v, want one Match for %s", resp.FragmentVerificationResults, idAddress)
	}
}

func TestVerifyMandateIs501(t *testing.T) { // case 8
	env := newTestServer(t)
	body := `{"attributes":[{"attributeIdentifier":"` + idFamilyName + `","attributeValue":{"family_name":"Jansen"}}],"mandate":{"type":"guardian"}}`
	rec := post(t, env, "/verify", tokenFrouke, body)
	assertProblem(t, rec, http.StatusNotImplemented)
}

func TestVerifyMalformedIs400(t *testing.T) { // case 9
	env := newTestServer(t)
	for name, body := range map[string]string{
		"not JSON":                         `{"attributes":`,
		"trailing data after the object":   verifyBody(idFamilyName, `{"family_name":"Jansen"}`) + ` trailing`,
		"neither attributes nor fragments": `{}`,
		"attribute without attributeValue": `{"attributes":[{"attributeIdentifier":"` + idFamilyName + `"}]}`,
		"attributeValue is not an object":  `{"attributes":[{"attributeIdentifier":"` + idFamilyName + `","attributeValue":"Jansen"}]}`,
		"fragment without location":        `{"attributeFragments":[{"attributeIdentifier":"` + idAddress + `","value":{"city":"Rotterdam"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(t, env, "/verify", tokenFrouke, body)
			assertProblem(t, rec, http.StatusBadRequest)
		})
	}
}

// A request that does not conform to the request schema is malformed, before
// an unknown identifier (404) or an unsupported mandate (501) is considered.
func TestSchemaViolationIs400BeforeOtherChecks(t *testing.T) { // case 9b, 9c
	env := newTestServer(t)
	for _, tc := range []struct{ name, path, body string }{
		{"verify: identifier is not a URI", "/verify", verifyBody("family_name", `{"family_name":"Jansen"}`)},
		{"retrieve: identifier is not a URI", "/retrieve", `{"attributeIdentifiers":[{"attributeIdentifier":"family_name"}]}`},
		{"verify: mandate is not an object", "/verify", `{"attributes":[{"attributeIdentifier":"` + idFamilyName + `","attributeValue":{"family_name":"Jansen"}}],"mandate":"guardian"}`},
		{"retrieve: mandate is not an object", "/retrieve", `{"attributeIdentifiers":[{"attributeIdentifier":"` + idFamilyName + `"}],"mandate":"guardian"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, env, tc.path, tokenFrouke, tc.body)
			assertProblem(t, rec, http.StatusBadRequest)
		})
	}
}

func TestMissingOrInvalidTokenIs401(t *testing.T) { // case 10
	env := newTestServer(t)
	body := verifyBody(idFamilyName, `{"family_name":"Jansen"}`)
	for name, token := range map[string]string{
		"no token":      "",
		"unknown token": "not-a-test-token",
	} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/verify", "/retrieve"} {
				rec := post(t, env, path, token, body)
				assertProblem(t, rec, http.StatusUnauthorized)
			}
		})
	}
}

// --- case 11: every result is sealed -----------------------------------------

func TestSuccessfulResponsesAreSealed(t *testing.T) { // case 11
	env := newTestServer(t)
	for _, tc := range []struct{ path, body string }{
		{"/verify", verifyBody(idFamilyName, `{"family_name":"Jansen"}`)},
		{"/retrieve", `{"attributeIdentifiers":[{"attributeIdentifier":"` + idFamilyName + `"}]}`},
	} {
		rec := post(t, env, tc.path, tokenFrouke, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.path, rec.Code, rec.Body)
		}
		verifySeal(t, env.caPool, rec)
	}
}

// --- case 12: retrieve -------------------------------------------------------

func TestRetrieveReturnsValue(t *testing.T) { // case 12
	env := newTestServer(t)
	rec := post(t, env, "/retrieve", tokenFrouke, `{"attributeIdentifiers":[{"attributeIdentifier":"`+idFamilyName+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var resp struct {
		Attributes []struct {
			AttributeIdentifier string          `json:"attributeIdentifier"`
			AttributeValue      json.RawMessage `json:"attributeValue"`
		} `json:"attributes"`
		Provider *struct {
			LegalName string `json:"legalName"`
		} `json:"provider"`
	}
	decode(t, rec, &resp)
	if resp.Provider == nil || resp.Provider.LegalName == "" {
		t.Fatalf("retrieveResponse has no provider")
	}
	if len(resp.Attributes) != 1 || resp.Attributes[0].AttributeIdentifier != idFamilyName {
		t.Fatalf("attributes = %+v, want one %s", resp.Attributes, idFamilyName)
	}
	assertJSONEqual(t, resp.Attributes[0].AttributeValue, `{"family_name":"Jansen"}`)
}

func TestRetrieveUnknownAttributeIdentifierIs404(t *testing.T) { // case 12, error path
	env := newTestServer(t)
	rec := post(t, env, "/retrieve", tokenFrouke, `{"attributeIdentifiers":[{"attributeIdentifier":"`+idNotInCat+`"}]}`)
	assertProblem(t, rec, http.StatusNotFound)
}

// --- the response always names provider and authentic source ----------------

func TestVerifyResponseNamesProviderAndAuthenticSource(t *testing.T) {
	// REQ-ASIP-6.1.1.2-07: provider is required; the authentic source is
	// named so a sealing intermediary still identifies the responsible body.
	env := newTestServer(t)
	rec := post(t, env, "/verify", tokenFrouke, verifyBody(idFamilyName, `{"family_name":"Jansen"}`))
	var resp struct {
		Provider *struct {
			LegalName string `json:"legalName"`
		} `json:"provider"`
		AuthenticSource *struct {
			LegalName string `json:"legalName"`
		} `json:"authenticSource"`
	}
	decode(t, rec, &resp)
	if resp.Provider == nil || resp.Provider.LegalName == "" {
		t.Fatalf("verifyResponse has no provider")
	}
	if resp.AuthenticSource == nil || resp.AuthenticSource.LegalName == "" {
		t.Fatalf("verifyResponse has no authenticSource")
	}
}

// --- helpers -----------------------------------------------------------------

type attributeResult struct {
	AttributeIdentifier         string          `json:"attributeIdentifier"`
	AttributeVerificationResult string          `json:"attributeVerificationResult"`
	AttributeValue              json.RawMessage `json:"attributeValue,omitempty"`
}

func verifyBody(id, value string) string {
	return `{"attributes":[{"attributeIdentifier":"` + id + `","attributeValue":` + value + `}]}`
}

func verifyAttribute(t *testing.T, env testEnv, token, id, value string) []attributeResult {
	t.Helper()
	rec := post(t, env, "/verify", token, verifyBody(id, value))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var resp struct {
		AttributeVerificationResults []attributeResult `json:"attributeVerificationResults"`
	}
	decode(t, rec, &resp)
	return resp.AttributeVerificationResults
}

// post sends a request to an operation of the contract (path without the
// base path) and checks that the response conforms to the published contract.
func post(t *testing.T, env testEnv, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, basePath+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	assertConformsToContract(t, req, body, rec, path)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response: %v; body %s", err, rec.Body)
	}
}

func assertResult(t *testing.T, res []attributeResult, id, want string) {
	t.Helper()
	if len(res) != 1 {
		t.Fatalf("got %d attribute results, want 1: %+v", len(res), res)
	}
	if res[0].AttributeIdentifier != id {
		t.Fatalf("attributeIdentifier = %q, want %q", res[0].AttributeIdentifier, id)
	}
	if res[0].AttributeVerificationResult != want {
		t.Fatalf("result = %q, want %q", res[0].AttributeVerificationResult, want)
	}
}

func assertNoAttributeValue(t *testing.T, res []attributeResult) {
	t.Helper()
	for _, r := range res {
		if len(r.AttributeValue) != 0 {
			t.Fatalf("result for %s carries attributeValue %s; the mock does not return values", r.AttributeIdentifier, r.AttributeValue)
		}
	}
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p struct {
		Status int    `json:"status"`
		Title  string `json:"title"`
	}
	decode(t, rec, &p)
	if p.Status != status || p.Title == "" {
		t.Fatalf("problem = %+v, want status %d and a title", p, status)
	}
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("value = %s, want %s", gb, wb)
	}
}

// verifySeal checks the detached JWS (RFC 7515, appendix F) in the
// X-JWS-Signature header against the response body and the test CA.
// The check is written independently of the sealer under test.
func verifySeal(t *testing.T, pool *x509.CertPool, rec *httptest.ResponseRecorder) {
	t.Helper()
	jws := rec.Header().Get("X-JWS-Signature")
	parts := strings.Split(jws, ".")
	if len(parts) != 3 || parts[1] != "" {
		t.Fatalf("X-JWS-Signature = %q, want a detached JWS header..signature", jws)
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode protected header: %v", err)
	}
	var header struct {
		Alg string   `json:"alg"`
		X5c []string `json:"x5c"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("parse protected header: %v", err)
	}
	if header.Alg != "ES256" || len(header.X5c) == 0 {
		t.Fatalf("protected header = %s, want alg ES256 and an x5c chain", headerJSON)
	}
	leafDER, err := base64.StdEncoding.DecodeString(header.X5c[0])
	if err != nil {
		t.Fatalf("decode x5c leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse x5c leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Fatalf("seal certificate does not chain to the test CA: %v", err)
	}
	signingInput := parts[0] + "." + base64.RawURLEncoding.EncodeToString(rec.Body.Bytes())
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature is not a 64-byte ES256 signature: %v", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("seal key is not ECDSA")
	}
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatalf("seal does not verify over the response body")
	}
}

func newTestCA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "GBO demo test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return key, cert
}

func newTestSealCertificate(t *testing.T, caKey crypto.Signer, ca *x509.Certificate) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "GBO demo ASIP seal (test)", Organization: []string{"GBO demo"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return key, cert
}
