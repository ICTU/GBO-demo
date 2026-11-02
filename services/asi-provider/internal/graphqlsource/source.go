// Package graphqlsource is an authentic source behind the GBO chain, asked
// over FSC through the ASIP's own Outway and grant-link. The Inway's PDP
// judges each query (for the BRP, rule ASI0001). It implements
// verification.Source.
//
// What the source holds and how its fields make up the attributes comes from
// its description, the asi capability it publishes in its source metadata,
// as the active Source Registry release holds it. Per request the ASIP asks
// one query derived from that description, with only the fields the
// requested attributes need and the BSN from the access token as a variable:
// one FSC transaction and one PDP decision per request.
package graphqlsource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/tracing"
	"gbo-demo/asi-provider/internal/verification"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseBody = 1 << 20
)

// Source asks one authentic source through the Outway.
type Source struct {
	sourceID string
	endpoint string
	describe func(sourceID string) (*asimapping.Capability, bool)
	client   *http.Client
}

// New returns the source with sourceID, its id in the Source Registry, that
// posts its queries to outwayURL plus path, the grant-link of the ASIP's
// contract with it. describe returns its current description.
func New(sourceID, outwayURL, path string, describe func(sourceID string) (*asimapping.Capability, bool)) *Source {
	return &Source{
		sourceID: sourceID,
		endpoint: strings.TrimRight(outwayURL, "/") + "/" + strings.TrimLeft(path, "/"),
		describe: describe,
		client:   &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

// Description returns the source's current description; without one the
// source is unavailable: the ASIP cannot say what it holds.
func (s *Source) Description() (*asimapping.Capability, error) {
	asi, ok := s.describe(s.sourceID)
	if !ok {
		return nil, fmt.Errorf("%w: no current description of source %q in the Source Registry", verification.ErrSourceUnavailable, s.sourceID)
	}
	return asi, nil
}

// Ask posts document through the Outway with the BSN as the description's
// subject variable, and returns the answer at its result_pointer, nil when
// the source does not know the person. Errors name the status, never the
// BSN, so they can be logged. Its span names the source, the FSC
// transaction and the attributes asked for, never the BSN or a value.
func (s *Source) Ask(ctx context.Context, bsn string, asi *asimapping.Capability, document string, identifiers []string) (_ any, err error) {
	txID, err := tracing.TransactionID(ctx)
	if err != nil {
		return nil, err
	}
	ctx, span := otel.Tracer("asi-provider/graphqlsource").Start(ctx, "source query", trace.WithAttributes(
		attribute.String("gbo.asi.source", s.sourceID),
		attribute.String(tracing.TransactionIDAttribute, txID),
		attribute.StringSlice("gbo.asi.attributes", identifiers),
	))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	body, err := json.Marshal(map[string]any{
		"query":     document,
		"variables": map[string]string{asi.GraphQL.SubjectVariable: bsn},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Fsc-Transaction-Id", txID)
	// The instrumented client adds the traceparent of its span, in this trace.

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", verification.ErrSourceUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", verification.ErrSourceUnavailable, err)
	}
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, fmt.Errorf("%w: status %d (Fsc-Transaction-Id %s)", verification.ErrSourceUnavailable, resp.StatusCode, txID)
	case resp.StatusCode != http.StatusOK:
		// The Inway answers a PDP denial with 401; any refusal is a fault
		// in the chain's configuration, not a missing value.
		return nil, fmt.Errorf("refused by the chain: status %d, code %q (Fsc-Transaction-Id %s)", resp.StatusCode, fscCode(raw), txID)
	}
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode the answer of source %q (Fsc-Transaction-Id %s): %w", s.sourceID, txID, err)
	}
	if errs, _ := response["errors"].([]any); len(errs) > 0 {
		return nil, fmt.Errorf("source %q answered %d GraphQL errors (Fsc-Transaction-Id %s)", s.sourceID, len(errs), txID)
	}
	result, err := asi.Result(response)
	if err != nil {
		return nil, fmt.Errorf("the answer of source %q (Fsc-Transaction-Id %s): %w", s.sourceID, txID, err)
	}
	return result, nil
}

// fscCode is the code of an FSC error body, empty for any other body.
func fscCode(raw []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Code
}

var _ verification.Source = (*Source)(nil)
