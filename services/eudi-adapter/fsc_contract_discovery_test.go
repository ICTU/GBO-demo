package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

const (
	testConsumerOIN = "99999999900000000100"
	testProviderOIN = "99999999900000000200"
)

func TestFSCContractDiscoverySelectsNewestValidConsumerGrant(t *testing.T) {
	now := time.Now().UTC()
	payload := fscContractPayload(t, []map[string]any{
		fscConnectionContract(testConsumerOIN, testProviderOIN, gboMetadataServiceName, "old-grant", now.Add(-time.Hour).Unix(), now),
		fscConnectionContract(testConsumerOIN, testProviderOIN, gboMetadataServiceName, "new-grant", now.Unix(), now),
		fscConnectionContract("99999999900000000999", testProviderOIN, gboMetadataServiceName, "other-consumer", now.Add(time.Minute).Unix(), now),
		fscConnectionContract(testConsumerOIN, testProviderOIN, "bri", "data-grant", now.Unix(), now),
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/peers" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fscPeerPayload(t))
			return
		}
		if got := request.URL.Query().Get("grant_type"); got != "GRANT_TYPE_SERVICE_CONNECTION" {
			t.Errorf("grant_type = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	snapshot, err := loadFSCContractSnapshot(context.Background(), server.Client(), server.URL, testConsumerOIN, now)
	if err != nil {
		t.Fatalf("load contracts: %v", err)
	}
	metadata := snapshot.metadataGrants()
	if len(metadata) != 1 || metadata[0].GrantHash != "new-grant" {
		t.Fatalf("metadata grants = %+v", metadata)
	}
	if data, ok := snapshot.service(testProviderOIN, "bri"); !ok || data.GrantHash != "data-grant" {
		t.Fatalf("data grant = %+v, exists=%v", data, ok)
	}
}

func TestFSCContractDiscoveryFollowsManagerPagination(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/peers" {
			_, _ = w.Write(fscPeerPayload(t))
			return
		}
		if request.URL.Query().Get("cursor") == "next-page" {
			_, _ = w.Write(fscContractPagePayload(t, []map[string]any{
				fscConnectionContract(testConsumerOIN, testProviderOIN, "bri", "data-grant", now.Unix(), now),
			}, ""))
			return
		}
		_, _ = w.Write(fscContractPagePayload(t, []map[string]any{
			fscConnectionContract(testConsumerOIN, testProviderOIN, gboMetadataServiceName, "metadata-grant", now.Unix(), now),
		}, "next-page"))
	}))
	defer server.Close()

	snapshot, err := loadFSCContractSnapshot(context.Background(), server.Client(), server.URL, testConsumerOIN, now)
	if err != nil {
		t.Fatalf("load paginated contracts: %v", err)
	}
	if _, ok := snapshot.service(testProviderOIN, "bri"); !ok {
		t.Fatal("data grant from second Manager page was not discovered")
	}
}

func TestFSCSourceReconcilerDerivesRegistrationFromContractsAndMetadata(t *testing.T) {
	now := time.Now().UTC()
	managerPayload := fscContractPayload(t, []map[string]any{
		fscConnectionContract(testConsumerOIN, testProviderOIN, gboMetadataServiceName, "metadata-grant", now.Unix(), now),
		fscConnectionContract(testConsumerOIN, testProviderOIN, "bri", "data-grant", now.Unix(), now),
	})
	manager := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/peers" {
			_, _ = w.Write(fscPeerPayload(t))
			return
		}
		_, _ = w.Write(managerPayload)
	}))
	defer manager.Close()
	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != gboWellKnownPath {
			t.Errorf("metadata path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Fsc-Grant-Hash"); got != "metadata-grant" {
			t.Errorf("metadata grant hash = %q", got)
		}
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write(metadataPayload)
	}))
	defer source.Close()
	backend := &capturingActivationBackend{}
	reconciler := &fscSourceReconciler{
		managerClient: manager.Client(), sourceClient: source.Client(),
		managerURL: manager.URL, consumerOIN: testConsumerOIN, outwayURL: source.URL,
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		store: staticCertificateStore{}, backend: backend,
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if backend.validated == nil {
		t.Fatal("source was not activated")
	}
	registration := backend.validated.Registration
	if registration.SourceOIN != testProviderOIN || registration.Name != "Belastingdienst" || registration.MetadataEndpoint.GrantHash != "metadata-grant" || registration.DataAccess.ServiceReference != "bri" || registration.DataAccess.GrantHash != "data-grant" {
		t.Fatalf("derived registration = %+v", registration)
	}
}

type staticCertificateStore struct{}

func (staticCertificateStore) Load(sourceRegistration) (certificateArtifacts, error) {
	return certificateArtifacts{}, nil
}

type capturingActivationBackend struct {
	validated *validatedSourceRegistration
}

func (b *capturingActivationBackend) Activate(validated *validatedSourceRegistration, _ certificateArtifacts) (*sourceActivation, error) {
	b.validated = validated
	return &sourceActivation{MetadataVersion: validated.Document.Version}, nil
}

func fscContractPayload(t *testing.T, contracts []map[string]any) []byte {
	return fscContractPagePayload(t, contracts, "")
}

func fscPeerPayload(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"peers": []map[string]any{{"id": testProviderOIN, "name": "Belastingdienst"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func fscContractPagePayload(t *testing.T, contracts []map[string]any, nextCursor string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"contracts": contracts, "pagination": map[string]any{"next_cursor": nextCursor}, "test_unknown_field": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func fscConnectionContract(consumerOIN, providerOIN, serviceName, grantHash string, createdAt int64, now time.Time) map[string]any {
	return map[string]any{
		"state": "CONTRACT_STATE_VALID",
		"content": map[string]any{
			"created_at": createdAt,
			"validity":   map[string]any{"not_before": now.Add(-time.Hour).Unix(), "not_after": now.Add(time.Hour).Unix()},
			"grants": []any{map[string]any{
				"type": "GRANT_TYPE_SERVICE_CONNECTION", "hash": grantHash,
				"outway":  map[string]any{"peer_id": consumerOIN},
				"service": map[string]any{"peer_id": providerOIN, "name": serviceName},
			}},
		},
	}
}

var _ certificateStore = staticCertificateStore{}
var _ activationBackend = (*capturingActivationBackend)(nil)
