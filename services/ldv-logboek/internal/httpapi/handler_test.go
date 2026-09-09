package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ldv-logboek/internal/ldv"
	"ldv-logboek/internal/sqlite"
)

const (
	writeToken = "test-token"
	readToken  = "test-read-token"
	// The read API identifies itself by its own URI, which is what a
	// record's dpl.read.nextLogbookId points at.
	testLogbookID = "https://logboek.belastingdienst.nl/data-processing-operations"
)

func newTestHandler(t *testing.T) (*Handler, *sqlite.Repository) {
	t.Helper()
	register, err := ldv.LoadRegister("../../config/verwerkingsactiviteiten-bd.json")
	if err != nil {
		t.Fatalf("load register: %v", err)
	}
	repository, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	logbook, err := ldv.NewLogbook(repository, register, time.Now)
	if err != nil {
		t.Fatalf("wire logbook: %v", err)
	}
	return NewHandler(logbook, writeToken, readToken, testLogbookID), repository
}

func validBody() map[string]any {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	return map[string]any{
		"trace_id": "0af7651916cd43dd8448eb211c80319c",
		"span_id":  "b7ad6b7169203331",
		"name":     "bronquery.doorgifte",
		"status":   "OK",
		// Wire format: epoch milliseconds, and resource nested under
		// attributes (§3.2.2.5-6, §3.2.2.8).
		"start_time": start.UnixMilli(),
		"end_time":   start.Add(time.Millisecond).UnixMilli(),
		"resource":   map[string]any{"attributes": map[string]any{"service.name": "bron-sidecar"}},
		"attributes": map[string]any{
			ldv.AttrProcessingActivityID: "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-bronquery-doorgifte/v1",
			ldv.AttrDataSubjectID:        "PI-abc123",
			ldv.AttrDataSubjectIDType:    "pi",
		},
	}
}

func post(t *testing.T, handler *Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/logboek/records", bytes.NewReader(encoded))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestWriteRecordConfirmsWithTheStoredIdentity(t *testing.T) {
	handler, repository := newTestHandler(t)

	response := post(t, handler, writeToken, validBody())
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var confirmation ldv.Confirmation
	if err := json.Unmarshal(response.Body.Bytes(), &confirmation); err != nil {
		t.Fatalf("decode confirmation: %v", err)
	}
	if confirmation.SpanID != "b7ad6b7169203331" || confirmation.ReceivedAt.IsZero() {
		t.Fatalf("confirmation = %#v", confirmation)
	}
	// The confirmation must not run ahead of the store.
	count, err := repository.Count(t.Context())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestWriteRecordReportsAReplayAsAlreadyStored(t *testing.T) {
	handler, _ := newTestHandler(t)

	if response := post(t, handler, writeToken, validBody()); response.Code != http.StatusCreated {
		t.Fatalf("first write status = %d", response.Code)
	}
	response := post(t, handler, writeToken, validBody())
	if response.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", response.Code)
	}
	var confirmation ldv.Confirmation
	if err := json.Unmarshal(response.Body.Bytes(), &confirmation); err != nil {
		t.Fatalf("decode confirmation: %v", err)
	}
	if !confirmation.Duplicate {
		t.Fatal("a replay must be flagged as a duplicate")
	}
}

