package dataservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dataservices.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const brp = `{"id":"brp","provider":"GBO demo ASIP","authentic_source":"BRP","country":"NL","source":"rvig","grant_path":"/brp/graphql",
	"attributes":["https://gbo.example/attributes/family_name/1"]}`

func TestTheShippedFileLoads(t *testing.T) {
	ds, err := Load(filepath.Join("..", "..", "config", "dataservices.json"))
	if err != nil || len(ds) != 1 || ds[0].ID != "brp" || len(ds[0].Attributes) != 5 || ds[0].AuthenticSource == "" {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

func TestLoad(t *testing.T) {
	ds, err := Load(write(t, `{"data_services":[`+brp+`,`+strings.Replace(brp, `"id":"brp"`, `"id":"brp-2"`, 1)+`]}`))
	if err != nil || len(ds) != 2 || ds[1].ID != "brp-2" || ds[0].Source != "rvig" || ds[0].Attributes[0] != "https://gbo.example/attributes/family_name/1" {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

// A file the ASIP cannot serve stops it at start (B8).
func TestRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no data services":     `{"data_services":[]}`,
		"twice":                `{"data_services":[` + brp + `,` + brp + `]}`,
		"no source":            `{"data_services":[` + strings.Replace(brp, `"source":"rvig",`, ``, 1) + `]}`,
		"attribute twice":      `{"data_services":[` + strings.Replace(brp, `["https://gbo.example/attributes/family_name/1"]`, `["https://gbo.example/attributes/family_name/1","https://gbo.example/attributes/family_name/1"]`, 1) + `]}`,
		"identifier not a URI": `{"data_services":[` + strings.Replace(brp, `"https://gbo.example/attributes/family_name/1"`, `"family_name"`, 1) + `]}`,
		"id not a segment":     `{"data_services":[` + strings.Replace(brp, `"id":"brp"`, `"id":"brp/x"`, 1) + `]}`,
		"no provider":          `{"data_services":[` + strings.Replace(brp, `"provider":"GBO demo ASIP"`, `"provider":""`, 1) + `]}`,
		"no country":           `{"data_services":[` + strings.Replace(brp, `"country":"NL",`, ``, 1) + `]}`,
		"country not a code":   `{"data_services":[` + strings.Replace(brp, `"country":"NL"`, `"country":"Nederland"`, 1) + `]}`,
		"no grant path":        `{"data_services":[` + strings.Replace(brp, `"grant_path":"/brp/graphql"`, `"grant_path":""`, 1) + `]}`,
		"no attributes":        `{"data_services":[` + strings.Replace(brp, `["https://gbo.example/attributes/family_name/1"]`, `[]`, 1) + `]}`,
		"not JSON":             `{`,
		"misspelt field":       `{"data_services":[` + strings.Replace(brp, `"authentic_source"`, `"authentic_sorce"`, 1) + `]}`,
		"unknown top-level":    `{"dataservices":[` + brp + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("err = nil, want the file refused")
			}
		})
	}
}
