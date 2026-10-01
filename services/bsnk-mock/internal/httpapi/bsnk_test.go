package httpapi

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bsnk-mock/internal/polymorphic"
)

const (
	consentServiceOIN  = "99999999900000000900"
	brokerOIN          = "99999999900000000800"
	sourceOIN          = "99999999900000000200"
	serviceProviderOIN = "99999999900000000300"
	testBSN            = "999991772"
)

// newTestServer serves the mux with the source as the only party that may
// receive the BSN.
func newTestServer(t *testing.T, randomize bool) *httptest.Server {
	t.Helper()
	mock, err := polymorphic.New([]string{sourceOIN})
	if err != nil {
		t.Fatalf("polymorphic.New: %v", err)
	}
	srv := httptest.NewServer(NewMux(mock, randomize))
	t.Cleanup(srv.Close)
	return srv
}

// request returns a request body with the fields every request carries.
func request(requester string, fields map[string]any) map[string]any {
	body := map[string]any{"RequestID": "_req1", "DateTime": "2026-09-30T10:00:00Z", "Requester": requester}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

// call posts a JSON body and returns the status and the raw answer.
func call(t *testing.T, srv *httptest.Server, path string, body any) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("POST %s: read answer: %v", path, err)
	}
	return resp.StatusCode, answer
}

// ok posts a request that must succeed and decodes its answer into out.
func ok(t *testing.T, srv *httptest.Server, path string, body any, out any) {
	t.Helper()
	status, answer := call(t, srv, path, body)
	if status != http.StatusOK {
		t.Fatalf("POST %s: status %d: %s", path, status, answer)
	}
	if err := json.Unmarshal(answer, out); err != nil {
		t.Fatalf("POST %s: decode answer: %v", path, err)
	}
}

type activateAnswer struct {
	ResponseID           string
	DateTime             string
	InResponseTo         string
	PolymorphicPseudonym []string
}

type transformAnswer struct {
	InResponseTo string
	Encrypted    []struct {
		EntityID       string
		KeySetVersion  int
		IdentifierType string
		Value          string `json:"value"`
	}
}

type keysAnswer struct {
	EncryptedDVKey []struct {
		KeyType             string
		RecipientKeyVersion string
		SchemeKeySetVersion string
		Value               string `json:"value"`
	}
}

func activateOverHTTP(t *testing.T, srv *httptest.Server) (pi, pp string) {
	t.Helper()
	var a activateAnswer
	ok(t, srv, "/v2/activate", request(consentServiceOIN, map[string]any{"RequesterKeySetVersion": 1, "BSN": testBSN}), &a)
	if len(a.PolymorphicPseudonym) != 2 {
		t.Fatalf("activate gave %d structures, want a PI and a PP", len(a.PolymorphicPseudonym))
	}
	return a.PolymorphicPseudonym[0], a.PolymorphicPseudonym[1]
}

// keyFilesOf asks the broker's question for a party and returns its key files.
func keyFilesOf(t *testing.T, srv *httptest.Server, oin, version string) []string {
	t.Helper()
	var k keysAnswer
	ok(t, srv, "/v2/provide-dv-keys?key_set_version="+version, request(brokerOIN, map[string]any{"RelyingParty": oin}), &k)
	files := make([]string, 0, len(k.EncryptedDVKey))
	for _, key := range k.EncryptedDVKey {
		file, err := base64.StdEncoding.DecodeString(key.Value)
		if err != nil {
			t.Fatalf("key file is not base64: %v", err)
		}
		files = append(files, string(file))
	}
	return files
}

func schemeKeysOverHTTP(t *testing.T, srv *httptest.Server) map[string]string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v2/scheme-keys")
	if err != nil {
		t.Fatalf("GET scheme keys: %v", err)
	}
	defer resp.Body.Close()
	var keys map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		t.Fatalf("decode scheme keys: %v", err)
	}
	return keys
}

