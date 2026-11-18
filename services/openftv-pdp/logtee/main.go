// Command logtee runs a command and passes its output through unchanged,
// while also exporting each line as an OpenTelemetry log record.
//
//	logtee /pdp [args...]
//
// The OpenFTV PDP has no OpenTelemetry instrumentation of its own. Its
// embedded OPA writes one "Decision Log" line per evaluation to stdout,
// which the developer portal shows as detail next to the Authorization
// Decision Log. Under compose, promtail shipped that console output to Loki.
// Under Aspire the dashboard serves OTLP logs, not console output, so this
// wrapper turns the console into OTLP logs.
//
// Without OTEL_EXPORTER_OTLP_ENDPOINT it only passes the output through.
// The export is best-effort observability: a line that cannot be exported
// is still written to the console, and the PDP never waits on the exporter.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// maxLine bounds one line: the decision-log lines carry the whole request
// and policy result and can be large.
const maxLine = 8 << 20

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: logtee <command> [args...]")
		os.Exit(2)
	}

	ctx := context.Background()
	logger, shutdown := newLogger(ctx)

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = os.Stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fail(err)
	}
	if err := cmd.Start(); err != nil {
		fail(err)
	}

	// Forward termination signals, so the PDP shuts down as it would alone.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for s := range signals {
			_ = cmd.Process.Signal(s)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); tee(stdout, os.Stdout, logger, otellog.SeverityInfo) }()
	go func() { defer wg.Done(); tee(stderr, os.Stderr, logger, otellog.SeverityWarn) }()
	wg.Wait()

	waitErr := cmd.Wait()
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = shutdown(flushCtx)

	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		os.Exit(exit.ExitCode())
	}
	if waitErr != nil {
		fail(waitErr)
	}
}

// tee copies r to w line by line and emits each line as a log record.
func tee(r io.Reader, w io.Writer, logger otellog.Logger, severity otellog.Severity) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = fmt.Fprintln(w, line)
		if logger == nil || strings.TrimSpace(line) == "" {
			continue
		}
		var record otellog.Record
		record.SetTimestamp(time.Now())
		record.SetSeverity(severity)
		record.SetBody(attribute.StringValue(line))
		logger.Emit(context.Background(), record)
	}
}

// newLogger returns nil when no OTLP endpoint is configured. The exporter
// reads OTEL_EXPORTER_OTLP_* itself: a URL with a scheme, the headers (the
// Aspire dashboard's API key) and the certificate settings.
func newLogger(ctx context.Context) (otellog.Logger, func(context.Context) error) {
	noop := func(context.Context) error { return nil }
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return nil, noop
	}
	var opts []otlploggrpc.Option
	if !strings.Contains(endpoint, "://") {
		opts = append(opts, otlploggrpc.WithEndpoint(endpoint), otlploggrpc.WithInsecure())
	}
	exporter, err := otlploggrpc.New(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logtee: OTLP log export disabled: %v\n", err)
		return nil, noop
	}
	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = "openftv-pdp"
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(semconv.ServiceName(name)))
	if err != nil {
		res = resource.Default()
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter, sdklog.WithExportInterval(time.Second))),
	)
	return provider.Logger("gbo-demo/openftv-pdp/logtee"), provider.Shutdown
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "logtee: %v\n", err)
	os.Exit(1)
}
