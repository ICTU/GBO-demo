package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testIssuancePayload struct {
	CitizenBSN      string   `json:"citizen_bsn"`
	ConsumerPeerID  string   `json:"dienstverlener_oin"`
	Scopes          []string `json:"scopes"`
	ValiditySeconds int      `json:"validity_seconds"`
	UseCase         string   `json:"use_case"`
}

func dvtpIssuancePayloads(t *testing.T, cfg config) map[string]testIssuancePayload {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/scenarios", nil)
	rec := httptest.NewRecorder()
	handleScenarios(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list predefined scenarios status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var scenarios []Scenario
	if err := json.NewDecoder(rec.Body).Decode(&scenarios); err != nil {
		t.Fatalf("decode predefined scenarios: %v", err)
	}
	payloads := make(map[string]testIssuancePayload)
	for _, scenario := range scenarios {
		if scenario.Tab != "issuance" {
			continue
		}
		var payload testIssuancePayload
		if err := json.Unmarshal(scenario.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", scenario.ID, err)
		}
		payloads[scenario.ID] = payload
	}
	return payloads
}

func TestPredefinedDvtpScenariosUseLocalDefaultWithoutConfiguration(t *testing.T) {
	t.Setenv("DVTP_CONSUMER_PEER_ID", "")
	cfg := loadConfig()
	cfg.PredefinedDir = "scenarios"
	cfg.VarDir = t.TempDir()

	if cfg.DvtpConsumerPeerID != defaultDvtpConsumerPeerID {
		t.Fatalf("DvtpConsumerPeerID = %q, want local default %q", cfg.DvtpConsumerPeerID, defaultDvtpConsumerPeerID)
	}
	for id, payload := range dvtpIssuancePayloads(t, cfg) {
		if payload.ConsumerPeerID != defaultDvtpConsumerPeerID {
			t.Errorf("%s dienstverlener_oin = %q, want %q", id, payload.ConsumerPeerID, defaultDvtpConsumerPeerID)
		}
	}
}

func TestPredefinedDvtpScenariosUseConfiguredPeerIDAndPreservePayload(t *testing.T) {
	const peerID = "0000009950HYPBV00000"
	t.Setenv("DVTP_CONSUMER_PEER_ID", peerID)
	cfg := loadConfig()
	cfg.PredefinedDir = "scenarios"
	cfg.VarDir = t.TempDir()

	want := map[string]testIssuancePayload{
		"issuance-hypotheek-2025-only": {
			CitizenBSN:      "123456789",
			ConsumerPeerID:  peerID,
			Scopes:          []string{"bd:ib:2025"},
			ValiditySeconds: 7776000,
			UseCase:         "hypotheek",
		},
		"issuance-hypotheek-2025-2024": {
			CitizenBSN:      "123456789",
			ConsumerPeerID:  peerID,
			Scopes:          []string{"bd:ib:2025", "bd:ib:2024"},
			ValiditySeconds: 7776000,
			UseCase:         "hypotheek",
		},
	}
	if got := dvtpIssuancePayloads(t, cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("DvTP issuance payloads = %#v, want %#v", got, want)
	}
}

func TestCustomIssuanceScenarioKeepsEnteredConsumerPeerID(t *testing.T) {
	const customPeerID = "custom-consumer-peer"
	cfg := config{
		VarDir:             t.TempDir(),
		PredefinedDir:      t.TempDir(),
		DvtpConsumerPeerID: "0000009950HYPBV00000",
	}
	custom := Scenario{
		ID:   "user-custom-issuance",
		Name: "Custom issuance",
		Tab:  "issuance",
		Payload: json.RawMessage(`{
			"citizen_bsn":"987654321",
			"dienstverlener_oin":"custom-consumer-peer",
			"scopes":["custom:scope"],
			"validity_seconds":123
		}`),
	}
	body, err := json.Marshal(custom)
	if err != nil {
		t.Fatalf("encode custom scenario: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/scenarios", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleScenarios(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/scenarios", nil)
	rec = httptest.NewRecorder()
	handleScenarios(cfg).ServeHTTP(rec, req)
	var scenarios []Scenario
	if err := json.NewDecoder(rec.Body).Decode(&scenarios); err != nil {
		t.Fatalf("decode scenarios: %v", err)
	}
	for _, scenario := range scenarios {
		if scenario.ID != custom.ID {
			continue
		}
		var payload testIssuancePayload
		if err := json.Unmarshal(scenario.Payload, &payload); err != nil {
			t.Fatalf("decode custom payload: %v", err)
		}
		if payload.ConsumerPeerID != customPeerID {
			t.Fatalf("custom dienstverlener_oin = %q, want %q", payload.ConsumerPeerID, customPeerID)
		}
		if !reflect.DeepEqual(payload.Scopes, []string{"custom:scope"}) || payload.ValiditySeconds != 123 {
			t.Fatalf("custom payload changed unexpectedly: %#v", payload)
		}
		return
	}
	t.Fatal("saved custom issuance scenario not returned")
}

func TestSaveScenarioRejectsIDThatEscapesStorageDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := config{
		VarDir:        filepath.Join(root, "var"),
		PredefinedDir: t.TempDir(),
	}
	scenario := Scenario{
		ID:      "../../escaped",
		Name:    "Unsafe scenario",
		Tab:     "issuance",
		Payload: json.RawMessage(`{}`),
	}
	body, err := json.Marshal(scenario)
	if err != nil {
		t.Fatalf("encode scenario: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/scenarios", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleScenarios(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("save status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.json")); !os.IsNotExist(err) {
		t.Fatalf("scenario escaped storage directory: stat err = %v", err)
	}
}

func TestPolicySourceConfinesReadsToPoliciesDirectory(t *testing.T) {
	root := t.TempDir()
	policiesDir := filepath.Join(root, "policies")
	if err := os.Mkdir(policiesDir, 0o755); err != nil {
		t.Fatalf("create policies directory: %v", err)
	}
	const validPolicy = "package valid"
	if err := os.WriteFile(filepath.Join(policiesDir, "valid.rego"), []byte(validPolicy), 0o644); err != nil {
		t.Fatalf("write valid policy: %v", err)
	}
	outside := filepath.Join(root, "outside.rego")
	const secret = "outside-policy-content"
	if err := os.WriteFile(outside, []byte(secret), 0o644); err != nil {
		t.Fatalf("write outside policy: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(policiesDir, "escape.rego")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	cfg := config{PoliciesDir: policiesDir}
	req := httptest.NewRequest(http.MethodGet, "/policy-source?id=valid.rego", nil)
	rec := httptest.NewRecorder()
	handlePolicySource(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), validPolicy) {
		t.Fatalf("valid policy response: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/policy-source?id=escape.rego", nil)
	rec = httptest.NewRecorder()
	handlePolicySource(cfg).ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("policy source followed an escaping symlink; body=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("policy source disclosed a file outside the policies directory")
	}
}

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
	srv := httptest.NewServer(newMux(cfg, hub, nil))
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

// The timeline is per-developer: your own runs plus the ones nobody could
// attribute. A colleague's run must not show up, because the Use-form
// prefills its consent_id from the most recent issuance in this list.
func TestHistoryFiltersBySession(t *testing.T) {
	cfg := config{VarDir: t.TempDir(), PredefinedDir: t.TempDir()}
	for _, run := range []HistoryRun{
		{RunID: "mine", TraceID: "a", DemoSession: "dev-a"},
		{RunID: "theirs", TraceID: "b", DemoSession: "dev-b"},
		{RunID: "untagged", TraceID: "c"},
	} {
		if err := appendHistory(cfg, run); err != nil {
			t.Fatalf("append %s: %v", run.RunID, err)
		}
	}

	runs, err := readHistory(cfg, 100, "dev-a")
	if err != nil {
		t.Fatalf("readHistory: %v", err)
	}
	got := map[string]bool{}
	for _, r := range runs {
		got[r.RunID] = true
	}
	if !got["mine"] {
		t.Error("own run missing from the timeline")
	}
	if !got["untagged"] {
		t.Error("unattributable run missing: untagged means unknown, not someone else's")
	}
	if got["theirs"] {
		t.Error("another session's run leaked into the timeline")
	}

	// No session asked for: the whole timeline, as before sessions existed.
	all, err := readHistory(cfg, 100, "")
	if err != nil {
		t.Fatalf("readHistory unfiltered: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("unfiltered history = %d runs, want 3", len(all))
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
	srv := httptest.NewServer(newMux(cfg, newTraceHub(10*time.Minute), nil))
	defer srv.Close()

	done := make(chan decisionsResponse, 1)
	go func() {
		var out decisionsResponse
		resp, err := http.Get(srv.URL + "/decisions?transaction_id=abc123")
		if err == nil {
			defer resp.Body.Close()
			_ = json.NewDecoder(resp.Body).Decode(&out)
		}
		done <- out
	}()

	select {
	case out := <-done:
		if out.Engine.Error == "" {
			t.Fatalf("slow Loki reported no error; expected the engine part to fail")
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

// ldvReadAPI is a stub logbook answering the read extension in its own shape:
// metadata plus dataProcessingOperations, camelCase. It keeps the last
// request's method, token and trace id for the test to check.
type ldvReadAPI struct {
	server                         *httptest.Server
	method, authorization, traceID string
}

func newLdvReadAPI(t *testing.T, logbookID, organization string, records []map[string]any) *ldvReadAPI {
	t.Helper()
	api := &ldvReadAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.method, api.authorization = r.Method, r.Header.Get("Authorization")
		var body struct {
			TraceID string `json:"traceId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		api.traceID = body.TraceID
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata":                 map[string]any{"logbookId": logbookID, "organizationName": organization},
			"dataProcessingOperations": records,
		})
	}))
	t.Cleanup(api.server.Close)
	return api
}

func ldvPointer(next string) map[string]any {
	return map[string]any{"dpl": map[string]any{"read": map[string]any{"nextLogbookId": next}}}
}

// The chain view reads the way the read extension says a reader does: it
// starts at the logbook of the application that started the processing and
// follows dpl.read.nextLogbookId from there, querying each logbook on the
// trace id. A logbook no pointer leads to is still queried, and marked as
// such, and one that is down is visible as down rather than as empty.
func TestLdvChainFollowsNextLogbookIdFromTheStart(t *testing.T) {
	const (
		afnemerID     = "https://logboek.hypotheek-bv.test/data-processing-operations"
		bdID          = "https://logboek.belastingdienst.nl/data-processing-operations"
		rvigID        = "https://logboek.rvig.nl/data-processing-operations"
		toestemmingID = "https://logboek.gbo.overheid.nl/toestemming/data-processing-operations"
	)
	afnemer := newLdvReadAPI(t, afnemerID, "Hypotheek-BV", []map[string]any{{
		"traceId": "0af76519-16cd-43dd-8448-eb211c80319c", "spanId": "1111111111111111",
		"name": "dataverwerking.inkomensgegevens-opvragen", "status": "Ok", "attributes": ldvPointer(bdID),
	}})
	bd := newLdvReadAPI(t, bdID, "Belastingdienst", []map[string]any{{
		"traceId": "0af76519-16cd-43dd-8448-eb211c80319c", "spanId": "2222222222222222", "parentSpanId": "1111111111111111",
		"name": "dataverwerking.bronquery-doorgifte", "status": "Ok", "attributes": map[string]any{},
	}})
	toestemming := newLdvReadAPI(t, toestemmingID, "GBO", []map[string]any{{
		"traceId": "0af76519-16cd-43dd-8448-eb211c80319c", "spanId": "3333333333333333",
		"name": "dataverwerking.toestemming-status", "status": "Ok", "attributes": map[string]any{},
	}})
	rvig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer rvig.Close()

	cfg := config{
		LdvReadToken: "read-token",
		LdvLogbooks: []ldvLogbook{
			{ID: toestemmingID, Name: "GBO toestemming", URL: toestemming.server.URL},
			{ID: bdID, Name: "BD", URL: bd.server.URL},
			{ID: rvigID, Name: "RvIG", URL: rvig.URL},
			{ID: afnemerID, Name: "HBV", URL: afnemer.server.URL, Start: true},
		},
	}
	srv := httptest.NewServer(handleLdvChain(cfg))
	defer srv.Close()

	// A trace id that doubles as a transaction id may arrive hyphenated; the
	// logbook stores the W3C spelling of the same value.
	response, err := http.Get(srv.URL + "/ldv/0af76519-16cd-43dd-8448-eb211c80319c")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	var payload ldvChainResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace_id = %q, want the W3C spelling", payload.TraceID)
	}
	if bd.method != http.MethodPost {
		t.Errorf("the logbook was queried with %s; the read extension is POST", bd.method)
	}
	if bd.traceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("the logbook was queried with %q", bd.traceID)
	}
	if bd.authorization != "Bearer read-token" {
		t.Errorf("Authorization = %q", bd.authorization)
	}

	want := []struct{ id, via, from string }{
		{afnemerID, reachedAsStart, ""},
		{bdID, reachedByPointer, afnemerID},
		{toestemmingID, reachedWithout, ""},
		{rvigID, reachedWithout, ""},
	}
	if len(payload.Logbooks) != len(want) {
		t.Fatalf("got %d logbooks, want %d: %+v", len(payload.Logbooks), len(want), payload.Logbooks)
	}
	for i, expected := range want {
		got := payload.Logbooks[i]
		if got.Logbook.ID != expected.id || got.ReachedVia != expected.via || got.From != expected.from {
			t.Errorf("logbook %d = %s reached via %q from %q, want %s via %q from %q",
				i, got.Logbook.ID, got.ReachedVia, got.From, expected.id, expected.via, expected.from)
		}
	}
	// The logbook names itself, and that wins over our configured label.
	if payload.Logbooks[1].Logbook.Name != "Belastingdienst" {
		t.Errorf("logbook name = %q", payload.Logbooks[1].Logbook.Name)
	}
	if len(payload.Logbooks[1].Records) != 1 {
		t.Errorf("the followed logbook returned %d records", len(payload.Logbooks[1].Records))
	}
	if payload.Logbooks[3].Error == "" {
		t.Error("an unreachable logbook must report an error rather than look empty")
	}
	if payload.Logbooks[3].Records == nil {
		t.Error("records should be an empty list rather than null, so the UI needs no null check")
	}
}

// A pointer to a logbook the portal has no address for is shown, not dropped:
// the reader learns that the chain continues somewhere it cannot follow.
func TestLdvChainShowsAPointerItCannotFollow(t *testing.T) {
	const (
		startID   = "https://logboek.hypotheek-bv.test/data-processing-operations"
		elsewhere = "https://logboek.elders.test/data-processing-operations"
	)
	start := newLdvReadAPI(t, startID, "Hypotheek-BV", []map[string]any{{
		"traceId": "0af7651916cd43dd8448eb211c80319c", "spanId": "1111111111111111",
		"name": "dataverwerking.inkomensgegevens-opvragen", "status": "Ok", "attributes": ldvPointer(elsewhere),
	}})
	srv := httptest.NewServer(handleLdvChain(config{
		LdvLogbooks: []ldvLogbook{{ID: startID, Name: "HBV", URL: start.server.URL, Start: true}},
	}))
	defer srv.Close()

	response, err := http.Get(srv.URL + "/ldv/0af7651916cd43dd8448eb211c80319c")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var payload ldvChainResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Logbooks) != 2 {
		t.Fatalf("got %d logbooks, want the start and the pointer it cannot follow", len(payload.Logbooks))
	}
	pointer := payload.Logbooks[1]
	if pointer.Logbook.ID != elsewhere || pointer.ReachedVia != reachedByPointer || pointer.From != startID {
		t.Errorf("pointer = %+v", pointer)
	}
	if pointer.Error == "" {
		t.Error("a pointer the portal cannot follow must say so")
	}
}

// With no logbooks configured the endpoint answers empty rather than failing,
// the way the txlog endpoint does when fsc-infra is not running.
func TestLdvChainWithoutConfiguredLogbooks(t *testing.T) {
	srv := httptest.NewServer(handleLdvChain(config{}))
	defer srv.Close()

	response, err := http.Get(srv.URL + "/ldv/0af7651916cd43dd8448eb211c80319c")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var payload ldvChainResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Logbooks) != 0 {
		t.Fatalf("logbooks = %+v, want none", payload.Logbooks)
	}
}

func TestParseLdvLogbooks(t *testing.T) {
	logbooks := parseLdvLogbooks("logboek-bd=Belastingdienst=http://logboek-bd:4016/,logboek-brp=RvIG=http://logboek-brp:4016,broken,=x=y," +
		"https://logboek.hypotheek-bv.test/data-processing-operations=Hypotheek-BV=http://logboek-afnemer:4016=start")
	if len(logbooks) != 3 {
		t.Fatalf("logbooks = %+v, want the three well-formed entries", logbooks)
	}
	if logbooks[0].URL != "http://logboek-bd:4016" {
		t.Errorf("trailing slash not trimmed: %q", logbooks[0].URL)
	}
	if logbooks[1].Name != "RvIG" {
		t.Errorf("name = %q", logbooks[1].Name)
	}
	// The id is a read-API URI, which the '=' separator leaves intact, and a
	// fourth field marks where a reader begins.
	if logbooks[2].ID != "https://logboek.hypotheek-bv.test/data-processing-operations" || logbooks[2].URL != "http://logboek-afnemer:4016" {
		t.Errorf("start entry = %+v", logbooks[2])
	}
	if !logbooks[2].Start || logbooks[0].Start {
		t.Errorf("start flags = %v, %v; want only the marked entry", logbooks[0].Start, logbooks[2].Start)
	}
}
