package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSourceConfigurations(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata
`)

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load configurations: %v", err)
	}
	if len(configurations) != 1 || configurations[0].SourceID != "belastingdienst" || configurations[0].MetadataEndpoint.ProviderPeerID != "0000009958MINBZK0000" {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func TestSourceCatalogDerivesRuntimeDefaultsFromMinimalConfiguration(t *testing.T) {
	catalog := sourceCatalogAdapter{sources: []sourceConfiguration{{
		SourceID: "belastingdienst",
		MetadataEndpoint: configuredSourceMetadataEndpoint{
			Transport: sourceTransportFSC, ProviderPeerID: "0000009958MINBZK0000", ServiceReference: "gbo-metadata-bd",
		},
	}}}
	sources, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %+v", sources)
	}
	source := sources[0]
	if source.OIN != "" || source.Name != "" {
		t.Fatalf("certificate-owned fields = %+v", source)
	}
	if source.MetadataEndpoint.Path != sourceMetadataWellKnownPath || source.DataAccessTransport != source.MetadataEndpoint.Transport {
		t.Fatalf("derived transport fields = %+v", source)
	}
}

func TestSourceConfigurationRejectsRemovedIdentityAndRuntimeFields(t *testing.T) {
	for _, field := range []string{
		`source_id: configured-value-is-not-allowed`,
		`name: Belastingdienst`,
		`source_oin: "99999999900000000200"`,
		`certificate_set: belastingdienst`,
		`data_access: {transport: fsc}`,
	} {
		directory := t.TempDir()
		writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
`+field+`
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata
`)
		if _, err := loadSourceConfigurations(directory); err == nil {
			t.Fatalf("removed field %q was accepted", field)
		}
	}
}

func TestLoadSourceConfigurationsFromGroupedDirectories(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, filepath.Join(directory, "configured"), "belastingdienst.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata
`)
	writeTestSourceConfiguration(t, filepath.Join(directory, "local-demo"), "demo.yaml", `
metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
`)

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load grouped configurations: %v", err)
	}
	if len(configurations) != 2 || configurations[0].SourceID != "belastingdienst" || configurations[1].SourceID != "demo" {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func TestLoadSourceConfigurationsIgnoresKubernetesProjectionDirectories(t *testing.T) {
	directory := t.TempDir()
	projectionDirectory := filepath.Join(directory, "..2026_08_19_12_32_38.100056535")
	writeTestSourceConfiguration(t, projectionDirectory, "belastingdienst.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata-bd
`)
	writeTestSourceConfiguration(t, projectionDirectory, "rvig.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata-rvig
`)
	if err := os.Symlink(filepath.Base(projectionDirectory), filepath.Join(directory, "..data")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"belastingdienst.yaml", "rvig.yaml"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load Kubernetes-projected configurations: %v", err)
	}
	if len(configurations) != 2 || configurations[0].SourceID != "belastingdienst" || configurations[1].SourceID != "rvig" {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func TestLoadSourceConfigurationsRejectsDuplicateSourceID(t *testing.T) {
	directory := t.TempDir()
	for _, group := range []string{"configured", "local-demo"} {
		writeTestSourceConfiguration(t, filepath.Join(directory, group), "belastingdienst.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata
`)
	}

	_, err := loadSourceConfigurations(directory)
	if err == nil || !strings.Contains(err.Error(), "source_id") {
		t.Fatalf("load error = %v", err)
	}
}

func TestLoadSourceConfigurationsAllowsLogicalSourcesUnderOneProvider(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata-bd
`)
	writeTestSourceConfiguration(t, directory, "rvig.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata-rvig
`)

	configurations, err := loadSourceConfigurations(directory)
	if err != nil {
		t.Fatalf("load shared-OID configurations: %v", err)
	}
	if len(configurations) != 2 || configurations[0].MetadataEndpoint.ProviderPeerID != configurations[1].MetadataEndpoint.ProviderPeerID {
		t.Fatalf("configurations = %+v", configurations)
	}
}

func TestSourceConfigurationRejectsResolvedGrantHashes(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "belastingdienst.yaml", `
metadata_endpoint:
  transport: fsc
  provider_peer_id: "0000009958MINBZK0000"
  service_reference: gbo-metadata
  grant_hash: operator-must-not-pin-this
`)

	_, err := loadSourceConfigurations(directory)
	if err == nil || !strings.Contains(err.Error(), "grant_hash") {
		t.Fatalf("load error = %v", err)
	}
}

func TestSourceConfigurationsAcceptUnsecuredHTTPSource(t *testing.T) {
	directory := t.TempDir()
	writeTestSourceConfiguration(t, directory, "demo.yaml", `
metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
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
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
