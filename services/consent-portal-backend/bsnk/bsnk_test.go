package bsnk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

type relyingPartyRequest struct {
	EntityID       string
	KeySetVersion  int
	IdentifierType string
}

// fourAtATime stands in for BSNk's transformation: it refuses more than four
// parties, like BSNk does, and answers with a value per party otherwise. Each
// request's parties are kept in requests.
func fourAtATime(t *testing.T, requests *[][]relyingPartyRequest) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RelyingParty []relyingPartyRequest
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("transform request: %v", err)
		}
		mu.Lock()
		*requests = append(*requests, req.RelyingParty)
		mu.Unlock()
		if len(req.RelyingParty) > maxRelyingParties {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"FaultReason":"ProvisioningRefused","FaultDescription":"more than four relying parties"}`))
			return
		}
		type encrypted struct {
			EntityID       string `json:"EntityID"`
			KeySetVersion  int    `json:"KeySetVersion"`
			IdentifierType string `json:"IdentifierType"`
			Value          string `json:"value"`
		}
		var out struct{ Encrypted []encrypted }
		for _, rp := range req.RelyingParty {
			out.Encrypted = append(out.Encrypted, encrypted{rp.EntityID, rp.KeySetVersion, rp.IdentifierType, "value-for-" + rp.EntityID})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func client(base string) Client {
	return Client{
		Base:      base,
		Caller:    upstream.Caller{Client: &http.Client{Timeout: 5 * time.Second}},
		Requester: "99999999900000000900",
	}
}

func parties(n int) []consent.Party {
	out := make([]consent.Party, n)
	for i := range out {
		out[i] = consent.Party{OIN: fmt.Sprintf("999999999000000002%02d", i), KeySetVersion: 20260101}
	}
	return out
}

// A consent with more parties than BSNk takes in one request still gets a
// value for each of them: the parties are split over requests of at most four.
func TestFiveOrMorePartiesAreSplitOverRequests(t *testing.T) {
	var requests [][]relyingPartyRequest
	bsnk := fourAtATime(t, &requests)
	defer bsnk.Close()

	encrypted, err := client(bsnk.URL).Transform(context.Background(), consent.Polymorphic{PI: "PI", PP: "PP"}, consent.FormPseudonym, parties(9))
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if len(requests) != 3 || len(requests[0]) != 4 || len(requests[1]) != 4 || len(requests[2]) != 1 {
		t.Errorf("requests = %v, want 4, 4 and 1 parties", requests)
	}
	for _, party := range parties(9) {
		if encrypted[party.OIN] != "value-for-"+party.OIN {
			t.Errorf("no value for %s: %v", party.OIN, encrypted)
		}
	}
}

// An answer that does not match the request is not taken for one: a value
// for a party that was not asked for, or in another form.
func TestAnAnswerThatDoesNotMatchIsRefused(t *testing.T) {
	for name, answer := range map[string]string{
		"another party": `{"Encrypted":[{"EntityID":"99999999900000000300","KeySetVersion":20260101,"IdentifierType":"Pseudonym","value":"v"}]}`,
		"another form":  `{"Encrypted":[{"EntityID":"99999999900000000200","KeySetVersion":20260101,"IdentifierType":"Identity","value":"v"}]}`,
		"no value":      `{"Encrypted":[{"EntityID":"99999999900000000200","KeySetVersion":20260101,"IdentifierType":"Pseudonym","value":""}]}`,
		"too few":       `{"Encrypted":[]}`,
	} {
		bsnk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(answer))
		}))
		_, err := client(bsnk.URL).Transform(context.Background(), consent.Polymorphic{PI: "PI", PP: "PP"}, consent.FormPseudonym, parties(1))
		bsnk.Close()
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
