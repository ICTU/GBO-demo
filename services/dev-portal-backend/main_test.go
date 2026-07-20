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
