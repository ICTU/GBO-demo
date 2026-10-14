// Package tracing ties the ASIP's spans to FSC, as the eudi-adapter does.
// Every request to the ASIP gets one Fsc-Transaction-Id, for its one query to
// the authentic source. Without an incoming traceparent the trace id is that
// id, so the FSC transaction log, the PDP decision log and the trace share
// one value, and the developer portal finds the transaction from the trace.
//
// Spans carry no BSN and no attribute value: names and identifiers only.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// TransactionIDAttribute is the span attribute the developer portal reads
// the FSC transaction from.
const TransactionIDAttribute = "gbo.fsc.transaction_id"

type transactionKey struct{}

// Init sets the W3C trace context propagator, and the OTLP exporter when
// OTEL_EXPORTER_OTLP_ENDPOINT is set; without it no span is exported (the
// service on its own, the tests). It returns the shutdown of the exporter.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("OTLP exporter: %w", err)
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(100*time.Millisecond)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Middleware mints the request's Fsc-Transaction-Id, ties the trace to it
// when the caller sent no traceparent, and starts the server span. The span
// is named after the method and path, which hold no personal data.
func Middleware(next http.Handler, serviceName string) http.Handler {
	instrumented := otelhttp.NewHandler(next, serviceName,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewV7()
		if err != nil {
			http.Error(w, "could not generate a transaction id", http.StatusInternalServerError)
			return
		}
		txID := id.String()
		if r.Header.Get("Traceparent") == "" {
			if span, err := randomSpanID(); err == nil {
				r.Header.Set("Traceparent", "00-"+strings.ReplaceAll(txID, "-", "")+"-"+span+"-01")
			}
		}
		instrumented.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), transactionKey{}, txID)))
	})
}

// TransactionID returns the Fsc-Transaction-Id of the request in ctx, or a
// fresh UUID v7 outside a request (the Outway rejects other versions).
func TransactionID(ctx context.Context) (string, error) {
	if id, ok := ctx.Value(transactionKey{}).(string); ok {
		return id, nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate Fsc-Transaction-Id: %w", err)
	}
	return id.String(), nil
}

func randomSpanID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
