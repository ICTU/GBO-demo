package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Happy-path integration test: create a consent, then fetch it by its
// generated consent_id. Verifies the POST/GET handlers share the same
// store and that the stored PI/status roundtrip through the mux.
func TestCreateThenGetConsent(t *testing.T) {
	srv := httptest.NewServer(newMux(NewStore()))
	defer srv.Close()

	createBody := bytes.NewBufferString(`{
		"pi": "PI-abc123",
		"dienstverlener_oin": "00000001234567890000",
		"scopes": ["bsn:read"],
		"use_case": "hypotheek-aanvraag"
	}`)
	createResp, err := http.Post(srv.URL+"/consents", "application/json", createBody)
	if err != nil {
		t.Fatalf("create consent: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", createResp.StatusCode)
	}
	var created struct {
		ConsentID string `json:"consent_id"`
		Status    string `json:"status"`
		PI        string `json:"pi"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ConsentID == "" {
		t.Fatalf("empty consent_id in create response: %+v", created)
	}
	if created.Status != "ACTIVE" {
		t.Fatalf("created status = %q, want ACTIVE", created.Status)
	}

	getResp, err := http.Get(srv.URL + "/consents/" + created.ConsentID)
	if err != nil {
		t.Fatalf("get consent: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", getResp.StatusCode)
	}
	var fetched struct {
		ConsentID string `json:"consent_id"`
		Status    string `json:"status"`
		PI        string `json:"pi"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.ConsentID != created.ConsentID {
		t.Fatalf("consent_id mismatch: got %q, want %q", fetched.ConsentID, created.ConsentID)
	}
	if fetched.PI != "PI-abc123" {
		t.Fatalf("PI mismatch: got %q, want %q", fetched.PI, "PI-abc123")
	}
	if fetched.Status != "ACTIVE" {
		t.Fatalf("fetched status = %q, want ACTIVE", fetched.Status)
	}
}
