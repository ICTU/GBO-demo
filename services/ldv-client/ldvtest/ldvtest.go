// Package ldvtest is a stand-in logbook for the tests of instrumented
// services. It lives next to the client rather than being copied into every
// service, for the same reason the client does.
package ldvtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ldv "gbo-demo/ldv-client"
)

// Logbook collects the records written to it and, like a real logbook,
// refuses any that names a verwerkingsactiviteit its register does not know —
// so a test asserts on what a component actually logged rather than on what it
// intended to log.
type Logbook struct {
	server     *httptest.Server
	mutex      sync.Mutex
	records    []ldv.Record
	activities []string
	refuse     bool
}

// New starts a fake logbook whose register holds exactly the given
// verwerkingsactiviteiten.
func New(t *testing.T, activities ...string) *Logbook {
	t.Helper()
	logbook := &Logbook{activities: activities}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /logboek/records", func(w http.ResponseWriter, r *http.Request) {
		logbook.mutex.Lock()
		refuse := logbook.refuse
		logbook.mutex.Unlock()
		if refuse {
			http.Error(w, `{"error":"invalid_record"}`, http.StatusUnprocessableEntity)
			return
		}
		var record ldv.Record
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !logbook.knows(record.Attributes[ldv.AttrProcessingActivityID]) {
			http.Error(w, `{"error":"invalid_record"}`, http.StatusUnprocessableEntity)
			return
		}
		logbook.mutex.Lock()
		logbook.records = append(logbook.records, record)
		logbook.mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"trace_id": record.TraceID, "span_id": record.SpanID})
	})
	logbook.server = httptest.NewServer(mux)
	t.Cleanup(logbook.server.Close)
	return logbook
}

// URL is the fake logbook's base URL.
func (l *Logbook) URL() string { return l.server.URL }

// knows reports whether the fake's register holds this reference.
func (l *Logbook) knows(activity any) bool {
	reference, _ := activity.(string)
	for _, known := range l.activities {
		if known == reference {
			return true
		}
	}
	return false
}

// Client returns a client pointed at this fake.
func (l *Logbook) Client(t *testing.T, serviceName string) *ldv.Client {
	t.Helper()
	client, err := ldv.New(ldv.Config{
		ServiceName:  serviceName,
		LogbookURL:   l.server.URL,
		WriteToken:   "test-token",
		PseudonymKey: "test-key",
	})
	if err != nil {
		t.Fatalf("configure logbook client: %v", err)
	}
	if client == nil {
		t.Fatal("logbook client is nil despite a logbook URL")
	}
	return client
}

// Written returns the records the fake has accepted.
func (l *Logbook) Written() []ldv.Record {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return append([]ldv.Record(nil), l.records...)
}

// RefuseEverything makes every subsequent write fail, to exercise the
// fail-closed path.
func (l *Logbook) RefuseEverything() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.refuse = true
}

// ByName picks the records of one Dataverwerking.
func ByName(records []ldv.Record, name string) []ldv.Record {
	var matched []ldv.Record
	for _, record := range records {
		if record.Name == name {
			matched = append(matched, record)
		}
	}
	return matched
}

// AssertNoBSN is the acceptance criterion stated as a test: no record may
// contain a BSN, whatever the flow, and every record must say which pseudonym
// space its subject lives in.
func AssertNoBSN(t *testing.T, records []ldv.Record, bsn string) {
	t.Helper()
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		if bsn != "" && strings.Contains(string(encoded), bsn) {
			t.Fatalf("record %q contains the BSN: %s", record.Name, encoded)
		}
		if record.Attributes[ldv.AttrDataSubjectIDType] == nil {
			t.Fatalf("record %q has no %s", record.Name, ldv.AttrDataSubjectIDType)
		}
	}
}
