package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

type credentialIssuerFixture struct {
	options      credentialIssuerOptions
	release      onboarding.SourceRelease
	registration walletRegistrationOptions
}

// newCredentialIssuerFixture is an active release with belastingdienst and
// one income type, its wallet registration provisioned, and the NL wallet
// providers mounted.
func newCredentialIssuerFixture(t *testing.T) credentialIssuerFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	secrets := t.TempDir()
	release := testReleaseWithTypes(t, secrets, now, []string{testIncome})
	caDir := filepath.Join(secrets, "development-ca")
	if err := createDevelopmentRegistrarRoot(caDir, now, rand.Reader); err != nil {
		t.Fatal(err)
	}
	source, err := walletRegistrationSourceFromRelease(release, "belastingdienst")
	if err != nil {
		t.Fatal(err)
	}
	registration := walletRegistrationOptions{
		secretsDir: secrets, publishDir: t.TempDir(), trustPublicURL: testTrustURL, kvkNumber: "12345678",
		now: func() time.Time { return now }, random: rand.Reader,
	}
	if err := provisionWalletRegistration(registration, source); err != nil {
		t.Fatal(err)
	}
	roots := t.TempDir()
	copyTestFile(t, filepath.Join(caDir, "issuer-ca-cert.pem"), filepath.Join(roots, nlWalletProvidersFile))
	return credentialIssuerFixture{
		options: credentialIssuerOptions{secretsDir: secrets, outputDir: t.TempDir(), adapterBaseURL: "https://adapter.example",
			rootsDir: roots, universalLinkBase: "walletdebuginteraction://wallet.edi.rijksoverheid.nl/",
			pidTrustAnchorsFile: writePIDAnchorsFile(t, filepath.Join(caDir, "issuer-ca-cert.pem"))},
		release:      release,
		registration: registration,
	}
}

func copyTestFile(t *testing.T, from, to string) {
	t.Helper()
	body, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f credentialIssuerFixture) materialized(t *testing.T) issuerFile {
	t.Helper()
	if err := materializeCredentialIssuer(f.options, f.release); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.options.outputDir, credentialIssuerConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var out issuerFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMaterializeCredentialIssuer(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	out := f.materialized(t)
	if len(out.Providers) != 1 || out.Providers[0].Name != "belastingdienst" {
		t.Fatalf("providers %+v", out.Providers)
	}
	p := out.Providers[0]

	t.Run("the provider signs with the source's own certificates", func(t *testing.T) {
		assertProviderCertificates(t, f, p)
	})

	t.Run("one credential configuration per activated type", func(t *testing.T) {
		if len(p.CredentialConfigurations) != 1 {
			t.Fatalf("configurations %+v", p.CredentialConfigurations)
		}
		c := p.CredentialConfigurations[0]
		if c.ID != "belastingdienst-typea" || c.Scope != c.ID || c.VCT != testIncome || c.StatusList != "typea" || c.ValidDays != 365 {
			t.Fatalf("configuration %+v", c)
		}
		if c.SourceURL != "https://adapter.example/attestations/belastingdienst/typea" {
			t.Fatalf("source URL %s: offer parameters come from the caller", c.SourceURL)
		}
		if !slices.Equal(c.Display, []issuerDisplay{{Name: "Example", Locale: "nl-NL"}}) {
			t.Fatalf("display %+v", c.Display)
		}
		mandatory := map[string]bool{}
		for _, claim := range c.Claims {
			mandatory[claim.Path[0]] = claim.Mandatory
			if len(claim.Display) != 1 || claim.Display[0].Name != claim.Path[0] || claim.Display[0].Locale != "nl-NL" {
				t.Fatalf("claim display %+v", claim)
			}
		}
		if !mandatory["value"] || mandatory["note"] || len(mandatory) != 2 {
			t.Fatalf("a claim is mandatory unless the source marks it optional: %v", mandatory)
		}
	})

	t.Run("only the profile whose wallet providers are mounted", func(t *testing.T) {
		if len(out.Profiles) != 1 || out.Profiles[0].Name != "nl" || out.Profiles[0].UniversalLinkBase == "" {
			t.Fatalf("profiles %+v", out.Profiles)
		}
		if !fileExists(filepath.Join(f.options.outputDir, out.Profiles[0].WalletProviderRootsFile)) {
			t.Fatal("the wallet provider roots must be next to the configuration")
		}
	})

	t.Run("the release marker", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(f.options.outputDir, "source-release.json"))
		if err != nil || !strings.Contains(string(raw), f.release.ID) {
			t.Fatalf("marker %s, %v", raw, err)
		}
	})
}

