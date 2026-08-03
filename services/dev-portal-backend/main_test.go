package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthAndEmptyHistory exercises the two happy-path endpoints that
// have no external dependencies: /health returns {status:"ok"} and
// /history returns [] when the history-file does not yet exist. Verifies
// the mux wires both handlers correctly and that readHistory's
// os.IsNotExist path returns an empty JSON array (not null).
func TestHealthAndEmptyHistory(t *testing.T) {
	cfg := config{
		VarDir:        t.TempDir(),
		PredefinedDir: t.TempDir(),
	}
	hub := newTraceHub(10 * time.Minute)
	srv := httptest.NewServer(newMux(cfg, hub))
	defer srv.Close()

	// /health
	healthResp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get /health: %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", healthResp.StatusCode)
	}
	var health map[string]string
	if err := json.NewDecoder(healthResp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health["status"] != "ok" {
		t.Fatalf("health = %+v, want status=ok", health)
	}

	// /history — empty state must be []
	histResp, err := http.Get(srv.URL + "/history")
	if err != nil {
		t.Fatalf("get /history: %v", err)
	}
	defer histResp.Body.Close()
	if histResp.StatusCode != http.StatusOK {
		t.Fatalf("history status = %d, want 200", histResp.StatusCode)
	}
	var hist []HistoryRun
	if err := json.NewDecoder(histResp.Body).Decode(&hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("history = %+v, want empty slice", hist)
	}
}

func TestLoadConfigAllowsKubernetesLokiSelector(t *testing.T) {
	const selector = `{namespace="fds-tenant-minbzk",container="opa"} |= "Decision Log"`
	t.Setenv("LOKI_DECISION_QUERY", selector)

	cfg := loadConfig()
	if cfg.LokiDecisionQuery != selector {
		t.Fatalf("LokiDecisionQuery = %q, want %q", cfg.LokiDecisionQuery, selector)
	}
}

func TestLoadConfigSupportsProviderLogsForBothConsumerPeers(t *testing.T) {
	t.Setenv("FSC_TXLOG_BD_HV_URL", "https://manager.example.test")
	t.Setenv("FSC_TXLOG_BD_HV_CERT", "/certs/hv/tls.crt")
	t.Setenv("FSC_TXLOG_BD_HV_KEY", "/certs/hv/tls.key")
	t.Setenv("FSC_TXLOG_BD_HV_CA", "/certs/hv/ca.crt")
	t.Setenv("FSC_TXLOG_BD_EDI_URL", "https://manager.example.test")
	t.Setenv("FSC_TXLOG_BD_EDI_CERT", "/certs/edi/tls.crt")
	t.Setenv("FSC_TXLOG_BD_EDI_KEY", "/certs/edi/tls.key")
	t.Setenv("FSC_TXLOG_BD_EDI_CA", "/certs/edi/ca.crt")

	cfg := loadConfig()
	found := map[string]fscTxlogPeer{}
	for _, peer := range cfg.FscTxlogPeers {
		found[peer.Name] = peer
	}
	for _, name := range []string{"bd-via-hv", "bd-via-edi"} {
		peer, ok := found[name]
		if !ok {
			t.Fatalf("missing FSC txlog source %q in %+v", name, cfg.FscTxlogPeers)
		}
		if peer.SendGroupID {
			t.Fatalf("provider Manager source %q must not send group_id", name)
		}
	}
}

// A slow Loki must not hang a dev-portal request. These handlers used
// http.DefaultClient, which has no timeout at all: an unresponsive upstream
// blocked the handler indefinitely rather than failing.
func TestSlowUpstreamFailsInsteadOfHanging(t *testing.T) {
	// Answers far later than the client's timeout, but returns as soon as the
	// client gives up so the server can shut down cleanly.
	slowLoki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slowLoki.Close()

	// Shorten the shared client for the test; the production value is 10s.
	restore := upstreamClient
	upstreamClient = &http.Client{Timeout: 150 * time.Millisecond}
	defer func() { upstreamClient = restore }()

	cfg := config{VarDir: t.TempDir(), PredefinedDir: t.TempDir(), LokiURL: slowLoki.URL}
	srv := httptest.NewServer(newMux(cfg, newTraceHub(10*time.Minute)))
	defer srv.Close()

	done := make(chan int, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/decision?trace_id=abc123")
		if err != nil {
			done <- -1
			return
		}
		defer resp.Body.Close()
		done <- resp.StatusCode
	}()

	select {
	case status := <-done:
		if status == http.StatusOK {
			t.Fatalf("slow upstream returned 200; expected a gateway failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler hung on an unresponsive upstream: the client has no timeout")
	}
}

// Guard: the shared client must always carry a timeout. Reverting any call
// site to http.DefaultClient reintroduces the hang.
func TestUpstreamClientHasTimeout(t *testing.T) {
	if upstreamClient.Timeout <= 0 {
		t.Fatal("upstreamClient has no timeout")
	}
}
