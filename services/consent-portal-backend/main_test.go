package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/logctx"
	"gbo-demo/consent-portal-backend/portalhttp"
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
	srv := httptest.NewServer(newMux(cfg, portalhttp.NewHub()))
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
	var login portalhttp.LoginResponse
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
	var give portalhttp.GiveConsentResponse
	if err := json.NewDecoder(giveResp.Body).Decode(&give); err != nil {
		t.Fatalf("decode give: %v", err)
	}
	if give.ConsentID != "c-1" {
		t.Errorf("consent_id = %q, want c-1", give.ConsentID)
	}
	if give.PI != "PI-xyz" {
		t.Errorf("pi = %q, want PI-xyz", give.PI)
	}
	// The dev-portal renders one card per upstream call: pseudonymise, then
	// create. Guards against the call log quietly gaining or losing entries.
	if len(give.APICalls) != 2 {
		t.Fatalf("api_calls = %d, want 2: %+v", len(give.APICalls), give.APICalls)
	}
	for i, want := range []string{"Pseudonymize BSN", "Create Consent"} {
		if give.APICalls[i].Label != want {
			t.Errorf("api_calls[%d].Label = %q, want %q", i, give.APICalls[i].Label, want)
		}
		if give.APICalls[i].Status != http.StatusOK {
			t.Errorf("api_calls[%d].Status = %d, want 200", i, give.APICalls[i].Status)
		}
	}
	// The register must never have seen the plain BSN.
	regMu.Lock()
	sent, _ := json.Marshal(created)
	regMu.Unlock()
	if strings.Contains(string(sent), "123456789") {
		t.Errorf("BSN reached the consent register: %s", sent)
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

// The SSE endpoint must stream through the access-log middleware. It did not:
// that middleware wraps the ResponseWriter, and the handler's old
// w.(http.Flusher) assertion failed against the wrapper, so /portal/events
// answered 500 "streaming not supported". Wire it exactly as main does.
func TestSSEStreamsThroughAccessLog(t *testing.T) {
	hub := portalhttp.NewHub()
	srv := httptest.NewServer(logctx.WithAccessLog(newMux(config{Port: "0"}, hub)))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/portal/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}

	// The greeting only arrives if the handler could actually flush.
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if !strings.Contains(line, `"step":"connected"`) {
		t.Fatalf("greeting = %q, want the connected event", line)
	}

	// A step emitted by the core reaches the connected panel.
	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.Observe(context.Background(), consent.Event{
			Step:      "pseudonymizing",
			Component: "bsnk-mock",
			Data:      map[string]any{"oin": "DV"},
		})
	}()
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		if strings.Contains(l, "pseudonymizing") {
			return // delivered
		}
	}
}
