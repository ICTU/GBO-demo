package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSourceMetadataCacheRefreshesConditionallyAndExpiresFailClosed(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 && request.Header.Get("If-None-Match") != `"source-v1"` {
			t.Errorf("If-None-Match = %q, want source ETag", request.Header.Get("If-None-Match"))
		}
		if requests == 2 {
			return metadataHTTPResponse(http.StatusNotModified, `"source-v1"`, nil), nil
		}
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, []byte(compact)), nil
	})}
	cache := newTestSourceMetadataCache(t, client, publicJWK)
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
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("If-None-Match"); got != "" {
			t.Errorf("unconditional refresh sent If-None-Match %q", got)
		}
		return metadataHTTPResponse(http.StatusOK, "", []byte(compact)), nil
	})}
	cache := newTestSourceMetadataCache(t, client, publicJWK)
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
			payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
			changed := bytes.Replace(payload, []byte(`"Inkomensverklaring"`), []byte(`"Gewijzigde inkomensverklaring"`), 1)
			responses := [][]byte{
				[]byte(signSourceMetadataForTest(t, payload, privateKey)),
				[]byte(signSourceMetadataForTest(t, changed, privateKey)),
			}
			requestIndex := 0
			client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				body := responses[requestIndex]
				requestIndex++
				return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
			})}
			storePath := t.TempDir()
			cache := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
			now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			if err := cache.Refresh(context.Background(), now); err != nil {
				t.Fatalf("initial refresh: %v", err)
			}
			if restart {
				cache = newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
			}
			if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "changed bytes") {
				t.Fatalf("changed same-version refresh error = %v", err)
			}
		})
	}
}

func TestSourceMetadataCacheKeepsOlderTypeVersionReachable(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	nextPayload := mutateSourceMetadataVersions(t, payload, "1.1.0", "1.1")
	responses := [][]byte{
		[]byte(signSourceMetadataForTest(t, payload, privateKey)),
		[]byte(signSourceMetadataForTest(t, nextPayload, privateKey)),
	}
	requestIndex := 0
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := responses[requestIndex]
		requestIndex++
		return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
	})}
	cache := newTestSourceMetadataCache(t, client, publicJWK)
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
			payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
			nextPayload := mutateSourceMetadataVersions(t, payload, "1.1.0", "1.1")
			responses := [][]byte{
				[]byte(signSourceMetadataForTest(t, payload, privateKey)),
				[]byte(signSourceMetadataForTest(t, nextPayload, privateKey)),
				[]byte(signSourceMetadataForTest(t, payload, privateKey)),
			}
			requestIndex := 0
			client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				body := responses[requestIndex]
				requestIndex++
				return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
			})}
			storePath := t.TempDir()
			cache := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
			now := time.Now()
			if err := cache.Refresh(context.Background(), now); err != nil {
				t.Fatalf("initial refresh: %v", err)
			}
			if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err != nil {
				t.Fatalf("forward refresh: %v", err)
			}
			if restart {
				cache = newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
			}
			if err := cache.Refresh(context.Background(), now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "rollback") {
				t.Fatalf("rollback error = %v, want rollback rejection", err)
			}
		})
	}
}

func TestSourceMetadataCacheRejectsTypeRollbackAfterRestart(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	nextPayload := mutateSourceMetadataVersions(t, payload, "1.1.0", "1.1")
	typeRollback := mutateSourceMetadataVersions(t, payload, "1.2.0", "1.0")
	responses := [][]byte{
		[]byte(signSourceMetadataForTest(t, payload, privateKey)),
		[]byte(signSourceMetadataForTest(t, nextPayload, privateKey)),
		[]byte(signSourceMetadataForTest(t, typeRollback, privateKey)),
	}
	requestIndex := 0
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := responses[requestIndex]
		requestIndex++
		return metadataHTTPResponse(http.StatusOK, `"source-refresh"`, body), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
	now := time.Now()
	if err := cache.Refresh(context.Background(), now); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if err := cache.Refresh(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatalf("forward refresh: %v", err)
	}
	restarted := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
	if err := restarted.Refresh(context.Background(), now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "type metadata version rollback") {
		t.Fatalf("type rollback error = %v, want type metadata rollback rejection", err)
	}
}

func TestAdapterActivatesOnlyCachedTypeAndBindsIssuableDocument(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "source.example":
			return metadataHTTPResponse(http.StatusOK, `"source-v1"`, []byte(compact)), nil
		case "outway.example":
			return jsonHTTPResponse(http.StatusOK, []byte(completeIncomeResponse)), nil
		default:
			t.Fatalf("unexpected outbound host %q", request.URL.Host)
			return nil, nil
		}
	})}
	cache := newTestSourceMetadataCache(t, client, publicJWK)
	if err := cache.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := cache.Current(time.Now())
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}

	uc := Usecase{
		AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
		Scope:           "bd:ib:2025",
		Belastingjaren:  []int{2025},
		OutwayPath:      "/bri/graphql",
	}
	mux := newMux(config{OutwayURL: "https://outway.example"}, &Catalog{Usecases: map[string]Usecase{
		"inkomensverklaring_2025": uc,
	}}, client, cache)

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
	issuanceRequest := httptest.NewRequest(http.MethodPost, "http://adapter/inkomensverklaring_2025/", strings.NewReader(disclosure))
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
	if got := documents[0].Attributes["vct#integrity"]; got != active.VCTIntegrity {
		t.Errorf("vct#integrity = %v, want %q", got, active.VCTIntegrity)
	}
}