func TestMaterializeCredentialIssuerEUDIProfile(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	caDir := filepath.Join(f.options.secretsDir, "development-ca")
	copyTestFile(t, filepath.Join(caDir, "reader-ca-cert.pem"), filepath.Join(f.options.rootsDir, eudiWalletProvidersFile))

	t.Run("refused without key attestation levels", func(t *testing.T) {
		if err := materializeCredentialIssuer(f.options, f.release); err == nil || !strings.Contains(err.Error(), "ISSU_27d") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("with levels, key attestations from the wallet provider", func(t *testing.T) {
		f.options.eudiKeyStorage, f.options.eudiUserAuthentication = []string{"iso_18045_high"}, []string{"iso_18045_high"}
		out := f.materialized(t)
		eudi := out.Profiles[1]
		if eudi.Name != "eudi" || eudi.KeyAttestationRootsFile != eudi.WalletProviderRootsFile || !slices.Equal(eudi.KeyStorage, []string{"iso_18045_high"}) {
			t.Fatalf("profile %+v", eudi)
		}
	})
	t.Run("key attestation roots mounted separately", func(t *testing.T) {
		copyTestFile(t, filepath.Join(caDir, "issuer-ca-cert.pem"), filepath.Join(f.options.rootsDir, eudiKeyAttestationFile))
		if eudi := f.materialized(t).Profiles[1]; eudi.KeyAttestationRootsFile != eudiKeyAttestationFile {
			t.Fatalf("profile %+v", eudi)
		}
	})
}

func TestMaterializeCredentialIssuerRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(t *testing.T, f *credentialIssuerFixture)
		want   string
	}{
		"no wallet providers mounted": {func(t *testing.T, f *credentialIssuerFixture) {
			f.options.rootsDir = t.TempDir()
		}, "no wallet profile"},
		"no adapter URL": {func(_ *testing.T, f *credentialIssuerFixture) { f.options.adapterBaseURL = "" }, "adapter base URL"},
		"a source without wallet registration": {func(t *testing.T, f *credentialIssuerFixture) {
			if err := os.Remove(filepath.Join(f.options.secretsDir, "belastingdienst", "wrprc.jwt")); err != nil {
				t.Fatal(err)
			}
		}, "make provision-wallet-registration SOURCE_ID=belastingdienst"},
		"a registration from before request encryption": {func(t *testing.T, f *credentialIssuerFixture) {
			if err := os.Remove(filepath.Join(f.options.secretsDir, "belastingdienst", requestEncryptionKeyFile)); err != nil {
				t.Fatal(err)
			}
		}, requestEncryptionKeyFile},
		"mounted certificates of another release": {func(t *testing.T, f *credentialIssuerFixture) {
			dir := filepath.Join(f.options.secretsDir, "belastingdienst")
			copyTestFile(t, filepath.Join(dir, "reader-cert.der.b64"), filepath.Join(dir, "issuer-cert.der.b64"))
		}, "does not match the active release"},
		"a source id the issuer does not take": {func(_ *testing.T, f *credentialIssuerFixture) {
			f.release.Sources[0].SourceID = "belasting_dienst"
		}, "lower-case letters, digits and hyphens"},
		"a source named after the status route": {func(_ *testing.T, f *credentialIssuerFixture) {
			f.release.Sources[0].SourceID = "status"
		}, `not "status"`},
		"a registration that predates an activated type": {func(t *testing.T, f *credentialIssuerFixture) {
			source, err := walletRegistrationSourceFromRelease(f.release, "belastingdienst")
			if err != nil {
				t.Fatal(err)
			}
			source.VCTs = []string{"https://adapter.example/types/other"}
			if err := provisionWalletRegistration(f.registration, source); err != nil {
				t.Fatal(err)
			}
		}, "does not name the activated type " + testIncome},
		"an expired access certificate": {func(_ *testing.T, f *credentialIssuerFixture) {
			f.options.now = func() time.Time { return time.Now().AddDate(2, 0, 0) }
		}, "access certificate has expired"},
		"an unreadable roots file": {func(t *testing.T, f *credentialIssuerFixture) {
			if err := os.Chmod(f.options.rootsDir, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(f.options.rootsDir, 0o700) })
		}, nlWalletProvidersFile},
		"a roots file with more than certificates": {func(t *testing.T, f *credentialIssuerFixture) {
			path := filepath.Join(f.options.rootsDir, nlWalletProvidersFile)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(body, []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "only PEM certificates"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCredentialIssuerFixture(t)
			tc.mutate(t, &f)
			err := materializeCredentialIssuer(f.options, f.release)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if fileExists(filepath.Join(f.options.outputDir, credentialIssuerConfigFile)) {
				t.Fatal("a refused run must not leave a configuration")
			}
		})
	}
}

