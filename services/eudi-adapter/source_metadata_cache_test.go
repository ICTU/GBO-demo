package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSourceMetadataCacheRefreshesConditionallyAndExpiresFailClosed(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 && request.Header.Get("If-None-Match") != `"source-v1"` {
			t.Errorf("If-None-Match = %q, want source ETag", request.Header.Get("If-None-Match"))
		}
		if requests == 2 {
			return metadataHTTPResponse(http.StatusNotModified, `"source-v1"`, nil), nil
		}
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, payload), nil
	})}
	cache := newTestSourceMetadataCache(t, client)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	if err := cache.Refresh(context.Background(), now); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := cache.Current(now)
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}
	if active.VCT == "" || active.VCTIntegrity == "" {
		t.Fatalf("active metadata lacks VCT binding: %+v", active)
	}
	if got, want := active.CacheState, "fresh"; got != want {
		t.Errorf("cache state = %q, want %q", got, want)
	}
	stale, err := cache.Current(now.Add(11 * time.Minute))
	if err != nil {
		t.Fatalf("stale metadata inside grace: %v", err)
	}
	if got, want := stale.CacheState, "stale"; got != want {
		t.Errorf("cache state = %q, want %q", got, want)
	}
	if err := cache.Refresh(context.Background(), now.Add(5*time.Minute)); err != nil {
		t.Fatalf("conditional refresh: %v", err)
	}
	if _, err := cache.Current(now.Add(21 * time.Minute)); err == nil || !strings.Contains(err.Error(), "outside stale grace") {
		t.Fatalf("expired cache error = %v, want fail-closed stale error", err)
	}
}

func TestSourceMetadataCacheAcceptsResponsesWithoutETag(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("If-None-Match"); got != "" {
			t.Errorf("unconditional refresh sent If-None-Match %q", got)
		}
		return metadataHTTPResponse(http.StatusOK, "", payload), nil
	})}
	cache := newTestSourceMetadataCache(t, client)
	now := time.Now()
	if err := cache.Refresh(context.Background(), now); err != nil {
		t.Fatalf("refresh without ETag: %v", err)
	}
	if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatalf("second refresh without ETag: %v", err)
	}
}

func TestSourceMetadataCacheRejectsChangedBytesAtSameVersion(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same process"
		if restart {
			name = "after restart"
		}
		t.Run(name, func(t *testing.T) {
			payload := sourceMetadataCacheFixture(t)
			changed := bytes.Replace(payload, []byte(`"Inkomensverklaring"`), []byte(`"Gewijzigde inkomensverklaring"`), 1)
			responses := [][]byte{
				payload,
				changed,
			}
			requestIndex := 0
			client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				body := responses[requestIndex]
				requestIndex++
				return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
			})}
			storePath := t.TempDir()
			cache := newTestSourceMetadataCacheAt(t, client, storePath)
			now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			if err := cache.Refresh(context.Background(), now); err != nil {
				t.Fatalf("initial refresh: %v", err)
			}
			if restart {
				cache = newTestSourceMetadataCacheAt(t, client, storePath)
			}
			if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "changed bytes") {
				t.Fatalf("changed same-version refresh error = %v", err)
			}
		})
	}
}

func TestSourceMetadataCacheKeepsOlderTypeVersionReachable(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	nextPayload := mutateSourceMetadataVersions(t, payload, "9.0.0", "9.0")
	responses := [][]byte{
		payload,
		nextPayload,
	}
	requestIndex := 0
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := responses[requestIndex]
		requestIndex++
		return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
	})}
	cache := newTestSourceMetadataCache(t, client)
	now := time.Now()
	if err := cache.Refresh(context.Background(), now); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	old, err := cache.Current(now)
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}
	if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatalf("next-version refresh: %v", err)
	}
	current, err := cache.Current(now.Add(time.Minute))
	if err != nil {
		t.Fatalf("current next metadata: %v", err)
	}
	for _, vct := range []string{old.VCT, current.VCT} {
		recorder := httptest.NewRecorder()
		cache.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, vct, nil))
		if got, want := recorder.Code, http.StatusOK; got != want {
			t.Errorf("GET %s status = %d, want %d", vct, got, want)
		}
	}
}

