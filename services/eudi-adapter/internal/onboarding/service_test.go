package onboarding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type testPrepared struct {
	source  ResolvedSource
	version string
}

type testCandidate struct {
	info   CandidateInfo
	source ResolvedSource
}

type testCatalog struct {
	sources []Source
	err     error
}

func (c testCatalog) List(context.Context) ([]Source, error) { return c.sources, c.err }

type testContracts struct {
	grants map[string]Grant
	err    error
}

func (c testContracts) Snapshot(context.Context, time.Time) (ContractSnapshot, error) {
	return c, c.err
}

func (c testContracts) Grant(providerPeerID, service string) (Grant, bool) {
	grant, ok := c.grants[providerPeerID+"/"+service]
	return grant, ok
}

type testMetadata struct {
	responses map[string]MetadataResponse
	errors    map[string]error
	requests  []MetadataRequest
}

func (m *testMetadata) Fetch(_ context.Context, request MetadataRequest) (MetadataResponse, error) {
	m.requests = append(m.requests, request)
	return m.responses[request.URL], m.errors[request.URL]
}

type testCompiler struct {
	description MetadataDescription
}

func (c testCompiler) Describe([]byte) (MetadataDescription, error) { return c.description, nil }

func (c testCompiler) Compile(request CompileRequest) (testPrepared, error) {
	return testPrepared{source: request.Source, version: "1.0"}, nil
}

type testCertificates struct {
	errors map[string]error
	loads  []string
}

func (c *testCertificates) Load(_ context.Context, source Source) (CertificateSet[string], error) {
	c.loads = append(c.loads, source.ID)
	return CertificateSet[string]{Artifacts: source.ID, SourceOIN: source.OIN, Name: source.Name}, c.errors[source.ID]
}

type testActivations struct {
	candidates map[string]*testCandidate
	activated  []string
	refreshed  []ResolvedSource
}

func (a *testActivations) Current(_ context.Context, sourceID string) (*testCandidate, bool, error) {
	candidate, ok := a.candidates[sourceID]
	return candidate, ok, nil
}

func (a *testActivations) Activate(_ context.Context, prepared testPrepared, _ string) (*testCandidate, error) {
	candidate := &testCandidate{source: prepared.source, info: CandidateInfo{
		SourceID: prepared.source.Source.ID, MetadataVersion: prepared.version,
		DataServiceReference: prepared.source.DataServiceReference, DeploymentDigest: "digest-" + prepared.source.Source.ID,
	}}
	a.candidates[prepared.source.Source.ID] = candidate
	a.activated = append(a.activated, prepared.source.Source.ID)
	return candidate, nil
}

func (a *testActivations) Refresh(_ context.Context, candidate *testCandidate, request RefreshRequest, _ string) (*testCandidate, error) {
	candidate.source = request.Source
	a.refreshed = append(a.refreshed, request.Source)
	return candidate, nil
}

func (a *testActivations) Info(candidate *testCandidate) (CandidateInfo, error) {
	return candidate.info, nil
}

func (a *testActivations) RolloutRequired(context.Context, *testCandidate) (bool, error) {
	return false, nil
}

type testStatuses struct {
	bySource map[string]Status
}

func (s *testStatuses) Put(_ context.Context, status Status) error {
	s.bySource[status.SourceID] = status
	return nil
}

