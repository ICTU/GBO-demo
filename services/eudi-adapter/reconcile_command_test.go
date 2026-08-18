package main

import (
	"io"
	"testing"
)

func TestReconcileCommandRequiresExplicitOnceOrWatchMode(t *testing.T) {
	base := []string{
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
