package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// testPIDFixture is a wallet registration fixture with the test PID
// directories next to it.
type testPIDFixture struct {
	walletRegistrationFixture
	dirs testPIDDirs
}

func newTestPIDFixture(t *testing.T) testPIDFixture {
	t.Helper()
	f := newWalletRegistrationFixture(t)
	f.options.kvkNumber = testPIDKvKNumber
	return testPIDFixture{walletRegistrationFixture: f, dirs: testPIDDirs{publicDir: t.TempDir(), rootDir: t.TempDir()}}
}

func (f testPIDFixture) provision(t *testing.T) {
	t.Helper()
	if err := provisionTestPID(f.options, f.dirs); err != nil {
		t.Fatal(err)
	}
}

func (f testPIDFixture) root(t *testing.T) issuedCertificate {
	t.Helper()
	root, err := loadRole(f.dirs.rootDir, testPIDRootRole, "test PID root")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func (f testPIDFixture) leaf(t *testing.T, role string) issuedCertificate {
	t.Helper()
	dir := filepath.Join(f.options.secretsDir, testPIDID)
	got, err := readKeyAndChain(filepath.Join(dir, role+"-key.pem"), filepath.Join(dir, role+"-chain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProvisionTestPID(t *testing.T) {
	f := newTestPIDFixture(t)
	f.provision(t)
	root := f.root(t).chain[0]

	t.Run("P1 issuer and status certificates under the test PID root, with one subject", func(t *testing.T) {
		issuer, status := f.leaf(t, "issuer").chain, f.leaf(t, "status").chain
		if len(issuer) != 1 || issuer[0].CheckSignatureFrom(root) != nil || len(status) != 1 || status[0].CheckSignatureFrom(root) != nil {
			t.Fatal("the issuer and status certificates must be issued directly by the test PID root, without it in the chain")
		}
		if !bytes.Equal(issuer[0].RawSubject, status[0].RawSubject) {
			t.Fatal("the status certificate must have the subject of the issuer certificate")
		}
		if !slices.ContainsFunc(issuer[0].UnknownExtKeyUsage, issuerEKUOID.Equal) || !slices.ContainsFunc(status[0].UnknownExtKeyUsage, statusEKUOID.Equal) {
			t.Fatal("EKUs")
		}
		if root.Subject.CommonName != "GBO test-PID root" || !root.IsCA {
			t.Fatalf("root %s", root.Subject)
		}
	})
	t.Run("P1 the root is its own and outside the secrets directory", func(t *testing.T) {
		for _, role := range []string{"issuer", "reader", registrarRootRole} {
			if bytes.Equal(root.Raw, f.caCert(t, role).Raw) {
				t.Fatalf("the test PID root must not be the %s root", role)
			}
		}
		if k, _ := caFiles(filepath.Join(f.options.secretsDir, "development-ca"), testPIDRootRole); fileExists(k) {
			t.Fatal("the test PID root key must not be in the secrets directory, which containers mount")
		}
	})
	t.Run("P1 WRPAC under the reader root and a PID_Provider WRPRC", func(t *testing.T) {
		wrpac := f.leaf(t, "wrpac").chain[0]
		if wrpac.CheckSignatureFrom(f.caCert(t, "reader")) != nil || organizationIdentifier(wrpac) != "NTRNL-"+testPIDKvKNumber {
			t.Fatalf("WRPAC %s", wrpac.Subject)
		}
		raw, err := os.ReadFile(filepath.Join(f.options.secretsDir, testPIDID, "wrprc.jwt"))
		if err != nil {
			t.Fatal(err)
		}
		_, _, claims := verifiedJWT(t, string(raw), f.caCert(t, registrarRootRole))
		entitlements, _ := claims["entitlements"].([]any)
		if len(entitlements) != 1 || entitlements[0] != wrprcPIDEntitlement || !slices.Equal(providedVCTs(claims), []string{testPIDType}) ||
			claims["sub"] != "NTRNL-"+testPIDKvKNumber {
			t.Fatalf("WRPRC claims %v", claims)
		}
		if _, err := readECKey(filepath.Join(f.options.secretsDir, testPIDID, requestEncryptionKeyFile)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("P1 the root certificate is published, its key is not", func(t *testing.T) {
		dir := filepath.Join(f.dirs.publicDir, "development-ca")
		if !fileExists(filepath.Join(dir, "test-pid-ca-cert.pem")) || fileExists(filepath.Join(dir, "test-pid-ca-key.pem")) {
			t.Fatal("public projection")
		}
	})
}

func TestProvisionTestPIDAgain(t *testing.T) {
	f := newTestPIDFixture(t)
	f.provision(t)
	root := f.root(t).chain[0]

	t.Run("P2 a second run keeps the root and every key", func(t *testing.T) {
		issuer, wrpac := f.leaf(t, "issuer"), f.leaf(t, "wrpac")
		f.provision(t)
		if !f.root(t).chain[0].Equal(root) {
			t.Fatal("the root must never be replaced")
		}
		if again := f.leaf(t, "issuer"); !again.key.Equal(issuer.key) || !again.chain[0].Equal(issuer.chain[0]) {
			t.Fatal("the issuer certificate must be kept")
		}
		if again := f.leaf(t, "wrpac"); !again.key.Equal(wrpac.key) {
			t.Fatal("the WRPAC key must be kept")
		}
	})
	t.Run("P2 near expiry the certificates are renewed for the same keys", func(t *testing.T) {
		issuer := f.leaf(t, "issuer")
		// The fixture's reader root lives a year; one that outlives the
		// renewal moment lets the WRPAC be issued again.
		writePreProvisionedReaderRoot(t, filepath.Join(f.options.secretsDir, "development-ca"), f.now.AddDate(1, 0, 0))
		later := f
		later.options.now = func() time.Time { return f.now.AddDate(0, 11, 15) }
		later.provision(t)
		again := f.leaf(t, "issuer")
		if !again.key.Equal(issuer.key) || again.chain[0].Equal(issuer.chain[0]) || !again.chain[0].NotAfter.After(issuer.chain[0].NotAfter) {
			t.Fatal("a certificate in its last month must be renewed for the same key")
		}
		if !f.root(t).chain[0].Equal(root) {
			t.Fatal("the root must never be replaced")
		}
	})
}

// P3: what a registration needs is checked before anything is created.
func TestProvisionTestPIDRefusesBeforeCreating(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, f *testPIDFixture){
		"no reader root": func(t *testing.T, f *testPIDFixture) {
			removeRole(t, filepath.Join(f.options.secretsDir, "development-ca"), "reader")
		},
		"no registrar root": func(t *testing.T, f *testPIDFixture) {
			removeRole(t, filepath.Join(f.options.secretsDir, "development-ca"), registrarRootRole)
		},
		"no trust URL": func(_ *testing.T, f *testPIDFixture) { f.options.trustPublicURL = "" },
		"a KvK number of a source": func(t *testing.T, f *testPIDFixture) {
			registration := f.options
			registration.kvkNumber = "12345678"
			if err := provisionWalletRegistration(registration, f.source); err != nil {
				t.Fatal(err)
			}
			f.options.kvkNumber = "12345678"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTestPIDFixture(t)
			mutate(t, &f)
			if err := provisionTestPID(f.options, f.dirs); err == nil {
				t.Fatal("expected refusal")
			}
			if k, _ := caFiles(f.dirs.rootDir, testPIDRootRole); fileExists(k) || fileExists(filepath.Join(f.options.secretsDir, testPIDID)) {
				t.Fatal("a refused run must create nothing")
			}
		})
	}
}

func removeRole(t *testing.T, caDir, role string) {
	t.Helper()
	keyPath, certPath := caFiles(caDir, role)
	for _, p := range []string{keyPath, certPath} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
}

// The name and the KvK number of the test PID provider are its own.
func TestTestPIDIsReserved(t *testing.T) {
	f := newTestPIDFixture(t)
	source := f.source
	source.SourceID = testPIDID
	if err := provisionWalletRegistration(f.options, source); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("a source named test-pid: %v", err)
	}
	f.provision(t)
	registration := f.options
	registration.kvkNumber = testPIDKvKNumber
	if err := provisionWalletRegistration(registration, f.source); err == nil || !strings.Contains(err.Error(), "registered for test-pid") {
		t.Fatalf("a source with the test PID's KvK number: %v", err)
	}
}

func TestMaterializeTestPID(t *testing.T) {
	t.Run("M2 without test PID material, no test PID provider", func(t *testing.T) {
		f := newCredentialIssuerFixture(t)
		f.options.testPIDSourceURL = "http://test-pid-source:8080/attestations"
		for _, p := range f.materialized(t).Providers {
			if p.Name == testPIDID {
				t.Fatal("unexpected test PID provider")
			}
		}
	})

	f := newCredentialIssuerFixture(t)
	f.options.testPIDSourceURL = "http://test-pid-source:8080/attestations"
	registration := f.registration
	registration.kvkNumber = testPIDKvKNumber
	dirs := testPIDDirs{publicDir: t.TempDir(), rootDir: t.TempDir()}
	if err := provisionTestPID(registration, dirs); err != nil {
		t.Fatal(err)
	}

	t.Run("M1 a test PID provider next to the sources", func(t *testing.T) {
		out := f.materialized(t)
		if len(out.Providers) != 2 || out.Providers[0].Name != "belastingdienst" || out.Providers[1].Name != testPIDID {
			t.Fatalf("providers %+v", out.Providers)
		}
		p := out.Providers[1]
		if len(p.CredentialConfigurations) != 1 {
			t.Fatalf("configurations %+v", p.CredentialConfigurations)
		}
		c := p.CredentialConfigurations[0]
		if c.ID != "test-pid-pid" || c.Scope != c.ID || c.VCT != testPIDType || c.StatusList != "pid" ||
			c.SourceURL != "http://test-pid-source:8080/attestations" || c.ValidDays != 365 {
			t.Fatalf("configuration %+v", c)
		}
		if len(c.Claims) != 12 || !c.Claims[0].Mandatory || c.Claims[6].Mandatory || c.Claims[7].Mandatory {
			t.Fatalf("claims %+v", c.Claims)
		}
		if !slices.Equal(c.Display, []issuerDisplay{{Name: "Test-PID", Locale: "nl"}, {Name: "Test PID", Locale: "en"}}) {
			t.Fatalf("display %+v", c.Display)
		}
		root, err := loadRole(dirs.rootDir, testPIDRootRole, "test PID root")
		if err != nil {
			t.Fatal(err)
		}
		issuer, err := readKeyAndChain(filepath.Join(f.options.outputDir, p.Issuer.KeyFile), filepath.Join(f.options.outputDir, p.Issuer.ChainFile))
		if err != nil || len(issuer.chain) != 1 || issuer.chain[0].CheckSignatureFrom(root.chain[0]) != nil {
			t.Fatalf("the issuer chain must be the leaf under the test PID root: %v", err)
		}
		if p.RegistrationCertificateFile == "" || p.RequestEncryptionKeyFile == "" || p.Access.KeyFile == "" {
			t.Fatalf("wallet registration %+v", p)
		}
	})
	t.Run("M1 a test PID without its bronpoort URL is refused", func(t *testing.T) {
		g := f
		g.options.testPIDSourceURL = ""
		if err := materializeCredentialIssuer(g.options, g.release); err == nil || !strings.Contains(err.Error(), "TEST_PID_SOURCE_URL") {
			t.Fatalf("got %v", err)
		}
	})
}

// M3: an expired test PID registration or certificate is refused, by name.
func TestMaterializeTestPIDRefusesExpired(t *testing.T) {
	// The test PID is provisioned eleven months before the sources, so at a
	// moment two months later only its material has expired.
	for name, role := range map[string]string{"WRPAC": "", "issuer leaf": "issuer", "status leaf": "status"} {
		t.Run(name, func(t *testing.T) {
			f := newCredentialIssuerFixture(t)
			f.options.testPIDSourceURL = "http://test-pid-source:8080/attestations"
			now := f.registration.now()
			early := f.registration
			early.kvkNumber = testPIDKvKNumber
			early.now = func() time.Time { return now.AddDate(0, -11, 0) }
			if err := provisionTestPID(early, testPIDDirs{publicDir: t.TempDir(), rootDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			if role != "" {
				// The registration is renewed now; only the leaves stay old.
				current := f.registration
				current.kvkNumber = testPIDKvKNumber
				if err := registerWallet(current, walletRegistrationSource{SourceID: testPIDID, Name: testPIDName,
					CertificateSet: testPIDID, VCTs: []string{testPIDType}}, wrprcPIDEntitlement, false); err != nil {
					t.Fatal(err)
				}
				if role == "status" {
					// The issuer leaf is renewed too; only the status leaf stays old.
					writeFreshTestPIDLeaf(t, f, current, "issuer")
				}
			}
			f.options.now = func() time.Time { return now.AddDate(0, 2, 0) }
			err := materializeCredentialIssuer(f.options, f.release)
			if err == nil || !strings.Contains(err.Error(), "test PID") || !strings.Contains(err.Error(), "expired") {
				t.Fatalf("got %v", err)
			}
			if role != "" && !strings.Contains(err.Error(), "the "+role+" certificate has expired") {
				t.Fatalf("want the %s leaf named: %v", role, err)
			}
		})
	}
}

// writeFreshTestPIDLeaf replaces a test PID leaf with one valid from now,
// issued by a throwaway root: only its validity matters here.
func writeFreshTestPIDLeaf(t *testing.T, f credentialIssuerFixture, options walletRegistrationOptions, role string) {
	t.Helper()
	dir := filepath.Join(f.registration.secretsDir, testPIDID)
	for _, name := range []string{role + "-key.pem", role + "-chain.pem"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	rootDir := t.TempDir()
	if err := createDevelopmentRoot(rootDir, "throwaway", testPIDSubject, options.now(), options.random); err != nil {
		t.Fatal(err)
	}
	root, err := loadRole(rootDir, "throwaway", "throwaway root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCertificate(options, dir, role, root, testPIDLeafTemplate(issuerEKUOID, options.now())); err != nil {
		t.Fatal(err)
	}
}

func providedVCTs(claims map[string]any) []string {
	var out []string
	provides, _ := claims["provides_attestations"].([]any)
	for _, p := range provides {
		meta, _ := p.(map[string]any)["meta"].(map[string]any)
		values, _ := meta["vct_values"].([]any)
		for _, v := range values {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
