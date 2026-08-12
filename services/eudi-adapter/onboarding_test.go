package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticOnboardingRejectsFSCRegistration(t *testing.T) {
	registrationPath := writeSourceRegistrationFixture(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	secretsDir := filepath.Join(t.TempDir(), "secrets")

	handled, err := runOnboardingCommand(context.Background(), []string{
		"validate-source",
		"--source", registrationPath,
		"--outway-url", "https://outway.example",
		"--schema", "../../schemas/gbo-source-metadata-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--state-dir", stateDir,
		"--secrets-dir", secretsDir,
	}, onboardingDependencies{
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("a static FSC registration must be rejected before network access")
			return nil, nil
		})},
		now: time.Now,
		resolveCertificateProvider: func(onboardingOptions) (certificateProvider, error) {
			t.Fatal("validate-source must not instantiate a certificate provider")
			return nil, nil
		},
		resolveCertificateStore: func(onboardingOptions) (certificateStore, error) {
			t.Fatal("validate-source must not instantiate a certificate store")
			return nil, nil
		},
		resolveActivationBackend: func(onboardingOptions) (activationBackend, error) {
			t.Fatal("validate-source must not instantiate an activation backend")
			return nil, nil
		},
		stdout: io.Discard,
		stderr: io.Discard,
	})
	if !handled {
		t.Fatal("validate-source was not handled")
	}
	if err == nil || !strings.Contains(err.Error(), "discovered from contracts") {
		t.Fatalf("error = %v, want contract-discovery guidance", err)
	}
	assertPathAbsent(t, stateDir)
	assertPathAbsent(t, secretsDir)
}

func TestSourceRegistrationRejectsUnknownAndDuplicateFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":   validRegistrationYAML() + "metadata_url: https://public.example\n",
		"duplicate": validRegistrationYAML() + "name: duplicate\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadSourceRegistration(path); err == nil {
				t.Fatal("invalid source registration was accepted")
			}
		})
	}
}

func TestHTTPSMTLSRegistrationIsModelledButFailsClosedAtRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.yaml")
	body := `source_oin: "99999999900000000200"
name: "Belastingdienst-mock"
metadata_endpoint:
  transport: "https-mtls"
  endpoint: "https://metadata.example/.well-known/gbo"
data_access:
  transport: "https-mtls"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	registration, err := loadSourceRegistration(path)
	if err != nil {
		t.Fatalf("load https-mtls registration: %v", err)
	}
	if _, err := registration.metadataURL("https://outway.example"); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("https-mtls runtime error = %v, want fail-closed not implemented", err)
	}
}

func TestGraphQLEndpointMustMatchOnboardedTransport(t *testing.T) {
	for name, test := range map[string]struct {
		endpoint  string
		transport string
		valid     bool
	}{
		"FSC path":              {endpoint: "/graphql", transport: sourceTransportFSC, valid: true},
		"FSC rejects URL":       {endpoint: "https://source.example/graphql", transport: sourceTransportFSC},
		"mTLS HTTPS URL":        {endpoint: "https://source.example/graphql", transport: sourceTransportHTTPSMTLS, valid: true},
		"mTLS rejects HTTP URL": {endpoint: "http://source.example/graphql", transport: sourceTransportHTTPSMTLS},
	} {
		t.Run(name, func(t *testing.T) {
			graphql := sourceGraphQL{Endpoint: test.endpoint}
			if test.transport == sourceTransportFSC {
				graphql.ServiceReference = "bri"
			}
			err := validateGraphQLEndpoint(graphql, test.transport)
			if test.valid && err != nil {
				t.Fatalf("endpoint rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid endpoint was accepted")
			}
		})
	}
}

func TestSourceActivationAllowsOnlyNewerVersions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "source.json")
	activation := func(version, digest string) (*sourceActivation, []byte) {
		value := &sourceActivation{
			SchemaVersion: "1.0",
			Source: sourceRegistration{
				SourceOIN: "99999999900000000200",
			},
			MetadataVersion:       version,
			MetadataPayloadDigest: digest,
		}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return value, body
	}
	first, firstBody := activation("1.0.0", "first")
	if err := writeSourceActivation(path, firstBody, first); err != nil {
		t.Fatalf("write first activation: %v", err)
	}
	conflict, conflictBody := activation("1.0.0", "different")
	if err := writeSourceActivation(path, conflictBody, conflict); err == nil {
		t.Fatal("same version with different bytes was accepted")
	}
	rotation, _ := activation("1.0.0", "first")
	rotation.Certificates.CertificateExpires = "2028-08-05T12:00:00Z"
	rotationBody, err := json.Marshal(rotation)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSourceActivation(path, rotationBody, rotation); err != nil {
		t.Fatalf("write certificate rotation for unchanged metadata: %v", err)
	}
	rollback, rollbackBody := activation("0.9.0", "rollback")
	if err := writeSourceActivation(path, rollbackBody, rollback); err == nil {
		t.Fatal("activation rollback was accepted")
	}
	upgrade, upgradeBody := activation("1.1.0", "upgrade")
	if err := writeSourceActivation(path, upgradeBody, upgrade); err != nil {
		t.Fatalf("write activation upgrade: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, upgradeBody) {
		t.Fatalf("stored activation = %s, want %s", stored, upgradeBody)
	}
}

func writeSourceRegistrationFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "99999999900000000200.yaml")
	if err := os.WriteFile(path, []byte(validRegistrationYAML()), 0o600); err != nil {
		t.Fatalf("write source registration: %v", err)
	}
	return path
}

func validRegistrationYAML() string {
	return "source_oin: \"99999999900000000200\"\n" +
		"name: \"Belastingdienst-mock\"\n" +
		"metadata_endpoint:\n" +
		"  transport: \"fsc\"\n" +
		"  service_reference: \"gbo-metadata\"\n" +
		"  path: \"/.well-known/gbo\"\n" +
		"  grant_hash: \"metadata-grant\"\n" +
		"data_access:\n" +
		"  transport: \"fsc\"\n" +
		"  service_reference: \"bri\"\n" +
		"  grant_hash: \"data-grant\"\n"
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %q exists after non-mutating command (err=%v)", path, err)
	}
}
