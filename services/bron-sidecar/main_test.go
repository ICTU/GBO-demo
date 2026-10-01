package main

import (
	"encoding/json"
	ldv "gbo-demo/ldv-client"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mustMux builds the sidecar's routing tree, failing the test when its keys
// cannot be read.
func mustMux(t *testing.T, cfg config, client *http.Client, logbook *ldv.Client) *http.ServeMux {
	t.Helper()
	mux, err := newMux(cfg, client, logbook)
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	return mux
}

// Without a consent token the request is forwarded unchanged: the GraphQL
// body goes to the upstream source verbatim and nothing is decrypted. The
// stub upstream captures what it received so we can assert pass-through
// fidelity.
func TestWithoutAConsentTokenTheRequestIsForwardedUnchanged(t *testing.T) {
	decryptions := 0
	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decryptions++
		http.Error(w, "not expected", http.StatusInternalServerError)
	}))
	defer component.Close()

	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"heeftBelastingjaarAangifte":[{"belastingjaar":2024}]}}}`))
	}))
	defer upstream.Close()

	cfg := config{
		Port:          "0",
		UpstreamURL:   upstream.URL,
		DecryptionURL: component.URL,
		OwnPeerOIN:    "99999999900000000200",
		PseudonymVars: "bsn",
	}
	client := &http.Client{Timeout: 5 * time.Second}
	srv := httptest.NewServer(mustMux(t, cfg, client, nil))
	defer srv.Close()

	reqBody := `{"query":"query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { heeftBelastingjaarAangifte { belastingjaar } } }","variables":{"bsn":"123456789"}}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	// No consent token, so the body is forwarded verbatim — no rewrite.
	if receivedBody != reqBody {
		t.Fatalf("upstream body mismatch:\n got: %s\nwant: %s", receivedBody, reqBody)
	}
	if decryptions != 0 {
		t.Fatalf("the decryption component was called %d times without a consent token", decryptions)
	}

	var out struct {
		Data struct {
			IngeschrevenPersoon struct {
				HeeftBelastingjaarAangifte []struct {
					Belastingjaar int `json:"belastingjaar"`
				} `json:"heeftBelastingjaarAangifte"`
			} `json:"ingeschrevenPersoon"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte) != 1 || out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte[0].Belastingjaar != 2024 {
		t.Fatalf("unexpected response shape: %+v", out)
	}
}

func TestHealth(t *testing.T) {
	cfg := config{UpstreamURL: "http://unused.invalid", DecryptionURL: "http://unused.invalid", PseudonymVars: "bsn"}
	srv := httptest.NewServer(mustMux(t, cfg, &http.Client{Timeout: time.Second}, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

// A bron without a logbook is a supported configuration — main() only warns
// when LDV_LOGBOOK_URL is unset. The forward must still work when such a bron
// is called over FSC, which is the only case that reaches into the client for
// the foreign processor.
func TestForwardOverFSCWithoutALogbook(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":null}}`))
	}))
	defer upstream.Close()

	cfg := config{
		Port:          "0",
		UpstreamURL:   upstream.URL,
		DecryptionURL: "http://unused.invalid",
		OwnPeerOIN:    "99999999900000000200",
		PseudonymVars: "bsn",
	}
	srv := httptest.NewServer(mustMux(t, cfg, &http.Client{Timeout: 5 * time.Second}, nil))
	defer srv.Close()

	body := `{"query":"query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { burgerservicenummer } }","variables":{"bsn":"123456789"}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The grant hash is what an inway always sets; it is the branch that
	// reaches for the client's peer URI base.
	req.Header.Set("Fsc-Grant-Hash", "$1$3$WHLGvnjSifhlcCQQXs4jzDCJt9PlDvjrFXe64yAD1miKBx1D")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, out)
	}
}

// A source that is told where its keys are does not start without them: it
// would otherwise pass the placeholder on to the source.
func TestASourceDoesNotStartWithoutTheKeysItWasPointedAt(t *testing.T) {
	cfg := config{UpstreamURL: "http://unused.invalid", PseudonymVars: "bsn", SubjectKeysDir: t.TempDir()}
	if _, err := newMux(cfg, &http.Client{Timeout: time.Second}, nil); err == nil {
		t.Fatal("newMux accepted a key directory without keys")
	}
}
