package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSourceConfigurations(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
source_id: belastingdienst
source_oin: "99999999900000000200"
name: Belastingdienst
certificate_set: belastingdienst
metadata_endpoint:
  transport: fsc
  service_reference: gbo-metadata
  path: /.well-known/gbo
data_access:
  transport: fsc
`)

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load configurations: %v", err)
	}
	if len(configurations) != 1 || configurations[0].SourceID != "belastingdienst" || configurations[0].CertificateSet != "belastingdienst" {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func TestLoadSourceConfigurationsRejectsDuplicateSourceID(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"one.yaml", "two.yaml"} {
		writeTestSourceConfiguration(t, directory, name, `
source_id: belastingdienst
source_oin: "99999999900000000200"
name: Belastingdienst
certificate_set: belastingdienst
metadata_endpoint:
  transport: fsc
  service_reference: gbo-metadata
  path: /.well-known/gbo
data_access:
  transport: fsc
`)
	}

	_, err := loadSourceConfigurations(directory)
	if err == nil || !strings.Contains(err.Error(), "source_id") {
		t.Fatalf("load error = %v", err)
	}
}

func TestLoadSourceConfigurationsAllowsLogicalSourcesUnderOneOIN(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
source_id: belastingdienst
source_oin: "99999999900000000200"
name: Belastingdienst
certificate_set: belastingdienst
metadata_endpoint:
  transport: fsc
  service_reference: gbo-metadata-bd
  path: /.well-known/gbo
data_access:
  transport: fsc
`)
	writeTestSourceConfiguration(t, directory, "rvig.yaml", `
source_id: rvig
source_oin: "99999999900000000200"
name: RvIG
certificate_set: rvig
metadata_endpoint:
  transport: fsc
  service_reference: gbo-metadata-rvig
  path: /.well-known/gbo
data_access:
  transport: fsc
`)

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load shared-OID configurations: %v", err)
	}
	if len(configurations) != 2 || configurations[0].SourceOIN != configurations[1].SourceOIN {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func TestSourceConfigurationRejectsResolvedGrantHashes(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
source_id: belastingdienst
source_oin: "99999999900000000200"
name: Belastingdienst
certificate_set: belastingdienst
metadata_endpoint:
  transport: fsc
  service_reference: gbo-metadata
  path: /.well-known/gbo
  grant_hash: operator-must-not-pin-this
data_access:
  transport: fsc
`)

	_, err := loadSourceConfigurations(directory)
	if err == nil || !strings.Contains(err.Error(), "grant_hash") {
		t.Fatalf("load error = %v", err)
	}
}

func TestSourceConfigurationsAcceptUnsecuredHTTPSource(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "demo.yaml", `
source_id: demo
source_oin: "99999999900000000900"
name: Demo source
certificate_set: demo
metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
data_access:
  transport: unsecured
`)

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load unsecured configuration: %v", err)
	}
	if len(configurations) != 1 || configurations[0].MetadataEndpoint.Endpoint != "http://demo-source:4000/.well-known/gbo" {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func writeTestSourceConfiguration(t *testing.T, directory, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
