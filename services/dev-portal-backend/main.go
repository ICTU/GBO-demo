// Package main implements the demo "dev-portal-backend" — a small persistence
// layer for the developer-portal frontend. Two concerns only:
//   - scenarios: list of pre-defined (source-controlled in ./scenarios) and
//     user-saved (in /var/scenarios) requests the portal can load.
//   - history: append-only log of runs started from the portal.
//
// Plus two passthrough endpoints that expose mock data from other services
// (citizens and organizations) so the builder does not need a separate
// hardcoded list — a single source of truth per dataset.
//
// The backend does NOT proxy the real chain calls. The frontend talks
// directly to consent-portal-backend (issuance) and dienstverlener-backend
// (use). This avoids polluting the trace with an extra hop.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type config struct {
	Port              string
	VarDir            string
	PredefinedDir     string
	CitizensFile      string
	OrganizationsFile string
	LokiURL           string
	PoliciesDir       string
	RulesFile         string
	FscGroupID        string
	FscTxlogPeers     []fscTxlogPeer
}

// fscTxlogPeer describes one FSC-org whose txlog-api we may query.
// Demo shortcut: intra-org internal-cert used as client-cert. See
// handleFscTxlog in fsctxlog.go.
type fscTxlogPeer struct {
	Name string // display label (edi, bd)
	URL  string
	Cert string
	Key  string
	CA   string
}

func loadConfig() config {
	peers := []fscTxlogPeer{}
	for _, prefix := range []string{"EDI", "HV", "BD"} {
		url := os.Getenv("FSC_TXLOG_" + prefix + "_URL")
		if url == "" {
			continue
		}
		peers = append(peers, fscTxlogPeer{
			Name: strings.ToLower(prefix),
			URL:  url,
			Cert: os.Getenv("FSC_TXLOG_" + prefix + "_CERT"),
			Key:  os.Getenv("FSC_TXLOG_" + prefix + "_KEY"),
			CA:   os.Getenv("FSC_TXLOG_" + prefix + "_CA"),
		})
	}
	return config{
		Port:              getEnv("PORT", "4007"),
		VarDir:            getEnv("VAR_DIR", "/var"),
		PredefinedDir:     getEnv("PREDEFINED_DIR", "/scenarios"),
		CitizensFile:      getEnv("CITIZENS_FILE", "/citizens/citizens.json"),
		OrganizationsFile: getEnv("ORGANIZATIONS_FILE", "/orgs/organizations.json"),
		LokiURL:           getEnv("LOKI_URL", "http://loki:3100"),
		PoliciesDir:       getEnv("POLICIES_DIR", "/policies"),
		RulesFile:         getEnv("RULES_FILE", "/rules.json"),
		FscGroupID:        getEnv("FSC_GROUP_ID", "fsc-demo"),
		FscTxlogPeers:     peers,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── HTTP helpers ─────────────────────────────────────────────────────────

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ── Scenarios ────────────────────────────────────────────────────────────

// Scenario is the storage shape for both pre-defined and user-saved scenarios.
// expected_outcome is only used for UI labelling, not for runtime comparison.
type Scenario struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Desc            string          `json:"desc"`
	Tab             string          `json:"tab"` // "issuance" | "use"
	ExpectedOutcome string          `json:"expected_outcome,omitempty"`
	UserSaved       bool            `json:"user_saved"`
	Payload         json.RawMessage `json:"payload"`
}

var (
	userScenariosMu sync.RWMutex
)

func userScenariosDir(cfg config) string { return filepath.Join(cfg.VarDir, "scenarios") }

func ensureDir(p string) error {
	return os.MkdirAll(p, 0o755)
}

func loadScenariosFromDir(dir string, userSaved bool) ([]Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Scenario
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s Scenario
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		s.UserSaved = userSaved
		if s.ID == "" {
			s.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		out = append(out, s)
	}
	return out, nil
}

func handleScenarios(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			pre, _ := loadScenariosFromDir(cfg.PredefinedDir, false)
			userScenariosMu.RLock()
			usr, _ := loadScenariosFromDir(userScenariosDir(cfg), true)
			userScenariosMu.RUnlock()
			result := append([]Scenario{}, pre...)
			result = append(result, usr...)
			if result == nil {
				result = []Scenario{}
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodPost:
			var s Scenario
			if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
				return
			}
			if s.Name == "" || s.Tab == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and tab required"})
				return
			}
			s.UserSaved = true
			if s.ID == "" {
				s.ID = "user-" + uuid.New().String()[:8]
			}
			dir := userScenariosDir(cfg)
			if err := ensureDir(dir); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			userScenariosMu.Lock()
			defer userScenariosMu.Unlock()
			path := filepath.Join(dir, s.ID+".json")
			data, _ := json.MarshalIndent(s, "", "  ")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, s)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleScenarioByID(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		id := strings.TrimPrefix(r.URL.Path, "/scenarios/")
		if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		userScenariosMu.Lock()
		defer userScenariosMu.Unlock()
		path := filepath.Join(userScenariosDir(cfg), id+".json")
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "scenario not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// ── History ──────────────────────────────────────────────────────────────

type HistoryRun struct {
	RunID        string          `json:"run_id"`
	ScenarioName string          `json:"scenario_name"`
	Tab          string          `json:"tab"`
	Payload      json.RawMessage `json:"payload"`
	TraceID      string          `json:"trace_id"`
	Outcome      string          `json:"outcome"` // "allow" | "deny" | "error"
	TS           string          `json:"ts"`
	// Filled for issuance runs: the consent_id the portal-backend returned, so
	// Use-form scenarios can re-use it without the user pasting hex strings.
	ConsentID string `json:"consent_id,omitempty"`
	// Filled by burger-FE bubble-POSTs so the dev-portal can render the
	// actual response body in its result-panel when watching a flow.
	// Self-triggered runs from dev-portal already have the body locally
	// and omit this field.
	Response json.RawMessage `json:"response,omitempty"`
}

var historyMu sync.Mutex

func historyFile(cfg config) string { return filepath.Join(cfg.VarDir, "history.jsonl") }

func appendHistory(cfg config, run HistoryRun) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	if err := ensureDir(cfg.VarDir); err != nil {
		return err
	}
	f, err := os.OpenFile(historyFile(cfg), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, _ := json.Marshal(run)
	_, err = fmt.Fprintln(f, string(line))
	return err
}

func readHistory(cfg config, limit int) ([]HistoryRun, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	data, err := os.ReadFile(historyFile(cfg))
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryRun{}, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var out []HistoryRun
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		var h HistoryRun
		if err := json.Unmarshal([]byte(ln), &h); err == nil {
			out = append(out, h)
		}
	}
	// Recent first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []HistoryRun{}
	}
	return out, nil
}

