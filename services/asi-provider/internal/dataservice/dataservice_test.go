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

const brp = `{"id":"brp","provider":"GBO demo ASIP","source":"rvig","grant_path":"/brp/graphql"}`

func TestTheShippedFileLoads(t *testing.T) {
	ds, err := Load(filepath.Join("..", "..", "config", "dataservices.json"))
	if err != nil || len(ds) != 1 || ds[0].ID != "brp" || ds[0].Source != "rvig" {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

func TestLoad(t *testing.T) {
	ds, err := Load(write(t, `{"data_services":[`+brp+`,`+strings.Replace(brp, `"id":"brp"`, `"id":"brp-2"`, 1)+`]}`))
	if err != nil || len(ds) != 2 || ds[1].ID != "brp-2" || ds[0].Source != "rvig" || ds[0].GrantPath != "/brp/graphql" {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

// A file the ASIP cannot serve stops it at start (B8). So does one that
// still names what now comes from the source's description: its authentic
// source, country or attributes (E7).
func TestRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no data services":    `{"data_services":[]}`,
		"twice":               `{"data_services":[` + brp + `,` + brp + `]}`,
		"no source":           `{"data_services":[` + strings.Replace(brp, `"source":"rvig",`, ``, 1) + `]}`,
		"id not a segment":    `{"data_services":[` + strings.Replace(brp, `"id":"brp"`, `"id":"brp/x"`, 1) + `]}`,
		"no provider":         `{"data_services":[` + strings.Replace(brp, `"provider":"GBO demo ASIP"`, `"provider":""`, 1) + `]}`,
		"no grant path":       `{"data_services":[` + strings.Replace(brp, `"grant_path":"/brp/graphql"`, `"grant_path":""`, 1) + `]}`,
		"not JSON":            `{`,
		"misspelt field":      `{"data_services":[` + strings.Replace(brp, `"grant_path"`, `"grant_pad"`, 1) + `]}`,
		"E7 authentic_source": `{"data_services":[` + strings.Replace(brp, `"source":`, `"authentic_source":"BRP","source":`, 1) + `]}`,
		"E7 country":          `{"data_services":[` + strings.Replace(brp, `"source":`, `"country":"NL","source":`, 1) + `]}`,
		"E7 attributes":       `{"data_services":[` + strings.Replace(brp, `"source":`, `"attributes":["https://gbo.example/attributes/family_name/1"],"source":`, 1) + `]}`,
		"unknown top-level":   `{"dataservices":[` + brp + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("err = nil, want the file refused")
			}
		})
	}
}
