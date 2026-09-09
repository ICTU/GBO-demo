package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §3.2.1: "Het Logboek MOET TLS kunnen afdwingen." The capability is required;
// using it is not, so an unconfigured logbook serves plaintext and a
// configured one terminates TLS itself.
//
// The case worth guarding is the half-configured one. A deployment that sets
// the certificate and forgets the key meant to serve TLS; without this it
// would silently serve plaintext instead — records of every processing about a
// person, in the clear, because of one unset variable.
func TestTLSRequiresBothFilesOrNeither(t *testing.T) {
	directory := t.TempDir()
	certificate := filepath.Join(directory, "cert.pem")
	key := filepath.Join(directory, "key.pem")
	for _, path := range []string{certificate, key} {
		if err := os.WriteFile(path, []byte("not a real key, only its presence is checked here"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	enabled, err := config{TLSCertPath: certificate, TLSKeyPath: key}.tlsEnabled()
	if err != nil || !enabled {
		t.Fatalf("both files present: enabled = %v, err = %v", enabled, err)
	}

	enabled, err = config{}.tlsEnabled()
	if err != nil || enabled {
		t.Fatalf("neither set: enabled = %v, err = %v", enabled, err)
	}

	for name, cfg := range map[string]config{
		"certificate without key": {TLSCertPath: certificate},
		"key without certificate": {TLSKeyPath: key},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := cfg.tlsEnabled(); err == nil {
				t.Fatal("a half-configured pair must fail rather than fall back to plaintext")
			}
		})
	}
}

// A path that does not resolve fails at startup rather than at the first
// connection, when it would be an outage instead of a misconfiguration.
func TestTLSFailsOnAMissingFile(t *testing.T) {
	directory := t.TempDir()
	certificate := filepath.Join(directory, "cert.pem")
	if err := os.WriteFile(certificate, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := config{TLSCertPath: certificate, TLSKeyPath: filepath.Join(directory, "absent.pem")}.tlsEnabled()
	if err == nil {
		t.Fatal("a missing key file must fail startup")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("the error should say which file is missing, got %v", err)
	}
}
