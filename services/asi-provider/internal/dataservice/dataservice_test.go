package dataservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gbo-demo/asi-provider/internal/brpsource"
)

var adapters = map[string][]string{"brp": {"address", "family_name"}}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dataservices.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const brp = `{"id":"brp","provider":"GBO demo ASIP","authentic_source":"BRP","source":"brp","grant_path":"/brp/graphql",
	"attributes":{"https://gbo.example/attributes/family_name/1":"family_name"}}`

// The file in config/ loads with the adapters of the ASIP's composition root.
func TestTheShippedFileLoads(t *testing.T) {
	ds, err := Load(filepath.Join("..", "..", "config", "dataservices.json"), map[string][]string{"brp": brpsource.Attributes()})
	if err != nil || len(ds) != 1 || ds[0].ID != "brp" || len(ds[0].Attributes) != 5 || ds[0].AuthenticSource == "" {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

func TestLoad(t *testing.T) {
	ds, err := Load(write(t, `{"data_services":[`+brp+`,`+strings.Replace(brp, `"id":"brp"`, `"id":"brp-2"`, 1)+`]}`), adapters)
	if err != nil || len(ds) != 2 || ds[1].ID != "brp-2" || ds[0].Attributes["https://gbo.example/attributes/family_name/1"] != "family_name" {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

// A file the ASIP cannot serve stops it at start (B8).
func TestRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no data services":      `{"data_services":[]}`,
		"twice":                 `{"data_services":[` + brp + `,` + brp + `]}`,
		"unknown adapter":       `{"data_services":[` + strings.Replace(brp, `"source":"brp"`, `"source":"duo"`, 1) + `]}`,
		"attribute not adapted": `{"data_services":[` + strings.Replace(brp, `:"family_name"`, `:"diploma"`, 1) + `]}`,
		"identifier not a URI":  `{"data_services":[` + strings.Replace(brp, `"https://gbo.example/attributes/family_name/1"`, `"family_name"`, 1) + `]}`,
		"id not a segment":      `{"data_services":[` + strings.Replace(brp, `"id":"brp"`, `"id":"brp/x"`, 1) + `]}`,
		"no provider":           `{"data_services":[` + strings.Replace(brp, `"provider":"GBO demo ASIP"`, `"provider":""`, 1) + `]}`,
		"no grant path":         `{"data_services":[` + strings.Replace(brp, `"grant_path":"/brp/graphql"`, `"grant_path":""`, 1) + `]}`,
		"no attributes":         `{"data_services":[` + strings.Replace(brp, `{"https://gbo.example/attributes/family_name/1":"family_name"}`, `{}`, 1) + `]}`,
		"not JSON":              `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body), adapters); err == nil {
				t.Fatal("err = nil, want the file refused")
			}
		})
	}
}
