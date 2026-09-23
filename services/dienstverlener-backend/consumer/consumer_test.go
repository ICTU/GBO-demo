package consumer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// ── fakes ──────────────────────────────────────────────────────────────────

type fakeSource struct {
	answer Answer
	err    error
	asked  []Question
}

func (s *fakeSource) Ask(_ context.Context, q Question) (Answer, error) {
	s.asked = append(s.asked, q)
	return s.answer, s.err
}

type fakeLogbook struct {
	at       Position
	refuse   bool
	recorded []Processing
}

func (l *fakeLogbook) Begin(context.Context) Position { return l.at }

func (l *fakeLogbook) Record(_ context.Context, p Processing) error {
	if l.refuse {
		return errors.New("logbook refused")
	}
	l.recorded = append(l.recorded, p)
	return nil
}

type fakeHistory struct{ runs []Run }

func (h *fakeHistory) Observe(_ context.Context, run Run) { h.runs = append(h.runs, run) }

func token(consentID string, scopes ...string) string {
	encode := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"consent_id": consentID, "pi": "PI-abc123", "scopes": scopes})
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(payload) + ".sig"
}

func answering(data string) *fakeSource {
	return &fakeSource{answer: Answer{Allowed: true, Data: json.RawMessage(data)}}
}

func refusing(reason string) *fakeSource {
	return &fakeSource{answer: Answer{Reason: reason}}
}

func ask(t *testing.T, c *Consumer, req Request) Result {
	t.Helper()
	result, err := c.Ask(context.Background(), req)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	return result
}

func onlyQuestion(t *testing.T, s *fakeSource) Question {
	t.Helper()
	if len(s.asked) != 1 {
		t.Fatalf("the source was asked %d times, want 1", len(s.asked))
	}
	return s.asked[0]
}

// inwayDeny is the reason text the FSC Inway sends on a policy deny.
func inwayDeny(code string) string {
	return "authorization server denied request: reasonUser-en: " + code + "; "
}

// ── the income question ────────────────────────────────────────────────────

// The consumer reads the token only to build the question, sends the token on
// untouched for the PDP to verify, and passes the source's answer back.
func TestAnIncomeQuestionIsAskedWithTheConsentsPI(t *testing.T) {
	source := answering(`{"data":{"ingeschrevenPersoon":{}}}`)
	c := &Consumer{Kind: Kinds["bd"], Source: source}
	tok := token("c-1", "bd:ib:2024", "bd:ib:2025")

	result := ask(t, c, Request{ConsentToken: tok, ScopeID: "bd:ib:2025", Belastingjaren: []int{2024}, TraceID: "t-1", TransactionID: "tx-1"})

	q := onlyQuestion(t, source)
	if q.ConsentToken != tok {
		t.Error("the consent token was not forwarded untouched")
	}
	if q.Variables["bsn"] != "PI-abc123" {
		t.Errorf("variables = %v, want the consent's PI as bsn", q.Variables)
	}
	// The year filter travels inside the query, so the PDP can enforce
	// per-year consent.
	if !strings.Contains(q.Query, "heeftBelastingjaarAangifte(belastingjaren: [2024])") {
		t.Errorf("query = %s, want the belastingjaren filter", q.Query)
	}
	if q.Scope != "bd:ib:2025" || q.TransactionID != "tx-1" {
		t.Errorf("question = %+v, want the request's scope and transaction id", q)
	}
	if !result.Allowed() || !strings.Contains(string(result.Data), "ingeschrevenPersoon") {
		t.Fatalf("result = %+v, want the source's answer", result)
	}
	if result.ConsentID != "c-1" || result.TraceID != "t-1" || result.TransactionID != "tx-1" {
		t.Errorf("result = %+v, want the run's identifiers", result)
	}
}

