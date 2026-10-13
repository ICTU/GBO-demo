package testpersons

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// S11: every test person has a BSN and the five mandatory PID attributes;
// otherwise the authorization server does not start.
func TestLoad(t *testing.T) {
	s, err := Load(filepath.Join("..", "..", "testdata", "test-persons.json"))
	if err != nil || s.Len() != 4 {
		t.Fatalf("Load = %d persons, %v; want the four test persons", s.Len(), err)
	}
	id, ok := s.Identify("frouke")
	if !ok || id.BSN != "999991772" || id.Birthdate != "2000-03-24" || id.PlaceOfBirth.Locality != "Rotterdam" || len(id.Nationalities) != 2 {
		t.Fatalf("frouke = %+v, want her identity", id)
	}
	valid := func() map[string]any {
		return map[string]any{"id": "p", "bsn": "1", "family_name": "F", "given_name": "G", "birthdate": "2000-01-02",
			"place_of_birth": map[string]string{"country": "NL"}, "nationalities": []string{"NL"}}
	}
	for name, edit := range map[string]func(map[string]any){
		"no id":                       func(p map[string]any) { delete(p, "id") },
		"no bsn":                      func(p map[string]any) { delete(p, "bsn") },
		"no family_name":              func(p map[string]any) { delete(p, "family_name") },
		"no given_name":               func(p map[string]any) { delete(p, "given_name") },
		"no birthdate":                func(p map[string]any) { delete(p, "birthdate") },
		"birthdate not a date":        func(p map[string]any) { p["birthdate"] = "02-01-2000" },
		"no place_of_birth":           func(p map[string]any) { delete(p, "place_of_birth") },
		"place_of_birth country code": func(p map[string]any) { p["place_of_birth"] = map[string]string{"country": "Nederland"} },
		"no nationalities":            func(p map[string]any) { delete(p, "nationalities") },
		"nationality not a code":      func(p map[string]any) { p["nationalities"] = []string{"nl"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid()
			edit(p)
			raw, _ := json.Marshal(map[string]any{"persons": []any{p}})
			path := filepath.Join(t.TempDir(), "persons.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("the test person was accepted")
			}
		})
	}
	raw, _ := json.Marshal(map[string]any{"persons": []any{valid()}})
	path := filepath.Join(t.TempDir(), "persons.json")
	_ = os.WriteFile(path, raw, 0o600)
	if _, err := Load(path); err != nil {
		t.Fatalf("a valid test person was refused: %v", err)
	}
}