func handleHistory(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			runs, err := readHistory(cfg, 100)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, runs)
		case http.MethodPost:
			var run HistoryRun
			if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
				return
			}
			if run.RunID == "" {
				run.RunID = uuid.New().String()
			}
			if run.TS == "" {
				run.TS = time.Now().UTC().Format(time.RFC3339)
			}
			if err := appendHistory(cfg, run); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, run)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

// ── Passthrough: citizens + organizations ────────────────────────────────

// passthroughFile returns the file as-is (the frontend parses it itself).
// Prevents drift: the upstream mockdata files (citizens, organizations)
// remain the single source of truth.
func passthroughFile(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		f, err := os.Open(path)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	}
}

// ── Decision lookup via Loki ────────────────────────────────────────────

// The OpenFTV PDP writes one "Decision Log" line per evaluation to stdout
// (embedded OPA console decision-logs). Promtail ships them to Loki under
// the {compose_service="openftv-pdp"} label. We query by trace_id (which
// pdp-service injects into the AuthZEN context) and return a normalized
// entry.

type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Values [][2]string `json:"values"` // [ts_ns, line]
		} `json:"result"`
	} `json:"data"`
}

const decisionLogExpr = `{compose_service="openftv-pdp"} |= "Decision Log"`

// traceIDOf extracts the trace_id from an OpenFTV decision-log entry.
// pdp-service places it in the AuthZEN context → input.context.trace_id.
func traceIDOf(entry map[string]any) string {
	input, _ := entry["input"].(map[string]any)
	ctx, _ := input["context"].(map[string]any)
	tid, _ := ctx["trace_id"].(string)
	return tid
}