// Partial consent: the consent covers 2025 only, so a request for 2024 and
// 2025 asks for 2025 and reports 2024 as denied, rather than letting the whole
// question fail policy (YEAR_NOT_COVERED).
func TestOnlyConsentedYearsAreAsked(t *testing.T) {
	source := answering(`{}`)
	c := &Consumer{Kind: Kinds["bd"], Source: source}

	result := ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025"), Belastingjaren: []int{2024, 2025}})

	if q := onlyQuestion(t, source).Query; !strings.Contains(q, "belastingjaren: [2025]") || strings.Contains(q, "2024") {
		t.Fatalf("query = %s, want it filtered to [2025]", q)
	}
	if !reflect.DeepEqual(result.DeniedYears, []int{2024}) {
		t.Fatalf("denied years = %v, want [2024]", result.DeniedYears)
	}
}

// No overlap still reaches the PDP, so token verification and revocation
// cannot be bypassed by a local shortcut.
func TestAQuestionWithoutConsentedYearsStillReachesThePDP(t *testing.T) {
	source := answering(`{}`)
	c := &Consumer{Kind: Kinds["bd"], Source: source}

	result := ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2023"), Belastingjaren: []int{2024, 2025}})

	onlyQuestion(t, source)
	if !reflect.DeepEqual(result.DeniedYears, []int{2024, 2025}) {
		t.Fatalf("denied years = %v, want [2024 2025]", result.DeniedYears)
	}
}

// The developer portal demonstrates raw policy outcomes: its question carries
// the requested years verbatim, so the PDP denies an unconsented year with a
// full trace.
func TestADevPortalRunIsAskedAsRequested(t *testing.T) {
	source := refusing(inwayDeny("YEAR_NOT_COVERED"))
	c := &Consumer{Kind: Kinds["bd"], Source: source}

	ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025"), Belastingjaren: []int{2024, 2025}, FromDevPortal: true})

	if q := onlyQuestion(t, source).Query; !strings.Contains(q, "belastingjaren: [2024,2025]") {
		t.Fatalf("query = %s, want the years verbatim", q)
	}
}

func TestTheKindsDefaultScopeIsUsedWhenNoneIsNamed(t *testing.T) {
	source := answering(`{}`)
	c := &Consumer{Kind: Kinds["bd"], Source: source}

	ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")})

	if got := onlyQuestion(t, source).Scope; got != "bd:ib:2025" {
		t.Fatalf("scope = %q, want the bd default", got)
	}
}

// ── the ownership question ─────────────────────────────────────────────────

// The Installatie Register asks LVG whether the citizen of the consent owns one
// verblijfsobject: the PI and the VBO-id travel as variables, under the LVG
// scope.
func TestAnOwnershipQuestionNamesTheCitizenAndTheBuilding(t *testing.T) {
	source := answering(`{"data":{"vbo":{"vboId":"0632010000099412"}}}`)
	c := &Consumer{Kind: Kinds["lvg"], Source: source}

	result := ask(t, c, Request{ConsentToken: token("c-ir", "lvg:vbo:eigendom"), VboID: " 0632010000099412 "})

	q := onlyQuestion(t, source)
	if !strings.Contains(q.Query, "vbo(bsn: $bsn, vboId: $vboId)") {
		t.Errorf("query = %q, want the ownership check", q.Query)
	}
	if q.Variables["bsn"] != "PI-abc123" || q.Variables["vboId"] != "0632010000099412" {
		t.Errorf("variables = %v, want the consent's PI and the trimmed VBO-id", q.Variables)
	}
	if q.Scope != "lvg:vbo:eigendom" {
		t.Errorf("scope = %q, want the LVG scope by default", q.Scope)
	}
	if !result.Allowed() {
		t.Fatalf("result = %+v, want LVG's answer", result)
	}
}

// Without a VBO-id there is no question to ask.
func TestAnOwnershipQuestionNeedsABuilding(t *testing.T) {
	source := answering(`{}`)
	c := &Consumer{Kind: Kinds["lvg"], Source: source}

	if _, err := c.Ask(context.Background(), Request{ConsentToken: token("c-ir", "lvg:vbo:eigendom")}); err == nil {
		t.Fatal("asked without a VBO-id")
	}
	if len(source.asked) != 0 {
		t.Fatal("the source was asked without a VBO-id")
	}
}

