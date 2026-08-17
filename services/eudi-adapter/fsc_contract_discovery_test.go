package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	testConsumerOIN        = "99999999900000000100"
	testProviderOIN        = "99999999900000000200"
	gboMetadataServiceName = "gbo-metadata"
	gboWellKnownPath       = "/.well-known/gbo"
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
	metadata, ok := snapshot.service(testProviderOIN, gboMetadataServiceName)
	if !ok || metadata.GrantHash != "new-grant" {
		t.Fatalf("metadata grant = %+v, exists=%v", metadata, ok)
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

func TestFSCSourceReconcilerActivatesOnlyConfiguredSource(t *testing.T) {
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
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		managerClient: manager.Client(), sourceClient: source.Client(),
		managerURL: manager.URL, consumerOIN: testConsumerOIN, outwayURL: source.URL,
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "belastingdienst", SourceOIN: testProviderOIN, Name: "Belastingdienst", CertificateSet: testProviderOIN,
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: gboMetadataServiceName, Path: gboWellKnownPath},
			DataAccess:       sourceDataAccess{Transport: sourceTransportFSC},
		}},
		store: staticCertificateStore{}, backend: backend, statuses: statuses,
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if backend.validated == nil {
		t.Fatal("source was not activated")
	}
	registration := backend.validated.Registration
	if registration.SourceID != "belastingdienst" || registration.CertificateSet != testProviderOIN || registration.SourceOIN != testProviderOIN || registration.Name != "Belastingdienst" || registration.MetadataEndpoint.GrantHash != "metadata-grant" || registration.DataAccess.ServiceReference != "bri" || registration.DataAccess.GrantHash != "data-grant" {
		t.Fatalf("derived registration = %+v", registration)
	}
	if statuses.last.State != sourceStateActive || statuses.last.SourceID != "belastingdienst" {
		t.Fatalf("status = %+v", statuses.last)
	}
}

func TestFSCSourceReconcilerIgnoresUnconfiguredMetadataContract(t *testing.T) {
	now := time.Now().UTC()
	manager := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/peers" {
			_, _ = w.Write(fscPeerPayload(t))
			return
		}
		_, _ = w.Write(fscContractPayload(t, []map[string]any{
			fscConnectionContract(testConsumerOIN, testProviderOIN, gboMetadataServiceName, "metadata-grant", now.Unix(), now),
		}))
	}))
	defer manager.Close()

	backend := &capturingActivationBackend{}
	reconciler := &sourceReconciler{
		managerClient: manager.Client(), sourceClient: http.DefaultClient,
		managerURL: manager.URL, consumerOIN: testConsumerOIN, outwayURL: "http://source.invalid",
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		store: staticCertificateStore{}, backend: backend, statuses: &capturingSourceStatusWriter{},
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if backend.validated != nil {
		t.Fatal("unconfigured FSC contract activated a source")
	}
}

func TestFSCSourceReconcilerChecksCertificatesBeforeNetwork(t *testing.T) {
	backend := &capturingActivationBackend{}
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		managerClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("FSC Manager was called before certificate preflight")
			return nil, nil
		})},
		sourceClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("source metadata was fetched before certificate preflight")
			return nil, nil
		})},
		managerURL: "https://manager.example", consumerOIN: testConsumerOIN, outwayURL: "http://outway.example",
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "belastingdienst", SourceOIN: testProviderOIN, Name: "Belastingdienst", CertificateSet: "missing-bd",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: gboMetadataServiceName, Path: gboWellKnownPath},
			DataAccess:       sourceDataAccess{Transport: sourceTransportFSC},
		}},
		store: failingCertificateStore{err: os.ErrNotExist}, backend: backend, statuses: statuses,
	}
	err := reconciler.Reconcile(context.Background(), time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), sourceReasonCertificateSetNotFound) {
		t.Fatalf("reconcile error = %v", err)
	}
	if backend.validated != nil {
		t.Fatal("source with missing certificates was activated")
	}
	if statuses.last.State != sourceStateBlocked || statuses.last.Reason != sourceReasonCertificateSetNotFound {
		t.Fatalf("status = %+v", statuses.last)
	}
}

func TestFSCSourceReconcilerReportsMissingConfiguredMetadataContract(t *testing.T) {
	now := time.Now().UTC()
	manager := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fscContractPayload(t, nil))
	}))
	defer manager.Close()
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		managerClient: manager.Client(), sourceClient: http.DefaultClient,
		managerURL: manager.URL, consumerOIN: testConsumerOIN, outwayURL: "http://source.invalid",
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "belastingdienst", SourceOIN: testProviderOIN, Name: "Belastingdienst", CertificateSet: testProviderOIN,
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: gboMetadataServiceName, Path: gboWellKnownPath},
			DataAccess:       sourceDataAccess{Transport: sourceTransportFSC},
		}},
		store: staticCertificateStore{}, backend: &capturingActivationBackend{}, statuses: statuses,
	}
	err := reconciler.Reconcile(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), sourceReasonMetadataContractMissing) {
		t.Fatalf("reconcile error = %v", err)
	}
	if statuses.last.State != sourceStateBlocked || statuses.last.Reason != sourceReasonMetadataContractMissing {
		t.Fatalf("status = %+v", statuses.last)
	}
}

