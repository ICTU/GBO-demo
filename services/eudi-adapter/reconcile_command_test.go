package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReconcileCommandRequiresExplicitOnceOrWatchMode(t *testing.T) {
	base := []string{
		"--consumer-peer-id", "0000009961MINEZK0000",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--sources-dir", t.TempDir(),
	}
	if _, err := parseReconcileOptions(base, io.Discard); err == nil {
		t.Fatal("reconcile mode was optional")
	}
	options, err := parseReconcileOptions(append(base, "--once"), io.Discard)
	if err != nil {
		t.Fatalf("parse --once: %v", err)
	}
	if !options.once || options.watch {
		t.Fatalf("options = %+v", options)
	}
	if _, err := parseReconcileOptions(append(base, "--once", "--watch"), io.Discard); err == nil {
		t.Fatal("mutually exclusive reconcile modes were accepted")
	}
}

func TestReconcileCommandRequiresConsumerPeerID(t *testing.T) {
	t.Setenv("FSC_CONSUMER_PEER_ID", "")
	_, err := parseReconcileOptions([]string{
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--sources-dir", t.TempDir(),
		"--once",
	}, io.Discard)
	if err == nil {
		t.Fatal("missing consumer Peer ID was accepted")
	}
}

func TestReconcileCommandAcceptsAlphanumericConsumerPeerID(t *testing.T) {
	options, err := parseReconcileOptions([]string{
		"--consumer-peer-id", "0000009961MINEZK0000",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--sources-dir", t.TempDir(),
		"--once",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.consumerPeerID != "0000009961MINEZK0000" {
		t.Fatalf("consumer peer ID = %q", options.consumerPeerID)
	}
}

func TestReconcileCommandRequiresAutoPromoteProducts(t *testing.T) {
	base := []string{
		"--consumer-peer-id", "0000009961MINEZK0000",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://adapter.example",
		"--sources-dir", t.TempDir(), "--once", "--auto-promote",
	}
	if _, err := parseReconcileOptions(base, io.Discard); err == nil {
		t.Fatal("auto-promote without output products was accepted")
	}
	options, err := parseReconcileOptions(append(base,
		"--issuance-template", "/config/issuance_server.toml.example",
		"--issuance-output", "/runtime/issuance_server.toml",
		"--offers-output", "/runtime/eudi-offers.json",
	), io.Discard)
	if err != nil {
		t.Fatalf("parse auto-promote: %v", err)
	}
	if !options.autoPromote {
		t.Fatal("auto-promote option was not retained")
	}
}

func TestHasRolloutRequiredStatus(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "belastingdienst.json"), []byte(`{"source_id":"belastingdienst","state":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	required, err := hasRolloutRequiredStatus(directory)
	if err != nil || required {
		t.Fatalf("active status: required=%v err=%v", required, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "rvig.json"), []byte(`{"source_id":"rvig","state":"rollout_required"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	required, err = hasRolloutRequiredStatus(directory)
	if err != nil || !required {
		t.Fatalf("rollout-required status: required=%v err=%v", required, err)
	}
}

func TestAutoPromoteReconciledSourcesGeneratesProductsAndActivates(t *testing.T) {
	root := t.TempDir()
	writeTestActivation(t, filepath.Join(root, "candidates"), root, "demo", "99999999900000000900", "example", "https://adapter.example/types/demo/example/v1.0", "demo", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	writeTestFile(t, filepath.Join(root, "sources", "demo.yaml"), []byte(`metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
`))
	status, err := json.Marshal(sourceReconcileStatus{SourceID: "demo", State: sourceStateRolloutRequired})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "status", "demo.json"), status)
	template := filepath.Join(root, "issuance.toml.example")
	writeTestFile(t, template, []byte(`public_url = "${EUDI_PUBLIC_URL}"
issuer_trust_anchors = [${EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR}]
reader_trust_anchors = [${EUDI_ONBOARDING_READER_TRUST_ANCHOR}]
metadata = [{{TYPE_METADATA_FILES}}]
# GBO_GENERATOR_ADAPTER_trust_anchors = ["adapter-ca"]
{{GENERATED_ISSUANCE_SETTINGS}}
`))
	t.Setenv("EUDI_PUBLIC_URL", "https://issuer.example")
	output := filepath.Join(root, "runtime", "issuance_server.toml")
	offers := filepath.Join(root, "runtime", "eudi-offers.json")
	promoted, err := autoPromoteReconciledSources(reconcileOptions{
		stateDir: root, sourcesDir: filepath.Join(root, "sources"), publicBaseURL: "https://adapter.example",
		issuanceTemplate: template, issuanceOutput: output, offersOutput: offers,
	})
	if err != nil {
		t.Fatalf("auto-promote: %v", err)
	}
	if !promoted {
		t.Fatal("rollout-required source was not promoted")
	}
	for _, path := range []string{output, offers, filepath.Join(root, "active", "demo.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("generated product %q: %v", path, err)
		}
	}
}
