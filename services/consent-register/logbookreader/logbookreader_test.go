package logbookreader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"consent-register/uses"
)

const trace = "4bf92f3577b34da6a3ce929d0e0e4736"

func TestReadSendsOneTraceIDToTheGrantLink(t *testing.T) {
	var path string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"metadata":{"logbookId":"x","organizationName":"Belastingdienst"},"dataProcessingOperations":[
			{"traceId":"` + trace + `","spanId":"00f067aa0ba902b7","name":"n","status":"Ok","startTime":"2026-11-19T09:00:00.5Z","endTime":"2026-11-19T09:00:01Z"},
			{"traceId":"00000000000000000000000000000001","spanId":"00f067aa0ba902b8","name":"n","status":"Ok","startTime":"2026-11-19T09:00:00Z","endTime":"2026-11-19T09:00:01Z"}]}`))
	}))
	defer server.Close()

	client := Client{Services: map[string]string{"99999999900000000200/ldv-lezen-bd": server.URL + "/ldv-lezen-bd"}, HTTP: server.Client()}
	records, err := client.Read(context.Background(), "99999999900000000200/ldv-lezen-bd", trace)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/ldv-lezen-bd/data-processing-operations" || len(body) != 1 || body["traceId"] != trace {
		t.Errorf("path %s, body %v", path, body)
	}
	if len(records) != 1 || records[0].StartTime.Nanosecond() != 500000000 {
		t.Errorf("records = %+v", records)
	}
}

func TestAnUnknownLogbookHasNoReadService(t *testing.T) {
	if _, err := (Client{}).Read(context.Background(), "99999999900000000200/bd", trace); !errors.Is(err, uses.ErrNoReadService) {
		t.Fatalf("err = %v", err)
	}
}

// A refusal by the source's PDP comes back from the Inway as 401.
func TestARefusalIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Fsc-Error-Code", "UNAUTHORIZED")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := Client{Services: map[string]string{"p/x": server.URL}, HTTP: server.Client()}
	if _, err := client.Read(context.Background(), "p/x", trace); err == nil || errors.Is(err, uses.ErrNoReadService) {
		t.Fatalf("err = %v", err)
	}
}