func TestFSCSourceReconcilerSupportsSharedOINWithDistinctServicesAndCertificates(t *testing.T) {
	now := time.Now().UTC()
	managerPayload := fscContractPayload(t, []map[string]any{
		fscConnectionContract(testConsumerOIN, testProviderOIN, "gbo-metadata-bd", "metadata-bd", now.Unix(), now),
		fscConnectionContract(testConsumerOIN, testProviderOIN, "gbo-metadata-rvig", "metadata-rvig", now.Unix(), now),
		fscConnectionContract(testConsumerOIN, testProviderOIN, "bri", "data-grant", now.Unix(), now),
	})
	manager := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(managerPayload)
	}))
	defer manager.Close()
	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Fsc-Grant-Hash"); got != "metadata-bd" && got != "metadata-rvig" {
			t.Errorf("metadata grant = %q", got)
		}
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write(metadataPayload)
	}))
	defer source.Close()
	backend := &capturingActivationBackend{}
	statuses := &capturingSourceStatusWriter{}
	store := &recordingCertificateStore{}
	reconciler := &sourceReconciler{
		managerClient: manager.Client(), sourceClient: source.Client(), managerURL: manager.URL,
		consumerOIN: testConsumerOIN, outwayURL: source.URL,
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{
			sharedOINSourceConfiguration("belastingdienst", "gbo-metadata-bd"),
			sharedOINSourceConfiguration("rvig", "gbo-metadata-rvig"),
		},
		store: store, backend: backend, statuses: statuses,
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("reconcile shared OIN: %v", err)
	}
	if len(backend.activations) != 2 || backend.activations[0].Registration.SourceID == backend.activations[1].Registration.SourceID {
		t.Fatalf("activations = %+v", backend.activations)
	}
	if got := strings.Join(store.loaded, ","); got != "belastingdienst,rvig" {
		t.Fatalf("loaded certificate sets = %q", got)
	}
	for _, sourceID := range []string{"belastingdienst", "rvig"} {
		if statuses.bySource[sourceID].State != sourceStateActive {
			t.Errorf("status %s = %+v", sourceID, statuses.bySource[sourceID])
		}
	}
}

func TestFSCSourceReconcilerIsolatesCertificateFailureForSharedOIN(t *testing.T) {
	now := time.Now().UTC()
	manager := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fscContractPayload(t, []map[string]any{
			fscConnectionContract(testConsumerOIN, testProviderOIN, "gbo-metadata-bd", "metadata-bd", now.Unix(), now),
			fscConnectionContract(testConsumerOIN, testProviderOIN, "bri", "data-grant", now.Unix(), now),
		}))
	}))
	defer manager.Close()
	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write(metadataPayload)
	}))
	defer source.Close()
	backend := &capturingActivationBackend{}
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		managerClient: manager.Client(), sourceClient: source.Client(), managerURL: manager.URL,
		consumerOIN: testConsumerOIN, outwayURL: source.URL,
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{
			sharedOINSourceConfiguration("belastingdienst", "gbo-metadata-bd"),
			sharedOINSourceConfiguration("rvig", "gbo-metadata-rvig"),
		},
		store: &recordingCertificateStore{missing: map[string]bool{"rvig": true}}, backend: backend, statuses: statuses,
	}
	err = reconciler.Reconcile(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), sourceReasonCertificateSetNotFound) {
		t.Fatalf("reconcile error = %v", err)
	}
	if len(backend.activations) != 1 || backend.activations[0].Registration.SourceID != "belastingdienst" {
		t.Fatalf("healthy activations = %+v", backend.activations)
	}
	if statuses.bySource["belastingdienst"].State != sourceStateActive || statuses.bySource["rvig"].Reason != sourceReasonCertificateSetNotFound {
		t.Fatalf("statuses = %+v", statuses.bySource)
	}
}

