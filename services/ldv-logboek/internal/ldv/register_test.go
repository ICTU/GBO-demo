package ldv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegister(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "verwerkingsactiviteiten.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write register: %v", err)
	}
	return path
}

const minimalRegister = `{
  "verantwoordelijke": "Belastingdienst",
  "base_uri": "https://logboek.belastingdienst.nl/verwerkingsactiviteiten",
  "disclaimer": "demo",
  "activities": [
    {"id": "bd-ib-2025", "version": "v1", "name": "Verstrekken IB 2025", "doel": "demo", "scope_id": "bd:ib:2025"},
    {"id": "bd-ib-2024", "version": "v1", "name": "Verstrekken IB 2024", "doel": "demo", "scope_id": "bd:ib:2024"}
  ]
}`

func TestLoadRegisterIndexesByVersionedReference(t *testing.T) {
	register, err := LoadRegister(writeRegister(t, minimalRegister))
	if err != nil {
		t.Fatalf("load register: %v", err)
	}
	activity, found := register.Resolve("https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2025/v1")
	if !found {
		t.Fatal("the 2025 activity should resolve by its URI")
	}
	if activity.ScopeID != "bd:ib:2025" {
		t.Fatalf("scope_id = %q, want bd:ib:2025", activity.ScopeID)
	}
	if _, found := register.Resolve("https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2025/v2"); found {
		t.Fatal("a different version must not resolve to the v1 entry")
	}
	if got := register.URIs(); len(got) != 2 || got[0] != "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2024/v1" {
		t.Fatalf("URIs() = %v, want a sorted list of two", got)
	}
}

func TestLoadRegisterRejectsUnusableDocuments(t *testing.T) {
	cases := map[string]string{
		"no verantwoordelijke": `{"base_uri": "https://l.test/v", "activities": [{"id": "a", "version": "v1", "name": "n", "doel": "d"}]}`,
		"no base_uri":          `{"verantwoordelijke": "BD", "activities": [{"id": "a", "version": "v1", "name": "n", "doel": "d"}]}`,
		"relative base_uri":    `{"verantwoordelijke": "BD", "base_uri": "/verwerkingsactiviteiten", "activities": [{"id": "a", "version": "v1", "name": "n", "doel": "d"}]}`,
		"no activities":        `{"verantwoordelijke": "BD", "base_uri": "https://l.test/v", "activities": []}`,
		"unversioned entry":    `{"verantwoordelijke": "BD", "base_uri": "https://l.test/v", "activities": [{"id": "a", "version": "1", "name": "n", "doel": "d"}]}`,
		"uppercase id":         `{"verantwoordelijke": "BD", "base_uri": "https://l.test/v", "activities": [{"id": "BD", "version": "v1", "name": "n", "doel": "d"}]}`,
		"missing doel":         `{"verantwoordelijke": "BD", "base_uri": "https://l.test/v", "activities": [{"id": "a", "version": "v1", "name": "n"}]}`,
		"duplicate reference": `{"verantwoordelijke": "BD", "base_uri": "https://l.test/v", "activities": [
			{"id": "a", "version": "v1", "name": "n", "doel": "d"},
			{"id": "a", "version": "v1", "name": "other", "doel": "d"}
		]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadRegister(writeRegister(t, body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// The registers the demo actually ships must load, and every activity a
// producer can name has to be in one of them. A typo here would only surface
// as a rejected write at runtime, in whichever flow happens to hit it.
func TestShippedRegistersLoad(t *testing.T) {
	cases := map[string]struct {
		path              string
		verantwoordelijke string
		references        []string
	}{
		"Belastingdienst": {
			path:              "../../config/verwerkingsactiviteiten-bd.json",
			verantwoordelijke: "Belastingdienst",
			references: []string{
				"https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-pi-bsn-resolutie/v1",
				"https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-bronquery-doorgifte/v1",
				"https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2025/v1",
				"https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2024/v1",
			},
		},
		"RvIG": {
			path:              "../../config/verwerkingsactiviteiten-brp.json",
			verantwoordelijke: "RvIG",
			references: []string{
				"https://logboek.rvig.nl/verwerkingsactiviteiten/brp-pi-bsn-resolutie/v1",
				"https://logboek.rvig.nl/verwerkingsactiviteiten/brp-bronquery-doorgifte/v1",
				"https://logboek.rvig.nl/verwerkingsactiviteiten/brp-akte-overlijden/v1",
				"https://logboek.rvig.nl/verwerkingsactiviteiten/brp-persoonsgegevens-verstrekking/v1",
			},
		},
		"GBO": {
			path:              "../../config/verwerkingsactiviteiten-gbo.json",
			verantwoordelijke: "GBO",
			references: []string{
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-verlenen/v1",
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-intrekken/v1",
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-status/v1",
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-inzage/v1",
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-bsn-pseudonimisering/v1",
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-pid-bsn-extractie/v1",
				"https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-attestatie-samenstellen/v1",
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			register, err := LoadRegister(testCase.path)
			if err != nil {
				t.Fatalf("load shipped register: %v", err)
			}
			if register.Verantwoordelijke != testCase.verantwoordelijke {
				t.Fatalf("verantwoordelijke = %q, want %q", register.Verantwoordelijke, testCase.verantwoordelijke)
			}
			if !strings.Contains(strings.ToLower(register.Disclaimer), "geen rvva") {
				t.Error("the register must say in so many words that it is not an RvVA")
			}
			for _, reference := range testCase.references {
				if _, found := register.Resolve(reference); !found {
					t.Errorf("%s is referenced by an instrumented component but missing from the register", reference)
				}
			}
			// Nothing beyond what the components name: an entry nobody writes
			// is a register that has drifted away from the chain.
			if got, want := len(register.URIs()), len(testCase.references); got != want {
				t.Errorf("register holds %d entries, the components name %d: %v", got, want, register.URIs())
			}
		})
	}
}