// normalizeDecisionEntry reshapes an OpenFTV decision-log line into the
// entry-shape the dev-portal renders: {path, input, result:{decision,
// context}, decision_id}. OpenFTV logs result = {allow, reason,
// response}; our authz policy puts the legacy {decision, context}
// document (granted[]/denied_fields[]/reason_admin) in `response`, so
// we unwrap it here — the frontend contract stays unchanged.
func normalizeDecisionEntry(entry map[string]any) map[string]any {
	result, _ := entry["result"].(map[string]any)
	if result == nil {
		return entry
	}
	out := map[string]any{
		"decision_id": entry["decision_id"],
		"path":        entry["path"],
		"input":       entry["input"],
		"ts":          entry["timestamp"],
	}
	if resp, ok := result["response"].(map[string]any); ok {
		out["result"] = resp
	} else {
		out["result"] = map[string]any{"decision": result["allow"], "context": map[string]any{}}
	}
	return out
}

func handleDecision(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		traceID := r.URL.Query().Get("trace_id")
		if traceID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace_id query param required"})
			return
		}

		// Search the last 30 minutes — plenty for the demo, bounded to avoid scans.
		now := time.Now()
		expr := decisionLogExpr
		u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=200&direction=backward",
			cfg.LokiURL, url.QueryEscape(expr), now.Add(-30*time.Minute).UnixNano(), now.UnixNano())

		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "loki unreachable: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("loki %d: %s", resp.StatusCode, string(body))})
			return
		}
		var lr lokiQueryResponse
		if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "loki decode: " + err.Error()})
			return
		}

		// Scan each line — accept the first whose trace_id matches.
		for _, stream := range lr.Data.Result {
			for _, v := range stream.Values {
				line := v[1]
				var entry map[string]any
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					continue
				}
				if traceIDOf(entry) == traceID {
					writeJSON(w, http.StatusOK, normalizeDecisionEntry(entry))
					return
				}
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no decision log entry for trace_id (last 30m)"})
	}
}

// ── Explain & policy-source (generic, no package-pinning) ───────────────

// handleExplain returns every decision that was recorded under a given
// trace_id, with the captured input + normalized result for each.
//
// TODO(OpenFTV): ?mode=full|fails used to replay the captured input
// against OPA's ?explain API for a rule-by-rule evaluation trace.
// OpenFTV PDP has no explain endpoint, so the replay is dropped and the
// UI shows the decision-log detail only. Possible future workaround:
// dev-only `opa eval --explain` sidecar (see ICTU-2 plan).
func handleExplain(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		traceID := r.URL.Query().Get("trace_id")
		if traceID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace_id query param required"})
			return
		}
		mode := r.URL.Query().Get("mode") // "", "full", or "fails"

		decisions, err := lokiDecisionsForTrace(r.Context(), cfg, traceID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		out := make([]map[string]any, 0, len(decisions))
		for _, d := range decisions {
			entry := normalizeDecisionEntry(d)
			if mode == "full" || mode == "fails" {
				entry["explanation_unavailable"] = "rule-evaluation trace is not available with the OpenFTV engine"
			}
			out = append(out, entry)
		}
		writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
	}
}

// lokiDecisionsForTrace pulls every "Decision Log" line that matches the
// given trace_id (last 30m) and returns the parsed entries in chronological
// order (oldest first), deduplicated by decision_id.
func lokiDecisionsForTrace(ctx context.Context, cfg config, traceID string) ([]map[string]any, error) {
	now := time.Now()
	expr := decisionLogExpr
	u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=500&direction=forward",
		cfg.LokiURL, url.QueryEscape(expr), now.Add(-30*time.Minute).UnixNano(), now.UnixNano())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("loki %d: %s", resp.StatusCode, string(body))
	}
	var lr lokiQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("loki decode: %w", err)
	}
	type stamped struct {
		ts  string
		raw map[string]any
	}
	var rows []stamped
	for _, stream := range lr.Data.Result {
		for _, v := range stream.Values {
			var entry map[string]any
			if json.Unmarshal([]byte(v[1]), &entry) != nil {
				continue
			}
			if traceIDOf(entry) == traceID {
				rows = append(rows, stamped{ts: v[0], raw: entry})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ts < rows[j].ts })
	seen := make(map[string]bool)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		id, _ := r.raw["decision_id"].(string)
		if id != "" && seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, r.raw)
	}
	return out, nil
}

