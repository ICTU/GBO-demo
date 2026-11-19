package usageindex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterPostsThePointerToTheOutwaysGrantLink(t *testing.T) {
	var path string
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := Client{URL: server.URL + "/data-usage-index", LogbookID: "99999999900000000200/bd", HTTP: server.Client()}
	if err := client.Register(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "c-7f3a"); err != nil {
		t.Fatal(err)
	}
	if path != "/data-usage-index/registrations" {
		t.Errorf("path = %q", path)
	}
	want := map[string]string{
		"logbook_id":     "99999999900000000200/bd",
		"trace_id":       "4bf92f3577b34da6a3ce929d0e0e4736",
		"grondslag_type": "consent",
		"grondslag_ref":  "c-7f3a",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the registration carries more than the pointer: %v", got)
	}
}

func TestARetryAnsweredWithTheStoredRegistrationIsASuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := Client{URL: server.URL, LogbookID: "99999999900000000200/bd", HTTP: server.Client()}
	if err := client.Register(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "c-7f3a"); err != nil {
		t.Fatal(err)
	}
}

func TestARefusalIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no grant", http.StatusForbidden)
	}))
	defer server.Close()
	client := Client{URL: server.URL, LogbookID: "99999999900000000200/bd", HTTP: server.Client()}
	if err := client.Register(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "c-7f3a"); err == nil {
		t.Fatal("want an error for a 403")
	}
}