func newTestService(t *testing.T, sources []Source, contracts testContracts, metadata *testMetadata, certificates *testCertificates, activations *testActivations, statuses *testStatuses) *Service[string, testPrepared, *testCandidate] {
	t.Helper()
	service, err := New(Options{OutwayURL: "http://outway.example"}, Dependencies[string, testPrepared, *testCandidate]{
		Sources: testCatalog{sources: sources}, Contracts: contracts, Metadata: metadata,
		Compiler:     testCompiler{description: MetadataDescription{SourceOIN: "00000000000000000001", DataServiceReference: "data"}},
		Certificates: certificates, Activations: activations, Statuses: statuses,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestReconcileIsolatesMissingCertificateSet(t *testing.T) {
	now := time.Now().UTC()
	sources := []Source{
		unsecuredSource("healthy", "http://healthy.example/metadata"),
		unsecuredSource("missing", "http://missing.example/metadata"),
	}
	metadata := &testMetadata{responses: map[string]MetadataResponse{
		"http://healthy.example/metadata": {Payload: []byte(`{}`)},
	}, errors: map[string]error{}}
	certificates := &testCertificates{errors: map[string]error{"missing": ErrCertificateNotFound}}
	activations := &testActivations{candidates: map[string]*testCandidate{}}
	statuses := &testStatuses{bySource: map[string]Status{}}
	service := newTestService(t, sources, testContracts{}, metadata, certificates, activations, statuses)

	report, err := service.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Err() == nil || !strings.Contains(report.Err().Error(), string(ReasonCertificateSetNotFound)) {
		t.Fatalf("report error = %v", report.Err())
	}
	if len(activations.activated) != 1 || activations.activated[0] != "healthy" {
		t.Fatalf("activated = %v", activations.activated)
	}
	if statuses.bySource["missing"].State != StateBlocked || statuses.bySource["healthy"].State != StateActive {
		t.Fatalf("statuses = %+v", statuses.bySource)
	}
}

func TestFSCManagerFailureDoesNotBlockUnsecuredSources(t *testing.T) {
	now := time.Now().UTC()
	sources := []Source{
		fscSource("fsc"),
		unsecuredSource("demo", "http://demo.example/metadata"),
	}
	metadata := &testMetadata{responses: map[string]MetadataResponse{"http://demo.example/metadata": {Payload: []byte(`{}`)}}, errors: map[string]error{}}
	certificates := &testCertificates{errors: map[string]error{}}
	activations := &testActivations{candidates: map[string]*testCandidate{}}
	statuses := &testStatuses{bySource: map[string]Status{}}
	service := newTestService(t, sources, testContracts{err: errors.New("manager unavailable")}, metadata, certificates, activations, statuses)

	report, err := service.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Err() == nil || statuses.bySource["fsc"].Reason != ReasonFSCManagerUnavailable {
		t.Fatalf("report=%v statuses=%+v", report.Err(), statuses.bySource)
	}
	if statuses.bySource["demo"].State != StateActive {
		t.Fatalf("unsecured source status = %+v", statuses.bySource["demo"])
	}
}

func TestNotModifiedRefreshesCurrentFSCGrantHashes(t *testing.T) {
	now := time.Now().UTC()
	source := fscSource("fsc")
	existing := &testCandidate{info: CandidateInfo{
		SourceID: source.ID, MetadataETag: `"v1"`, MetadataVersion: "1.0",
		DataServiceReference: "data", DeploymentDigest: "digest",
	}}
	metadataURL := "http://outway.example/.well-known/gbo"
	metadata := &testMetadata{responses: map[string]MetadataResponse{metadataURL: {NotModified: true}}, errors: map[string]error{}}
	certificates := &testCertificates{errors: map[string]error{}}
	activations := &testActivations{candidates: map[string]*testCandidate{source.ID: existing}}
	statuses := &testStatuses{bySource: map[string]Status{}}
	contracts := testContracts{grants: map[string]Grant{
		source.ProviderPeerID + "/metadata": {Hash: "metadata-v2"},
		source.ProviderPeerID + "/data":     {Hash: "data-v2"},
	}}
	service := newTestService(t, []Source{source}, contracts, metadata, certificates, activations, statuses)

	report, err := service.Reconcile(context.Background(), now)
	if err != nil || report.Err() != nil {
		t.Fatalf("reconcile: err=%v report=%v", err, report.Err())
	}
	if len(activations.refreshed) != 1 || activations.refreshed[0].MetadataGrantHash != "metadata-v2" || activations.refreshed[0].DataGrantHash != "data-v2" {
		t.Fatalf("refreshed source = %+v", activations.refreshed)
	}
	if len(metadata.requests) != 1 || metadata.requests[0].ETag != `"v1"` {
		t.Fatalf("metadata requests = %+v", metadata.requests)
	}
}

func TestUnavailableMetadataUsesCandidateStaleGrace(t *testing.T) {
	now := time.Now().UTC()
	source := unsecuredSource("demo", "http://demo.example/metadata")
	existing := &testCandidate{info: CandidateInfo{
		SourceID: source.ID, MetadataVersion: "1.0", DeploymentDigest: "digest", StaleUntil: now.Add(time.Hour),
	}}
	metadata := &testMetadata{responses: map[string]MetadataResponse{}, errors: map[string]error{source.MetadataEndpoint.Endpoint: errors.New("offline")}}
	certificates := &testCertificates{errors: map[string]error{}}
	activations := &testActivations{candidates: map[string]*testCandidate{source.ID: existing}}
	statuses := &testStatuses{bySource: map[string]Status{}}
	service := newTestService(t, []Source{source}, testContracts{}, metadata, certificates, activations, statuses)

	report, err := service.Reconcile(context.Background(), now)
	if err != nil || report.Err() == nil {
		t.Fatalf("reconcile: err=%v report=%v", err, report.Err())
	}
	if statuses.bySource[source.ID].State != StateStale {
		t.Fatalf("status = %+v", statuses.bySource[source.ID])
	}
}

func unsecuredSource(id, endpoint string) Source {
	return Source{
		ID: id, OIN: "00000000000000000001", Name: id,
		MetadataEndpoint:    MetadataEndpoint{Transport: TransportUnsecured, Endpoint: endpoint},
		DataAccessTransport: TransportUnsecured,
	}
}

func fscSource(id string) Source {
	return Source{
		ID: id, ProviderPeerID: "0000009958MINBZK0000", OIN: "00000000000000000001", Name: id,
		MetadataEndpoint:    MetadataEndpoint{Transport: TransportFSC, ServiceReference: "metadata", Path: "/.well-known/gbo"},
		DataAccessTransport: TransportFSC,
	}
}