func TestSourceMetadataCacheRejectsRollbackBeforeAndAfterRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same process"
		if restart {
			name = "after restart"
		}
		t.Run(name, func(t *testing.T) {
			payload := sourceMetadataCacheFixture(t)
			nextPayload := mutateSourceMetadataVersions(t, payload, "9.0.0", "9.0")
			responses := [][]byte{
				payload,
				nextPayload,
				payload,
			}
			requestIndex := 0
			client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				body := responses[requestIndex]
				requestIndex++
				return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
			})}
			storePath := t.TempDir()
			cache := newTestSourceMetadataCacheAt(t, client, storePath)
			now := time.Now()
			if err := cache.Refresh(context.Background(), now); err != nil {
				t.Fatalf("initial refresh: %v", err)
			}
			if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err != nil {
				t.Fatalf("forward refresh: %v", err)
			}
			if restart {
				cache = newTestSourceMetadataCacheAt(t, client, storePath)
			}
			if err := cache.Refresh(context.Background(), now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "rollback") {
				t.Fatalf("rollback error = %v, want rollback rejection", err)
			}
		})
	}
}

func TestSourceMetadataCacheRejectsTypeRollbackAfterRestart(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	nextPayload := mutateSourceMetadataVersions(t, payload, "9.0.0", "9.0")
	typeRollback := mutateSourceMetadataVersions(t, payload, "10.0.0", "0.1")
	responses := [][]byte{
		payload,
		nextPayload,
		typeRollback,
	}
	requestIndex := 0
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := responses[requestIndex]
		requestIndex++
		return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, storePath)
	now := time.Now()
	if err := cache.Refresh(context.Background(), now); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatalf("forward refresh: %v", err)
	}
	restarted := newTestSourceMetadataCacheAt(t, client, storePath)
	if err := restarted.Refresh(context.Background(), now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "type metadata version rollback") {
		t.Fatalf("type rollback error = %v, want type metadata rollback rejection", err)
	}
}

func TestAdapterActivatesOnlyCachedTypeAndBindsIssuableDocument(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "source.example":
			return metadataHTTPResponse(http.StatusOK, `"source-v1"`, payload), nil
		case "outway.example":
			return jsonHTTPResponse(http.StatusOK, []byte(completeIncomeResponse)), nil
		default:
			t.Fatalf("unexpected outbound host %q", request.URL.Host)
			return nil, nil
		}
	})}
	cache := newTestSourceMetadataCache(t, client)
	if err := cache.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := cache.Current(time.Now())
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}

	mux := newMux(config{OutwayURL: "https://outway.example", SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant"}, client, cache)

	metadataRequest := httptest.NewRequest(http.MethodGet, active.VCT, nil)
	metadataRecorder := httptest.NewRecorder()
	mux.ServeHTTP(metadataRecorder, metadataRequest)
	if got, want := metadataRecorder.Code, http.StatusOK; got != want {
		t.Fatalf("Type Metadata status = %d, want %d", got, want)
	}
	unknownRequest := httptest.NewRequest(http.MethodGet, "https://issuer.example/types/99999999900000000200/inkomensverklaring/v9.9", nil)
	unknownRecorder := httptest.NewRecorder()
	mux.ServeHTTP(unknownRecorder, unknownRequest)
	if got, want := unknownRecorder.Code, http.StatusNotFound; got != want {
		t.Fatalf("unknown Type Metadata status = %d, want %d", got, want)
	}

	disclosure := `[{
		"id":"request-1",
		"attestations":[{
			"attestation_type":"urn:eudi:pid:nl:1",
			"attributes":{"urn:eudi:pid:nl:1":{"bsn":"123456789"}}
		}]
	}]`
	issuanceRequest := httptest.NewRequest(http.MethodPost, "http://adapter/attestations/99999999900000000200/inkomensverklaring?jaar=2025", strings.NewReader(disclosure))
	issuanceRecorder := httptest.NewRecorder()
	mux.ServeHTTP(issuanceRecorder, issuanceRequest)
	if got, want := issuanceRecorder.Code, http.StatusOK; got != want {
		t.Fatalf("issuable document status = %d, want %d; body=%s", got, want, issuanceRecorder.Body.String())
	}
	if got, want := issuanceRecorder.Header().Get("X-GBO-Metadata-Cache"), "fresh"; got != want {
		t.Errorf("X-GBO-Metadata-Cache = %q, want %q", got, want)
	}
	var documents []attestation
	if err := json.Unmarshal(issuanceRecorder.Body.Bytes(), &documents); err != nil {
		t.Fatalf("decode issuable documents: %v", err)
	}
	if got := documents[0].AttestationType; got != active.VCT {
		t.Errorf("attestation_type = %q, want active VCT %q", got, active.VCT)
	}
	for _, internalClaim := range []string{"vct", "vct#integrity"} {
		if value, exists := documents[0].Attributes[internalClaim]; exists {
			t.Errorf("internal claim %q was sent as an IssuableDocument attribute: %v", internalClaim, value)
		}
	}
}

func TestPublishedTypeMetadataSurvivesCacheRestart(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, payload), nil
	})}
	storePath := t.TempDir()
	first := newTestSourceMetadataCacheAt(t, client, storePath)
	if err := first.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := first.Current(time.Now())
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}

	restarted := newTestSourceMetadataCacheAt(t, client, storePath)
	request := httptest.NewRequest(http.MethodGet, active.VCT, nil)
	recorder := httptest.NewRecorder()
	restarted.ServeHTTP(recorder, request)
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("restored Type Metadata status = %d, want %d", got, want)
	}
	if digest := sha256.Sum256(recorder.Body.Bytes()); "sha256-"+base64.StdEncoding.EncodeToString(digest[:]) != active.VCTIntegrity {
		t.Fatal("restored Type Metadata bytes no longer match the credential integrity")
	}
}