func TestSourceReconcilerActivatesUnsecuredHTTPSourceWithoutFSCManager(t *testing.T) {
	now := time.Now().UTC()
	raw, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	graphql := document["capabilities"].(map[string]any)["eudi"].(map[string]any)["attestations"].([]any)[0].(map[string]any)["graphql"].(map[string]any)
	delete(graphql, "service_reference")

	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Fsc-Grant-Hash") != "" || request.Header.Get("Fsc-Transaction-Id") != "" {
			t.Fatalf("unsecured metadata request contains FSC headers: %v", request.Header)
		}
		graphql["endpoint"] = source.URL + "/graphql"
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_ = json.NewEncoder(w).Encode(document)
	}))
	defer source.Close()

	backend := &capturingActivationBackend{}
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		managerClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unsecured-only reconciliation called the FSC Manager")
			return nil, nil
		})},
		sourceClient: source.Client(), managerURL: "https://manager.invalid", consumerOIN: testConsumerOIN,
		schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "demo", SourceOIN: testProviderOIN, Name: "Demo", CertificateSet: "demo",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: source.URL},
			DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
		}},
		store: staticCertificateStore{}, backend: backend, statuses: statuses,
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("reconcile unsecured source: %v", err)
	}
	if backend.validated == nil || backend.validated.Registration.DataAccess.Transport != sourceTransportUnsecured {
		t.Fatalf("activation = %+v", backend.validated)
	}
	if statuses.last.State != sourceStateActive || statuses.last.TransportAuthenticated {
		t.Fatalf("status = %+v", statuses.last)
	}
}

func TestUnsecuredSourceFailureIsReportedAsUnauthenticated(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer source.Close()
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		sourceClient: source.Client(), schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "demo", SourceOIN: testProviderOIN, Name: "Demo", CertificateSet: "demo",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: source.URL},
			DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
		}},
		store: staticCertificateStore{}, backend: &capturingActivationBackend{}, statuses: statuses,
	}
	if err := reconciler.Reconcile(context.Background(), time.Now().UTC()); err == nil {
		t.Fatal("unavailable unsecured source reconciled successfully")
	}
	if statuses.last.TransportAuthenticated {
		t.Fatalf("status incorrectly authenticates unsecured transport: %+v", statuses.last)
	}
}

func TestReconcilerUsesETagAndRefreshesCandidateLifetime(t *testing.T) {
	now := time.Now().UTC()
	raw, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	graphql := document["capabilities"].(map[string]any)["eudi"].(map[string]any)["attestations"].([]any)[0].(map[string]any)["graphql"].(map[string]any)
	delete(graphql, "service_reference")
	requestCount := 0
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestCount++
		if requestCount == 2 {
			if got, want := request.Header.Get("If-None-Match"), `"metadata-v1"`; got != want {
				t.Errorf("If-None-Match = %q, want %q", got, want)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		graphql["endpoint"] = source.URL + "/graphql"
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		w.Header().Set("ETag", `"metadata-v1"`)
		_ = json.NewEncoder(w).Encode(document)
	}))
	defer source.Close()
	stateDir := t.TempDir()
	backend := newFilesystemActivationBackend(stateDir)
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		sourceClient: source.Client(), schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "demo", SourceOIN: testProviderOIN, Name: "Demo", CertificateSet: "demo",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: source.URL},
			DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
		}}, store: staticCertificateStore{}, backend: backend, statuses: statuses,
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	first, err := backend.CurrentCandidate("demo")
	if err != nil {
		t.Fatal(err)
	}
	if statuses.last.State != sourceStateRolloutRequired {
		t.Fatalf("initial status = %+v", statuses.last)
	}
	if err := reconciler.Reconcile(context.Background(), now.Add(10*time.Minute)); err != nil {
		t.Fatalf("conditional reconcile: %v", err)
	}
	second, err := backend.CurrentCandidate("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !second.FreshUntil.After(first.FreshUntil) {
		t.Fatalf("fresh lifetime did not advance: first=%s second=%s", first.FreshUntil, second.FreshUntil)
	}
	if requestCount != 2 {
		t.Fatalf("metadata requests = %d, want 2", requestCount)
	}
}

func TestReconcilerKeepsLastCandidateDuringStaleGrace(t *testing.T) {
	now := time.Now().UTC()
	raw, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	graphql := document["capabilities"].(map[string]any)["eudi"].(map[string]any)["attestations"].([]any)[0].(map[string]any)["graphql"].(map[string]any)
	delete(graphql, "service_reference")
	available := true
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !available {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		graphql["endpoint"] = source.URL + "/graphql"
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_ = json.NewEncoder(w).Encode(document)
	}))
	defer source.Close()
	backend := newFilesystemActivationBackend(t.TempDir())
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		sourceClient: source.Client(), schemaPath: "../../schemas/gbo-source-metadata-v1.schema.json", publicBaseURL: "https://issuer.example",
		sources: []sourceConfiguration{{
			SourceID: "demo", SourceOIN: testProviderOIN, Name: "Demo", CertificateSet: "demo",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: source.URL},
			DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
		}}, store: staticCertificateStore{}, backend: backend, statuses: statuses,
	}
	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	before, err := backend.CurrentCandidate("demo")
	if err != nil {
		t.Fatal(err)
	}
	available = false
	if err := reconciler.Reconcile(context.Background(), now.Add(20*time.Minute)); err == nil {
		t.Fatal("unavailable source did not report a reconciliation error")
	}
	after, err := backend.CurrentCandidate("demo")
	if err != nil {
		t.Fatal(err)
	}
	if statuses.last.State != sourceStateStale || after.MetadataPayloadDigest != before.MetadataPayloadDigest {
		t.Fatalf("status=%+v before=%+v after=%+v", statuses.last, before, after)
	}
}

