package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// jwtPayload decodes the payload of a compact JWT without verifying it.
func jwtPayload(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	parts := strings.Split(strings.TrimSpace(string(raw)), ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWT: %s", raw)
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// R1 and R2: a source gets a WRPRC for its verifier role next to its
// issuer WRPRC, with a status entry of its own that a new run keeps.
func TestVerifierRegistration(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	dir := filepath.Join(f.options.secretsDir, "belastingdienst")
	issuer := jwtPayload(t, readFile(t, filepath.Join(dir, "wrprc.jwt")))
	verifier := jwtPayload(t, readFile(t, filepath.Join(dir, verifierRegistrationFile)))

	if verifier["sub"] != issuer["sub"] || verifier["id"] != "gbo-belastingdienst-verifier" {
		t.Fatalf("sub %v id %v", verifier["sub"], verifier["id"])
	}
	if e := verifier["entitlements"].([]any); len(e) != 1 || e[0] != wrprcServiceProviderEntitlement {
		t.Fatalf("entitlements %v", e)
	}
	if _, ok := verifier["provides_attestations"]; ok {
		t.Fatal("the verifier role provides no attestations")
	}
	creds, _ := json.Marshal(verifier["credentials"])
	if string(creds) != `[{"claim":[{"path":["bsn"]}],"format":"dc+sd-jwt","meta":{"vct_values":["urn:eudi:pid:nl:1"]}}]` {
		t.Fatalf("credentials %s", creds)
	}
	purpose := verifier["purpose"].([]any)
	if len(purpose) != 2 || !strings.Contains(purpose[0].(map[string]any)["value"].(string), "zodat Belastingdienst uw gegevens kan ophalen") ||
		purpose[1].(map[string]any)["lang"] != "en" {
		t.Fatalf("purpose %v", purpose)
	}
	idx := func(p map[string]any) any { return p["status"].(map[string]any)["status_list"].(map[string]any)["idx"] }
	if idx(verifier) == idx(issuer) {
		t.Fatal("the verifier role needs a status entry of its own")
	}
	iat, exp := time.Unix(int64(verifier["iat"].(float64)), 0), time.Unix(int64(verifier["exp"].(float64)), 0)
	if exp.After(iat.AddDate(1, 0, 0)) {
		t.Fatalf("valid from %v to %v: more than twelve months", iat, exp)
	}

	source, err := walletRegistrationSourceFromRelease(f.release, "belastingdienst")
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionWalletRegistration(f.registration, source); err != nil {
		t.Fatal(err)
	}
	again := jwtPayload(t, readFile(t, filepath.Join(dir, verifierRegistrationFile)))
	if idx(again) != idx(verifier) {
		t.Fatalf("R2: status index %v, was %v", idx(again), idx(verifier))
	}
}

// R3: the test PID asks for no PID, so it has no verifier WRPRC; its public
// root lies next to its registration for the materializer.
func TestTestPIDHasNoVerifierRegistration(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	registration := f.registration
	registration.kvkNumber = testPIDKvKNumber
	if err := provisionTestPID(registration, testPIDDirs{publicDir: t.TempDir(), rootDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.options.secretsDir, testPIDID)
	if fileExists(filepath.Join(dir, verifierRegistrationFile)) {
		t.Fatal("the test PID must have no verifier registration")
	}
	if _, err := readCertificateFile(filepath.Join(dir, testPIDRootFile)); err != nil {
		t.Fatalf("test PID root: %v", err)
	}
}

// M1 and M3: the issuer configuration names the verifier WRPRC, an offer
// per source offer, and the PID trust anchors.
func TestMaterializeDisclosure(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	out := f.materialized(t)
	p := out.Providers[0]
	if p.VerifierRegistrationCertificateFile != "belastingdienst/verifier-registration-certificate.jwt" {
		t.Fatalf("verifier registration file %q", p.VerifierRegistrationCertificateFile)
	}
	if got, want := readFile(t, filepath.Join(f.options.outputDir, p.VerifierRegistrationCertificateFile)),
		readFile(t, filepath.Join(f.options.secretsDir, "belastingdienst", verifierRegistrationFile)); string(got) != string(want) {
		t.Fatal("the verifier registration is not copied as is")
	}
	want := []issuerDisclosureOffer{{Key: "offera", Provider: "belastingdienst", CredentialConfigurationIDs: []string{p.CredentialConfigurations[0].ID}}}
	if !reflect.DeepEqual(out.DisclosureOffers, want) {
		t.Fatalf("disclosure offers %+v, want %+v", out.DisclosureOffers, want)
	}
	if len(out.PIDTrustAnchors) != 1 || out.PIDTrustAnchors[0] != (issuerAnchors{Name: "nl", RootsFile: "pid-trust/nl.pem"}) {
		t.Fatalf("PID trust anchors %+v", out.PIDTrustAnchors)
	}
	if _, err := readCertificateFile(filepath.Join(f.options.outputDir, "pid-trust", "nl.pem")); err != nil {
		t.Fatal(err)
	}

	t.Run("with the test PID", func(t *testing.T) {
		g := newCredentialIssuerFixture(t)
		g.options.testPIDSourceURL = "http://test-pid-source:8080/attestations"
		registration := g.registration
		registration.kvkNumber = testPIDKvKNumber
		if err := provisionTestPID(registration, testPIDDirs{publicDir: t.TempDir(), rootDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		out := g.materialized(t)
		if len(out.PIDTrustAnchors) != 2 || out.PIDTrustAnchors[1] != (issuerAnchors{Name: testPIDID, RootsFile: "pid-trust/test-pid.pem"}) {
			t.Fatalf("PID trust anchors %+v", out.PIDTrustAnchors)
		}
		if got := readFile(t, filepath.Join(g.options.outputDir, "pid-trust", "test-pid.pem")); string(got) !=
			string(readFile(t, filepath.Join(g.options.secretsDir, testPIDID, testPIDRootFile))) {
			t.Fatal("the test PID root is not copied")
		}
		for _, provider := range out.Providers {
			if provider.Name == testPIDID && provider.VerifierRegistrationCertificateFile != "" {
				t.Fatal("the test PID provider has no verifier registration")
			}
		}
		if err := os.Remove(filepath.Join(g.options.secretsDir, testPIDID, testPIDRootFile)); err != nil {
			t.Fatal(err)
		}
		if err := materializeCredentialIssuer(g.options, g.release); err == nil || !strings.Contains(err.Error(), "make provision-test-pid") {
			t.Fatalf("a test PID without its root: %v", err)
		}
	})
	t.Run("without NL PID issuers", func(t *testing.T) {
		g := newCredentialIssuerFixture(t)
		empty := filepath.Join(t.TempDir(), "anchors.txt")
		if err := os.WriteFile(empty, []byte("# none\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{empty, filepath.Join(t.TempDir(), "missing.txt")} {
			g.options.pidTrustAnchorsFile = path
			if err := materializeCredentialIssuer(g.options, g.release); err == nil || !strings.Contains(err.Error(), "PID_TRUST_ANCHORS_FILE") {
				t.Errorf("%s: %v", path, err)
			}
		}
	})
}

// M2: without a valid verifier WRPRC the materializer stops and names the
// command that makes one.
func TestMaterializeRefusesVerifierRegistration(t *testing.T) {
	fake := func(payload map[string]any) []byte {
		body, _ := json.Marshal(payload)
		return []byte("e30." + base64.RawURLEncoding.EncodeToString(body) + ".c2ln")
	}
	f := newCredentialIssuerFixture(t)
	dir := filepath.Join(f.options.secretsDir, "belastingdienst")
	valid := jwtPayload(t, readFile(t, filepath.Join(dir, verifierRegistrationFile)))
	with := func(changes map[string]any) []byte {
		p := map[string]any{}
		for k, v := range valid {
			p[k] = v
		}
		for k, v := range changes {
			p[k] = v
		}
		return fake(p)
	}
	for name, tc := range map[string]struct {
		body []byte
		want string
	}{
		"missing":                  {nil, "no verifier registration"},
		"another organization":     {with(map[string]any{"sub": "NTRNL-00000000"}), "does not belong to the access certificate"},
		"expired":                  {with(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}), "has expired"},
		"the issuer role":          {with(map[string]any{"entitlements": []string{wrprcPubEAAEntitlement}}), "Service_Provider"},
		"another claim":            {with(map[string]any{"credentials": []map[string]any{{"format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{nlPIDType}}, "claim": []map[string]any{{"path": []string{"given_name"}}}}}}), "does not authorize the bsn"},
		"another type of PID":      {with(map[string]any{"credentials": []map[string]any{{"format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{"urn:eudi:pid:1"}}, "claim": []map[string]any{{"path": []string{"bsn"}}}}}}), "does not authorize the bsn"},
		"bsn restricted to values": {with(map[string]any{"credentials": []map[string]any{{"format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{nlPIDType}}, "claim": []map[string]any{{"path": []string{"bsn"}, "values": []string{"1"}}}}}}), "does not authorize the bsn"},
		"not a compact JWT":        {[]byte("a.b"), "not a compact JWT"},
	} {
		path := filepath.Join(dir, verifierRegistrationFile)
		if tc.body == nil {
			_ = os.Remove(path)
		} else if err := os.WriteFile(path, tc.body, 0o644); err != nil {
			t.Fatal(err)
		}
		err := materializeCredentialIssuer(f.options, f.release)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "make provision-wallet-registration SOURCE_ID=belastingdienst") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// M4: an offer id becomes the offer key as is, and only when it is one path
// segment; parameters become the text of the bronpoort query.
func TestDisclosureOffer(t *testing.T) {
	got, err := disclosureOffer("belastingdienst", "belastingdienst-inkomen", sourceOffer{ID: "inkomensverklaring_2025",
		Parameters: map[string]any{"jaar": float64(2025), "definitief": true, "soort": "IB"}})
	if err != nil {
		t.Fatal(err)
	}
	want := issuerDisclosureOffer{Key: "inkomensverklaring_2025", Provider: "belastingdienst", CredentialConfigurationIDs: []string{"belastingdienst-inkomen"},
		Parameters: map[string]string{"jaar": "2025", "definitief": "true", "soort": "IB"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if _, err := disclosureOffer("belastingdienst", "c", sourceOffer{ID: "inkomen.2025"}); err != nil {
		t.Errorf("a dotted id, as the source metadata schema allows: %v", err)
	}
	for _, id := range []string{"a/b", "a b", "", "jaar?2025", ".", "..", "a..b", ".a", "a."} {
		if _, err := disclosureOffer("belastingdienst", "c", sourceOffer{ID: id}); err == nil || !strings.Contains(err.Error(), "offer key") {
			t.Errorf("%q: %v", id, err)
		}
	}
	if _, err := disclosureOffer("belastingdienst", "c", sourceOffer{ID: "x", Parameters: map[string]any{"jaar": 2025.5}}); err == nil {
		t.Error("a fractional parameter must be refused")
	}
}