// assertProviderCertificates checks that the provider signs with what was
// provisioned for the source: its issuer and status leaves alone, its WRPAC
// and its WRPRC.
func assertProviderCertificates(t *testing.T, f credentialIssuerFixture, p issuerProviderFile) {
	t.Helper()
	sourceDir := filepath.Join(f.options.secretsDir, "belastingdienst")
	for role, files := range map[string]issuerKeyFiles{"issuer": p.Issuer, "status": p.Status} {
		got, err := readKeyAndChain(filepath.Join(f.options.outputDir, files.KeyFile), filepath.Join(f.options.outputDir, files.ChainFile))
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		_, want, err := loadDevelopmentLeaf(filepath.Join(sourceDir, role+"-key.der.b64"), filepath.Join(sourceDir, role+"-cert.der.b64"))
		if err != nil || len(got.chain) != 1 || !got.chain[0].Equal(want) {
			t.Fatalf("%s: the chain must be the provisioned leaf alone", role)
		}
	}
	access, err := readKeyAndChain(filepath.Join(f.options.outputDir, p.Access.KeyFile), filepath.Join(f.options.outputDir, p.Access.ChainFile))
	if err != nil {
		t.Fatal(err)
	}
	wrpac, err := readKeyAndChain(filepath.Join(sourceDir, "wrpac-key.pem"), filepath.Join(sourceDir, "wrpac-chain.pem"))
	if err != nil || !access.chain[0].Equal(wrpac.chain[0]) {
		t.Fatal("the access certificate must be the WRPAC")
	}
	rc, _ := os.ReadFile(filepath.Join(f.options.outputDir, p.RegistrationCertificateFile))
	want, _ := os.ReadFile(filepath.Join(sourceDir, "wrprc.jwt"))
	if len(rc) == 0 || string(rc) != string(want) {
		t.Fatal("the registration certificate must be the WRPRC")
	}
	info, err := os.Stat(filepath.Join(f.options.outputDir, p.Issuer.KeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("issuer key mode %v, %v", info.Mode(), err)
	}
	requestKey, err := readECKey(filepath.Join(f.options.outputDir, p.RequestEncryptionKeyFile))
	provisioned, _ := readECKey(filepath.Join(sourceDir, requestEncryptionKeyFile))
	if err != nil || provisioned == nil || !requestKey.Equal(provisioned) {
		t.Fatalf("the request encryption key must be the provisioned one: %v", err)
	}
	if info, err := os.Stat(filepath.Join(f.options.outputDir, p.RequestEncryptionKeyFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("request encryption key mode %v, %v", info.Mode(), err)
	}
}

func TestMaterializeCredentialIssuerReplacesItsOutput(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	f.materialized(t)
	config := filepath.Join(f.options.outputDir, credentialIssuerConfigFile)
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a failed run leaves the previous configuration", func(t *testing.T) {
		broken := f.options
		broken.adapterBaseURL = "https://adapter.example"
		broken.rootsDir = t.TempDir()
		if err := materializeCredentialIssuer(broken, f.release); err == nil {
			t.Fatal("expected refusal")
		}
		after, err := os.ReadFile(config)
		if err != nil || string(after) != string(before) {
			t.Fatal("the previous configuration must stay as it was")
		}
		if !fileExists(filepath.Join(f.options.outputDir, "belastingdienst", "issuer-key.pem")) {
			t.Fatal("the keys of the previous configuration must stay")
		}
		assertNoStaging(t, f.options.outputDir)
	})
	t.Run("a source that left the release takes its keys along", func(t *testing.T) {
		gone := filepath.Join(f.options.outputDir, "gone")
		if err := os.MkdirAll(gone, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gone, "issuer-key.pem"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.materialized(t)
		if fileExists(gone) {
			t.Fatal("the directory of a source that left the release must be removed")
		}
		assertNoStaging(t, f.options.outputDir)
	})
}

func assertNoStaging(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatalf("staging directory %s left behind", e.Name())
		}
	}
}

func TestMaterializeCredentialIssuerHandsOutputToOwner(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	// A test cannot become another user; handing to itself exercises the walk.
	uid, gid := os.Getuid(), os.Getgid()
	f.options.owner = &fileOwner{uid: uid, gid: gid}
	f.materialized(t)
	if _, err := os.Stat(filepath.Join(f.options.outputDir, "belastingdienst", "issuer-key.pem")); err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]*fileOwner{"": nil, "1000": {1000, 1000}, "1000:2000": {1000, 2000}} {
		got, err := parseFileOwner(value)
		if err != nil || (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Fatalf("%q: %+v, %v", value, got, err)
		}
	}
	for _, bad := range []string{"x", "1000:", "-1", "1000:y"} {
		if _, err := parseFileOwner(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

// writePIDAnchorsFile writes the certificate in pemFile as a PID trust
// anchors file: base64 DER per line, with a comment.
func writePIDAnchorsFile(t *testing.T, pemFile string) string {
	t.Helper()
	raw, err := os.ReadFile(pemFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	path := filepath.Join(t.TempDir(), "pid-anchors.txt")
	body := "# test PID issuer CA\n\n" + base64.StdEncoding.EncodeToString(block.Bytes) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
