package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateSourceCommandMakesNoPermanentChanges(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	registrationPath := writeSourceRegistrationFixture(t, publicJWK)
	stateDir := filepath.Join(t.TempDir(), "state")
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	client := metadataValidationClient(t, payload, privateKey)
	var output bytes.Buffer

	handled, err := runOnboardingCommand(context.Background(), []string{
		"validate-source",
		"--source", registrationPath,
		"--outway-url", "https://outway.example",
		"--schema", "../../schemas/gbo-attestations-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--state-dir", stateDir,
		"--secrets-dir", secretsDir,
	}, onboardingDependencies{
		client: client,
		now:    time.Now,
		resolveCertificateProvider: func(onboardingOptions) (certificateProvider, error) {
			t.Fatal("validate-source must not instantiate a certificate provider")
			return nil, nil
		},
		resolveActivationBackend: func(onboardingOptions) (activationBackend, error) {
			t.Fatal("validate-source must not instantiate an activation backend")
			return nil, nil
		},
		stdout: &output,
		stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("validate-source: %v", err)
	}
	if !handled {
		t.Fatal("validate-source was not handled")
	}
	if !strings.Contains(output.String(), "source 99999999900000000200 is valid") {
		t.Fatalf("output = %q", output.String())
	}
	assertPathAbsent(t, stateDir)
	assertPathAbsent(t, secretsDir)
}

func TestOnboardSourceDryRunCreatesNothing(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	registrationPath := writeSourceRegistrationFixture(t, publicJWK)
	stateDir := filepath.Join(t.TempDir(), "state")
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	providerCalled := false

	_, err := runOnboardingCommand(context.Background(), []string{
		"onboard-source", "--source", registrationPath, "--dry-run",
		"--outway-url", "https://outway.example",
		"--schema", "../../schemas/gbo-attestations-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--state-dir", stateDir, "--secrets-dir", secretsDir,
	}, onboardingDependencies{
		client: metadataValidationClient(t, payload, privateKey),
		now:    time.Now,
		resolveCertificateProvider: func(onboardingOptions) (certificateProvider, error) {
			providerCalled = true
			return nil, nil
		},
		resolveActivationBackend: func(onboardingOptions) (activationBackend, error) {
			providerCalled = true
			return nil, nil
		},
		stdout: io.Discard,
		stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("onboard-source --dry-run: %v", err)
	}
	if providerCalled {
		t.Fatal("dry-run instantiated the certificate provider")
	}
	assertPathAbsent(t, stateDir)
	assertPathAbsent(t, secretsDir)
}

func TestOnboardSourceInjectsOnlyConfiguredInfrastructure(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	registrationPath := writeSourceRegistrationFixture(t, publicJWK)
	for name, config := range map[string]struct {
		arguments []string
		wantError string
	}{
		"unknown storage backend": {
			arguments: []string{"--storage-backend", "database", "--certificate-provider", "development-ca"},
			wantError: "unsupported onboarding storage backend",
		},
		"unknown certificate provider": {
			arguments: []string{"--storage-backend", "filesystem", "--certificate-provider", "wallet-ca"},
			wantError: "unsupported certificate provider",
		},
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			secretsDir := filepath.Join(t.TempDir(), "secrets")
			arguments := []string{
				"onboard-source", "--source", registrationPath,
				"--outway-url", "https://outway.example",
				"--schema", "../../schemas/gbo-attestations-v1.schema.json",
				"--type-metadata-base-url", "https://issuer.example",
				"--state-dir", stateDir, "--secrets-dir", secretsDir,
			}
			arguments = append(arguments, config.arguments...)
			dependencies := defaultOnboardingDependencies()
			dependencies.client = metadataValidationClient(t, payload, privateKey)
			dependencies.stdout = io.Discard
			dependencies.stderr = io.Discard
			if _, err := runOnboardingCommand(context.Background(), arguments, dependencies); err == nil || !strings.Contains(err.Error(), config.wantError) {
				t.Fatalf("error = %v, want %q", err, config.wantError)
			}
			assertPathAbsent(t, stateDir)
			assertPathAbsent(t, secretsDir)
		})
	}
}

