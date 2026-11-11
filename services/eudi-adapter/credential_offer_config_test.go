package main

// B26 (#614): one credential configuration per source offer, so a wallet
// that starts at the issuer names an offer by its scope (HAIP 1.0 section
// 4.1 asks a scope for every configuration). Cases M1-M7.

import (
	"crypto/rand"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const testDeath = "https://source.example/types/death"

// offersFixture is the credential issuer fixture with the release given by
// offers per vct.
func offersFixture(t *testing.T, vcts []string, offers map[string][]sourceOffer) credentialIssuerFixture {
	t.Helper()
	f := newCredentialIssuerFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	f.release = testReleaseWithOffers(t, f.options.secretsDir, now, vcts, offers)
	source, err := walletRegistrationSourceFromRelease(f.release, "belastingdienst")
	if err != nil {
		t.Fatal(err)
	}
	f.registration.now = func() time.Time { return now }
	f.registration.random = rand.Reader
	if err := provisionWalletRegistration(f.registration, source); err != nil {
		t.Fatal(err)
	}
	return f
}

func yearOffers() []sourceOffer {
	return []sourceOffer{
		{ID: "inkomensverklaring_2024", Label: "Inkomensverklaring 2024", Parameters: map[string]any{}},
		{ID: "inkomensverklaring_2025", Label: "Inkomensverklaring 2025", Parameters: map[string]any{}},
	}
}

func TestOneConfigurationPerOffer(t *testing.T) { // M1, M2, M7
	f := offersFixture(t, []string{testIncome}, map[string][]sourceOffer{testIncome: yearOffers()})
	out := f.materialized(t)
	p := out.Providers[0]
	if len(p.CredentialConfigurations) != 2 {
		t.Fatalf("configurations %+v, want one per offer", p.CredentialConfigurations)
	}
	for i, year := range []string{"2024", "2025"} {
		c := p.CredentialConfigurations[i]
		id := "belastingdienst-inkomensverklaring_" + year
		if c.ID != id || c.Scope != id || c.VCT != testIncome || c.StatusList != "typea" ||
			c.SourceURL != "https://adapter.example/attestations/belastingdienst/typea" || len(c.Claims) != 2 {
			t.Errorf("configuration %+v", c)
		}
		if !slices.Equal(c.Display, []issuerDisplay{{Name: "Inkomensverklaring " + year, Locale: "nl"}}) {
			t.Errorf("display %+v, want the offer's label in Dutch", c.Display)
		}
	}
	want := []issuerDisclosureOffer{
		{Key: "inkomensverklaring_2024", Provider: "belastingdienst", CredentialConfigurationIDs: []string{"belastingdienst-inkomensverklaring_2024"}},
		{Key: "inkomensverklaring_2025", Provider: "belastingdienst", CredentialConfigurationIDs: []string{"belastingdienst-inkomensverklaring_2025"}},
	}
	if !reflect.DeepEqual(out.DisclosureOffers, want) {
		t.Fatalf("disclosure offers %+v, want %+v", out.DisclosureOffers, want)
	}
	// M7: the registration check still names the type's vct, once.
	if err := checkWalletRegistration(filepath.Join(f.options.secretsDir, "belastingdienst"), []string{testIncome}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestATypeWithOneOffer(t *testing.T) { // M3
	f := offersFixture(t, []string{testIncome}, map[string][]sourceOffer{
		testIncome: {{ID: "akte_van_overlijden", Label: "Akte van overlijden", Parameters: map[string]any{}}},
	})
	c := f.materialized(t).Providers[0].CredentialConfigurations
	if len(c) != 1 || c[0].ID != "belastingdienst-akte_van_overlijden" || c[0].Display[0].Name != "Akte van overlijden" {
		t.Fatalf("configurations %+v", c)
	}
}

// M4-M6 cannot reach the materializer: the release refuses a type without
// offers and an offer without a label, and the Source Registry an offer key
// that two types share (M6, source-registry/onboarding registry_test.go).
// The materializer has no fallback for them.
func TestTheReleaseRefusesWhatHasNoConfiguration(t *testing.T) { // M4, M5
	for name, tc := range map[string]struct {
		vcts   []string
		offers map[string][]sourceOffer
		reason string
	}{
		"M4: a type without offers":  {[]string{testIncome, testDeath}, map[string][]sourceOffer{testDeath: {}}, "attestation type is incomplete"},
		"M5: an offer without label": {[]string{testIncome}, map[string][]sourceOffer{testIncome: {{ID: "zonder_label", Parameters: map[string]any{}}}}, "offer id and label are required"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCredentialIssuerFixture(t)
			release := testReleaseWithOffers(t, f.options.secretsDir, time.Now().UTC().Truncate(time.Second), tc.vcts, tc.offers)
			_, err := walletRegistrationSourceFromRelease(release, "belastingdienst")
			if err == nil {
				err = materializeCredentialIssuer(f.options, release)
			}
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("release = %v, want %q", err, tc.reason)
			}
		})
	}
}
