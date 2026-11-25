package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The test relying parties (#737): two Service_Providers under the reader
// CA, each registered for every attestation type of the issuer with all
// its claims (one entry per vct), and a configuration for the service.
func TestProvisionTestRelyingParties(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	metadata := map[string]string{
		"/.well-known/openid-credential-issuer/belastingdienst/eudi": `{"credential_configurations_supported":{
			"belastingdienst-inkomensverklaring_2024":{"vct":"urn:income","credential_metadata":{"display":[{"name":"Inkomensverklaring 2024"}],"claims":[{"path":["jaar"]},{"path":["adres","woonplaats"]}]}},
			"belastingdienst-inkomensverklaring_2025":{"vct":"urn:income","credential_metadata":{"display":[{"name":"Inkomensverklaring 2025"}],"claims":[{"path":["jaar"]},{"path":["inkomen"]}]}}}}`,
		"/.well-known/openid-credential-issuer/test-pid/eudi": `{"credential_configurations_supported":{
			"test-pid-pid":{"vct":"urn:eudi:pid:nl:1","credential_metadata":{"display":[{"name":"PID"}],"claims":[{"path":["birthdate"]}]}}}}`,
	}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := metadata[r.URL.Path]
		if !ok || r.Header.Get("Accept") != "application/json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer issuer.Close()
	caDir := filepath.Join(f.registration.secretsDir, "development-ca")
	options := testRPOptions{walletRegistrationOptions: f.registration, publicURL: "https://rp.example/",
		issuerURL: issuer.URL, providers: []string{"belastingdienst", "test-pid"}, client: issuer.Client(),
		issuerCAFile: filepath.Join(caDir, "issuer-ca-cert.pem"), testPIDCAFile: filepath.Join(caDir, "missing.pem")}
	if _, err := provisionTestRelyingParties(options); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(f.registration.secretsDir, testRPDir)
	var config struct {
		PublicURL      string              `json:"public_url"`
		RelyingParties []map[string]string `json:"relying_parties"`
		Credentials    []testRPCredential  `json:"credentials"`
		IssuerAnchors  []map[string]string `json:"issuer_anchors"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(base, "config.json"))), &config); err != nil {
		t.Fatal(err)
	}
	if config.PublicURL != "https://rp.example" || len(config.RelyingParties) != 2 || len(config.Credentials) != 3 ||
		len(config.IssuerAnchors) != 1 || config.IssuerAnchors[0]["name"] != "gbo-sources" {
		t.Fatalf("config %+v", config)
	}
	if c := config.Credentials[0]; c.ID != "belastingdienst-inkomensverklaring_2024" || c.Name != "Inkomensverklaring 2024" || len(c.Claims) != 2 || c.Claims[1][1] != "woonplaats" {
		t.Errorf("credential %+v", c)
	}
	reader, err := loadRole(filepath.Join(f.registration.secretsDir, "development-ca"), "reader", "reader")
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range testRelyingParties {
		party := config.RelyingParties[i]
		chain, err := readKeyAndChain(filepath.Join(base, party["key_file"]), filepath.Join(base, party["chain_file"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := chain.chain[0].CheckSignatureFrom(reader.chain[0]); err != nil || chain.chain[0].Subject.CommonName != p.name {
			t.Errorf("%s: WRPAC %s: %v", p.id, chain.chain[0].Subject, err)
		}
		rc := jwtPayload(t, readFile(t, filepath.Join(base, party["registration_certificate_file"])))
		creds, _ := json.Marshal(rc["credentials"])
		want := `[{"claim":[{"path":["jaar"]},{"path":["adres","woonplaats"]},{"path":["inkomen"]}],"format":"dc+sd-jwt","meta":{"vct_values":["urn:income"]}},` +
			`{"claim":[{"path":["birthdate"]}],"format":"dc+sd-jwt","meta":{"vct_values":["urn:eudi:pid:nl:1"]}}]`
		if rc["sub"] != "NTRNL-"+p.kvk || rc["entitlements"].([]any)[0] != wrprcServiceProviderEntitlement || rc["provides_attestations"] != nil || string(creds) != want {
			t.Errorf("%s: WRPRC sub %v, credentials %s", p.id, rc["sub"], creds)
		}
		purpose, _ := json.Marshal(rc["purpose"])
		if !strings.Contains(string(purpose), "Demonstratie van het gebruik van exemplaren") {
			t.Errorf("%s: purpose %s", p.id, purpose)
		}
	}
	// A second run keeps the KvK numbers and keys.
	before := readFile(t, filepath.Join(base, "a", "wrpac-chain.pem"))
	if _, err := provisionTestRelyingParties(options); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, filepath.Join(base, "a", "wrpac-chain.pem"))) != string(before) {
		t.Error("a second run issued a new WRPAC")
	}
}

func TestProvisionTestRelyingPartiesNeedsTheIssuer(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	options := testRPOptions{walletRegistrationOptions: f.registration, publicURL: "https://rp.example",
		issuerURL: "http://127.0.0.1:1", providers: []string{"test-pid"}, client: http.DefaultClient}
	if _, err := provisionTestRelyingParties(options); err == nil || !strings.Contains(err.Error(), "is the issuer running") {
		t.Errorf("%v, want a hint that the issuer must run", err)
	}
}

// Without --providers the command takes the providers the issuer has: a
// provider without metadata (the test PID when it is not provisioned) is
// skipped. A list given on purpose stays strict.
func TestProvisionTestRelyingPartiesSkipsAbsentProviders(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-credential-issuer/belastingdienst/eudi" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"credential_configurations_supported":{"income":{"vct":"urn:income","credential_metadata":{"claims":[{"path":["jaar"]}]}}}}`))
	}))
	defer issuer.Close()
	caDir := filepath.Join(f.registration.secretsDir, "development-ca")
	options := testRPOptions{walletRegistrationOptions: f.registration, publicURL: "https://rp.example",
		issuerURL: issuer.URL, providers: []string{"test-pid", "belastingdienst"}, client: issuer.Client(),
		issuerCAFile: filepath.Join(caDir, "issuer-ca-cert.pem"), testPIDCAFile: filepath.Join(caDir, "missing.pem")}
	skipped, err := provisionTestRelyingParties(options)
	if err != nil || strings.Join(skipped, ",") != "test-pid" {
		t.Fatalf("default providers: skipped %v, %v; want test-pid skipped", skipped, err)
	}
	options.explicitProviders = true
	if _, err := provisionTestRelyingParties(options); err == nil || !strings.Contains(err.Error(), "test-pid") {
		t.Errorf("an explicit list with an absent provider: %v, want an error naming it", err)
	}
	options.explicitProviders, options.providers = false, []string{"test-pid"}
	if _, err := provisionTestRelyingParties(options); err == nil {
		t.Error("no provider with metadata: want an error")
	}
}
