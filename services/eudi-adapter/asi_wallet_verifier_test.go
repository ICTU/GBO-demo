package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func asiVerifierFixture(t *testing.T, asURL string) (credentialIssuerFixture, asiVerifierOptions) {
	t.Helper()
	f := newCredentialIssuerFixture(t)
	options := asiVerifierOptions{walletRegistrationOptions: f.registration, publicURL: "https://verifier.example/",
		authorizationServerURL: asURL, pidTrustAnchorsFile: f.options.pidTrustAnchorsFile}
	options.kvkNumber = asiVerifierKvKNumber
	return f, options
}

// P1 of #639: the authorization server is registered as a Service_Provider
// that asks the five PID claims of its login, with the purpose decided for
// it, and the verifier's configuration names its WRPAC, WRPRC, PID trust
// and a same-device return for an https authorization server.
func TestASIWalletVerifierRegistration(t *testing.T) {
	_, options := asiVerifierFixture(t, "https://as.example")
	if err := provisionASIWalletVerifier(options); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(options.secretsDir, asiVerifierDir)
	rc := jwtPayload(t, readFile(t, filepath.Join(dir, verifierRegistrationFile)))
	creds, _ := json.Marshal(rc["credentials"])
	if rc["sub"] != "NTRNL-"+asiVerifierKvKNumber || rc["entitlements"].([]any)[0] != wrprcServiceProviderEntitlement ||
		rc["id"] != "gbo-asi-authorization-server-verifier" || rc["provides_attestations"] != nil ||
		string(creds) != `[{"claim":[{"path":["bsn"]},{"path":["family_name"]},{"path":["given_name"]},{"path":["birthdate"]},{"path":["nationalities"]}],"format":"dc+sd-jwt","meta":{"vct_values":["urn:eudi:pid:nl:1"]}}]` {
		t.Fatalf("WRPRC %v", rc)
	}
	purpose, _ := json.Marshal(rc["purpose"])
	if !strings.Contains(string(purpose), "Uw identiteit vaststellen, zodat u toestemming kunt geven voor het opvragen van uw gegevens bij de bron.") {
		t.Errorf("purpose %s", purpose)
	}
	if fileExists(filepath.Join(dir, "wrprc.jwt")) {
		t.Error("the authorization server provides no attestations")
	}
	chain, err := readKeyAndChain(filepath.Join(dir, "wrpac-key.pem"), filepath.Join(dir, "wrpac-chain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := loadRole(filepath.Join(options.secretsDir, "development-ca"), "reader", "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.chain[0].CheckSignatureFrom(reader.chain[0]); err != nil || chain.chain[0].Subject.CommonName != asiVerifierName {
		t.Errorf("WRPAC %s: %v", chain.chain[0].Subject, err)
	}

	var config struct {
		PublicURL             string              `json:"public_url"`
		APIKeyFile            string              `json:"api_key_file"`
		PIDAnchors            []map[string]string `json:"pid_anchors"`
		PIDTrustedAuthorities []string            `json:"pid_trusted_authorities"`
		Usecases              map[string]struct {
			KeyFile, ChainFile, RegistrationCertificateFile string
			PIDType                                         string   `json:"pid_type"`
			Claims                                          []string `json:"claims"`
			ReturnURL                                       string   `json:"return_url"`
		} `json:"usecases"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(dir, asiVerifierConfig)), &config); err != nil {
		t.Fatal(err)
	}
	login := config.Usecases[asiVerifierUsecase]
	if config.PublicURL != "https://verifier.example" || login.ReturnURL != "same_device" || login.PIDType != nlPIDType ||
		!slices.Equal(login.Claims, asiLoginClaims) || len(config.PIDTrustedAuthorities) == 0 {
		t.Fatalf("config %+v", config)
	}
	for _, a := range config.PIDAnchors {
		if !fileExists(filepath.Join(dir, a["roots_file"])) {
			t.Errorf("anchor %v has no file", a)
		}
	}
	key := readFile(t, filepath.Join(dir, config.APIKeyFile))
	if len(key) < 32 || string(readFile(t, filepath.Join(options.secretsDir, asiVerifierClientDir, asiVerifierAPIKey))) != string(key) {
		t.Error("the authorization server has another API key than the verifier")
	}

	// A second run keeps the keys and the API key.
	wrpacKey := readFile(t, filepath.Join(dir, "wrpac-key.pem"))
	if err := provisionASIWalletVerifier(options); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, filepath.Join(dir, "wrpac-key.pem"))) != string(wrpacKey) || string(readFile(t, filepath.Join(dir, asiVerifierAPIKey))) != string(key) {
		t.Error("a second run replaced the WRPAC key or the API key")
	}
}

// An http authorization server gets no same-device return: the verifier
// would refuse its sessions without a return URL template otherwise.
func TestASIWalletVerifierLocalHTTP(t *testing.T) {
	_, options := asiVerifierFixture(t, "http://localhost:9425")
	if err := provisionASIWalletVerifier(options); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(options.secretsDir, asiVerifierDir, asiVerifierConfig))
	if !strings.Contains(string(raw), `"return_url": "neither"`) {
		t.Fatalf("config %s", raw)
	}
	options.authorizationServerURL = "not a url"
	if err := provisionASIWalletVerifier(options); err == nil {
		t.Fatal("an invalid authorization server URL was accepted")
	}
	if _, err := os.Stat(filepath.Join(options.secretsDir, asiVerifierDir, asiVerifierConfig)); err != nil {
		t.Fatal(err)
	}
}
