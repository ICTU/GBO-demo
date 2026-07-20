package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Happy-path integration test: login -> give consent -> list consents.
// The bsnk-mock and consent-register downstreams are stubbed with two
// httptest.Servers; the portal itself is wired through newMux.
func TestPortalGiveThenList(t *testing.T) {
	// Stub bsnk-mock: /pseudonymize returns deterministic pseudonym + pi.
	bsnk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pseudonymize" {
			t.Errorf("bsnk path = %q, want /pseudonymize", r.URL.Path)
		}
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pseudonym":"EP-abc","pi":"PI-xyz"}`))
	}))
	defer bsnk.Close()

	// Stub consent-register: POST /consents creates, GET /consents?pi= lists.
	var (
		regMu   sync.Mutex
		created []map[string]any
	)
	register := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/consents":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["consent_id"] = "c-1"
			body["status"] = "ACTIVE"
			regMu.Lock()
			created = append(created, body)
			regMu.Unlock()
			_, _ = w.Write([]byte(`{"consent_id":"c-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consents":
			pi := r.URL.Query().Get("pi")
			regMu.Lock()
			out := make([]map[string]any, 0, len(created))
			for _, rec := range created {
				if rec["pi"] == pi {
					out = append(out, rec)
				}
			}
			regMu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected register call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer register.Close()

	cfg := config{
		Port:       "0",
		BSNkURL:    bsnk.URL,
		ConsentURL: register.URL,
	}
	srv := httptest.NewServer(newMux(cfg, NewSSEHub()))
	defer srv.Close()

	// /health sanity check.
	healthResp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", healthResp.StatusCode)
	}

	// Step 1: mock-DigiD login.
	loginBody := bytes.NewBufferString(`{"citizen_bsn":"123456789"}`)
	loginResp, err := http.Post(srv.URL+"/portal/login", "application/json", loginBody)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", loginResp.StatusCode)
	}
	var login LoginResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if login.Token == "" {
		t.Fatal("empty token")
	}

	// Step 2: give consent (auth via bearer).
	giveBody := strings.NewReader(`{
		"dienstverlener_oin": "00000003000000003000",
		"scopes": ["bd:ib:2025"],
		"scope_entries": []
	}`)
	giveReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/portal/consents", giveBody)
	giveReq.Header.Set("Authorization", "Bearer "+login.Token)
	giveReq.Header.Set("Content-Type", "application/json")
	giveResp, err := http.DefaultClient.Do(giveReq)
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	defer giveResp.Body.Close()
	if giveResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(giveResp.Body)
		t.Fatalf("give consent status = %d, want 200; body = %s", giveResp.StatusCode, string(raw))
	}
	var give GiveConsentResponse
	if err := json.NewDecoder(giveResp.Body).Decode(&give); err != nil {
		t.Fatalf("decode give: %v", err)
	}
	if give.ConsentID != "c-1" {
		t.Errorf("consent_id = %q, want c-1", give.ConsentID)
	}
	if give.PI != "PI-xyz" {
		t.Errorf("pi = %q, want PI-xyz", give.PI)
	}

	// Step 3: list consents — should surface the one we just created.
	listReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/portal/consents", nil)
	listReq.Header.Set("Authorization", "Bearer "+login.Token)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(listResp.Body)
		t.Fatalf("list status = %d, want 200; body = %s", listResp.StatusCode, string(raw))
	}
	var list []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0]["consent_id"] != "c-1" {
		t.Errorf("listed consent_id = %v, want c-1", list[0]["consent_id"])
	}
	if list[0]["effective_status"] != "active" {
		t.Errorf("effective_status = %v, want active", list[0]["effective_status"])
	}
}