func TestOnboardSourceIsIdempotentAndActivatesLast(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	registrationPath := writeSourceRegistrationFixture(t, publicJWK)
	stateDir := filepath.Join(t.TempDir(), "state")
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	dependencies := onboardingDependencies{
		client:                     metadataValidationClient(t, payload, privateKey),
		now:                        time.Now,
		resolveCertificateProvider: configuredCertificateProvider,
		resolveActivationBackend:   configuredActivationBackend,
		stdout:                     io.Discard,
		stderr:                     io.Discard,
	}
	arguments := []string{
		"onboard-source", "--source", registrationPath,
		"--storage-backend", "filesystem", "--certificate-provider", "development-ca",
		"--outway-url", "https://outway.example",
		"--schema", "../../schemas/gbo-attestations-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--state-dir", stateDir, "--secrets-dir", secretsDir,
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := runOnboardingCommand(context.Background(), arguments, dependencies); err != nil {
			t.Fatalf("onboard-source attempt %d: %v", attempt+1, err)
		}
	}
	activationPath := filepath.Join(stateDir, "active", "99999999900000000200.json")
	rawActivation, err := os.ReadFile(activationPath)
	if err != nil {
		t.Fatalf("read activation: %v", err)
	}
	var activation sourceActivation
	if err := json.Unmarshal(rawActivation, &activation); err != nil {
		t.Fatalf("parse activation: %v", err)
	}
	if len(activation.Types) != 1 || activation.Types[0].VCTIntegrity == "" {
		t.Fatalf("activation types = %+v", activation.Types)
	}
	issuanceEnv, err := os.ReadFile(activation.IssuanceConfigReference)
	if err != nil {
		t.Fatalf("read generated issuance environment: %v", err)
	}
	for _, variable := range []string{
		"EUDI_ISSUER_KEY=", "EUDI_ISSUER_CERT=", "EUDI_READER_KEY=", "EUDI_READER_CERT=",
		"EUDI_STATUS_KEY=", "EUDI_STATUS_CERT=", "EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR=",
		"EUDI_ONBOARDING_READER_TRUST_ANCHOR=",
	} {
		if !bytes.Contains(issuanceEnv, []byte(variable)) {
			t.Errorf("generated issuance environment has no %s", variable)
		}
	}
	if info, err := os.Stat(activation.IssuanceConfigReference); err != nil {
		t.Fatalf("stat generated issuance environment: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("generated issuance environment mode = %o, want 600", got)
	}
	for _, path := range []string{
		activation.Certificates.IssuerKeyReference,
		activation.Certificates.ReaderKeyReference,
		activation.Certificates.StatusKeyReference,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat private key %q: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("private key %q mode = %o, want 600", path, got)
		}
	}
}

func TestOnboardSourceCertificateFailureLeavesSourceInactive(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	registrationPath := writeSourceRegistrationFixture(t, publicJWK)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, err := runOnboardingCommand(context.Background(), []string{
		"onboard-source", "--source", registrationPath,
		"--storage-backend", "filesystem", "--certificate-provider", "development-ca",
		"--outway-url", "https://outway.example",
		"--schema", "../../schemas/gbo-attestations-v1.schema.json",
		"--type-metadata-base-url", "https://issuer.example",
		"--state-dir", stateDir, "--secrets-dir", filepath.Join(t.TempDir(), "secrets"),
	}, onboardingDependencies{
		client: metadataValidationClient(t, payload, privateKey),
		now:    time.Now,
		resolveCertificateProvider: func(onboardingOptions) (certificateProvider, error) {
			return failingCertificateProvider{}, nil
		},
		resolveActivationBackend: configuredActivationBackend,
		stdout:                   io.Discard,
		stderr:                   io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "certificate provider failed") {
		t.Fatalf("error = %v, want certificate provider failure", err)
	}
	assertPathAbsent(t, filepath.Join(stateDir, "active", "99999999900000000200.json"))
}

func TestSourceRegistrationRejectsUnknownAndDuplicateFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":   validRegistrationYAML("sha256-"+strings.Repeat("A", 43)) + "metadata_url: https://public.example\n",
		"duplicate": validRegistrationYAML("sha256-"+strings.Repeat("A", 43)) + "name: duplicate\n",
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

type failingCertificateProvider struct{}

func (failingCertificateProvider) Provision(sourceRegistration) (certificateArtifacts, error) {
	return certificateArtifacts{}, errors.New("certificate provider failed")
}

func writeSourceRegistrationFixture(t *testing.T, publicJWK json.RawMessage) string {
	t.Helper()
	var jwk sourceMetadataJWK
	if err := json.Unmarshal(publicJWK, &jwk); err != nil {
		t.Fatalf("parse public JWK: %v", err)
	}
	path := filepath.Join(t.TempDir(), "99999999900000000200.yaml")
	if err := os.WriteFile(path, []byte(validRegistrationYAML("sha256-"+sourceMetadataJWKThumbprint(jwk))), 0o600); err != nil {
		t.Fatalf("write source registration: %v", err)
	}
	return path
}

func validRegistrationYAML(thumbprint string) string {
	return "source_oin: \"99999999900000000200\"\n" +
		"name: \"Belastingdienst-mock\"\n" +
		"metadata_fsc_service_reference: \"gbo-attestation-metadata\"\n" +
		"metadata_signing_jwk_thumbprint: \"" + thumbprint + "\"\n" +
		"data_fsc_service_reference: \"bri\"\n"
}

func metadataValidationClient(t *testing.T, payload []byte, privateKey ed25519.PrivateKey) *http.Client {
	t.Helper()
	compact := signSourceMetadataForTest(t, payload, privateKey)
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got, want := request.URL.Path, "/gbo-attestation-metadata/.well-known/gbo-attestations"; got != want {
			t.Fatalf("metadata path = %q, want %q", got, want)
		}
		if request.Header.Get("Fsc-Transaction-Id") == "" {
			t.Fatal("metadata request has no Fsc-Transaction-Id")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{sourceMetadataMediaType}},
			Body:       io.NopCloser(strings.NewReader(compact)),
			Request:    request,
		}, nil
	})}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %q exists after non-mutating command (err=%v)", path, err)
	}
}
