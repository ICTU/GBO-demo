package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ldv-logboek/internal/ldv"
	"ldv-logboek/internal/sqlite"
)

const (
	writeToken = "test-token"
	readToken  = "test-read-token"
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
	return NewHandler(logbook, writeToken, readToken), repository
}

func validBody() map[string]any {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	return map[string]any{
		"trace_id":   "0af7651916cd43dd8448eb211c80319c",
		"span_id":    "b7ad6b7169203331",
		"name":       "bronquery.doorgifte",
		"status":     "OK",
		"start_time": start.Format(time.RFC3339Nano),
		"end_time":   start.Add(time.Millisecond).Format(time.RFC3339Nano),
		"resource":   map[string]string{"service.name": "bron-sidecar"},
		"attributes": map[string]any{
			ldv.AttrProcessingActivityID: "bd-bronquery-doorgifte@v1",
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
	body["attributes"].(map[string]any)[ldv.AttrProcessingActivityID] = "bd-ib-1999@v1"
	response := post(t, handler, writeToken, body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	var problem map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem["error"] != "invalid_record" {
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
func TestRegisterEntriesAreServedAtTheReferencedURI(t *testing.T) {
	handler, _ := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/verwerkingsactiviteiten/bd-ib-2025@v1", nil)
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

	request := httptest.NewRequest(http.MethodGet, "/verwerkingsactiviteiten/bd-ib-1999@v1", nil)
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

// get issues an authenticated read.
func get(t *testing.T, handler *Handler, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The read extension answers on the three axes LDV names.
func TestReadRecordsByEachSelector(t *testing.T) {
	handler, _ := newTestHandler(t)
	if response := post(t, handler, writeToken, validBody()); response.Code != http.StatusCreated {
		t.Fatalf("seed write status = %d", response.Code)
	}

	for name, path := range map[string]string{
		"by trace":    "/logboek/records?traceID=0af7651916cd43dd8448eb211c80319c",
		"by activity": "/logboek/records?processingActivityID=bd-bronquery-doorgifte@v1",
		"by subject":  "/logboek/records?dataSubjectId=PI-abc123&dataSubjectIdType=pi",
	} {
		t.Run(name, func(t *testing.T) {
			response := get(t, handler, readToken, path)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body)
			}
			var payload struct {
				Verantwoordelijke string       `json:"verantwoordelijke"`
				Records           []ldv.Stored `json:"records"`
				Truncated         bool         `json:"truncated"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(payload.Records) != 1 {
				t.Fatalf("read %d records, want 1: %s", len(payload.Records), response.Body)
			}
			if payload.Verantwoordelijke != "Belastingdienst" {
				t.Errorf("verantwoordelijke = %q", payload.Verantwoordelijke)
			}
			if payload.Records[0].Attribute(ldv.AttrDataSubjectID) != "PI-abc123" {
				t.Errorf("record did not round-trip: %#v", payload.Records[0])
			}
		})
	}
}

// A read that names no axis would be a request to browse the logbook.
func TestReadWithoutASelectorIs400(t *testing.T) {
	handler, _ := newTestHandler(t)

	response := get(t, handler, readToken, "/logboek/records")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	var problem map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem["error"] != "no_selector" {
		t.Fatalf("problem = %#v", problem)
	}
}

// Reading and writing are different capabilities, so the write token does not
// open the read extension.
func TestReadRequiresItsOwnToken(t *testing.T) {
	handler, _ := newTestHandler(t)

	for name, token := range map[string]string{
		"no token":    "",
		"write token": writeToken,
		"wrong token": "guessed",
	} {
		t.Run(name, func(t *testing.T) {
			response := get(t, handler, token, "/logboek/records?traceID=0af7651916cd43dd8448eb211c80319c")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
}

func TestReadRejectsANonsenseLimit(t *testing.T) {
	handler, _ := newTestHandler(t)

	response := get(t, handler, readToken, "/logboek/records?traceID=0af7651916cd43dd8448eb211c80319c&limit=nope")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// A reader must never be silently handed a partial logbook.
func TestReadSaysWhenTheCapTruncated(t *testing.T) {
	handler, _ := newTestHandler(t)

	for _, spanID := range []string{"b7ad6b7169203331", "00f067aa0ba902b7"} {
		body := validBody()
		body["span_id"] = spanID
		if response := post(t, handler, writeToken, body); response.Code != http.StatusCreated {
			t.Fatalf("seed write status = %d", response.Code)
		}
	}

	response := get(t, handler, readToken, "/logboek/records?traceID=0af7651916cd43dd8448eb211c80319c&limit=1")
	var payload struct {
		Records   []ldv.Stored `json:"records"`
		Truncated bool         `json:"truncated"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Records) != 1 || !payload.Truncated {
		t.Fatalf("records = %d, truncated = %v; want 1 and true", len(payload.Records), payload.Truncated)
	}
}
