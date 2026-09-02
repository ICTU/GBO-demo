// Package ldv is the driven adapter for the Logboek Dataverwerkingen: it
// turns the core's Processing into the OTel-shaped record LDV v1.0.0 defines
// and writes it to this Verantwoordelijke's logbook.
//
// Unlike the other instrumented services this one does not carry the shared
// `ldv.go` client. It needs none of it: there is no FSC boundary here (the
// citizen is the caller, so no record has a foreign operation processor), and
// no BSN ever reaches a record, so there is nothing to derive a pseudonym
// from. Copying the full client would mean copying dead code.
//
// It is deliberately not an OTel exporter. A span here is best-effort exhaust
// of a technical operation; an LDV record is an administrative record that
// must exist for every processing, is confirmed on write and is never sampled
// (REQ-32).
//
// The portal names the verwerkingsactiviteit it performs and the logbook
// refuses a reference its own register does not resolve, which fails the
// citizen's action. Checking the register up front would only move the same
// failure earlier, at the cost of a second protocol between them.
package ldv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"gbo-demo/consent-portal-backend/consent"
)

// Attribute keys reserved by the standard.
const (
	attrProcessingActivityID = "dpl.core.processing_activity_id"
	attrDataSubjectID        = "dpl.core.data_subject_id"
	attrDataSubjectIDType    = "dpl.core.data_subject_id_type"

	// The portal-scoped subject reference: what this side of the chain names
	// a Betrokkene by. Not the BSN it started from and not the PI it derives
	// for the dienstverlener; `data_subject_id_type` is what keeps those
	// apart for whoever reads the record later.
	subjectTypePortalSubject = "portal-subject"
)

// Logbook writes records to one logbook — the logbook of the Verantwoordelijke
// this portal belongs to. It implements consent.Logbook.
type Logbook struct {
	endpoint    string
	token       string
	serviceName string
	client      *http.Client
}

// New reads the configuration. It returns (nil, nil) when logbookURL is
// empty: the portal is then simply not part of an LDV chain and writes no
// records. Every other misconfiguration is an error, because a
// half-configured logbook silently logging nothing is the failure mode this
// package exists to prevent.
func New(serviceName, logbookURL, token string) (*Logbook, error) {
	base := strings.TrimRight(logbookURL, "/")
	if base == "" {
		return nil, nil
	}
	if token == "" {
		return nil, fmt.Errorf("a logbook URL is set but no write token")
	}
	return &Logbook{
		endpoint:    base + "/logboek/records",
		token:       token,
		serviceName: serviceName,
		client:      &http.Client{Timeout: 5 * time.Second},
	}, nil
}

// record is the wire shape: the OTel log record LDV reuses.
type record struct {
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	Name       string            `json:"name"`
	StartTime  time.Time         `json:"start_time"`
	EndTime    time.Time         `json:"end_time"`
	Status     string            `json:"status"`
	Resource   map[string]string `json:"resource,omitempty"`
	Attributes map[string]any    `json:"attributes"`
}

// Record writes one Dataverwerking and returns only once the logbook has
// confirmed it. The error is meant to be propagated by the core: the citizen's
// action must fail rather than complete unlogged.
func (l *Logbook) Record(ctx context.Context, processing consent.Processing) error {
	attributes := map[string]any{
		attrProcessingActivityID: processing.Activity,
		attrDataSubjectID:        string(processing.Subject),
		attrDataSubjectIDType:    subjectTypePortalSubject,
	}
	for key, value := range processing.Attributes {
		if text, isText := value.(string); isText && text == "" {
			continue
		}
		if value != nil {
			attributes[key] = value
		}
	}

	body, err := json.Marshal(record{
		TraceID:    traceID(ctx),
		SpanID:     spanID(),
		Name:       processing.Name,
		StartTime:  processing.Start,
		EndTime:    processing.End,
		Status:     status(processing.Failed),
		Resource:   map[string]string{"service.name": l.serviceName},
		Attributes: attributes,
	})
	if err != nil {
		return fmt.Errorf("encode log record: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build log request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+l.token)

	response, err := l.client.Do(request)
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

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// traceID derives the record's trace id from the ambient OTel trace, so a
// citizen action and everything it triggers share one id. There is no
// Fsc-Transaction-Id here: the citizen calls this portal directly, and the
// FSC hop happens later, downstream of the consent.
func traceID(ctx context.Context) string {
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.HasTraceID() {
		if candidate := spanContext.TraceID().String(); traceIDPattern.MatchString(candidate) {
			return candidate
		}
	}
	return randomHex(16)
}

// spanID mints the identity of one record. It is the record's own, not the
// OTel span's: borrowing the span id would tie an administrative record to a
// sampling decision.
func spanID() string { return randomHex(8) }

func randomHex(byteCount int) string {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever
		// does, a time-derived id still keeps the record writable.
		return fmt.Sprintf("%0*x", byteCount*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func status(failed bool) string {
	if failed {
		return "ERROR"
	}
	return "OK"
}
