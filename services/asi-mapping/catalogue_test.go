package asimapping

import (
	"strings"
	"testing"
)

func TestCatalogueShipped(t *testing.T) {
	catalogue := loadTestCatalogue(t)
	for _, name := range []string{"family_name", "given_name", "birth_date", "nationality", "address"} {
		id := "https://gbo.example/attributes/" + name + "/1"
		if _, ok := catalogue[id]; !ok {
			t.Errorf("catalogue has no %s", id)
		}
	}
}

func TestParseCatalogueRejects(t *testing.T) {
	cases := []struct {
		name     string
		document string
		reason   string
	}{
		{"C1 keyword outside the subset", `{"attributes": [
			{"identifier": "https://gbo.example/attributes/given_name/1", "value_schema": {
				"type": "object",
				"properties": {"given_name": {"type": "string", "pattern": "^[A-Z]"}},
				"required": ["given_name"]}}]}`, "pattern"},
		{"C2 duplicate identifier", `{"attributes": [
			{"identifier": "https://gbo.example/attributes/given_name/1", "value_schema": {
				"type": "object", "properties": {"given_name": {"type": "string"}}, "required": ["given_name"]}},
			{"identifier": "https://gbo.example/attributes/given_name/1", "value_schema": {
				"type": "object", "properties": {"given_names": {"type": "string"}}, "required": ["given_names"]}}]}`, "duplicate"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseCatalogue([]byte(test.document))
			if err == nil {
				t.Fatal("ParseCatalogue() accepted the catalogue")
			}
			if !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("ParseCatalogue() error = %q, want it to name %q", err, test.reason)
			}
		})
	}
}