func TestPublishedTypeMetadataSurvivesCacheRestart(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, []byte(compact)), nil
	})}
	storePath := t.TempDir()
	first := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
	if err := first.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := first.Current(time.Now())
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}

	restarted := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
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
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, []byte(compact)), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
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
		URL:         "https://source.example/.well-known/gbo-attestations",
		ExpectedOIN: "99999999900000000200",
		PublicJWK:   publicJWK,
		TypeID:      "inkomensverklaring",
	}, "inkomensverklaring_2025", "https://issuer.example", storePath, sourceMetadataCachePolicy{
		MinimumValidity:  time.Hour,
		MaximumFreshness: 10 * time.Minute,
		StaleGrace:       5 * time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "filename integrity") {
		t.Fatalf("restart error = %v, want cache corruption rejection", err)
	}
}

func TestCacheRestartRejectsCorruptedActivationState(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, []byte(compact)), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
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
	body = bytes.Replace(body, []byte(`"source_version":"1.0.0"`), []byte(`"source_version":"9.0.0"`), 1)
	if err := os.WriteFile(statePath, body, 0o600); err != nil {
		t.Fatalf("corrupt activation state: %v", err)
	}
	_, err = newSourceMetadataCache(client, sourceMetadataConfig{
		URL:         "https://source.example/.well-known/gbo-attestations",
		ExpectedOIN: "99999999900000000200",
		PublicJWK:   publicJWK,
		TypeID:      "inkomensverklaring",
	}, "inkomensverklaring_2025", "https://issuer.example", storePath, sourceMetadataCachePolicy{
		MinimumValidity:  time.Hour,
		MaximumFreshness: 10 * time.Minute,
		StaleGrace:       5 * time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("restart error = %v, want activation state corruption rejection", err)
	}
}

func TestConfiguredMetadataUsecaseFailsClosedWithoutActiveCache(t *testing.T) {
	runtime := newUnavailableSourceMetadataRuntime("inkomensverklaring_2025", t.TempDir(), io.ErrUnexpectedEOF)
	uc := Usecase{
		AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
		Belastingjaren:  []int{2025},
		OutwayPath:      "/bri/graphql",
	}
	mux := newMux(config{}, &Catalog{Usecases: map[string]Usecase{
		"inkomensverklaring_2025": uc,
	}}, http.DefaultClient, runtime)
	disclosure := `[{"attestations":[{"attributes":{"bsn":"123456789"}}]}]`
	request := httptest.NewRequest(http.MethodPost, "http://adapter/inkomensverklaring_2025/", strings.NewReader(disclosure))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if got, want := recorder.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status = %d, want fail-closed %d", got, want)
	}
}

func TestUnavailableSourceStillServesPreviouslyPublishedTypeMetadata(t *testing.T) {
	payload, publicJWK, privateKey := sourceMetadataCacheFixture(t)
	compact := signSourceMetadataForTest(t, payload, privateKey)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return metadataHTTPResponse(http.StatusOK, `"source-v1"`, []byte(compact)), nil
	})}
	storePath := t.TempDir()
	cache := newTestSourceMetadataCacheAt(t, client, publicJWK, storePath)
	if err := cache.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	active, err := cache.Current(time.Now())
	if err != nil {
		t.Fatalf("current metadata: %v", err)
	}
	mux := newRuntimeMux(context.Background(), config{
		SourceMetadataCacheEnabled: true,
		SourceMetadataUsecaseKey:   "inkomensverklaring_2025",
		TypeMetadataStorePath:      storePath,
	}, &Catalog{Usecases: map[string]Usecase{}}, http.DefaultClient)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, active.VCT, nil))
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("restored Type Metadata status = %d, want %d", got, want)
	}
}

func sourceMetadataCacheFixture(t *testing.T) ([]byte, json.RawMessage, ed25519.PrivateKey) {
	t.Helper()
	payload, err := os.ReadFile("../graphql-server/config/gbo-attestations.json")
	if err != nil {
		t.Fatalf("read source metadata fixture: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	publicJWK, err := json.Marshal(map[string]string{
		"kty": "OKP",
		"crv": "Ed25519",
		"x":   testBase64URL.EncodeToString(publicKey),
	})
	if err != nil {
		t.Fatalf("marshal public JWK: %v", err)
	}
	return payload, publicJWK, privateKey
}

func mutateSourceMetadataVersions(t *testing.T, payload []byte, sourceVersion, typeVersion string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode source metadata fixture: %v", err)
	}
	document["version"] = sourceVersion
	attestations := document["attestations"].([]any)
	attestations[0].(map[string]any)["type_version"] = typeVersion
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode source metadata fixture: %v", err)
	}
	return mutated
}

func newTestSourceMetadataCache(t *testing.T, client *http.Client, publicJWK json.RawMessage) *sourceMetadataCache {
	t.Helper()
	return newTestSourceMetadataCacheAt(t, client, publicJWK, t.TempDir())
}

func newTestSourceMetadataCacheAt(t *testing.T, client *http.Client, publicJWK json.RawMessage, storePath string) *sourceMetadataCache {
	t.Helper()
	cache, err := newSourceMetadataCache(client, sourceMetadataConfig{
		URL:         "https://source.example/.well-known/gbo-attestations",
		ExpectedOIN: "99999999900000000200",
		PublicJWK:   publicJWK,
		TypeID:      "inkomensverklaring",
	}, "inkomensverklaring_2025", "https://issuer.example", storePath, sourceMetadataCachePolicy{
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
