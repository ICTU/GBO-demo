package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"consent-register/logbookreader"
	"consent-register/usageindex"
	"consent-register/uses"

	"gbo-demo/ldv-client/ldvtest"
)

const usesTestProvider = "99999999900000000300"

// indexUnderTest serves the index's lookup for one consent. Its handler
// checks what the adapter sends, so the adapter is tested on the way.
func indexUnderTest(t *testing.T, registrations func(consentID string) []map[string]string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer read-token" || r.URL.Path != "/registrations" ||
			r.URL.Query().Get("grondslag_type") != "consent" {
			http.Error(w, "bad lookup", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"registrations": registrations(r.URL.Query().Get("grondslag_ref"))})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func registerWithUses(t *testing.T, logbook *ldvtest.Logbook, usage uses.Service) string {
	t.Helper()
	issuer, err := NewConsentIssuer(config{SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatal(err)
	}
	var client *registerLogbook
	if logbook != nil {
		client = newRegisterLogbook(logbook.Client(t, "consent-register"))
	}
	server := httptest.NewServer(newMux(NewStore(), issuer, client, usage))
	t.Cleanup(server.Close)
	return server.URL
}

func createTestConsent(t *testing.T, url string) string {
	t.Helper()
	body := bytes.NewBufferString(`{
		"encrypted_subject": {"99999999900000000200": {"pseudonym": {"key_set_version": 20260101, "value": "cHNldWRv"}}},
		"subject_ref": "EP-portal-abc123",
		"dienstverlener_oin": "` + usesTestProvider + `",
		"scopes": ["bri:read"],
		"use_case": "hypotheek-aanvraag"
	}`)
	response, err := http.Post(url+"/consents", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var created struct {
		ConsentID string `json:"consent_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil || created.ConsentID == "" {
		t.Fatalf("create consent: status %d, %v", response.StatusCode, err)
	}
	return created.ConsentID
}

type usesAnswer struct {
	Uses []struct {
		UsedAt             string `json:"used_at"`
		ServiceProviderOIN string `json:"service_provider_oin"`
		ServiceProvider    string `json:"service_provider"`
		Source             string `json:"source"`
	} `json:"uses"`
	Notices []struct {
		Kind string `json:"kind"`
	} `json:"notices"`
}

func getUses(t *testing.T, url, consentID string) (int, usesAnswer) {
	t.Helper()
	response, err := http.Get(url + "/consents/" + consentID + "/uses")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var answer usesAnswer
	_ = json.NewDecoder(response.Body).Decode(&answer)
	return response.StatusCode, answer
}

func TestTheUsesOfAConsentComeFromTheIndex(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)
	var consentID string
	index := indexUnderTest(t, func(ref string) []map[string]string {
		if ref != consentID {
			return []map[string]string{}
		}
		return []map[string]string{{
			"logbook_id": "99999999900000000200/bd", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
			"grondslag_type": "consent", "grondslag_ref": ref, "registered_at": at.Format(time.RFC3339Nano),
		}}
	})
	logbook := ldvtest.New(t, consentGrantActivity, consentListActivity)
	url := registerWithUses(t, logbook, uses.Service{
		Index:            usageindex.Client{URL: index, ReadToken: "read-token"},
		Sources:          map[string]string{"99999999900000000200/bd": "Belastingdienst"},
		ServiceProviders: map[string]string{usesTestProvider: "Hypotheek-BV"},
	})
	consentID = createTestConsent(t, url)

	status, answer := getUses(t, url, consentID)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(answer.Notices) != 0 || len(answer.Uses) != 1 {
		t.Fatalf("answer = %+v", answer)
	}
	use := answer.Uses[0]
	if use.Source != "Belastingdienst" || use.ServiceProviderOIN != usesTestProvider || use.ServiceProvider != "Hypotheek-BV" ||
		use.UsedAt != at.Format(time.RFC3339Nano) {
		t.Errorf("use = %+v", use)
	}
}

func TestAnUnreachableIndexIsANoticeNotAnEmptyList(t *testing.T) {
	url := registerWithUses(t, nil, uses.Service{
		Index: usageindex.Client{URL: "http://127.0.0.1:1", ReadToken: "read-token", HTTP: &http.Client{Timeout: time.Second}},
	})
	consentID := createTestConsent(t, url)

	status, answer := getUses(t, url, consentID)
	if status != http.StatusOK || len(answer.Notices) != 1 || answer.Notices[0].Kind != uses.NoticeIndexUnavailable {
		t.Fatalf("status %d, answer %+v", status, answer)
	}
}

func TestTheUsesOfAnUnknownConsentAreNotFound(t *testing.T) {
	url := registerWithUses(t, nil, uses.Service{})
	if status, _ := getUses(t, url, "c-unknown"); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestParseSources(t *testing.T) {
	got := parseSources("99999999900000000200/bd=Belastingdienst; 99999999900000000400/brp = BRP (RvIG);broken")
	if len(got) != 2 || got["99999999900000000400/brp"] != "BRP (RvIG)" {
		t.Fatalf("sources = %v", got)
	}
}

// #687: viewing the uses produces a record of the register's read activity,
// naming the sources read and not the trace IDs; a source that cannot be
// read gives its notice in the answer.
func TestReadingTheSourcesIsLoggedWithoutTraceIDs(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)
	const trace = "4bf92f3577b34da6a3ce929d0e0e4736"
	var consentID string
	index := indexUnderTest(t, func(ref string) []map[string]string {
		if ref != consentID {
			return []map[string]string{}
		}
		return []map[string]string{
			{"logbook_id": "99999999900000000200/ldv-lezen-bd", "trace_id": trace, "grondslag_type": "consent", "grondslag_ref": ref, "registered_at": at.Format(time.RFC3339Nano)},
			{"logbook_id": "99999999900000000400/ldv-lezen-brp", "trace_id": trace, "grondslag_type": "consent", "grondslag_ref": ref, "registered_at": at.Format(time.RFC3339Nano)},
		}
	})
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"dataProcessingOperations":[{"traceId":"` + trace + `","startTime":"2026-11-19T09:29:59Z"}]}`))
	}))
	t.Cleanup(source.Close)

	logbook := ldvtest.New(t, consentGrantActivity, consentListActivity, consentUsageReadActivity)
	issuer, err := NewConsentIssuer(config{SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	client := newRegisterLogbook(logbook.Client(t, "consent-register"))
	server := httptest.NewServer(newMux(store, issuer, client, uses.Service{
		Index: usageindex.Client{URL: index, ReadToken: "read-token"},
		Sources: map[string]string{
			"99999999900000000200/ldv-lezen-bd":  "Belastingdienst",
			"99999999900000000400/ldv-lezen-brp": "Basisregistratie Personen",
		},
		Logbooks: logbookreader.Client{Services: map[string]string{
			"99999999900000000200/ldv-lezen-bd":  source.URL,
			"99999999900000000400/ldv-lezen-brp": "http://127.0.0.1:1",
		}},
	}))
	t.Cleanup(server.Close)
	url := server.URL
	consentID = createTestConsent(t, url)

	status, answer := getUses(t, url, consentID)
	if status != http.StatusOK || len(answer.Uses) != 1 || answer.Uses[0].UsedAt != "2026-11-19T09:29:59Z" {
		t.Fatalf("status %d, answer %+v", status, answer)
	}
	if len(answer.Notices) != 1 || answer.Notices[0].Kind != uses.NoticeSourceUnavailable {
		t.Fatalf("notices = %+v", answer.Notices)
	}

	// Records commit with the mutation and are delivered from the outbox.
	if err := client.deliverOutbox(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	for _, record := range logbook.Written() {
		if record.Name != "dataverwerking.bronlogboek-lezen" {
			continue
		}
		encoded, _ := json.Marshal(record.Attributes)
		if strings.Contains(string(encoded), trace) {
			t.Fatalf("the read record lists a trace ID: %s", encoded)
		}
		if !strings.Contains(string(encoded), "Belastingdienst") {
			t.Fatalf("the read record does not name the source read: %s", encoded)
		}
		return
	}
	t.Fatal("no record of the register's read of the sources")
}