// The chain a consent goes through, over HTTP and with the field names of the
// real interfaces: the consent service activates the BSN and has values made;
// each party fetches its keys once and reads its own value with them.
func TestActivateTransformAndDecryptOverHTTP(t *testing.T) {
	srv := newTestServer(t, false)

	var a activateAnswer
	ok(t, srv, "/v2/activate", request(consentServiceOIN, map[string]any{
		"RequesterKeySetVersion": 1, "BSN": testBSN,
		"GivenNames": "Jan", "SurName": "Jansen", "DateOfBirth": "1990-01-01",
	}), &a)
	if a.InResponseTo != "_req1" || a.ResponseID == "" || a.DateTime == "" || len(a.PolymorphicPseudonym) != 2 {
		t.Fatalf("activate answer = %+v", a)
	}
	pi, pp := a.PolymorphicPseudonym[0], a.PolymorphicPseudonym[1]

	var pseudonyms, identities transformAnswer
	ok(t, srv, "/v2/transform", request(consentServiceOIN, map[string]any{
		"PolymorphicPseudonym": pp,
		"RelyingParty": []map[string]any{
			{"EntityID": sourceOIN, "KeySetVersion": 20260101, "IdentifierType": "Pseudonym"},
			{"EntityID": serviceProviderOIN, "KeySetVersion": 20260301, "IdentifierType": "Pseudonym"},
		},
	}), &pseudonyms)
	ok(t, srv, "/v2/transform", request(consentServiceOIN, map[string]any{
		"PolymorphicIdentity": pi,
		"RelyingParty": []map[string]any{
			{"EntityID": sourceOIN, "KeySetVersion": 20260101, "IdentifierType": "Identity", "Nonce": "consent0001"},
		},
	}), &identities)
	if len(pseudonyms.Encrypted) != 2 || len(identities.Encrypted) != 1 {
		t.Fatalf("got %d pseudonyms and %d identities, want 2 and 1", len(pseudonyms.Encrypted), len(identities.Encrypted))
	}
	if e := identities.Encrypted[0]; e.EntityID != sourceOIN || e.KeySetVersion != 20260101 || e.IdentifierType != "Identity" {
		t.Errorf("Encrypted = %+v", e)
	}

	schemeKeys := schemeKeysOverHTTP(t, srv)

	var atSource struct {
		BSN          string `json:"bsn"`
		DecodedInput struct {
			NotationIdentifier string `json:"notationIdentifier"`
			SignedEI           struct {
				EncryptedIdentity struct {
					Recipient              string `json:"recipient"`
					RecipientKeySetVersion string `json:"recipientKeySetVersion"`
				} `json:"encryptedIdentity"`
			} `json:"signedEI"`
		} `json:"decodedInput"`
		DecryptionResult struct {
			Type       string `json:"type"`
			Length     int    `json:"length"`
			Identifier string `json:"identifier"`
		} `json:"decryption_result"`
		ExtraElements []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"extraElements"`
	}
	ok(t, srv, "/signed-encrypted-identity", map[string]any{
		"signedEncryptedIdentity": identities.Encrypted[0].Value,
		"serviceProviderKeys":     keyFilesOf(t, srv, sourceOIN, "20260101"),
		"schemeKeys":              schemeKeys,
	}, &atSource)
	if atSource.BSN != testBSN || atSource.DecryptionResult.Identifier != testBSN || atSource.DecryptionResult.Length != 9 || atSource.DecryptionResult.Type != "B" {
		t.Errorf("identity at the source = %+v", atSource)
	}
	if got := atSource.DecodedInput.SignedEI.EncryptedIdentity; got.Recipient != sourceOIN || got.RecipientKeySetVersion != "20260101" {
		t.Errorf("decodedInput = %+v", atSource.DecodedInput)
	}
	if len(atSource.ExtraElements) != 1 || atSource.ExtraElements[0].Key != "RP:Nonce" || atSource.ExtraElements[0].Value != "consent0001" {
		t.Errorf("extraElements = %+v, want the nonce", atSource.ExtraElements)
	}

	var atProvider struct {
		Pseudonym        string `json:"pseudonym"`
		DecodedPseudonym struct {
			Recipient              string `json:"recipient"`
			RecipientKeySetVersion string `json:"recipientKeySetVersion"`
			Type                   string `json:"type"`
			PseudonymValue         string `json:"pseudonymValue"`
		} `json:"decodedPseudonym"`
		BSN *string `json:"bsn"`
	}
	ok(t, srv, "/signed-encrypted-pseudonym", map[string]any{
		"signedEncryptedPseudonym": pseudonyms.Encrypted[1].Value,
		"serviceProviderKeys":      keyFilesOf(t, srv, serviceProviderOIN, "20260301"),
		"schemeKeys":               schemeKeys,
	}, &atProvider)
	if got := atProvider.DecodedPseudonym; got.Recipient != serviceProviderOIN || got.RecipientKeySetVersion != "20260301" || got.Type != "B" || got.PseudonymValue == "" {
		t.Errorf("pseudonym at the service provider = %+v", atProvider)
	}
	if atProvider.BSN != nil || atProvider.Pseudonym == "" {
		t.Errorf("service provider answer = %+v, want a pseudonym and no BSN", atProvider)
	}
}

func TestAFaultCarriesBSNksReason(t *testing.T) {
	srv := newTestServer(t, false)
	pi, pp := activateOverHTTP(t, srv)
	identityFor := func(oin string) []map[string]any {
		return []map[string]any{{"EntityID": oin, "KeySetVersion": 20260101, "IdentifierType": "Identity"}}
	}

	for name, tc := range map[string]struct {
		path   string
		body   map[string]any
		status int
		reason string
	}{
		"an identity for a party that may not have the BSN": {"/v2/transform",
			request(consentServiceOIN, map[string]any{"PolymorphicIdentity": pi, "RelyingParty": identityFor(serviceProviderOIN)}),
			http.StatusForbidden, "ProvisioningRefused"},
		"a PI of another requester": {"/v2/transform",
			request(serviceProviderOIN, map[string]any{"PolymorphicIdentity": pi, "RelyingParty": identityFor(sourceOIN)}),
			http.StatusForbidden, "AuthorizationError"},
		"a PP where a PI is needed": {"/v2/transform",
			request(consentServiceOIN, map[string]any{"PolymorphicIdentity": pp, "RelyingParty": identityFor(sourceOIN)}),
			http.StatusBadRequest, "SyntaxError"},
		"a BSN of eight digits": {"/v2/activate",
			request(consentServiceOIN, map[string]any{"RequesterKeySetVersion": 1, "BSN": "12345678"}),
			http.StatusBadRequest, "SyntaxError"},
		"a misspelled field": {"/v2/activate",
			request(consentServiceOIN, map[string]any{"RequesterKeySetVersion": 1, "Bsn-nummer": testBSN}),
			http.StatusBadRequest, "SyntaxError"},
		"no RequestID": {"/v2/activate",
			map[string]any{"DateTime": "2026-09-30T10:00:00Z", "Requester": consentServiceOIN, "RequesterKeySetVersion": 1, "BSN": testBSN},
			http.StatusBadRequest, "SyntaxError"},
		"a DateTime that is not one": {"/v2/activate",
			map[string]any{"RequestID": "_r", "DateTime": "yesterday", "Requester": consentServiceOIN, "RequesterKeySetVersion": 1, "BSN": testBSN},
			http.StatusBadRequest, "SyntaxError"},
		"an encrypted BSN": {"/v2/activate",
			request(consentServiceOIN, map[string]any{"RequesterKeySetVersion": 1, "EncryptedBSN": "…"}),
			http.StatusBadRequest, "SyntaxError"},
		"link verification": {"/v2/transform",
			request(consentServiceOIN, map[string]any{"PolymorphicIdentity": pi, "RelyingParty": []map[string]any{
				{"EntityID": sourceOIN, "KeySetVersion": 20260101, "IdentifierType": "Identity", "LinkVerification": map[string]any{"Profile": "ServiceIntermediary"}},
			}}),
			http.StatusBadRequest, "SyntaxError"},
		"keys for a party that expects the BSN key and may not have it": {"/v2/provide-dv-keys?key_set_version=20260101",
			request(brokerOIN, map[string]any{"RelyingParty": serviceProviderOIN, "ProvideEIDecryptionKey": "EI_DECRYPTION_KEY_EXPECTED"}),
			http.StatusForbidden, "ProvisioningRefused"},
		"keys without a certificate or a date": {"/v2/provide-dv-keys",
			request(brokerOIN, map[string]any{"RelyingParty": sourceOIN}),
			http.StatusBadRequest, "InvalidRequest"},
	} {
		status, answer := call(t, srv, tc.path, tc.body)
		var f struct{ FaultReason, FaultDescription string }
		_ = json.Unmarshal(answer, &f)
		if status != tc.status || f.FaultReason != tc.reason || f.FaultDescription == "" {
			t.Errorf("%s: status %d, fault %+v; want %d %s with a description", name, status, f, tc.status, tc.reason)
		}
	}
}

// A party's key set version is the date its certificate was issued.
func TestKeysTakeTheirVersionFromTheCertificate(t *testing.T) {
	srv := newTestServer(t, false)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "source.example", SerialNumber: sourceOIN},
		NotBefore:    time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2029, 3, 14, 9, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certificate := map[string]any{"X509Data": map[string]any{"X509Certificate": base64.StdEncoding.EncodeToString(der)}}

	var k keysAnswer
	ok(t, srv, "/v2/provide-dv-keys", request(brokerOIN, map[string]any{"RelyingParty": sourceOIN, "RelyingPartyPKIoCertificate": certificate}), &k)
	if len(k.EncryptedDVKey) != 3 {
		t.Fatalf("got %d keys, want 3 for a party that may have the BSN", len(k.EncryptedDVKey))
	}
	for _, dv := range k.EncryptedDVKey {
		if dv.RecipientKeyVersion != "20260314" || dv.SchemeKeySetVersion != "1" {
			t.Errorf("key %s: version %s, scheme key set %s; want 20260314 and 1", dv.KeyType, dv.RecipientKeyVersion, dv.SchemeKeySetVersion)
		}
	}

	status, answer := call(t, srv, "/v2/provide-dv-keys", request(brokerOIN, map[string]any{"RelyingParty": serviceProviderOIN, "RelyingPartyPKIoCertificate": certificate}))
	if status != http.StatusBadRequest || !strings.Contains(string(answer), "InvalidRequest") {
		t.Errorf("a certificate of another party: status %d, %s", status, answer)
	}
}

// Like the component it stands in for, a failed decryption answers 500 with
// the reason as plain text.
func TestAFailedDecryptionIs500WithAReason(t *testing.T) {
	srv := newTestServer(t, false)
	pi, _ := activateOverHTTP(t, srv)
	var identities transformAnswer
	ok(t, srv, "/v2/transform", request(consentServiceOIN, map[string]any{
		"PolymorphicIdentity": pi,
		"RelyingParty":        []map[string]any{{"EntityID": sourceOIN, "KeySetVersion": 20260101, "IdentifierType": "Identity"}},
	}), &identities)
	vi := identities.Encrypted[0].Value
	schemeKeys := schemeKeysOverHTTP(t, srv)

	for name, body := range map[string]map[string]any{
		"the keys of another party": {"signedEncryptedIdentity": vi, "serviceProviderKeys": keyFilesOf(t, srv, serviceProviderOIN, "20260101"), "schemeKeys": schemeKeys},
		"a key set of another date": {"signedEncryptedIdentity": vi, "serviceProviderKeys": keyFilesOf(t, srv, sourceOIN, "20250101"), "schemeKeys": schemeKeys},
		"no scheme keys":            {"signedEncryptedIdentity": vi, "serviceProviderKeys": keyFilesOf(t, srv, sourceOIN, "20260101")},
		"a PI instead of a VI":      {"signedEncryptedIdentity": pi, "serviceProviderKeys": keyFilesOf(t, srv, sourceOIN, "20260101"), "schemeKeys": schemeKeys},
	} {
		status, answer := call(t, srv, "/signed-encrypted-identity", body)
		if status != http.StatusInternalServerError || len(bytes.TrimSpace(answer)) == 0 || json.Valid(answer) {
			t.Errorf("%s: status %d, answer %q; want 500 with a plain-text reason", name, status, answer)
		}
	}

	status, _ := call(t, srv, "/signed-encrypted-pseudonym", map[string]any{
		"signedEncryptedPseudonym": vi, "serviceProviderKeys": keyFilesOf(t, srv, sourceOIN, "20260101"), "schemeKeys": schemeKeys,
	})
	if status != http.StatusInternalServerError {
		t.Errorf("a VI read as a pseudonym: status %d, want 500", status)
	}
}

func TestRandomizeFollowsTheDefaultUnlessTheRequestSays(t *testing.T) {
	body := request(consentServiceOIN, map[string]any{"RequesterKeySetVersion": 1, "BSN": testBSN})
	twice := func(srv *httptest.Server, query string) (string, string) {
		var a, b activateAnswer
		ok(t, srv, "/v2/activate"+query, body, &a)
		ok(t, srv, "/v2/activate"+query, body, &b)
		return a.PolymorphicPseudonym[0], b.PolymorphicPseudonym[0]
	}

	stable := newTestServer(t, false)
	if a, b := twice(stable, ""); a != b {
		t.Error("default off: two activations differ")
	}
	if a, b := twice(stable, "?randomize=true"); a == b {
		t.Error("randomize requested: two activations are equal")
	}

	random := newTestServer(t, true)
	if a, b := twice(random, ""); a == b {
		t.Error("default on: two activations are equal")
	}
	if a, b := twice(random, "?randomize=false"); a != b {
		t.Error("randomize switched off in the request: two activations differ")
	}

	status, _ := call(t, stable, "/v2/activate?randomize=sometimes", body)
	if status != http.StatusBadRequest {
		t.Errorf("randomize=sometimes: status %d, want 400", status)
	}
}

func TestTheBSNAuthorisationListIsPublished(t *testing.T) {
	srv := newTestServer(t, false)
	resp, err := http.Get(srv.URL + "/v2/bsn-authorisation-list")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		AuthorizedOrganization []struct{ OIN string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(out.AuthorizedOrganization) != 1 || out.AuthorizedOrganization[0].OIN != sourceOIN {
		t.Errorf("status %d, list %+v; want 200 and the source", resp.StatusCode, out)
	}
}

func TestEndpointsRejectOtherMethods(t *testing.T) {
	srv := newTestServer(t, false)
	for _, path := range []string{"/v2/activate", "/v2/transform", "/v2/provide-dv-keys", "/signed-encrypted-identity", "/signed-encrypted-pseudonym", "/pseudonymize"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: status = %d, want 405", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/v2/bsn-authorisation-list", "/v2/scheme-keys"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: status = %d, want 405", path, resp.StatusCode)
		}
	}
}
