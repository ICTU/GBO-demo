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

// The register the demo actually ships must load, and every activity a
// producer can name has to be in it. A typo here would only surface as a
// rejected write at runtime.
func TestShippedBelastingdienstRegisterLoads(t *testing.T) {
	register, err := LoadRegister("../../config/verwerkingsactiviteiten-bd.json")
	if err != nil {
		t.Fatalf("load shipped BD register: %v", err)
	}
	if register.Verantwoordelijke != "Belastingdienst" {
		t.Fatalf("verantwoordelijke = %q", register.Verantwoordelijke)
	}
	if !strings.Contains(strings.ToLower(register.Disclaimer), "geen rvva") {
		t.Fatal("the register must say in so many words that it is not an RvVA")
	}
	for _, reference := range []string{
		"bd-pi-bsn-resolutie@v1",
		"bd-bronquery-doorgifte@v1",
		"bd-ib-2025@v1",
		"bd-ib-2024@v1",
	} {
		if _, found := register.Resolve(reference); !found {
			t.Errorf("%s is referenced by the instrumented components but missing from the register", reference)
		}
	}
}