// handleRules serves the pre-generated rules.json (rule metadata:
// covers_types, covers_fields, spec) so the dev-portal's RuleSpecPanel
// can render a rule's declared scope + evaluation criteria. OpenFTV PDP
// has no data-API to evaluate data.dvtp.gbo.rules live, so the file is
// generated from the Rego sources by scripts/gen-rules-json.sh (CI
// checks freshness). Content is identical to the old live evaluation.
func handleRules(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		raw, err := os.ReadFile(cfg.RulesFile)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "rules.json unreadable (run scripts/gen-rules-json.sh): " + err.Error()})
			return
		}
		// File shape: { "<rule_pkg_leaf>": {rule_id, covers_types, covers_fields, spec} }
		var rules map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rules); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "rules.json decode: " + err.Error()})
			return
		}
		// Flatten to a list — the package-leaf key (e.g. "dvt0001") is
		// redundant since each rule carries its own rule_id.
		out := make([]json.RawMessage, 0, len(rules))
		for _, m := range rules {
			out = append(out, m)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// policyFile is one Rego source file read from the policies directory.
type policyFile struct {
	ID  string // path relative to the policies dir, e.g. "dvtp/gbo/engine.rego"
	Raw string
}

// readPolicyFiles walks cfg.PoliciesDir and reads every .rego file.
// Replaces OPA's /v1/policies listing: the OpenFTV PDP has no policy
// introspection API, so the dev-portal reads the same files the engine
// has mounted.
func readPolicyFiles(dir string) ([]policyFile, error) {
	var out []policyFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".rego") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, policyFile{ID: filepath.ToSlash(rel), Raw: string(raw)})
		return nil
	})
	return out, err
}

// handlePolicySource returns the raw Rego for a single policy file.
// Used for the "click rule → show snippet" feature. `id` query-param is
// the policy path relative to the policies dir (e.g. "dvtp/gbo/engine.rego").
func handlePolicySource(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id query param required"})
			return
		}
		clean := filepath.Clean(id)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid policy id"})
			return
		}
		raw, err := os.ReadFile(filepath.Join(cfg.PoliciesDir, clean))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "policy not found: " + id})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "raw": string(raw)})
	}
}

// handlePolicySnippet locates the Rego file + line where a given reason-code
// string literal appears (in `:= "<code>"` form, the convention used by
// reason-cascade rules). Returns the raw file + matched line so the UI can
// render a snippet view centred on that line.
//
// Generic across policies that follow the convention `:= "<code>"` in a
// rule body. Path is the policy package-path (e.g. "dvtp/gbo/lib" maps to
// `package dvtp.authz`).
func handlePolicySnippet(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		path := r.URL.Query().Get("path")
		code := r.URL.Query().Get("code")
		if path == "" || code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path and code query params required"})
			return
		}
		pkgName := strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", ".")

		policies, err := readPolicyFiles(cfg.PoliciesDir)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "read policies: " + err.Error()})
			return
		}
		// Four-tier search, most-specific-first. The goal is to land on the
		// line that EMITS this code as a DENY-reason, not on lookup-tables
		// or the matching PASS-clause:
		//   1. file declaring the request's package AND containing the literal
		//   2. line containing BOTH <needle> AND `"fail")` (RFC0052-style
		//      GBO-lib: `_step("<CODE>", ..., "fail")` on one line)
		//   3. line containing BOTH <needle> AND `deny_reason(` (older
		//      reason-cascade style)
		//   4. fall-through: any line containing the literal
		needle := fmt.Sprintf("%q", code)
		var picked struct {
			id, raw string
			line    int
		}
		for _, p := range policies {
			if !strings.Contains(p.Raw, "package "+pkgName) {
				continue
			}
			if line := findLineWith(p.Raw, needle); line > 0 {
				picked.id, picked.raw, picked.line = p.ID, p.Raw, line
				break
			}
		}
		if picked.id == "" {
			for _, p := range policies {
				if line := findLineWithAll(p.Raw, needle, `"fail")`); line > 0 {
					picked.id, picked.raw, picked.line = p.ID, p.Raw, line
					break
				}
			}
		}
		if picked.id == "" {
			for _, p := range policies {
				if line := findLineWithAll(p.Raw, needle, "deny_reason("); line > 0 {
					picked.id, picked.raw, picked.line = p.ID, p.Raw, line
					break
				}
			}
		}
		if picked.id == "" {
			for _, p := range policies {
				if line := findLineWith(p.Raw, needle); line > 0 {
					picked.id, picked.raw, picked.line = p.ID, p.Raw, line
					break
				}
			}
		}
		if picked.id == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no rule emitting %s found", needle)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":   picked.id,
			"line": picked.line,
			"raw":  picked.raw,
		})
	}
}

