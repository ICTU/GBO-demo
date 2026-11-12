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

func writeConfiguration(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "onboarding.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const serviceProviderConfigurationJSON = `{
  "source_holders": [{"peer_id":"0000009958MINBZK0000","name":"MinBZK"}],
  "service_providers": [{
    "peer_id":"0000009950HYPBV00000",
    "return_locations":["${TEST_PROVIDER_URL}/return", "https://fixed.example/${TEST_PROVIDER_PATH}"],
    "services":[{"service":"aangiftegegevens","purpose":"Hypotheek-aanvraag"}]
  }]
}`

func TestLoadConfigurationExpandsReturnLocations(t *testing.T) {
	t.Setenv("TEST_PROVIDER_URL", "https://hypotheek.example")
	t.Setenv("TEST_PROVIDER_PATH", "terug")
	configuration, err := LoadConfiguration(writeConfiguration(t, serviceProviderConfigurationJSON))
	if err != nil {
		t.Fatal(err)
	}
	got := configuration.ServiceProviders[0].ReturnLocations
	if len(got) != 2 || got[0] != "https://hypotheek.example/return" || got[1] != "https://fixed.example/terug" {
		t.Fatalf("return locations = %q", got)
	}
}

func TestLoadConfigurationRejectsUnsetVariable(t *testing.T) {
	t.Setenv("TEST_PROVIDER_URL", "https://hypotheek.example")
	t.Setenv("TEST_PROVIDER_PATH", "")
	_, err := LoadConfiguration(writeConfiguration(t, serviceProviderConfigurationJSON))
	if err == nil || !strings.Contains(err.Error(), "TEST_PROVIDER_PATH") {
		t.Fatalf("LoadConfiguration error = %v", err)
	}
	if err := os.Unsetenv("TEST_PROVIDER_PATH"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfiguration(writeConfiguration(t, serviceProviderConfigurationJSON)); err == nil || !strings.Contains(err.Error(), "TEST_PROVIDER_PATH") {
		t.Fatalf("LoadConfiguration error = %v", err)
	}
}

func TestLoadConfigurationExpandsOnlyReturnLocations(t *testing.T) {
	t.Setenv("TEST_PROVIDER_URL", "https://hypotheek.example")
	t.Setenv("TEST_PROVIDER_PATH", "terug")
	t.Setenv("TEST_NAME", "expanded")
	body := strings.Replace(serviceProviderConfigurationJSON, `"name":"MinBZK"`, `"name":"${TEST_NAME}"`, 1)
	configuration, err := LoadConfiguration(writeConfiguration(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.SourceHolders[0].Name != "${TEST_NAME}" {
		t.Fatalf("source holder name = %q", configuration.SourceHolders[0].Name)
	}
}

func loadAndValidate(t *testing.T, path string) Configuration {
	t.Helper()
	configuration, err := LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(newMemoryRepository(), configuration); err != nil {
		t.Fatal(err)
	}
	return configuration
}

func TestDemoConfigurationLoads(t *testing.T) {
	t.Setenv("DIENSTVERLENER_PUBLIC_URL", "http://localhost:9001")
	t.Setenv("KEYPER_PUBLIC_URL", "http://localhost:9004")
	configuration := loadAndValidate(t, filepath.Join("..", "..", "config", "demo.json"))
	if len(configuration.ServiceProviders) == 0 {
		t.Fatal("demo configuration registers no service providers")
	}
}

func TestExampleConfigMapLoads(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "helm", "gbo-app", "examples", "dvtp-onboarding-configmap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(manifest), "onboarding.json: |\n")
	if !found {
		t.Fatal("example ConfigMap has no onboarding.json")
	}
	var body strings.Builder
	for _, line := range strings.Split(block, "\n") {
		body.WriteString(strings.TrimPrefix(line, "    ") + "\n")
	}
	loadAndValidate(t, writeConfiguration(t, body.String()))
}