func TestWriteRecordRequiresTheToken(t *testing.T) {
	handler, repository := newTestHandler(t)

	for name, token := range map[string]string{"no token": "", "wrong token": "guessed"} {
		t.Run(name, func(t *testing.T) {
			if response := post(t, handler, token, validBody()); response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
	count, err := repository.Count(t.Context())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("an unauthorized write reached the store")
	}
}

// A record the logbook refuses is a defect in the producer, not a transient
// failure, so it gets 422 and the producer is told why.
func TestWriteRecordRejectsAnUnlawfulRecordWith422(t *testing.T) {
	handler, _ := newTestHandler(t)

	body := validBody()
	body["attributes"].(map[string]any)[ldv.AttrProcessingActivityID] = "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-1999/v1"
	response := post(t, handler, writeToken, body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	// problem+json carries a numeric status alongside the code.
	var problem map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem["error"] != "invalid_record" || problem["status"] != float64(422) {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestWriteRecordRejectsMalformedJSONWith400(t *testing.T) {
	handler, _ := newTestHandler(t)

	request := httptest.NewRequest(http.MethodPost, "/logboek/records", bytes.NewReader([]byte("{not json")))
	request.Header.Set("Authorization", "Bearer "+writeToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// An unrecognised field is a producer that thinks it is sending something the
// logbook stores. Silently dropping it would be worse than refusing.
func TestWriteRecordRejectsUnknownFields(t *testing.T) {
	handler, _ := newTestHandler(t)

	body := validBody()
	body["severity_text"] = "INFO"
	if response := post(t, handler, writeToken, body); response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// Every record names a verwerkingsactiviteit; that reference has to resolve
// somewhere, and this is the somewhere.
func TestRegisterEntriesAreServedAtTheURITheRecordsCarry(t *testing.T) {
	handler, _ := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/verwerkingsactiviteiten/bd-ib-2025/v1", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var payload struct {
		Verantwoordelijke     string       `json:"verantwoordelijke"`
		Disclaimer            string       `json:"disclaimer"`
		Verwerkingsactiviteit ldv.Activity `json:"verwerkingsactiviteit"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Verwerkingsactiviteit.ScopeID != "bd:ib:2025" {
		t.Fatalf("entry = %#v", payload.Verwerkingsactiviteit)
	}
	if payload.Disclaimer == "" {
		t.Fatal("every served entry must carry the not-an-RvVA disclaimer")
	}
}

func TestUnknownRegisterEntryIs404(t *testing.T) {
	handler, _ := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/verwerkingsactiviteiten/bd-ib-1999/v1", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestHealthIsUnauthenticated(t *testing.T) {
	handler, _ := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

// read issues an authenticated read against the standard endpoint.
func read(t *testing.T, handler *Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/data-processing-operations", bytes.NewReader(encoded))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The read extension is POST /data-processing-operations with a body, not a
// query string of our own devising, and it answers in its own vocabulary:
// camelCase names, RFC 3339 times, Ok/Error/Unset.
func TestReadExtensionAnswersInItsOwnShape(t *testing.T) {
	handler, _ := newTestHandler(t)
	if response := post(t, handler, writeToken, validBody()); response.Code != http.StatusCreated {
		t.Fatalf("seed write status = %d, body = %s", response.Code, response.Body)
	}

	response := read(t, handler, readToken, map[string]any{"traceId": "0af76519-16cd-43dd-8448-eb211c80319c"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var payload struct {
		Metadata struct {
			LogbookID        string `json:"logbookId"`
			OrganizationName string `json:"organizationName"`
		} `json:"metadata"`
		Operations []map[string]any `json:"dataProcessingOperations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Metadata.LogbookID != testLogbookID || payload.Metadata.OrganizationName != "Belastingdienst" {
		t.Fatalf("metadata = %#v", payload.Metadata)
	}
	if len(payload.Operations) != 1 {
		t.Fatalf("read %d operations, want 1: %s", len(payload.Operations), response.Body)
	}
	operation := payload.Operations[0]
	// The read side names fields in camelCase, unlike the write side.
	for _, field := range []string{"traceId", "spanId", "status", "name", "startTime", "endTime"} {
		if _, present := operation[field]; !present {
			t.Errorf("field %q missing: %s", field, response.Body)
		}
	}
	// RFC 3339 here, milliseconds on the write side. The standard's own
	// difference, followed per side rather than unified.
	if _, err := time.Parse(time.RFC3339Nano, operation["startTime"].(string)); err != nil {
		t.Errorf("startTime is not RFC 3339: %v", operation["startTime"])
	}
	if operation["status"] != "Ok" {
		t.Errorf("status = %v, want the read extension's Ok", operation["status"])
	}
}

// The three selectors the standard names, one of which must be present.
func TestReadExtensionSelectors(t *testing.T) {
	handler, _ := newTestHandler(t)
	if response := post(t, handler, writeToken, validBody()); response.Code != http.StatusCreated {
		t.Fatalf("seed write status = %d", response.Code)
	}

	for name, body := range map[string]any{
		"by trace":      map[string]any{"traceId": "0af7651916cd43dd8448eb211c80319c"},
		"by trace uuid": map[string]any{"traceId": "0af76519-16cd-43dd-8448-eb211c80319c"},
		// The shape the schema specifies: allOf over Attributes, so dpl sits
		// at the top level of the request body — not under an `attributes`
		// wrapper, which is where a *response* puts it.
		"by activity, as the schema defines it": map[string]any{"dpl": map[string]any{"core": map[string]any{
			"processingActivityId": "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-bronquery-doorgifte/v1",
		}}},
		"by subject, as the schema defines it": map[string]any{"dpl": map[string]any{"core": map[string]any{
			"dataSubjectId": "PI-abc123", "dataSubjectIdType": "pi",
		}}},
		// Tolerated: a caller holding a record's own attributes should not
		// have to reshape them to ask about it.
		"by activity, flat attributes": map[string]any{"attributes": map[string]any{
			ldv.AttrProcessingActivityID: "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-bronquery-doorgifte/v1",
		}},
		"by subject, flat attributes": map[string]any{"attributes": map[string]any{
			ldv.AttrDataSubjectID: "PI-abc123", ldv.AttrDataSubjectIDType: "pi",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			response := read(t, handler, readToken, body)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body)
			}
			var payload struct {
				Operations []map[string]any `json:"dataProcessingOperations"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &payload)
			if len(payload.Operations) != 1 {
				t.Fatalf("read %d operations, want 1", len(payload.Operations))
			}
		})
	}
}

// "A request where all three these parameters are missing MUST result in an
// HTTP 400 Bad Request", and the error is problem+json.
func TestReadExtensionWithoutASelectorIs400ProblemJSON(t *testing.T) {
	handler, _ := newTestHandler(t)

	response := read(t, handler, readToken, map[string]any{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var problem map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem["status"] != float64(400) || problem["title"] == nil {
		t.Errorf("problem = %#v", problem)
	}
}

func TestReadExtensionRequiresItsOwnToken(t *testing.T) {
	handler, _ := newTestHandler(t)

	for name, token := range map[string]string{
		"no token":    "",
		"write token": writeToken,
		"wrong token": "guessed",
	} {
		t.Run(name, func(t *testing.T) {
			response := read(t, handler, token, map[string]any{"traceId": "0af7651916cd43dd8448eb211c80319c"})
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
}

// An unconfigured token must authorize nobody. This is the regression test for
// a bypass: an empty server-side token matched a request that sent no
// Authorization header, because ConstantTimeCompare found two empty strings
// equal — so a logbook started without LDV_READ_TOKEN served every record to
// anyone, while logging that it would refuse every request.
func TestAnUnconfiguredTokenAuthorizesNobody(t *testing.T) {
	register, err := ldv.LoadRegister("../../config/verwerkingsactiviteiten-bd.json")
	if err != nil {
		t.Fatalf("load register: %v", err)
	}
	repository, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	logbook, err := ldv.NewLogbook(repository, register, time.Now)
	if err != nil {
		t.Fatalf("wire logbook: %v", err)
	}
	handler := NewHandler(logbook, "", "", testLogbookID)

	for name, request := range map[string]*http.Request{
		"read, no header":  httptest.NewRequest(http.MethodPost, "/data-processing-operations", strings.NewReader(`{"traceId":"0af7651916cd43dd8448eb211c80319c"}`)),
		"write, no header": httptest.NewRequest(http.MethodPost, "/logboek/records", strings.NewReader("{}")),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 — an absent credential is not a credential", recorder.Code)
			}
		})
	}
}

// The request and the response are asymmetric, and following the response's
// shape on both sides made an official request fail on an unknown field. This
// drives the real handler rather than the translation function, which is what
// let that through.
func TestTheSchemasRequestShapeIsAcceptedEndToEnd(t *testing.T) {
	handler, _ := newTestHandler(t)
	if response := post(t, handler, writeToken, validBody()); response.Code != http.StatusCreated {
		t.Fatalf("seed write status = %d", response.Code)
	}

	response := read(t, handler, readToken, map[string]any{
		"dpl": map[string]any{"core": map[string]any{
			"dataSubjectId":     "PI-abc123",
			"dataSubjectIdType": "pi",
		}},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var payload struct {
		Operations []map[string]any `json:"dataProcessingOperations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Operations) != 1 {
		t.Fatalf("read %d operations, want 1: %s", len(payload.Operations), response.Body)
	}
}

// A body that carries only the narrowing fields still names no selector.
func TestATimeWindowAloneIsNotASelector(t *testing.T) {
	handler, _ := newTestHandler(t)

	response := read(t, handler, readToken, map[string]any{
		"startTime": "2026-09-02T00:00:00Z",
		"endTime":   "2026-09-03T00:00:00Z",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — a window alone would return every Betrokkene in it", response.Code)
	}
}
