// Package ldvclient writes Logboek Dataverwerkingen records (Logius LDV
// v1.0.0) to the logbook of the Verantwoordelijke a component belongs to.
//
// It is deliberately *not* a second OTel exporter. The spans a service already
// emits are observability exhaust — sampled, short-retention, about technical
// operations. An LDV record is an administrative record about a
// Dataverwerking: it MUST exist for every processing, MUST be confirmed by the
// logbook, and MUST NOT be sampled (REQ-32). Hence a separate client, a
// separate store, and a write path with no buffering in it anywhere.
//
// Fail-closed. When a logbook URL is configured, the component is part of an
// LDV chain and a processing that cannot be logged does not happen: the caller
// propagates Write's error and fails its own request. A component names the
// verwerkingsactiviteit it performs; the logbook refuses a reference its own
// register does not resolve, and that refusal fails the request like any
// other. Checking the register up front would only move the same failure
// earlier, at the cost of a second protocol between them. With no URL configured
// the component is not in an LDV chain and New returns nil, so no records are
// produced at all — that is how the deliberately unsecured demo source stays
// out of it. There is no third mode where records are dropped quietly, because
// that is precisely the guarantee LDV adds over an observability pipeline.
package ldvclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Attribute keys reserved by the standard. Anything a component adds beyond
// them is prefixed `gbo.` by convention, so a reader can tell normative fields
// from local colour at a glance.
const (
	AttrProcessingActivityID      = "dpl.core.processing_activity_id"
	AttrDataSubjectID             = "dpl.core.data_subject_id"
	AttrDataSubjectIDType         = "dpl.core.data_subject_id_type"
	AttrForeignOperationProcessor = "dpl.core.foreign_operation.processor"

	// AttrNextLogbookID points at the logbook where this processing continues,
	// at another Verantwoordelijke. It is what makes a chain view assemblable
	// iteratively: a reader follows the trace id into the next logbook rather
	// than needing one place that holds everything — which is precisely what
	// LDV's per-Verantwoordelijke model rules out.
	AttrNextLogbookID = "dpl.read.nextLogbookId"
)

// Headers carrying LDV metadata between components of the same chain. Only
// trace metadata crosses a component boundary; the records themselves stay
// with their own Verantwoordelijke's logbook.
const (
	HeaderTraceID       = "Gbo-Ldv-Trace-Id"
	HeaderParentSpanID  = "Gbo-Ldv-Parent-Span-Id"
	HeaderSubjectID     = "Gbo-Ldv-Subject-Id"
	HeaderSubjectIDType = "Gbo-Ldv-Subject-Id-Type"
)

// Record statuses.
const (
	StatusOK    = "OK"
	StatusError = "ERROR"
)

// Record is the OTel-shaped log record LDV defines.
type Record struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Status       string            `json:"status"`
	Resource     map[string]string `json:"resource,omitempty"`
	Attributes   map[string]any    `json:"attributes"`
}

// Config is what a component needs to join an LDV chain. LogbookURL empty
// means it does not join one.
type Config struct {
	// ServiceName lands in the record's resource, so a reader can tell which
	// component of a Verantwoordelijke performed the processing.
	ServiceName string
	// LogbookURL is the logbook of this component's Verantwoordelijke.
	LogbookURL string
	// WriteToken authenticates the write endpoint.
	WriteToken string
	// PseudonymKey derives logbook-local subject pseudonyms. Required only by
	// components that hold a BSN; see Subject.
	PseudonymKey string
}

// Client writes records to one logbook.
type Client struct {
	endpoint     string
	token        string
	resource     map[string]string
	pseudonymKey []byte
	http         *http.Client
}

// New reads the configuration. It returns (nil, nil) when LogbookURL is empty:
// the component is then simply not part of an LDV chain. Every other
// misconfiguration is an error, because a half-configured logbook silently
// logging nothing is the failure mode this package exists to prevent.
//
// PseudonymKey is optional. A component that never holds a BSN — the consent
// register works with a portal-scoped reference throughout — has nothing to
// derive a pseudonym from, and calling Subject without a key panics rather
// than silently producing a keyless one.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.LogbookURL, "/")
	if base == "" {
		return nil, nil
	}
	if cfg.WriteToken == "" {
		return nil, fmt.Errorf("a logbook URL is set but no write token")
	}
	return &Client{
		endpoint:     base + "/logboek/records",
		token:        cfg.WriteToken,
		resource:     map[string]string{"service.name": cfg.ServiceName},
		pseudonymKey: []byte(cfg.PseudonymKey),
		http:         &http.Client{Timeout: 5 * time.Second},
	}, nil
}

