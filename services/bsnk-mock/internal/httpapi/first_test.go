package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTheFirstInterfaceGivesAPseudonymPerRecipient(t *testing.T) {
	srv := newTestServer(t, false)

	var atSource, atServiceProvider struct {
		Pseudonym string `json:"pseudonym"`
	}
	ok(t, srv, "/pseudonymize", map[string]string{"bsn": "987654321", "recipient_oin": sourceOIN}, &atSource)
	ok(t, srv, "/pseudonymize", map[string]string{"bsn": "987654321", "recipient_oin": serviceProviderOIN}, &atServiceProvider)
	if atSource.Pseudonym == "" || atSource.Pseudonym == atServiceProvider.Pseudonym {
		t.Fatalf("pseudonyms = %q and %q, want one per recipient", atSource.Pseudonym, atServiceProvider.Pseudonym)
	}
	if strings.Contains(atSource.Pseudonym, "987654321") {
		t.Errorf("the pseudonym %q carries the BSN", atSource.Pseudonym)
	}
}

func TestTheFirstInterfaceRefusals(t *testing.T) {
	srv := newTestServer(t, false)

	status, answer := call(t, srv, "/pseudonymize", map[string]string{"recipient_oin": sourceOIN})
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(answer, &e)
	if status != http.StatusBadRequest || e.Error == "" {
		t.Errorf("pseudonymize without a BSN: status %d, answer %s; want 400 with an error", status, answer)
	}

	// A PI is no longer turned back into a BSN here: a party reads the value
	// made for it with its own keys.
	if status, _ := call(t, srv, "/transform", map[string]string{"pi": "PI-0000000000000000", "recipient_oin": sourceOIN}); status != http.StatusNotFound {
		t.Errorf("/transform answered %d, want 404", status)
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t, false)
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
