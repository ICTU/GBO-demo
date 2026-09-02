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
	activity, found := register.Resolve("bd-ib-2025@v1")
	if !found {
		t.Fatal("bd-ib-2025@v1 should resolve")
	}
	if activity.ScopeID != "bd:ib:2025" {
		t.Fatalf("scope_id = %q, want bd:ib:2025", activity.ScopeID)
	}
	if _, found := register.Resolve("bd-ib-2025@v2"); found {
		t.Fatal("a different version must not resolve to the v1 entry")
	}
	if got := register.References(); len(got) != 2 || got[0] != "bd-ib-2024@v1" {
		t.Fatalf("References() = %v, want a sorted list of two", got)
	}
}

func TestLoadRegisterRejectsUnusableDocuments(t *testing.T) {
	cases := map[string]string{
		"no verantwoordelijke": `{"activities": [{"id": "a", "version": "v1", "name": "n", "doel": "d"}]}`,
		"no activities":        `{"verantwoordelijke": "BD", "activities": []}`,
		"unversioned entry":    `{"verantwoordelijke": "BD", "activities": [{"id": "a", "version": "1", "name": "n", "doel": "d"}]}`,
		"uppercase id":         `{"verantwoordelijke": "BD", "activities": [{"id": "BD", "version": "v1", "name": "n", "doel": "d"}]}`,
		"missing doel":         `{"verantwoordelijke": "BD", "activities": [{"id": "a", "version": "v1", "name": "n"}]}`,
		"duplicate reference": `{"verantwoordelijke": "BD", "activities": [
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
				"bd-pi-bsn-resolutie@v1",
				"bd-bronquery-doorgifte@v1",
				"bd-ib-2025@v1",
				"bd-ib-2024@v1",
			},
		},
		"RvIG": {
			path:              "../../config/verwerkingsactiviteiten-brp.json",
			verantwoordelijke: "RvIG",
			references: []string{
				"brp-pi-bsn-resolutie@v1",
				"brp-bronquery-doorgifte@v1",
				"brp-akte-overlijden@v1",
				"brp-persoonsgegevens-verstrekking@v1",
			},
		},
		"GBO": {
			path:              "../../config/verwerkingsactiviteiten-gbo.json",
			verantwoordelijke: "GBO",
			references: []string{
				"gbo-toestemming-verlenen@v1",
				"gbo-toestemming-intrekken@v1",
				"gbo-toestemming-status@v1",
				"gbo-toestemming-inzage@v1",
				"gbo-bsn-pseudonimisering@v1",
				"gbo-pid-bsn-extractie@v1",
				"gbo-attestatie-samenstellen@v1",
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
			if got, want := len(register.References()), len(testCase.references); got != want {
				t.Errorf("register holds %d entries, the components name %d: %v", got, want, register.References())
			}
		})
	}
}
