// Package consumerhttp is the consumer backend's HTTP API, a driving adapter
// for consumer.Consumer. It owns the request and response shapes, the status
// codes and the correlation between the OTel trace and the FSC transaction id.
//
// Endpoint: POST /api/dvtp/query  {consent_token, scope_id?, belastingjaren?, fields?, vbo_id?}
//
//	→ FSC Outway: pick contract by grant-link, sign token, open mTLS to Inway
//	→ FSC Inway proxy: forward GraphQL query (with PI as bsn variable)
//	   ↳ PEP → OpenFTV → sidecar (PI → BSN) → source
//	→ return  {allowed, data | reason, denial_code, trace_id, fsc_transaction_id}
package consumerhttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"gbo-demo/dienstverlener-backend/consumer"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// NewHandler is the whole HTTP stack. withFscTraceContext wraps otelhttp: the
// header it sets must be in place before otelhttp extracts the parent context.
func NewHandler(c *consumer.Consumer, serviceName string) http.Handler {
	return withFscTraceContext(otelhttp.NewHandler(withDemoSession(withAccessLog(NewMux(c))), serviceName))
}

// NewMux is the routing tree without the middleware, for tests.
func NewMux(c *consumer.Consumer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/dvtp/query", handleQuery(c))
	return mux
}

type queryRequest struct {
	ConsentToken   string   `json:"consent_token"`
	ScopeID        string   `json:"scope_id,omitempty"`
	Belastingjaren []int    `json:"belastingjaren,omitempty"`
	Fields         []string `json:"fields,omitempty"`
	VboID          string   `json:"vbo_id,omitempty"`
}

// statusFor is the HTTP status of each outcome. A refusal is a successful
// exchange: the caller gets its answer, which is "no".
var statusFor = map[consumer.Outcome]int{
	consumer.Allowed:      http.StatusOK,
	consumer.Denied:       http.StatusOK,
	consumer.InvalidToken: http.StatusForbidden,
	consumer.Unlogged:     http.StatusInternalServerError,
	consumer.Unreachable:  http.StatusBadGateway,
}

func handleQuery(c *consumer.Consumer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var body queryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		ctx, span := otel.Tracer("dienstverlener-backend").Start(r.Context(), "dvtp.query")
		defer span.End()
		log := loggerFromCtx(ctx)

		// The identifier that travels through FSC (and so ends up as the
		// OpenFTV decision log's trace). The middleware already minted it;
		// mint one only when the middleware is not in the chain (tests).
		fscTxID, _ := ctx.Value(fscTxIDCtxKey).(string)
		if fscTxID == "" {
			fscTxID = newFscTransactionID()
		}
		span.SetAttributes(attribute.String("gbo.fsc.transaction_id", fscTxID))

		result, err := c.Ask(ctx, consumer.Request{
			ConsentToken:   body.ConsentToken,
			ScopeID:        body.ScopeID,
			Belastingjaren: body.Belastingjaren,
			Fields:         body.Fields,
			VboID:          body.VboID,
			FromDevPortal:  r.Header.Get("X-Demo-Source") == "dev-portal",
			TraceID:        traceIDFromSpan(span),
			TransactionID:  fscTxID,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		switch result.Outcome {
		case consumer.Allowed:
			log.Info("query allowed", "consent_id", result.ConsentID, "trace_id", result.TraceID)
		case consumer.Denied:
			log.Info("query denied",
				"consent_id", result.ConsentID,
				"reason", result.Reason,
				"policy_code", consumer.PolicyCode(result.Reason),
				"denial_code", result.DenialCode,
				"trace_id", result.TraceID)
		case consumer.InvalidToken:
			log.Warn("consent token decode failed", "reason", result.Reason)
		case consumer.Unreachable:
			log.Error("fsc outway call failed", "reason", result.Reason)
		}
		writeJSON(w, statusFor[result.Outcome], result)
	}
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func traceIDFromSpan(span trace.Span) string {
	sc := span.SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// newFscTransactionID returns a UUID v7 used as both the FSC transaction id
// and the OTel trace id — one identifier end-to-end across the chain. The FSC
// Outway strictly validates v7, so v4 is not accepted.
func newFscTransactionID() string {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

// fscTxIDCtxKey stashes the Fsc-Transaction-Id the middleware generated, so
// the handler reuses it instead of minting a second UUID — which would break
// the correlation between the response's trace_id and the PDP's trace.
type fscTxIDCtxKeyType struct{}

var fscTxIDCtxKey = fscTxIDCtxKeyType{}

// withFscTraceContext ties the OTel trace id to the Fsc-Transaction-Id (UUID
// v7, 128 bits — exactly the OTel trace-id format). Without it the backend
// span gets a fresh random trace id while the PDP reconstructs its trace from
// the FSC transaction id, and the two never match — breaking decision-log
// lookups by trace_id (dev-portal /explain). Same pattern as the
// eudi-adapter. Sets the Traceparent header BEFORE otelhttp extracts the
// parent context.
func withFscTraceContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fscTxID := newFscTransactionID()
		if r.Header.Get("Traceparent") == "" {
			traceHex := strings.ReplaceAll(fscTxID, "-", "")
			spanHex := randomSpanIDHex()
			if len(traceHex) == 32 && spanHex != "" {
				r.Header.Set("Traceparent", "00-"+traceHex+"-"+spanHex+"-01")
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), fscTxIDCtxKey, fscTxID))
		next.ServeHTTP(w, r)
	})
}

// withDemoSession copies X-Demo-Session onto the server span, which is how
// the dev-portal's watch mode tells one developer's run from another's. It
// gates nothing. Wrap INSIDE otelhttp, or there is no span to annotate yet.
func withDemoSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := r.Header.Get("X-Demo-Session"); s != "" {
			trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("gbo.demo.session", s))
		}
		next.ServeHTTP(w, r)
	})
}

// randomSpanIDHex returns 8 bytes of hex — a valid OTel span id.
func randomSpanIDHex() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