func TestAnUnknownKindIsRefused(t *testing.T) {
	if _, err := LookupKind("brp"); err == nil {
		t.Fatal("LookupKind accepted an unknown kind")
	}
}

// ── what cannot be asked ───────────────────────────────────────────────────

func TestAQuestionNeedsAConsentToken(t *testing.T) {
	source := answering(`{}`)
	c := &Consumer{Kind: Kinds["bd"], Source: source}

	if _, err := c.Ask(context.Background(), Request{}); err == nil {
		t.Fatal("asked without a consent token")
	}
	if len(source.asked) != 0 {
		t.Fatal("the source was asked without a consent token")
	}
}

func TestAnUnreadableTokenAsksNothing(t *testing.T) {
	source := answering(`{}`)
	logbook := &fakeLogbook{}
	c := &Consumer{Kind: Kinds["bd"], Source: source, Logbook: logbook}

	result := ask(t, c, Request{ConsentToken: "not-a-jwt", TransactionID: "tx-1"})

	if result.Outcome != InvalidToken || result.DenialCode != DenialCodeUnavailable {
		t.Fatalf("result = %+v, want an invalid token the citizen is not told about", result)
	}
	if len(source.asked) != 0 || len(logbook.recorded) != 0 {
		t.Fatal("an unreadable token still reached the source or the logbook")
	}
	if result.TransactionID != "" {
		t.Error("no question travelled through FSC, so no transaction id belongs on the result")
	}
}

// ── the logbook ────────────────────────────────────────────────────────────

// The call is logged as the consumer's processing, with the source's records
// hanging under the position the logbook minted.
func TestACallIsLoggedUnderThePositionTheSourceReceives(t *testing.T) {
	source := answering(`{}`)
	logbook := &fakeLogbook{at: Position{TraceID: "trace-1", SpanID: "span-1"}}
	c := &Consumer{Kind: Kinds["bd"], Source: source, Logbook: logbook}

	ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025"), ScopeID: "bd:ib:2025"})

	if got := onlyQuestion(t, source).Parent; got != logbook.at {
		t.Errorf("the source hangs under %+v, want the record's %+v", got, logbook.at)
	}
	if len(logbook.recorded) != 1 {
		t.Fatalf("recorded %d processings, want 1", len(logbook.recorded))
	}
	p := logbook.recorded[0]
	if p.At != logbook.at || p.Activity != Kinds["bd"].Activity || p.Name != Kinds["bd"].RecordName {
		t.Errorf("processing = %+v, want the bd activity at the minted position", p)
	}
	if p.Subject != "PI-abc123" || p.Scope != "bd:ib:2025" || p.Failed {
		t.Errorf("processing = %+v, want the PI, the scope and success", p)
	}
}

// A refused or failed call is a processing too.
func TestARefusedOrFailedCallIsLoggedAsFailed(t *testing.T) {
	for name, source := range map[string]*fakeSource{
		"refused":     refusing(inwayDeny("CONSENT_WITHDRAWN")),
		"unreachable": {err: errors.New("fsc_outway_call_failed: connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			logbook := &fakeLogbook{}
			c := &Consumer{Kind: Kinds["bd"], Source: source, Logbook: logbook}

			ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")})

			if len(logbook.recorded) != 1 || !logbook.recorded[0].Failed {
				t.Fatalf("recorded %+v, want one failed processing", logbook.recorded)
			}
		})
	}
}

// A call that cannot be logged withholds its answer: the data came in, but it
// does not go out unrecorded.
func TestAnUnloggedCallWithholdsTheAnswer(t *testing.T) {
	history := &fakeHistory{}
	c := &Consumer{
		Kind:    Kinds["bd"],
		Source:  answering(`{"data":{"ingeschrevenPersoon":{"bsn":"PI-abc123"}}}`),
		Logbook: &fakeLogbook{refuse: true},
		History: history,
	}

	result := ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")})

	if result.Outcome != Unlogged || result.Data != nil {
		t.Fatalf("result = %+v, want the answer withheld", result)
	}
	if len(history.runs) != 0 {
		t.Fatal("an unlogged answer reached the dev-portal timeline")
	}
}

