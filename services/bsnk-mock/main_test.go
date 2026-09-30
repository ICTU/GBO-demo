package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// post sends a JSON body to the mux under test and decodes the answer.
func post(t *testing.T, url string, body any, out any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("POST %s: decode: %v", url, err)
	}
}

// Happy-path integration test of the chain the demo runs: the consent portal
// activates a BSN and has it transformed for a source, and the source reads
// the BSN back with the keys it was issued. Verifies that the composition
// root wires BSNk's interface and the decryption component to one mock.
func TestActivateTransformAndDecrypt(t *testing.T) {
	const portal, source = "00000000000000000002", "99999999900000000200"
	mux, err := newMux(config{BSNAuthorisedOINs: []string{source}})
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	base := func(fields map[string]any) map[string]any {
		body := map[string]any{"RequestID": "_r1", "DateTime": "2026-09-30T10:00:00Z", "Requester": portal}
		for k, v := range fields {
			body[k] = v
		}
		return body
	}

	var activated struct {
		PolymorphicPseudonym []string `json:"PolymorphicPseudonym"`
	}
	post(t, srv.URL+"/v2/activate", base(map[string]any{"RequesterKeySetVersion": 1, "BSN": "987654321"}), &activated)
	if len(activated.PolymorphicPseudonym) != 2 {
		t.Fatalf("activate gave %d values, want a PI and a PP", len(activated.PolymorphicPseudonym))
	}

	var transformed struct {
		Encrypted []struct {
			Value string `json:"value"`
		} `json:"Encrypted"`
	}
	post(t, srv.URL+"/v2/transform", base(map[string]any{
		"PolymorphicIdentity": activated.PolymorphicPseudonym[0],
		"RelyingParty":        []map[string]any{{"EntityID": source, "KeySetVersion": 20260101, "IdentifierType": "Identity"}},
	}), &transformed)
	if len(transformed.Encrypted) != 1 {
		t.Fatalf("transform gave %d values, want 1", len(transformed.Encrypted))
	}

	var issued struct {
		EncryptedDVKey []struct {
			Value string `json:"value"`
		} `json:"EncryptedDVKey"`
	}
	post(t, srv.URL+"/v2/provide-dv-keys?key_set_version=20260101", base(map[string]any{"RelyingParty": source}), &issued)
	keys := make([]string, 0, len(issued.EncryptedDVKey))
	for _, key := range issued.EncryptedDVKey {
		file, err := base64.StdEncoding.DecodeString(key.Value)
		if err != nil {
			t.Fatalf("key file: %v", err)
		}
		keys = append(keys, string(file))
	}
	schemeKeysResp, err := http.Get(srv.URL + "/v2/scheme-keys")
	if err != nil {
		t.Fatalf("scheme keys: %v", err)
	}
	defer schemeKeysResp.Body.Close()
	var schemeKeys map[string]string
	if err := json.NewDecoder(schemeKeysResp.Body).Decode(&schemeKeys); err != nil {
		t.Fatalf("scheme keys: %v", err)
	}

	var read struct {
		BSN string `json:"bsn"`
	}
	post(t, srv.URL+"/signed-encrypted-identity", map[string]any{
		"signedEncryptedIdentity": transformed.Encrypted[0].Value,
		"serviceProviderKeys":     keys,
		"schemeKeys":              schemeKeys,
	}, &read)
	if read.BSN != "987654321" {
		t.Fatalf("the source read %q, want the BSN that was activated", read.BSN)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("BSN_AUTHORISED_OINS", " 99999999900000000200, 99999999900000000210 ,")
	t.Setenv("RANDOMIZE_VALUES", "true")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.BSNAuthorisedOINs) != 2 || cfg.BSNAuthorisedOINs[1] != "99999999900000000210" || !cfg.Randomize {
		t.Errorf("cfg = %+v", cfg)
	}

	t.Setenv("RANDOMIZE_VALUES", "sometimes")
	if _, err := loadConfig(); err == nil {
		t.Error("RANDOMIZE_VALUES=sometimes was accepted")
	}

	t.Setenv("RANDOMIZE_VALUES", "")
	t.Setenv("DECRYPTION_COMPONENT_ONLY", "true")
	if cfg, err := loadConfig(); err != nil || !cfg.DecryptionComponentOnly {
		t.Errorf("DECRYPTION_COMPONENT_ONLY=true gave %+v, %v", cfg, err)
	}
	t.Setenv("DECRYPTION_COMPONENT_ONLY", "partly")
	if _, err := loadConfig(); err == nil {
		t.Error("DECRYPTION_COMPONENT_ONLY=partly was accepted")
	}
}

// An instance that stands in for a party's own decryption component serves
// that component and nothing of BSNk.
func TestTheDecryptionComponentAloneServesNothingOfBSNk(t *testing.T) {
	mux, err := newMux(config{DecryptionComponentOnly: true})
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for path, want := range map[string]int{
		"/health":                    http.StatusOK,
		"/signed-encrypted-identity": http.StatusInternalServerError, // served: an empty request is refused
		"/v2/activate":               http.StatusNotFound,
		"/v2/transform":              http.StatusNotFound,
		"/v2/provide-dv-keys":        http.StatusNotFound,
		"/pseudonymize":              http.StatusNotFound,
		"/transform":                 http.StatusNotFound,
	} {
		method := http.MethodPost
		if path == "/health" {
			method = http.MethodGet
		}
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// A misconfigured list stops the service at start instead of silently
// leaving a party without its BSN.
func TestNewMuxRejectsAMalformedOIN(t *testing.T) {
	if _, err := newMux(config{BSNAuthorisedOINs: []string{"bd-mock"}}); err == nil {
		t.Error("newMux accepted a name as an OIN")
	}
}
