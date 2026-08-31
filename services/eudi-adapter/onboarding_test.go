package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

func TestCertificateProvisioningUsesSourceIDAsItsOnlySetKey(t *testing.T) {
	options, err := parseOnboardingOptions("provision-development-certificates", []string{
		"--source-id=belastingdienst",
		"--source-oin=99999999900000000200",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse provisioning options: %v", err)
	}
	if options.sourceID != "belastingdienst" {
		t.Fatalf("source ID = %q", options.sourceID)
	}
	if _, err := parseOnboardingOptions("provision-development-certificates", []string{
		"--source-id=belastingdienst",
		"--source-oin=99999999900000000200",
		"--certificate-set=duplicate-key",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("removed --certificate-set option was accepted")
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
