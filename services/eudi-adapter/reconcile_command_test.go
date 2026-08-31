package main

import (
	"io"
	"testing"
)

func TestReconcileCommandRequiresExplicitOnceOrWatchMode(t *testing.T) {
	base := []string{
		"--consumer-peer-id", "0000009961MINEZK0000",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--sources-dir", t.TempDir(),
		"--database-url", "postgres://registry.example/source_registry",
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
		"--database-url", "postgres://registry.example/source_registry",
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
		"--database-url", "postgres://registry.example/source_registry",
		"--database-reader-role", "source_registry_reader",
		"--migrate",
		"--once",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.consumerPeerID != "0000009961MINEZK0000" {
		t.Fatalf("consumer peer ID = %q", options.consumerPeerID)
	}
	if !options.migrate || options.databaseReaderRole != "source_registry_reader" {
		t.Fatalf("migration options were not parsed: %+v", options)
	}
}

func TestReconcileCommandRejectsFilesystemState(t *testing.T) {
	_, err := parseReconcileOptions([]string{
		"--consumer-peer-id", "0000009961MINEZK0000",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--sources-dir", t.TempDir(),
		"--database-url", "postgres://registry.example/source_registry",
		"--storage-backend", "filesystem",
		"--once",
	}, io.Discard)
	if err == nil {
		t.Fatal("filesystem onboarding state was accepted after the hard cutover")
	}
}

func TestReconcileCommandAcceptsRegistryAutoPromote(t *testing.T) {
	base := []string{
		"--consumer-peer-id", "0000009961MINEZK0000",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://adapter.example",
		"--sources-dir", t.TempDir(),
		"--database-url", "postgres://registry.example/source_registry",
		"--once", "--auto-promote",
	}
	options, err := parseReconcileOptions(base, io.Discard)
	if err != nil {
		t.Fatalf("parse auto-promote: %v", err)
	}
	if !options.autoPromote {
		t.Fatal("auto-promote option was not retained")
	}
}
