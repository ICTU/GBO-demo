package onboarding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onboarding.json")
	body := `{
  "source_holders": [{"peer_id":"0000009958MINBZK0000","name":"MinBZK"}],
  "system_participants": [{
    "peer_id":"0000009961MINEZK0000",
    "name":"EUDI issuer",
    "active":true,
    "allowed_source_peer_ids":["0000009958MINBZK0000"]
  }]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.SourceHolders) != 1 || len(configuration.SystemParticipants) != 1 {
		t.Fatalf("configuration = %+v", configuration)
	}
}

func TestLoadConfigurationRejectsUnknownAndTrailingData(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"source_holders":[],"source_oins":[]}`,
		"trailing data": `{"source_holders":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "onboarding.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfiguration(path)
			if err == nil || !strings.Contains(err.Error(), "decode onboarding configuration") {
				t.Fatalf("LoadConfiguration error = %v", err)
			}
		})
	}
}

func TestLoadConfigurationRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onboarding.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", maximumConfigurationSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfiguration(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadConfiguration error = %v", err)
	}
}
