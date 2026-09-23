package outway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consumer"
	ldv "gbo-demo/ldv-client"
)

func question() consumer.Question {
	return consumer.Question{
		Query:         `query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { bsn } }`,
		Variables:     map[string]any{"bsn": "PI-abc123"},
		Scope:         "bd:ib:2025",
		ConsentToken:  "a.b.c",
		TransactionID: "0190c5a2-7b1e-7cc0-8f6e-1a2b3c4d5e6f",
	}
}

func source(t *testing.T, handler http.HandlerFunc) *Source {
	t.Helper()
	outway := httptest.NewServer(handler)
	t.Cleanup(outway.Close)
	return &Source{URL: outway.URL + "/bri/graphql", Client: &http.Client{Timeout: 2 * time.Second}}
}

// The question goes to the grant-link path as plain GraphQL, with the consent
// token for the PDP and the transaction id FSC carries.
func TestAQuestionReachesTheOutwayAsGraphQL(t *testing.T) {
	var path string
	var header http.Header
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	s := source(t, func(w http.ResponseWriter, r *http.Request) {
		path, header = r.URL.Path, r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"bsn":"PI-abc123"}}}`))
	})

	answer, err := s.Ask(context.Background(), question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !answer.Allowed || !strings.Contains(string(answer.Data), "ingeschrevenPersoon") {
		t.Fatalf("answer = %+v, want the data", answer)
	}
	if path != "/bri/graphql" {
		t.Errorf("path = %q, want the grant-link path", path)
	}
	if body.Query != question().Query || body.Variables["bsn"] != "PI-abc123" {
		t.Errorf("body = %+v, want the query and its variables", body)
	}
	if header.Get("X-GBO-Consent-Token") != "a.b.c" || header.Get("X-GBO-Scope") != "bd:ib:2025" {
		t.Errorf("headers = %v, want the consent token and the scope", header)
	}
	if header.Get("Fsc-Transaction-Id") != question().TransactionID {
		t.Errorf("Fsc-Transaction-Id = %q, want the question's", header.Get("Fsc-Transaction-Id"))
	}
	if header.Get("X-GBO-Consent-Id") != "" {
		t.Error("the legacy X-GBO-Consent-Id header was sent")
	}
}

// The source's records hang under the consumer's record, not under the OTel
// span.
func TestTheSourceHangsUnderTheParentPosition(t *testing.T) {
	var header http.Header
	s := source(t, func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	})
	q := question()
	q.Parent = consumer.Position{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7"}

	if _, err := s.Ask(context.Background(), q); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := ldv.TraceContextFrom(context.Background(), header, "").TraceID; got != q.Parent.TraceID {
		t.Errorf("trace = %q, want %q", got, q.Parent.TraceID)
	}
	if got := ldv.ParentSpanFor(header, q.Parent.TraceID); got != q.Parent.SpanID {
		t.Errorf("parent span = %q, want %q", got, q.Parent.SpanID)
	}
}

// Anything but 200 is a refusal. The Inway puts the reason in `message`;
// other upstreams use `reason`; a body with neither still says what happened.
func TestARefusalCarriesTheUpstreamReason(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"inway":     {http.StatusUnauthorized, `{"message":"authorization server denied request: reasonUser-en: CONSENT_WITHDRAWN; ","code":"UNAUTHORIZED"}`, "authorization server denied request: reasonUser-en: CONSENT_WITHDRAWN; "},
		"reason":    {http.StatusForbidden, `{"allowed":false,"reason":"YEAR_NOT_COVERED"}`, "YEAR_NOT_COVERED"},
		"bare":      {http.StatusInternalServerError, `upstream exploded`, "upstream_error: status 500"},
		"not a 200": {http.StatusNoContent, ``, "upstream_error: status 204"},
	} {
		t.Run(name, func(t *testing.T) {
			s := source(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			answer, err := s.Ask(context.Background(), question())
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			if answer.Allowed || answer.Reason != tc.want {
				t.Fatalf("answer = %+v, want a refusal with reason %q", answer, tc.want)
			}
		})
	}
}

// A question that never reaches the Outway is an error, not a refusal.
func TestAnOutwayThatDoesNotAnswerIsAnError(t *testing.T) {
	s := source(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	s.Client.Timeout = 25 * time.Millisecond

	_, err := s.Ask(context.Background(), question())
	if err == nil || !strings.HasPrefix(err.Error(), "fsc_outway_call_failed:") {
		t.Fatalf("err = %v, want fsc_outway_call_failed", err)
	}
}
