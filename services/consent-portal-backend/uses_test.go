package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/portalhttp"
)

// The portal passes the register's uses through for the owner, and answers
// 404 for a consent of another citizen without asking for its uses.
func TestTheUsesEndpointChecksOwnership(t *testing.T) {
	refs := consent.SubjectRefs{Key: []byte(testSubjectRefKey), Version: "1"}
	owner, err := refs.For("123456789")
	if err != nil {
		t.Fatal(err)
	}
	usesAsked := 0
	register := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/consents/c-1":
			_, _ = w.Write([]byte(`{"consent_id":"c-1","status":"ACTIVE","subject_ref":"` + string(owner) + `"}`))
		case "/consents/c-1/uses":
			usesAsked++
			_, _ = w.Write([]byte(`{"consent_id":"c-1","uses":[{"used_at":"2026-11-19T09:30:00.123Z","service_provider_oin":"99999999900000001800","service_provider":"Hypotheek-BV","source":"Belastingdienst","logbook_id":"99999999900000000200/bd"}],"notices":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer register.Close()

	cfg := config{
		Port:                 "0",
		ConsentURL:           register.URL,
		SubjectRefKey:        []byte(testSubjectRefKey),
		SubjectRefKeyVersion: "1",
	}
	hub := portalhttp.NewHub()
	srv := httptest.NewServer(newMux(newPortal(cfg, hub, nil), hub))
	defer srv.Close()

	get := func(bsn string) (int, string) {
		t.Helper()
		loginResp, err := http.Post(srv.URL+"/portal/login", "application/json", bytes.NewBufferString(`{"citizen_bsn":"`+bsn+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var login portalhttp.LoginResponse
		_ = json.NewDecoder(loginResp.Body).Decode(&login)
		_ = loginResp.Body.Close()

		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/portal/consents/c-1/uses", nil)
		req.Header.Set("Authorization", "Bearer "+login.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	status, body := get("123456789")
	if status != http.StatusOK || !strings.Contains(body, `"source":"Belastingdienst"`) || !strings.Contains(body, `"service_provider":"Hypotheek-BV"`) || !strings.Contains(body, `"used_at":"2026-11-19T09:30:00Z"`) {
		t.Fatalf("owner: %d %s", status, body)
	}
	if strings.Contains(body, "logbook_id") {
		t.Errorf("the page needs no logbook ID: %s", body)
	}

	asked := usesAsked
	if status, body := get("999999990"); status != http.StatusNotFound {
		t.Fatalf("other citizen: %d %s, want 404", status, body)
	}
	if usesAsked != asked {
		t.Error("the uses of another citizen's consent were asked for")
	}
}