// ── what a citizen may be told ─────────────────────────────────────────────

// Only a revoked or expired consent is named; every other denial is
// UNAVAILABLE.
func TestADenialIsCitizenSafe(t *testing.T) {
	for name, tc := range map[string]struct{ reason, want string }{
		"revoked consent":     {inwayDeny("CONSENT_WITHDRAWN"), "CONSENT_WITHDRAWN"},
		"expired consent":     {inwayDeny("CONSENT_EXPIRED"), "CONSENT_EXPIRED"},
		"administrative deny": {inwayDeny("ACTOR_NOT_ALLOWED"), DenialCodeUnavailable},
		"unknown future code": {inwayDeny("SOME_FUTURE_CODE"), DenialCodeUnavailable},
		"source unavailable":  {"upstream_error: status 500", DenialCodeUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Consumer{Kind: Kinds["bd"], Source: refusing(tc.reason)}

			result := ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")})

			if result.Outcome != Denied || result.Reason != tc.reason {
				t.Fatalf("result = %+v, want a denial with the upstream reason", result)
			}
			if result.DenialCode != tc.want {
				t.Fatalf("denial code = %q, want %q", result.DenialCode, tc.want)
			}
		})
	}
}

// A transport failure is not a consent problem.
func TestAnUnreachableSourceIsNotAConsentProblem(t *testing.T) {
	c := &Consumer{Kind: Kinds["bd"], Source: &fakeSource{err: errors.New("fsc_outway_call_failed: connection refused")}}

	result := ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")})

	if result.Outcome != Unreachable || result.DenialCode != DenialCodeUnavailable {
		t.Fatalf("result = %+v, want unreachable and UNAVAILABLE", result)
	}
	if !strings.Contains(result.Reason, "fsc_outway_call_failed") {
		t.Fatalf("reason = %q, want the transport failure for the operator", result.Reason)
	}
}

func TestAnAllowedAnswerCarriesNoDenialCode(t *testing.T) {
	c := &Consumer{Kind: Kinds["bd"], Source: answering(`{}`)}

	if result := ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")}); result.DenialCode != "" {
		t.Fatalf("denial code = %q, want none on an allow", result.DenialCode)
	}
}

// ── the dev-portal timeline ────────────────────────────────────────────────

// Every question that reached the source's side goes on the timeline, allowed
// or denied; one that never got there does not.
func TestTheTimelineSeesEveryAnsweredQuestion(t *testing.T) {
	for name, tc := range map[string]struct {
		source *fakeSource
		want   int
	}{
		"allowed":     {answering(`{}`), 1},
		"denied":      {refusing(inwayDeny("CONSENT_WITHDRAWN")), 1},
		"unreachable": {&fakeSource{err: errors.New("down")}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			history := &fakeHistory{}
			c := &Consumer{Kind: Kinds["bd"], Source: tc.source, History: history}

			ask(t, c, Request{ConsentToken: token("c-1", "bd:ib:2025")})

			if len(history.runs) != tc.want {
				t.Fatalf("timeline got %d runs, want %d", len(history.runs), tc.want)
			}
		})
	}
}

// ── the result on the wire ─────────────────────────────────────────────────

func TestAResultSaysWhetherItWasAllowed(t *testing.T) {
	encoded, err := json.Marshal(Result{Outcome: Denied, ConsentID: "c-1", Reason: "no", TraceID: "t-1", TransactionID: "tx-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	_ = json.Unmarshal(encoded, &wire)
	if wire["allowed"] != false || wire["reason"] != "no" || wire["trace_id"] != "t-1" || wire["fsc_transaction_id"] != "tx-1" {
		t.Fatalf("wire = %s", encoded)
	}
	if _, leaked := wire["ConsentID"]; leaked {
		t.Fatalf("the consent id leaked onto the wire: %s", encoded)
	}
}