// Write sends one record and returns only once the logbook has confirmed it.
// The error is meant to be propagated: the caller must fail its own request
// rather than continue with an unlogged Dataverwerking.
func (c *Client) Write(ctx context.Context, record Record) error {
	record.Resource = c.resource
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode log record: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build log request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("logboek unreachable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("logboek refused the record: status %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	return nil
}

// Attributes assembles the attribute map: the three mandatory dpl.core fields,
// the optional foreign-operation processor, and whatever local colour the
// caller adds. Empty values are dropped rather than written as empty strings —
// an absent attribute says "not applicable", an empty one says nothing at all.
func Attributes(activity, subjectID, subjectIDType, processor string, extra map[string]any) map[string]any {
	attributes := map[string]any{
		AttrProcessingActivityID: activity,
		AttrDataSubjectID:        subjectID,
		AttrDataSubjectIDType:    subjectIDType,
	}
	if processor != "" {
		attributes[AttrForeignOperationProcessor] = processor
	}
	for key, value := range extra {
		if text, isText := value.(string); isText && text == "" {
			continue
		}
		if value == nil {
			continue
		}
		attributes[key] = value
	}
	return attributes
}

// Status maps an outcome onto the record status. A Dataverwerking that failed
// is still a Dataverwerking, and the logbook records that it failed rather
// than staying silent about it.
func Status(err error) string {
	if err != nil {
		return StatusError
	}
	return StatusOK
}

// StatusFromHTTP maps an upstream response onto the record status.
func StatusFromHTTP(statusCode int) string {
	if statusCode >= 400 {
		return StatusError
	}
	return StatusOK
}

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TraceID derives a record's trace id from the request.
//
// The Fsc-Transaction-Id is a UUID, which is exactly 32 hex characters once
// the hyphens come off — so LDV's traceID, the ADL's trace id and the FSC
// transaction log all end up carrying the same value for one request. That
// shared id is a mitigation as much as a design: FSC v2.4.0 drops
// `traceparent` between peers, so the id has to travel in a field FSC does
// propagate (REQ-55).
//
// Falls back to the ambient OTel trace id, and then to a fresh id, so a record
// is never dropped for want of a correlation handle.
func TraceID(ctx context.Context, header http.Header) string {
	for _, name := range []string{HeaderTraceID, "Fsc-Transaction-Id", "X-Request-Id"} {
		if candidate := NormalizeTraceID(header.Get(name)); candidate != "" {
			return candidate
		}
	}
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.HasTraceID() {
		return spanContext.TraceID().String()
	}
	return randomHex(16)
}

// NormalizeTraceID turns a UUID-shaped correlator into an OTel trace id, or
// returns "" when it is not one. Exported because a component that mints the
// Fsc-Transaction-Id itself holds the value rather than reading it back from a
// header.
func NormalizeTraceID(value string) string {
	candidate := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if traceIDPattern.MatchString(candidate) {
		return candidate
	}
	return ""
}

// SpanID mints the identity of one Dataverwerking record. It is the record's
// own, not the OTel span's: an LDV record and a span are different objects
// with different lifetimes, and borrowing the span id would tie an
// administrative record to a sampling decision.
func SpanID() string { return randomHex(8) }

// ParentSpanFromHeader returns the LDV span a component of the same
// Verantwoordelijke filed upstream, so this record hangs under it. Empty when
// this component starts the tree.
func ParentSpanFromHeader(header http.Header) string {
	return strings.TrimSpace(header.Get(HeaderParentSpanID))
}

func randomHex(byteCount int) string {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever
		// does, a time-derived id still keeps the record writable.
		return fmt.Sprintf("%0*x", byteCount*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// Claims decodes the payload of an Fsc-Authorization bearer token without
// verifying it. The chain of trust sits on the FSC-Inway, which validated the
// token before the request reached this process; re-verifying here would need
// the peer's keys for no gain.
func Claims(authorization string) map[string]any {
	token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer"))
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

// peerIDPattern is the 20-character alphanumeric FSC Peer ID.
var peerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// ForeignProcessor names the application that initiated this processing when
// it was not this Verantwoordelijke — `dpl.core.foreign_operation.processor`.
//
// The FSC peer that called us is the honest answer, and the token's `sub`/`iss`
// carry it when the Manager sets them. When they do not, the grant hash is
// used instead: it is the identity of the countersigned connection the caller
// acts under, which is the one thing about the caller this side can actually
// verify. Empty when the request did not come through FSC at all — a locally
// initiated processing has no foreign processor.
func ForeignProcessor(r *http.Request) string {
	claims := Claims(r.Header.Get("Fsc-Authorization"))
	for _, name := range []string{"sub", "iss"} {
		if value, ok := claims[name].(string); ok && peerIDPattern.MatchString(value) {
			return "fsc-peer:" + value
		}
	}
	if grantHash := strings.TrimSpace(r.Header.Get("Fsc-Grant-Hash")); grantHash != "" {
		return "fsc-grant:" + grantHash
	}
	return ""
}

// LogFailure records why a request was refused. The record itself did not
// land, so this line is the only trace of the attempt.
func LogFailure(name string, err error) {
	slog.Error("LDV record not confirmed; refusing the processing",
		"record", name, "err", err.Error())
}
