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

// testSource is the source the portal under test gives consent for.
const testSource = "99999999900000000200"

// testSubjectRefKey is the portal's secret for its references in these tests.
const testSubjectRefKey = "test-subject-ref-key-of-32-bytes!"

// Happy-path integration test: login -> give consent -> list consents.
// The BSNk and consent-register downstreams are stubbed with two
// httptest.Servers; the portal itself is wired through newMux.
func TestPortalGiveThenList(t *testing.T) {
	var transformed []map[string]any
	bsnk := stubBSNk(t, &transformed)
	defer bsnk.Close()

	// Stub consent-register: POST creates a token; GET lists by subject_ref.
	var (
		regMu       sync.Mutex
		created     []map[string]any
		received    map[string]any
		polymorphic = map[string]string{}
	)
	register := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/consents":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			regMu.Lock()
			received, _ = body["encrypted_subject"].(map[string]any)
			regMu.Unlock()
			delete(body, "encrypted_subject") // transient signing input is not persisted
			body["consent_id"] = "c-1"
			body["status"] = "ACTIVE"
			regMu.Lock()
			created = append(created, body)
			regMu.Unlock()
			_, _ = w.Write([]byte(`{"consent_id":"c-1","consent_token":"signed-consent-token"}`))
		case strings.HasPrefix(r.URL.Path, "/subjects/") && strings.HasSuffix(r.URL.Path, "/polymorphic"):
			regMu.Lock()
			defer regMu.Unlock()
			if r.Method == http.MethodPut {
				body, _ := io.ReadAll(r.Body)
				polymorphic[r.URL.Path] = string(body)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			kept, ok := polymorphic[r.URL.Path]
			if !ok {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(kept))
		case r.Method == http.MethodGet && r.URL.Path == "/consents":
			subjectRef := r.URL.Query().Get("subject_ref")
			regMu.Lock()
			out := make([]map[string]any, 0, len(created))
			for _, rec := range created {
				if rec["subject_ref"] == subjectRef {
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
		Sources:    []consent.Party{{OIN: testSource, KeySetVersion: 20260101}},

		SubjectRefKey:        []byte(testSubjectRefKey),
		SubjectRefKeyVersion: "1",
	}
	srv := httptest.NewServer(newMux(cfg, portalhttp.NewHub(), nil))
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
	if give.ConsentToken != "signed-consent-token" {
		t.Errorf("consent_token = %q", give.ConsentToken)
	}
	// The dev-portal renders one card per upstream call. A first consent
	// finds no kept values, activates, keeps them, reads the BSN
	// authorisation list, transforms per form and creates. Guards against
	// the call log quietly gaining or losing entries.
	assertPrivateAPICalls(t, give.APICalls,
		"Find polymorphic values", "Activate BSN", "Keep polymorphic values", "Read the BSN authorisation list",
		"Transform to pseudonyms", "Transform to identities", "Create Consent")

	assertValuesRequestedForTheSource(t, transformed)
	regMu.Lock()
	assertRegisterGotTheSourcesValue(t, received)
	regMu.Unlock()
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

// stubBSNk stands in for BSNk: activate gives the polymorphic values, the
// BSN authorisation list names the source, and transform gives a value per
// party in the form asked for ("signed-VI" or "signed-VP").
// Every transform request is kept in transformed.
func stubBSNk(t *testing.T, transformed *[]map[string]any) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/activate":
			_, _ = w.Write([]byte(`{"PolymorphicPseudonym":["signed-PI","signed-PP"]}`))
		case "/v2/bsn-authorisation-list":
			_, _ = w.Write([]byte(`{"AuthorizedOrganization":[{"OIN":"` + testSource + `"}]}`))
		case "/v2/transform":
			var req struct {
				RelyingParty []struct {
					EntityID       string
					KeySetVersion  int
					IdentifierType string
				}
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			var kept map[string]any
			_ = json.Unmarshal(body, &kept)
			mu.Lock()
			*transformed = append(*transformed, kept)
			mu.Unlock()
			type encrypted struct {
				EntityID       string `json:"EntityID"`
				KeySetVersion  int    `json:"KeySetVersion"`
				IdentifierType string `json:"IdentifierType"`
				Value          string `json:"value"`
			}
			var out struct{ Encrypted []encrypted }
			for _, rp := range req.RelyingParty {
				out.Encrypted = append(out.Encrypted, encrypted{rp.EntityID, rp.KeySetVersion, rp.IdentifierType, "signed-V" + rp.IdentifierType[:1]})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected BSNk call: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

// BSNk was asked for a pseudonym for the source, from the polymorphic
// pseudonym it had just made for the portal, and then for an identity, from
// the polymorphic identity: the source is on the BSN authorisation list.
func assertValuesRequestedForTheSource(t *testing.T, transformed []map[string]any) {
	t.Helper()
	if len(transformed) != 2 {
		t.Fatalf("%d transform requests, want one per form", len(transformed))
	}
	for i, want := range []struct{ form, polymorphic, value string }{
		{"Pseudonym", "PolymorphicPseudonym", "signed-PP"},
		{"Identity", "PolymorphicIdentity", "signed-PI"},
	} {
		request := transformed[i]
		parties, _ := request["RelyingParty"].([]any)
		if request["Requester"] != portalOIN || request[want.polymorphic] != want.value || len(parties) != 1 {
			t.Fatalf("%s request = %+v", want.form, request)
		}
		if party := parties[0].(map[string]any); party["EntityID"] != testSource || party["IdentifierType"] != want.form || party["KeySetVersion"] != float64(20260101) {
			t.Errorf("%s relying party = %+v", want.form, party)
		}
	}
	// A request carries the polymorphic value of its own form only.
	if _, sent := transformed[0]["PolymorphicIdentity"]; sent {
		t.Error("the pseudonym request carried the polymorphic identity")
	}
}

// The register got the values BSNk made, keyed by the party they are for.
func assertRegisterGotTheSourcesValue(t *testing.T, received map[string]any) {
	t.Helper()
	values, _ := received[testSource].(map[string]any)
	identity, _ := values["identity"].(map[string]any)
	pseudonym, _ := values["pseudonym"].(map[string]any)
	if identity["value"] != "signed-VI" || identity["key_set_version"] != float64(20260101) ||
		pseudonym["value"] != "signed-VP" || pseudonym["key_set_version"] != float64(20260101) {
		t.Errorf("encrypted_subject sent to the register = %+v", received)
	}
}

func assertPrivateAPICalls(t *testing.T, calls []consent.APICall, labels ...string) {
	t.Helper()
	if len(calls) != len(labels) {
		t.Fatalf("api_calls = %d, want %d: %+v", len(calls), len(labels), calls)
	}
	for i, want := range labels {
		if calls[i].Label != want {
			t.Errorf("api_calls[%d].Label = %q, want %q", i, calls[i].Label, want)
		}
		// A first consent finds nothing kept: that lookup answers 404.
		answered := calls[i].Status >= 200 && calls[i].Status < 300
		if !answered && (want != "Find polymorphic values" || calls[i].Status != http.StatusNotFound) {
			t.Errorf("api_calls[%d] %s: status %d", i, want, calls[i].Status)
		}
		// The authorisation list names organisations, not the citizen.
		if want != "Read the BSN authorisation list" && (len(calls[i].RequestBody) != 0 || len(calls[i].ResponseBody) != 0) {
			t.Errorf("api_calls[%d] exposed private request/response bodies", i)
		}
	}
}

// The SSE endpoint must stream through the access-log middleware. It did not:
// that middleware wraps the ResponseWriter, and the handler's old
// w.(http.Flusher) assertion failed against the wrapper, so /portal/events
// answered 500 "streaming not supported". Wire it exactly as main does.
func TestSSEStreamsThroughAccessLog(t *testing.T) {
	hub := portalhttp.NewHub()
	srv := httptest.NewServer(logctx.WithAccessLog(newMux(config{Port: "0"}, hub, nil)))
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
			Data:      map[string]any{"recipients": []string{testSource}},
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

func TestParseSources(t *testing.T) {
	got, err := parseSources(" 99999999900000000200@20260101 , 99999999900000000210@20260314 ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []consent.Party{
		{OIN: "99999999900000000200", KeySetVersion: 20260101},
		{OIN: "99999999900000000210", KeySetVersion: 20260314},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("sources = %+v, want %+v", got, want)
	}

	for name, value := range map[string]string{
		"none":                   "",
		"no version":             "99999999900000000200",
		"no OIN":                 "belastingdienst@20260101",
		"version is not a date":  "99999999900000000200@1",
		"30 February":            "99999999900000000200@20260230",
		"month 13":               "99999999900000000200@20261301",
		"version with a sign":    "99999999900000000200@+2026010",
		"one source named twice": "99999999900000000200@20260101,99999999900000000200@20260314",
	} {
		if _, err := parseSources(value); err == nil {
			t.Errorf("%s: %q was accepted", name, value)
		}
	}
}

// A second consent of the same citizen finds the values kept at the first and
// does not activate again.
func TestASecondConsentOnlyTransforms(t *testing.T) {
	var (
		mu          sync.Mutex
		activations int
		polymorphic = map[string]string{}
	)
	var transformed []map[string]any
	bsnk := stubBSNk(t, &transformed)
	defer bsnk.Close()
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activate" {
			mu.Lock()
			activations++
			mu.Unlock()
		}
		proxy, _ := http.NewRequest(r.Method, bsnk.URL+r.URL.Path, r.Body)
		resp, err := http.DefaultClient.Do(proxy)
		if err != nil {
			t.Errorf("stub BSNk: %v", err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer counting.Close()

	register := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/polymorphic") && r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			polymorphic[r.URL.Path] = string(body)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/polymorphic"):
			kept, ok := polymorphic[r.URL.Path]
			if !ok {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(kept))
		case r.URL.Path == "/consents" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"consent_id":"c-1","consent_token":"signed-consent-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer register.Close()

	srv := httptest.NewServer(newMux(config{
		BSNkURL: counting.URL, ConsentURL: register.URL,
		Sources:       []consent.Party{{OIN: testSource, KeySetVersion: 20260101}},
		SubjectRefKey: []byte(testSubjectRefKey), SubjectRefKeyVersion: "1",
	}, portalhttp.NewHub(), nil))
	defer srv.Close()

	loginResp, err := http.Post(srv.URL+"/portal/login", "application/json", bytes.NewBufferString(`{"citizen_bsn":"123456789"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	var login portalhttp.LoginResponse
	_ = json.NewDecoder(loginResp.Body).Decode(&login)
	_ = loginResp.Body.Close()

	for i, labels := range [][]string{
		{"Find polymorphic values", "Activate BSN", "Keep polymorphic values", "Read the BSN authorisation list", "Transform to pseudonyms", "Transform to identities", "Create Consent"},
		{"Find polymorphic values", "Read the BSN authorisation list", "Transform to pseudonyms", "Transform to identities", "Create Consent"},
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/portal/consents",
			strings.NewReader(`{"dienstverlener_oin":"00000003000000003000","scopes":["bd:ib:2025"]}`))
		req.Header.Set("Authorization", "Bearer "+login.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("consent %d: %v", i+1, err)
		}
		var give portalhttp.GiveConsentResponse
		_ = json.NewDecoder(resp.Body).Decode(&give)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("consent %d: status %d", i+1, resp.StatusCode)
		}
		assertPrivateAPICalls(t, give.APICalls, labels...)
	}
	if activations != 1 {
		t.Errorf("BSNk activated %d times for two consents of one citizen, want 1", activations)
	}
}

func TestLoadConfigNeedsASubjectReferenceKey(t *testing.T) {
	t.Setenv("CONSENT_SOURCES", testSource+"@20260101")

	t.Setenv("SUBJECT_REF_KEY", "")
	if _, err := loadConfig(); err == nil {
		t.Error("the portal started without a subject reference key")
	}
	t.Setenv("SUBJECT_REF_KEY", "too-short")
	if _, err := loadConfig(); err == nil {
		t.Error("the portal accepted a key shorter than 32 characters")
	}

	t.Setenv("SUBJECT_REF_KEY", testSubjectRefKey)
	t.Setenv("SUBJECT_REF_KEY_VERSION", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if string(cfg.SubjectRefKey) != testSubjectRefKey || cfg.SubjectRefKeyVersion != "1" {
		t.Errorf("key %q, version %q", cfg.SubjectRefKey, cfg.SubjectRefKeyVersion)
	}
	t.Setenv("SUBJECT_REF_KEY_VERSION", "v-2")
	if _, err := loadConfig(); err == nil {
		t.Error("a key version with a dash was accepted; it would break the reference format")
	}
}
