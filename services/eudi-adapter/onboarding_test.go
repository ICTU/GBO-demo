package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsecuredMetadataFetchSendsNoFSCHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Fsc-Grant-Hash") != "" || request.Header.Get("Fsc-Transaction-Id") != "" {
			t.Fatalf("unsecured request contains FSC headers: %v", request.Header)
		}
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	if _, err := fetchSourceMetadata(context.Background(), server.Client(), server.URL, sourceTransportUnsecured, ""); err != nil {
		t.Fatalf("fetch unsecured metadata: %v", err)
	}
}

func TestUnsecuredMetadataFetchRejectsRedirect(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalled = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := fetchSourceMetadata(context.Background(), redirect.Client(), redirect.URL, sourceTransportUnsecured, "")
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if targetCalled {
		t.Fatal("unsecured metadata redirect crossed the configured endpoint boundary")
	}
}

func TestGraphQLEndpointMustMatchOnboardedTransport(t *testing.T) {
	for name, test := range map[string]struct {
		endpoint  string
		transport string
		valid     bool
	}{
		"FSC path":            {endpoint: "/graphql", transport: sourceTransportFSC, valid: true},
		"FSC rejects URL":     {endpoint: "https://source.example/graphql", transport: sourceTransportFSC},
		"unsecured HTTPS URL": {endpoint: "https://source.example/graphql", transport: sourceTransportUnsecured, valid: true},
		"unsecured HTTP URL":  {endpoint: "http://source.example/graphql", transport: sourceTransportUnsecured, valid: true},
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
	path := filepath.Join(t.TempDir(), "source.json")
	activation := func(version, digest string) (*sourceActivation, []byte) {
		value := &sourceActivation{SchemaVersion: "1.0", Source: sourceRegistration{SourceOIN: "99999999900000000200"}, MetadataVersion: version, MetadataPayloadDigest: digest}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return value, body
	}
	first, firstBody := activation("1.0.0", "first")
	if err := writeSourceActivation(path, firstBody, first); err != nil {
		t.Fatal(err)
	}
	conflict, conflictBody := activation("1.0.0", "different")
	if err := writeSourceActivation(path, conflictBody, conflict); err == nil {
		t.Fatal("same version with different bytes was accepted")
	}
	rotation, _ := activation("1.0.0", "first")
	rotation.Certificates.CertificateExpires = "2028-08-05T12:00:00Z"
	rotationBody, _ := json.Marshal(rotation)
	if err := writeSourceActivation(path, rotationBody, rotation); err != nil {
		t.Fatalf("certificate rotation: %v", err)
	}
	rollback, rollbackBody := activation("0.9.0", "rollback")
	if err := writeSourceActivation(path, rollbackBody, rollback); err == nil {
		t.Fatal("activation rollback was accepted")
	}
	upgrade, upgradeBody := activation("1.1.0", "upgrade")
	if err := writeSourceActivation(path, upgradeBody, upgrade); err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(path)
	if !bytes.Equal(stored, upgradeBody) {
		t.Fatalf("stored activation = %s, want %s", stored, upgradeBody)
	}
}

func TestFilesystemActivationUsesSourceIDForSharedOIN(t *testing.T) {
	stateDir := t.TempDir()
	backend := newFilesystemActivationBackend(stateDir)
	for _, sourceID := range []string{"belastingdienst", "rvig"} {
		validated := &validatedSourceRegistration{
			Registration: sourceRegistration{SourceID: sourceID, SourceOIN: "99999999900000000200", Name: sourceID, CertificateSet: sourceID},
			Document:     sourceMetadataDocument{Version: "1.0"}, Payload: []byte(sourceID), MetadataURL: "https://metadata.example/" + sourceID,
		}
		if _, err := backend.Activate(validated, certificateArtifacts{}); err != nil {
			t.Fatalf("activate %s: %v", sourceID, err)
		}
	}
	for _, sourceID := range []string{"belastingdienst", "rvig"} {
		if _, err := os.Stat(filepath.Join(stateDir, "candidates", sourceID+".json")); err != nil {
			t.Errorf("activation %s: %v", sourceID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "candidates", "99999999900000000200.json")); !os.IsNotExist(err) {
		t.Fatalf("shared OIN was used as activation key: %v", err)
	}
}
