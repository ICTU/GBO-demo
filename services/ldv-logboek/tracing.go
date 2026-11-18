package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp/filters"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// initTracer exports the logbook's request spans over OTLP. These spans are
// observability, best-effort and sampled like any other service's. They are
// not the logbook's records, which are the administrative store this service
// exists for. Without OTEL_EXPORTER_OTLP_ENDPOINT nothing is exported, so a
// logbook outside an instrumented environment runs as before.
func initTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlpTraceOptions(endpoint)...)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(semconv.ServiceName(serviceName)))
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

// traced wraps the logbook's handler in a server span per request and takes
// the caller's trace context over from traceparent, so a write shows up in the
// trace of the processing it records. Health probes are left out.
func traced(handler http.Handler) http.Handler {
	return otelhttp.NewHandler(handler, "ldv-logboek",
		otelhttp.WithFilter(filters.Not(filters.Path("/health"))),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			path := r.URL.Path
			if strings.HasPrefix(path, "/verwerkingsactiviteiten/") {
				path = "/verwerkingsactiviteiten/{id}/{version}"
			}
			return r.Method + " " + path
		}),
	)
}

// otlpTraceOptions picks how the OTLP exporter reaches the collector. A bare
// host:port, as docker-compose.yml and older deployments set it, uses a
// plaintext gRPC connection. A URL, as Aspire sets it, is left to the SDK: it
// reads the scheme (http means plaintext), the headers and the certificate
// settings from the OTEL_EXPORTER_OTLP_* variables itself, which WithEndpoint
// and WithInsecure would override.
func otlpTraceOptions(endpoint string) []otlptracegrpc.Option {
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return nil
	}
	return []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure()}
}
