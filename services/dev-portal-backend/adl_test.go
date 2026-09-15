package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestLoadConfigReadsTheADLDatabaseURL(t *testing.T) {
	const url = "postgres://adl_reader:pw@postgres-ftv:5432/ftv_adl?sslmode=disable"
	t.Setenv("ADL_DATABASE_URL", url)

	if got := loadConfig().ADLDatabaseURL; got != url {
		t.Fatalf("ADLDatabaseURL = %q, want %q", got, url)
	}
}

// A denial's reason is read from the recorded AuthZEN response, where
// OpenFTV puts the policy's reason code: context.reason_user.en.
func TestADLRecordReadsDecisionAndReasonFromTheResponse(t *testing.T) {
	rec, err := adlRecordFromRow(adlRow{
		Timestamp: 1757240058042,
		TraceID:   ptr("28dbeec32e77635cc19bc3204ec56c41"),
		SpanID:    ptr("5e3c8a4f9b2d1e07"),
		EventName: "adl.access_evaluation",
		Status:    "Ok",
		Body: []byte(`{
			"adl.core.request": {"subject": {"type": "identity", "id": "99999999900000000300"}},
			"adl.core.response": {"decision": false, "context": {"id": "not-authorized", "reason_user": {"en": "CONSENT_WITHDRAWN"}}}
		}`),
		Attributes: []byte(`{"adl.fsc.transaction_id": "tx-1"}`),
		MatchedOn:  "adl.fsc.transaction_id",
	})
	if err != nil {
		t.Fatalf("adlRecordFromRow: %v", err)
	}
	if rec.Decision == nil || *rec.Decision {
		t.Fatalf("decision = %v, want false", rec.Decision)
	}
	if rec.Reason != "CONSENT_WITHDRAWN" {
		t.Errorf("reason = %q, want CONSENT_WITHDRAWN", rec.Reason)
	}
	if rec.FscTransactionID != "tx-1" {
		t.Errorf("fsc_transaction_id = %q, want tx-1", rec.FscTransactionID)
	}
	if len(rec.Request) == 0 || len(rec.Response) == 0 {
		t.Error("request and response must be served as recorded")
	}
}

// On an allow OpenFTV answers reason_user.en "ok"; that is not a reason.
func TestADLRecordAllowCarriesNoReason(t *testing.T) {
	rec, err := adlRecordFromRow(adlRow{
		EventName: "adl.access_evaluation",
		Status:    "Ok",
		Body:      []byte(`{"adl.core.response": {"decision": true, "context": {"id": "ok", "reason_user": {"en": "ok"}}}}`),
	})
	if err != nil {
		t.Fatalf("adlRecordFromRow: %v", err)
	}
	if rec.Decision == nil || !*rec.Decision {
		t.Fatalf("decision = %v, want true", rec.Decision)
	}
	if rec.Reason != "" {
		t.Errorf("reason = %q, want none on an allow", rec.Reason)
	}
}

type fakeADL struct {
	records []adlRecord
	gotTxID string
}

func (f *fakeADL) DecisionsForTransaction(_ context.Context, txID string, _ time.Time) ([]adlRecord, error) {
	f.gotTxID = txID
	return f.records, nil
}