func TestFSCManagerFailureKeepsCandidateDuringStaleGrace(t *testing.T) {
	now := time.Now().UTC()
	backend := &candidateActivationBackend{candidate: &sourceActivation{
		SchemaVersion: "1.0", MetadataVersion: "1.0", StaleUntil: now.Add(time.Hour),
		Source: sourceRegistration{SourceID: "belastingdienst", SourceOIN: testProviderOIN},
	}}
	statuses := &capturingSourceStatusWriter{}
	reconciler := &sourceReconciler{
		managerClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("manager unavailable")
		})},
		sourceClient: http.DefaultClient, managerURL: "https://manager.example", consumerOIN: testConsumerOIN,
		sources: []sourceConfiguration{sharedOINSourceConfiguration("belastingdienst", "gbo-metadata-bd")},
		store:   staticCertificateStore{}, backend: backend, statuses: statuses,
	}

	if err := reconciler.Reconcile(context.Background(), now); err == nil {
		t.Fatal("manager outage reconciled successfully")
	}
	if statuses.last.State != sourceStateStale {
		t.Fatalf("status = %+v, want stale candidate", statuses.last)
	}
}

func sharedOINSourceConfiguration(sourceID, metadataService string) sourceConfiguration {
	return sourceConfiguration{
		SourceID: sourceID, SourceOIN: testProviderOIN, Name: sourceID, CertificateSet: sourceID,
		MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: metadataService, Path: gboWellKnownPath},
		DataAccess:       sourceDataAccess{Transport: sourceTransportFSC},
	}
}

type staticCertificateStore struct{}

func (staticCertificateStore) Load(sourceRegistration) (certificateArtifacts, error) {
	return certificateArtifacts{}, nil
}

type failingCertificateStore struct{ err error }

func (s failingCertificateStore) Load(sourceRegistration) (certificateArtifacts, error) {
	return certificateArtifacts{}, s.err
}

type capturingActivationBackend struct {
	validated   *validatedSourceRegistration
	activations []*validatedSourceRegistration
}

type candidateActivationBackend struct{ candidate *sourceActivation }

func (b *candidateActivationBackend) Activate(*validatedSourceRegistration, certificateArtifacts) (*sourceActivation, error) {
	return b.candidate, nil
}

func (b *candidateActivationBackend) CurrentCandidate(string) (*sourceActivation, error) {
	return b.candidate, nil
}

func (b *candidateActivationBackend) RefreshCandidate(string, time.Time) (*sourceActivation, error) {
	return b.candidate, nil
}

func (*candidateActivationBackend) RolloutRequired(*sourceActivation) (bool, error) {
	return false, nil
}

type capturingSourceStatusWriter struct {
	last     sourceReconcileStatus
	bySource map[string]sourceReconcileStatus
}

func (w *capturingSourceStatusWriter) Write(status sourceReconcileStatus) error {
	w.last = status
	if w.bySource == nil {
		w.bySource = make(map[string]sourceReconcileStatus)
	}
	w.bySource[status.SourceID] = status
	return nil
}

type recordingCertificateStore struct {
	missing map[string]bool
	loaded  []string
}

func (s *recordingCertificateStore) Load(registration sourceRegistration) (certificateArtifacts, error) {
	s.loaded = append(s.loaded, registration.CertificateSet)
	if s.missing[registration.CertificateSet] {
		return certificateArtifacts{}, os.ErrNotExist
	}
	return certificateArtifacts{}, nil
}

func (b *capturingActivationBackend) Activate(validated *validatedSourceRegistration, _ certificateArtifacts) (*sourceActivation, error) {
	b.validated = validated
	b.activations = append(b.activations, validated)
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
var _ certificateStore = failingCertificateStore{}
var _ certificateStore = (*recordingCertificateStore)(nil)
var _ activationBackend = (*capturingActivationBackend)(nil)
var _ activationLifecycleBackend = (*candidateActivationBackend)(nil)
var _ sourceStatusWriter = (*capturingSourceStatusWriter)(nil)