func TestCacheRestartRejectsCorruptedTypeMetadata(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, payload), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, storePath)
	if err := cache.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	entries, err := os.ReadDir(storePath)
	if err != nil {
		t.Fatalf("read Type Metadata store: %v", err)
	}
	var storedName string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			storedName = entry.Name()
		}
	}
	if storedName == "" {
		t.Fatal("stored Type Metadata file is missing")
	}
	storedPath := storePath + "/" + storedName
	body, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("read stored Type Metadata: %v", err)
	}
	if err := os.WriteFile(storedPath, append(body, ' '), 0o644); err != nil {
		t.Fatalf("corrupt stored Type Metadata: %v", err)
	}
	_, err = newSourceMetadataCache(client, sourceMetadataConfig{
		URL: "https://source.example/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
	}, "https://issuer.example", storePath, sourceMetadataCachePolicy{
		MinimumValidity:  time.Hour,
		MaximumFreshness: 10 * time.Minute,
		StaleGrace:       5 * time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "filename integrity") {
		t.Fatalf("restart error = %v, want cache corruption rejection", err)
	}
}

func TestCacheRestartRejectsCorruptedActivationState(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, payload), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, storePath)
	if err := cache.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	entries, err := os.ReadDir(storePath)
	if err != nil {
		t.Fatalf("read Type Metadata store: %v", err)
	}
	var statePath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".state") {
			statePath = storePath + "/" + entry.Name()
		}
	}
	if statePath == "" {
		t.Fatal("activation state file is missing")
	}
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read activation state: %v", err)
	}
	var corrupted map[string]any
	if err := json.Unmarshal(body, &corrupted); err != nil {
		t.Fatalf("parse activation state: %v", err)
	}
	corrupted["source_version"] = "corrupted"
	body, err = json.Marshal(corrupted)
	if err != nil {
		t.Fatalf("marshal corrupted activation state: %v", err)
	}
	if err := os.WriteFile(statePath, body, 0o600); err != nil {
		t.Fatalf("corrupt activation state: %v", err)
	}
	_, err = newSourceMetadataCache(client, sourceMetadataConfig{
		URL: "https://source.example/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
	}, "https://issuer.example", storePath, sourceMetadataCachePolicy{
		MinimumValidity:  time.Hour,
		MaximumFreshness: 10 * time.Minute,
		StaleGrace:       5 * time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("restart error = %v, want activation state corruption rejection", err)
	}
}

func TestActivatedSourceFailsClosedWithoutActiveCache(t *testing.T) {
	runtime := newUnavailableSourceMetadataRuntime("99999999900000000200", "inkomensverklaring", t.TempDir(), io.ErrUnexpectedEOF)
	mux := newMux(config{}, http.DefaultClient, runtime)
	disclosure := `[{"attestations":[{"attributes":{"bsn":"123456789"}}]}]`
	request := httptest.NewRequest(http.MethodPost, "http://adapter/attestations/99999999900000000200/inkomensverklaring?jaar=2025", strings.NewReader(disclosure))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if got, want := recorder.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status = %d, want fail-closed %d", got, want)
	}
}

