package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The dashboard returns OTLP JSON; the portal reads Jaeger's trace shape.
func TestJaegerFromOTLP(t *testing.T) {
	const traceID = "0af7651916cd43dd8448eb211c80319c"
	body := `{"data":{"resourceSpans":[
	  {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"graphql-server"}}]},
	   "scopeSpans":[{"spans":[
	     {"traceId":"` + traceID + `","spanId":"b7ad6b7169203331","name":"POST /graphql",
	      "startTimeUnixNano":"2000000","endTimeUnixNano":"5000000",
	      "attributes":[{"key":"http.response.status_code","value":{"intValue":"500"}}],
	      "status":{"code":2}},
	     {"traceId":"` + traceID + `","spanId":"00f067aa0ba902b7","parentSpanId":"b7ad6b7169203331",
	      "name":"resolve","startTimeUnixNano":"3000000","endTimeUnixNano":"4000000"}]}]},
	  {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"logboek-bd"}}]},
	   "scopeSpans":[{"spans":[{"traceId":"` + traceID + `","spanId":"1111111111111111","name":"POST /logboek/records",
	      "startTimeUnixNano":"1000000","endTimeUnixNano":"1500000"}]}]}]}}`
	var resp dashboardTraceResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	trace := jaegerFromOTLP(traceID, resp.Data)
	if len(trace.Spans) != 3 || len(trace.Processes) != 2 {
		t.Fatalf("spans=%d processes=%d, want 3 and 2", len(trace.Spans), len(trace.Processes))
	}
	first := trace.Spans[0]
	if first.OperationName != "POST /logboek/records" || first.StartTime != 1000 || first.Duration != 500 {
		t.Fatalf("spans not ordered by start, or times not in microseconds: %+v", first)
	}
	if trace.Processes[first.ProcessID].ServiceName != "logboek-bd" {
		t.Fatalf("process = %+v", trace.Processes[first.ProcessID])
	}
	child := trace.Spans[2]
	if len(child.References) != 1 || child.References[0].SpanID != "b7ad6b7169203331" || child.References[0].RefType != "CHILD_OF" {
		t.Fatalf("parent reference = %+v", child.References)
	}
	errored := false
	for _, tag := range trace.Spans[1].Tags {
		if tag.Key == "error" && tag.Value == true {
			errored = true
		}
	}
	if !errored {
		t.Fatalf("an OTLP error status must become error=true: %+v", trace.Spans[1].Tags)
	}
}

// The engine detail comes from the PDP's log records, filtered on the FSC
// transaction id, with the API key on the request.
func TestDashboardDecisionsForTrace(t *testing.T) {
	line := func(tx, id string) string {
		b, _ := json.Marshal(map[string]any{
			"msg": "Decision Log", "decision_id": id,
			"input": map[string]any{"context": map[string]any{"headers": map[string]any{"fsc-transaction-id": tx}}},
		})
		return string(b)
	}
	var gotKey, gotResource string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotResource = r.URL.Query().Get("resource")
		now := time.Now().UnixNano()
		records := []map[string]any{
			{"timeUnixNano": json.Number(itoa(now)), "body": map[string]any{"stringValue": line("tx-1", "d2")}},
			{"timeUnixNano": json.Number(itoa(now - 1)), "body": map[string]any{"stringValue": line("tx-1", "d1")}},
			{"timeUnixNano": json.Number(itoa(now)), "body": map[string]any{"stringValue": line("tx-2", "d3")}},
			{"timeUnixNano": json.Number(itoa(now)), "body": map[string]any{"stringValue": `{"msg":"listening"}`}},
			{"timeUnixNano": json.Number(itoa(now)), "body": map[string]any{"stringValue": line("tx-1", "d2")}},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"resourceLogs": []any{
			map[string]any{"scopeLogs": []any{map[string]any{"logRecords": records}}},
		}}})
	}))
	defer srv.Close()

	cfg := config{TelemetryAPIURL: srv.URL, TelemetryAPIKey: "k", PdpResourceName: "openftv-pdp"}
	got, err := dashboardDecisionsForTrace(context.Background(), cfg, "tx-1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "k" || gotResource != "openftv-pdp" {
		t.Fatalf("request key=%q resource=%q", gotKey, gotResource)
	}
	if len(got) != 2 || got[0]["decision_id"] != "d1" || got[1]["decision_id"] != "d2" {
		t.Fatalf("decisions = %v, want d1 then d2 once", got)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
