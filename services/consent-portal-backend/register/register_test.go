package register

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

const hypotheekPeer consent.PeerID = "99999999900000000300"

// stubRegister answers every call with status and answer, and keeps the
// body it was sent.
func stubRegister(t *testing.T, path string, status int, answer string) (Client, *map[string]any) {
	t.Helper()
	sent := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode the request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return Client{Base: server.URL, Caller: upstream.Caller{Client: server.Client()}}, &sent
}

func TestCreateWithAClientPeerGetsARefreshToken(t *testing.T) {
	client, sent := stubRegister(t, "/consents", http.StatusCreated,
		`{"consent_id":"c-1","consent_token":"signed","expires_at":"2026-11-10T12:05:00Z","refresh_token":"rt"}`)
	rec, err := client.Create(context.Background(), consent.Draft{SubjectRef: "ref", ClientPeer: hypotheekPeer})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if (*sent)["client_peer"] != string(hypotheekPeer) {
		t.Errorf("client_peer sent = %v", (*sent)["client_peer"])
	}
	want := time.Date(2026, 11, 10, 12, 5, 0, 0, time.UTC)
	if rec.ID != "c-1" || rec.Token != "signed" || !rec.TokenExpiresAt.Equal(want) || rec.RefreshToken != "rt" {
		t.Errorf("record = %+v", rec)
	}
}

func TestCreateWithoutAClientPeerSendsNone(t *testing.T) {
	client, sent := stubRegister(t, "/consents", http.StatusCreated,
		`{"consent_id":"c-1","consent_token":"signed","expires_at":"2027-11-10T12:00:00Z"}`)
	rec, err := client.Create(context.Background(), consent.Draft{SubjectRef: "ref"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, found := (*sent)["client_peer"]; found {
		t.Errorf("client_peer sent without a client: %v", *sent)
	}
	if rec.RefreshToken != "" {
		t.Errorf("refresh token = %q, want none", rec.RefreshToken)
	}
}

func TestCreateNeedsExpiresAt(t *testing.T) {
	client, _ := stubRegister(t, "/consents", http.StatusCreated, `{"consent_id":"c-1","consent_token":"signed"}`)
	if _, err := client.Create(context.Background(), consent.Draft{SubjectRef: "ref"}); err == nil {
		t.Error("Create without expires_at gave no error")
	}
}

func TestRefreshSendsTokenPeerAndScope(t *testing.T) {
	client, sent := stubRegister(t, "/proofs/refresh", http.StatusOK,
		`{"consent_id":"c-1","consent_token":"refreshed","expires_at":"2026-11-10T12:05:00Z","scopes":["bd:ib:2025"]}`)
	proof, err := client.Refresh(context.Background(), "rt", hypotheekPeer, []string{"bd:ib:2025", "bd:ib:2024"})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if (*sent)["refresh_token"] != "rt" || (*sent)["client_peer"] != string(hypotheekPeer) || (*sent)["scope"] != "bd:ib:2025 bd:ib:2024" {
		t.Errorf("sent = %v", *sent)
	}
	want := time.Date(2026, 11, 10, 12, 5, 0, 0, time.UTC)
	if proof.ConsentID != "c-1" || proof.ConsentToken != "refreshed" || !proof.ExpiresAt.Equal(want) || len(proof.Scopes) != 1 {
		t.Errorf("proof = %+v", proof)
	}
}

func TestRefreshWithoutAScopeSendsNone(t *testing.T) {
	client, sent := stubRegister(t, "/proofs/refresh", http.StatusOK,
		`{"consent_id":"c-1","consent_token":"refreshed","expires_at":"2026-11-10T12:05:00Z","scopes":["bd:ib:2025"]}`)
	if _, err := client.Refresh(context.Background(), "rt", hypotheekPeer, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, found := (*sent)["scope"]; found {
		t.Errorf("scope sent: %v", *sent)
	}
}

func TestRefreshTranslatesTheRegistersAnswer(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		answer  string
		refusal error // nil: the register's own failure
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant"}`, consent.ErrInvalidGrant},
		{"invalid_scope", 400, `{"error":"invalid_scope"}`, consent.ErrInvalidScope},
		{"invalid_request", 400, `{"error":"invalid_request"}`, nil},
		{"server_error", 500, `{"error":"server_error"}`, nil},
		{"no expires_at", 200, `{"consent_id":"c-1","consent_token":"refreshed"}`, nil},
		{"no token", 200, `{"consent_id":"c-1","expires_at":"2026-11-10T12:05:00Z"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := stubRegister(t, "/proofs/refresh", tc.status, tc.answer)
			_, err := client.Refresh(context.Background(), "rt", hypotheekPeer, nil)
			if err == nil {
				t.Fatal("Refresh gave no error")
			}
			if tc.refusal != nil && !errors.Is(err, tc.refusal) {
				t.Errorf("Refresh = %v, want %v", err, tc.refusal)
			}
			if tc.refusal == nil && (errors.Is(err, consent.ErrInvalidGrant) || errors.Is(err, consent.ErrInvalidScope)) {
				t.Errorf("Refresh = %v, want the register's own failure", err)
			}
		})
	}
}

func TestRefreshWithTheRegisterDownIsNoRefusal(t *testing.T) {
	client := Client{Base: "http://127.0.0.1:1", Caller: upstream.Caller{Client: http.DefaultClient}}
	_, err := client.Refresh(context.Background(), "rt", hypotheekPeer, nil)
	if err == nil || errors.Is(err, consent.ErrInvalidGrant) {
		t.Errorf("Refresh = %v, want a transport failure", err)
	}
}