func TestActiveSourceRegistrationResolvesRuntimeBinding(t *testing.T) {
	activation := sourceActivation{
		SchemaVersion: "1.0",
		Source: sourceRegistration{
			SourceOIN: "99999999900000000200", Name: "Belastingdienst-mock",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: "gbo-metadata", Path: "/metadata/v1", GrantHash: "metadata-grant"},
			DataAccess:       sourceDataAccess{Transport: sourceTransportFSC, ServiceReference: "bri", GrantHash: "data-grant"},
		},
		Types: []activatedType{{
			TypeID: "inkomensverklaring", TypeVersion: "1.0",
			VCT:                   "https://issuer.example/types/99999999900000000200/inkomensverklaring/v1.0",
			VCTIntegrity:          "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
			TypeMetadataReference: "/metadata/type.json",
			Offers:                []sourceOffer{{ID: "inkomensverklaring_2025", Label: "Inkomensverklaring 2025", Parameters: map[string]any{"jaar": 2025}}},
		}, {
			TypeID: "jaaropgave", TypeVersion: "1.0",
			VCT:                   "https://issuer.example/types/99999999900000000200/jaaropgave/v1.0",
			VCTIntegrity:          "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
			TypeMetadataReference: "/metadata/jaaropgave.json",
			Offers:                []sourceOffer{{ID: "jaaropgave_2025", Label: "Jaaropgave 2025", Parameters: map[string]any{"jaar": 2025}}},
		}},
	}
	raw, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	activationPath := filepath.Join(t.TempDir(), "active.json")
	if err := os.WriteFile(activationPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := configsFromSourceActivation(config{SourceActivationPath: activationPath, OutwayURL: "https://outway.example"})
	if err != nil {
		t.Fatalf("resolve active source registration: %v", err)
	}
	if got, want := len(resolved), 2; got != want {
		t.Fatalf("runtime bindings = %d, want %d", got, want)
	}
	if got, want := resolved[0].SourceMetadataOIN, activation.Source.SourceOIN; got != want {
		t.Errorf("source OIN = %q, want %q", got, want)
	}
	if got, want := resolved[0].SourceMetadataTypeID, "inkomensverklaring"; got != want {
		t.Errorf("type ID = %q, want %q", got, want)
	}
	if got, want := resolved[1].SourceMetadataTypeID, "jaaropgave"; got != want {
		t.Errorf("second type ID = %q, want %q", got, want)
	}
	if got, want := resolved[0].SourceMetadataURL, "https://outway.example/metadata/v1"; got != want {
		t.Errorf("metadata URL = %q, want %q", got, want)
	}
	if got, want := resolved[0].SourceDataFSCServiceReference, "bri"; got != want {
		t.Errorf("data FSC service = %q, want %q", got, want)
	}
}

func TestUnavailableSourceStillServesPreviouslyPublishedTypeMetadata(t *testing.T) {
	payload := sourceMetadataCacheFixture(t)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, payload), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, storePath)
	if err := cache.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := cache.Current(time.Now())
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}
	mux := newRuntimeMux(context.Background(), config{
		TypeMetadataStorePath: storePath,
	}, http.DefaultClient)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, active.VCT, nil))
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("restored Type Metadata status = %d, want %d", got, want)
	}
}

func sourceMetadataCacheFixture(t *testing.T) []byte {
	t.Helper()
	payload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read source metadata fixture: %v", err)
	}
	return payload
}

func mutateSourceMetadataVersions(t *testing.T, payload []byte, sourceVersion, typeVersion string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode source metadata fixture: %v", err)
	}
	document["version"] = sourceVersion
	attestations := document["capabilities"].(map[string]any)["eudi"].(map[string]any)["attestations"].([]any)
	attestations[0].(map[string]any)["type_version"] = typeVersion
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode source metadata fixture: %v", err)
	}
	return mutated
}

func newTestSourceMetadataCache(t *testing.T, client *http.Client) *sourceMetadataCache {
	t.Helper()
	return newTestSourceMetadataCacheAt(t, client, t.TempDir())
}

func newTestSourceMetadataCacheAt(t *testing.T, client *http.Client, storePath string) *sourceMetadataCache {
	t.Helper()
	cache, err := newSourceMetadataCache(client, sourceMetadataConfig{
		URL: "https://source.example/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
	}, "https://issuer.example", storePath, sourceMetadataCachePolicy{
		MinimumValidity:  time.Hour,
		MaximumFreshness: 10 * time.Minute,
		StaleGrace:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("new source metadata cache: %v", err)
	}
	return cache
}

func metadataHTTPResponse(status int, etag string, body []byte) *http.Response {
	header := make(http.Header)
	header.Set("ETag", etag)
	header.Set("Content-Type", sourceMetadataMediaType)
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func jsonHTTPResponse(status int, body []byte) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