// findLineWithAll returns the 1-based line number of the first line in raw
// that contains ALL given needles. Used to match e.g. `_step("CODE", "fail")`
// where the code + status-marker live on the same line but in different
// positions.
func findLineWithAll(raw string, needles ...string) int {
	lines := strings.Split(raw, "\n")
	for i, l := range lines {
		matched := true
		for _, n := range needles {
			if !strings.Contains(l, n) {
				matched = false
				break
			}
		}
		if matched {
			return i + 1
		}
	}
	return 0
}

func findLineWith(raw, needle string) int {
	lines := strings.Split(raw, "\n")
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	return 0
}

// ── Policy chain (derived from the Rego sources on disk) ────────────────

// Parses the ordered deny-cascade out of the evaluation library: the
// `_step("<CODE>", ...)` literals in lib.rego appear in cascade order
// (consent-exists → not-withdrawn → not-expired → scope → constraint →
// pid → scope-whitelist → actor-whitelist). Keeps the developer-portal
// in sync with the policy without hardcoding.

var chainRE = regexp.MustCompile(`_step\("([^"]+)"`)

func handlePolicyChain(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		policies, err := readPolicyFiles(cfg.PoliciesDir)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "read policies: " + err.Error()})
			return
		}
		// Find the evaluation library; that's where the cascade lives.
		var raw string
		for _, p := range policies {
			if strings.Contains(p.Raw, "package dvtp.gbo.lib") {
				raw = p.Raw
				break
			}
		}
		if raw == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "dvtp.gbo.lib policy not found on disk"})
			return
		}
		matches := chainRE.FindAllStringSubmatch(raw, -1)
		seen := make(map[string]bool)
		codes := make([]string, 0, len(matches))
		for _, m := range matches {
			if !seen[m[1]] {
				seen[m[1]] = true
				codes = append(codes, m[1])
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
	}
}

// ── Main ─────────────────────────────────────────────────────────────────

// newMux builds the routing tree with the given config and trace-hub.
// Extracted from main so integration tests can wire the handlers to an
// httptest.Server without starting the real listener or the hub's
// cleanupLoop goroutine (tests can pass a hub they created themselves).
func newMux(cfg config, hub *traceHub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/scenarios", handleScenarios(cfg))
	mux.HandleFunc("/scenarios/", handleScenarioByID(cfg))
	mux.HandleFunc("/history", handleHistory(cfg))
	mux.HandleFunc("/citizens", passthroughFile(cfg.CitizensFile))
	mux.HandleFunc("/organizations", passthroughFile(cfg.OrganizationsFile))
	mux.HandleFunc("/decision", handleDecision(cfg))
	mux.HandleFunc("/policy-chain", handlePolicyChain(cfg))
	mux.HandleFunc("/rules", handleRules(cfg))
	mux.HandleFunc("/explain", handleExplain(cfg))
	mux.HandleFunc("/policy-source", handlePolicySource(cfg))
	mux.HandleFunc("/policy-snippet", handlePolicySnippet(cfg))

	mux.HandleFunc("/fsc/txlog/", handleFscTxlog(cfg))
	mux.HandleFunc("/v1/traces", handleOTLPTraces(hub))
	mux.HandleFunc("/events", handleChainEvents(hub))
	mux.HandleFunc("/watch-next", handleWatchNext(hub))
	return mux
}

func main() {
	cfg := loadConfig()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}).
		WithAttrs([]slog.Attr{slog.String("service", "dev-portal-backend")})))

	hub := newTraceHub(10 * time.Minute)
	mux := newMux(cfg, hub)

	handler := otelhttp.NewHandler(withAccessLog(mux), "dev-portal-backend")
	addr := ":" + cfg.Port
	slog.Info("dev-portal-backend starting", "addr", addr, "var_dir", cfg.VarDir, "predefined_dir", cfg.PredefinedDir)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "err", err)
	}
}