// lokiWithDecision answers a query_range with one console decision-log line
// for the given transaction.
func lokiWithDecision(t *testing.T, txID string) *httptest.Server {
	t.Helper()
	line, _ := json.Marshal(map[string]any{
		"decision_id": "d-1",
		"path":        "authz",
		"input":       map[string]any{"context": map[string]any{"trace_id": txID}},
		"result": map[string]any{
			"allow":  false,
			"reason": "NO_APPLICABLE_RULE",
			"response": map[string]any{
				"decision": false,
				"context": map[string]any{
					"reason_admin":  map[string]any{"code": "NO_APPLICABLE_RULE"},
					"denied_fields": []any{map[string]any{"field": "Query.x.box2Inkomen", "code": "NO_APPLICABLE_RULE"}},
				},
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"result": []any{
				map[string]any{"values": [][2]string{{"1", string(line)}}},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getDecisions(t *testing.T, srv *httptest.Server, query string) (int, decisionsResponse) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/decisions" + query)
	if err != nil {
		t.Fatalf("GET /decisions: %v", err)
	}
	defer resp.Body.Close()
	var out decisionsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// The ADL record and the engine's console entry are served side by side,
// never merged: the first is the record of the decision, the second is
// observability.
func TestDecisionsServesAuditAndEngineSeparately(t *testing.T) {
	adl := &fakeADL{records: []adlRecord{{EventName: "adl.access_evaluation", Decision: ptr(false), Reason: "NO_APPLICABLE_RULE"}}}
	cfg := config{VarDir: t.TempDir(), PredefinedDir: t.TempDir(), LokiURL: lokiWithDecision(t, "tx-1").URL}
	srv := httptest.NewServer(newMux(cfg, newTraceHub(time.Minute), adl))
	defer srv.Close()

	status, out := getDecisions(t, srv, "?transaction_id=tx-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if adl.gotTxID != "tx-1" {
		t.Errorf("ADL queried for %q, want tx-1", adl.gotTxID)
	}
	if len(out.Audit.Records) != 1 || out.Audit.Records[0].Reason != "NO_APPLICABLE_RULE" {
		t.Errorf("audit = %+v, want the ADL record", out.Audit)
	}
	if len(out.Engine.Decisions) != 1 {
		t.Fatalf("engine decisions = %d, want 1", len(out.Engine.Decisions))
	}
	result, _ := out.Engine.Decisions[0]["result"].(map[string]any)
	if _, ok := result["context"].(map[string]any)["denied_fields"]; !ok {
		t.Errorf("engine entry lost the per-field detail: %v", result)
	}
}

// Without an ADL the audit block says so, and the engine detail still shows.
func TestDecisionsReportsAnUnconfiguredADL(t *testing.T) {
	cfg := config{VarDir: t.TempDir(), PredefinedDir: t.TempDir(), LokiURL: lokiWithDecision(t, "tx-1").URL}
	srv := httptest.NewServer(newMux(cfg, newTraceHub(time.Minute), nil))
	defer srv.Close()

	_, out := getDecisions(t, srv, "?transaction_id=tx-1")
	if out.Audit.Error == "" {
		t.Error("audit reported no error without an ADL")
	}
	if len(out.Engine.Decisions) != 1 {
		t.Errorf("engine decisions = %d, want 1", len(out.Engine.Decisions))
	}
}

// part=audit answers from the ADL without touching Loki, so the decision of
// record does not wait on an observability store.
func TestDecisionsAuditPartSkipsLoki(t *testing.T) {
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("part=audit queried Loki")
	}))
	defer loki.Close()
	adl := &fakeADL{records: []adlRecord{{EventName: "adl.access_evaluation", Decision: ptr(true)}}}
	cfg := config{VarDir: t.TempDir(), PredefinedDir: t.TempDir(), LokiURL: loki.URL}
	srv := httptest.NewServer(newMux(cfg, newTraceHub(time.Minute), adl))
	defer srv.Close()

	status, out := getDecisions(t, srv, "?transaction_id=tx-1&part=audit")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(out.Audit.Records) != 1 {
		t.Errorf("audit records = %d, want 1", len(out.Audit.Records))
	}
	if len(out.Engine.Decisions) != 0 || out.Engine.Error != "" {
		t.Errorf("engine = %+v, want an untouched empty part", out.Engine)
	}
}

func TestDecisionsRequiresATransactionID(t *testing.T) {
	cfg := config{VarDir: t.TempDir(), PredefinedDir: t.TempDir()}
	srv := httptest.NewServer(newMux(cfg, newTraceHub(time.Minute), nil))
	defer srv.Close()

	if status, _ := getDecisions(t, srv, ""); status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}
