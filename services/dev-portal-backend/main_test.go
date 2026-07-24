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
