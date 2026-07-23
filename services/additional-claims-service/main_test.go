package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const grantRequest = `{
  "grant": {
    "data": {
      "type": "GRANT_TYPE_SERVICE_CONNECTION",
      "outway": {
        "peer_id": "0000009950DVTPCON000",
        "public_key_thumbprint": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
      },
      "service": {
        "type": "SERVICE_TYPE_SERVICE",
        "peer_id": "0000009958MINBZK0000",
        "name": "bri"
      }
    }
  }
}`

func TestAdditionalClaims(t *testing.T) {
	service := claimsService{rules: []claimRule{{
		OutwayPeerID:  "0000009950DVTPCON000",
		ServicePeerID: "0000009958MINBZK0000",
		ServiceName:   "bri",
		Add: map[string]any{
			"flow":            "dvtp:query",
			"subject_id_type": "pseudonym",
		},
	}}}
	server := httptest.NewServer(newMux(service))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/additional-claims", "application/json", strings.NewReader(grantRequest))
	if err != nil {
		t.Fatalf("post additional claims: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Add["flow"] != "dvtp:query" {
		t.Fatalf("flow = %v, want dvtp:query", result.Add["flow"])
	}
	if result.Add["subject_id_type"] != "pseudonym" {
		t.Fatalf("subject_id_type = %v, want pseudonym", result.Add["subject_id_type"])
	}
}

func TestAdditionalClaimsRejectsUnconfiguredGrant(t *testing.T) {
	server := httptest.NewServer(newMux(claimsService{}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/additional-claims", "application/json", strings.NewReader(grantRequest))
	if err != nil {
		t.Fatalf("post additional claims: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestAdditionalClaimsRejectsInvalidRequest(t *testing.T) {
	server := httptest.NewServer(newMux(claimsService{}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/additional-claims", "application/json", strings.NewReader(`{"grant":`))
	if err != nil {
		t.Fatalf("post additional claims: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHealth(t *testing.T) {
	server := httptest.NewServer(newMux(claimsService{}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
