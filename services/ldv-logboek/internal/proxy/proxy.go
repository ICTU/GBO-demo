// Package proxy is the logbook's second driving adapter (#689): the
// reference component a source places between its Inway and its own service.
//
//	Inway → proxy (this) → bron-sidecar → source service
//
// It forwards every request unchanged, in the trace it arrived in, and holds
// the response. A request with a consent token that the source answers with
// 200 is a delivery under that consent: the component records it in its own
// logbook and registers the trace at the consent usage index, and only then
// lets the response go. If either fails, the caller gets 502 and no body.
// Anything else passes through unrecorded.
//
// The component reads consent_id from the token's claims without checking
// the signature. That is a demo shortcut with a reason: the source's PDP
// verified the token at the Inway, and nothing but the Inway reaches the
// component. A deployment where that does not hold must verify the token
// here against the register's JWKS.
package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ldv-logboek/internal/ldv"

	ldvclient "gbo-demo/ldv-client"
)

// ConsentTokenHeader puts a request under the consent regime, here as at the
// PDP and in the sidecar.
const ConsentTokenHeader = "X-GBO-Consent-Token"

const (
	maxRequestBytes  = 4 << 20
	maxResponseBytes = 16 << 20
)

// Recorder is the use case the proxy drives (ldv.DeliveryRecorder).
type Recorder interface {
	Record(ctx context.Context, delivery ldv.Delivery) error
}

// hopByHop are the headers that belong to one connection, not to the
// message (RFC 9110 §7.6.1), so a proxy does not forward them.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

var peerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// Handler forwards to Upstream and records deliveries.
type Handler struct {
	Upstream string
	Client   *http.Client
	Recorder Recorder
	Now      func() time.Time
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := h.now()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		http.Error(w, "the request body could not be read", http.StatusBadRequest)
		return
	}

	// The trace the request arrived in, and this component's own span in
	// it: the sidecar and the source file their records below that span.
	traceID := ldvclient.TraceID(r.Context(), r.Header)
	spanID := ldvclient.SpanID()
	parentSpanID := ldvclient.ParentSpanFor(r.Header, traceID)

	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(h.Upstream, "/")+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "the request could not be forwarded", http.StatusBadGateway)
		return
	}
	copyHeaders(upstream.Header, r.Header)
	// On the traceparent, and on the context for ldvclient.Transport, so an
	// OTel client span injected by an outer transport cannot replace it.
	upstream = ldvclient.CarryTraceparent(upstream, ldvclient.TraceContext{TraceID: traceID, SpanID: spanID, Sampled: true})

	response, err := h.Client.Do(upstream)
	if err != nil {
		slog.Error("upstream unreachable", "err", err.Error())
		http.Error(w, "the source could not be reached", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responseBody) > maxResponseBytes {
		http.Error(w, "the source response could not be read", http.StatusBadGateway)
		return
	}

	// The sidecar's name for the citizen (D1) is for this component alone.
	// It is removed whatever happens next, so it never reaches the caller.
	subjectID := strings.TrimSpace(response.Header.Get(ldvclient.HeaderSubjectID))
	subjectIDType := strings.TrimSpace(response.Header.Get(ldvclient.HeaderSubjectIDType))
	response.Header.Del(ldvclient.HeaderSubjectID)
	response.Header.Del(ldvclient.HeaderSubjectIDType)

	consentToken := r.Header.Get(ConsentTokenHeader)
	if consentToken != "" && response.StatusCode == http.StatusOK {
		err := h.Recorder.Record(r.Context(), ldv.Delivery{
			TraceID:       traceID,
			SpanID:        spanID,
			ParentSpanID:  parentSpanID,
			Start:         start,
			End:           h.now(),
			ConsentID:     consentID(consentToken),
			SubjectID:     subjectID,
			SubjectIDType: subjectIDType,
			Processor:     processor(r),
		})
		if err != nil {
			// Fail closed (#689): no record and registration, no data.
			slog.Error("delivery withheld", "trace_id", traceID, "err", err.Error())
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		slog.Info("delivery recorded", "trace_id", traceID)
	}

	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func copyHeaders(to, from http.Header) {
	for name, values := range from {
		canonical := http.CanonicalHeaderKey(name)
		if hopByHop[canonical] || canonical == "Host" || canonical == "Content-Length" {
			continue
		}
		for _, value := range values {
			to.Add(name, value)
		}
	}
}

// consentID reads consent_id from the token's claims, unverified (see the
// package comment).
func consentID(token string) string {
	value, _ := ldvclient.Claims(token)["consent_id"].(string)
	return strings.TrimSpace(value)
}

// processor names the FSC peer that asked, from the access token the Inway
// forwards, as dpl.core.foreign_operation.processor.
func processor(r *http.Request) string {
	sub, _ := ldvclient.Claims(r.Header.Get("Fsc-Authorization"))["sub"].(string)
	if !peerIDPattern.MatchString(sub) {
		return ""
	}
	return ldvclient.DefaultPeerURIBase + "/" + sub
}
