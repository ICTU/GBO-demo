// Package main implements the bron-sidecar — a gateway sitting between the
// FSC-Inway and the source service. Its role:
//
//  1. Take the FSC-Authorization access-token from the incoming request and
//     read the grant property 'subject_id_type':
//     - "direct"    → pass-through (BSN is already in the query, no action)
//     - "pseudonym" → resolve PI values in query-variables to BSN via
//     BSNk-mock, substitute, forward
//  2. The source service (behind the sidecar) stays unchanged — it always
//     speaks BSN, regardless of whether the consumer sends PI or BSN.
//
// Advantages over the previous pep-service pipeline:
//   - BSN no longer ends up in the authorization envelope
//   - The sidecar is source-owned; the PDP does not perform data transformation
//     (gateway responsibility, not policy responsibility)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	ldv "gbo-demo/ldv-client"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"os/signal"
	"syscall"
)

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain after SIGTERM: stop accepting, let
// in-flight requests finish, then close whatever is left.
const shutdownTimeout = 15 * time.Second

type config struct {
	Port          string
	UpstreamURL   string // source service (e.g. http://graphql-server:4000)
	BSNkURL       string // http://bsnk-mock:4003
	OwnPeerOIN    string // passed to BSNk /transform as recipient_oin
	PseudonymVars string // comma-separated variable names that carry PI values (default: "bsn")
	// LDVResolutionActivity and LDVForwardActivity name this sidecar's two
	// Dataverwerkingen in its Verantwoordelijke's register. They are
	// configuration because the same image runs in front of every bron, and
	// each bron's register names its activities in its own terms.
	LDVResolutionActivity string
	LDVForwardActivity    string
}

func loadConfig() config {
	return config{
		Port:          getEnv("PORT", "4011"),
		UpstreamURL:   getEnv("UPSTREAM_URL", "http://graphql-server:4000"),
		BSNkURL:       getEnv("BSNK_URL", "http://bsnk-mock:4003"),
		OwnPeerOIN:    getEnv("OWN_PEER_OIN", "99999999900000000200"),
		PseudonymVars: getEnv("PSEUDONYM_VARS", "bsn"),

		LDVResolutionActivity: getEnv("LDV_RESOLUTION_ACTIVITY", ""),
		LDVForwardActivity:    getEnv("LDV_FORWARD_ACTIVITY", ""),
	}
}

func getEnv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// grantPropertiesFromAuth returns the 'prp' claim of the Fsc-Authorization
// token: the properties of the service-connection grant (fsc-core
// §Properties), part of the grant hash and therefore countersigned by both
// peers. Decoding happens in the LDV client's Claims, which does not verify —
// the
// chain-of-trust is on the FSC-Inway that already validated this token.
// Returns nil for a missing/invalid token — the caller treats that as the
// 'direct' flow (no data transformation).
func grantPropertiesFromAuth(auth string) map[string]any {
	properties, _ := ldv.Claims(auth)["prp"].(map[string]any)
	return properties
}

// resolvePI asks BSNk for PI → BSN. Only called when
// subject_id_type=pseudonym. On error the caller returns HTTP 400 so the
// source never sees a non-resolvable PI (fail-safe).
func resolvePI(ctx context.Context, client *http.Client, cfg config, pi string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"pi":            pi,
		"recipient_oin": cfg.OwnPeerOIN,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BSNkURL+"/transform", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("bsnk /transform status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		BSN string `json:"bsn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.BSN == "" {
		return "", fmt.Errorf("bsnk returned empty bsn")
	}
	return out.BSN, nil
}

// subjectFromBody reads the pseudonym-carrying GraphQL variables out of the
// request body without changing it. The sidecar already had to do this in the
// pseudonym flow to substitute them; LDV needs it in the direct flow too,
// because a record is per Betrokkene and the sidecar has to know who that is
// before it can log the forward.
//
// A body that is not a GraphQL request, or that names no subject variable,
// yields nothing. That is not a gap: a request that identifies no Betrokkene
// is not a Dataverwerking of personal data, so there is no record to write.
func subjectFromBody(body []byte, pseudoVars map[string]bool) map[string]string {
	var gql struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &gql); err != nil {
		return nil
	}
	subjects := map[string]string{}
	for name := range pseudoVars {
		if value, ok := gql.Variables[name].(string); ok && value != "" {
			subjects[name] = value
		}
	}
	return subjects
}

// forwardHandler inspects Fsc-Authorization, substitutes PI variables when
// needed, and forwards to the upstream. GraphQL body shape: {"query": "...",
// "variables": {...}}. We only rewrite variables listed in cfg.PseudonymVars;
// the query itself stays unchanged (source schema unaffected).
//
// It is also where two of this Verantwoordelijke's Dataverwerkingen are
// logged to its Logboek Dataverwerkingen: the de-pseudonymisation and the
// forward itself. When a logbook is configured, a record that the logbook
// does not confirm fails the request — the response is withheld rather than
// returned unlogged.
func forwardHandler(cfg config, client *http.Client, logbook *ldv.Client) http.HandlerFunc {
	pseudoVars := map[string]bool{}
	for _, v := range strings.Split(cfg.PseudonymVars, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			pseudoVars[v] = true
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())

		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Determine binding from the trusted grant property.
		auth := r.Header.Get("Fsc-Authorization")
		props := grantPropertiesFromAuth(auth)
		subjectIDType, _ := props["subject_id_type"].(string)
		if subjectIDType == "" {
			subjectIDType = "direct" // fail-safe default
		}
		span.SetAttributes(attribute.String("gbo.sidecar.subject_id_type", subjectIDType))

		slog.Info("sidecar request",
			"method", r.Method, "path", r.URL.Path,
			"subject_id_type", subjectIDType,
			"body_len", len(body),
		)

		// The forward is the outer Dataverwerking; its span is the parent of
		// the de-pseudonymisation below and of whatever the source logs
		// downstream, so the whole request reads as one tree in the logboek.
		forward := ldvOperation{
			traceID:   ldv.TraceID(r.Context(), r.Header),
			spanID:    ldv.SpanID(),
			startTime: time.Now().UTC(),
			processor: ldv.ForeignProcessor(r),
		}

		// Who the request is about, named the way it arrived. In the
		// pseudonym flow that is the PI; in the direct flow the sidecar holds
		// only a BSN and must derive a logbook-local pseudonym instead,
		// because REQ-60/72 keeps the BSN out of every record.
		subjects := subjectFromBody(body, pseudoVars)

		if subjectIDType == "pseudonym" {
			var gql struct {
				Query     string                 `json:"query"`
				Variables map[string]any         `json:"variables,omitempty"`
				OpName    string                 `json:"operationName,omitempty"`
				Extra     map[string]interface{} `json:"-"`
			}
			if err := json.Unmarshal(body, &gql); err != nil {
				http.Error(w, "parse graphql body: "+err.Error(), http.StatusBadRequest)
				return
			}
			// Substitute PI variables in place.
			resolved := 0
			for varName := range pseudoVars {
				piVal, ok := gql.Variables[varName].(string)
				if !ok || piVal == "" {
					continue
				}
				resolutionStart := time.Now().UTC()
				bsn, resolveErr := resolvePI(r.Context(), client, cfg, piVal)

				// The de-pseudonymisation is itself a Dataverwerking, and it
				// is logged whether or not it succeeded. The record names the
				// Betrokkene by the PI the request arrived with — a record
				// *about* turning a PI into a BSN still may not contain the
				// BSN it produced.
				if logbook != nil {
					record := ldv.Record{
						TraceID:      forward.traceID,
						SpanID:       ldv.SpanID(),
						ParentSpanID: forward.spanID,
						Name:         "dataverwerking.pi-bsn-resolutie",
						Status:       ldv.Status(resolveErr),
						StartTime:    resolutionStart,
						EndTime:      time.Now().UTC(),
						Attributes: ldv.Attributes(cfg.LDVResolutionActivity, piVal, ldv.SubjectTypePI, forward.processor, map[string]any{
							"gbo.graphql.variable": varName,
							"gbo.bsnk.recipient":   cfg.OwnPeerOIN,
						}),
					}
					if writeErr := logbook.Write(r.Context(), record); writeErr != nil {
						ldv.LogFailure(record.Name, writeErr)
						http.Error(w, "de-pseudonymisation could not be logged; refusing the request", http.StatusInternalServerError)
						return
					}
				}

				if resolveErr != nil {
					slog.Error("PI resolve failed", "var", varName, "err", resolveErr.Error())
					http.Error(w, "PI resolve failed for var "+varName, http.StatusBadRequest)
					return
				}
				gql.Variables[varName] = bsn
				resolved++
			}
			span.SetAttributes(attribute.Int("gbo.sidecar.vars_resolved", resolved))
			body, _ = json.Marshal(gql)
		}

		// Forward to upstream with original headers (except Host).
		req, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.UpstreamURL+r.URL.Path, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "build upstream request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		for k, vv := range r.Header {
			if strings.EqualFold(k, "Host") {
				continue
			}
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		otel.GetTextMapPropagator().Inject(r.Context(), propagation.HeaderCarrier(req.Header))

		// Hand the source the trace metadata it needs to file its own records
		// under the same trace and below this one. Only metadata crosses:
		// the records stay in each component's own logboek, and here both
		// components happen to share one because they share a
		// Verantwoordelijke.
		if logbook != nil {
			req.Header.Set(ldv.HeaderTraceID, forward.traceID)
			req.Header.Set(ldv.HeaderParentSpanID, forward.spanID)
			// In the pseudonym flow the source receives a BSN and would
			// otherwise have to invent a subject reference. Passing the PI on
			// keeps both components naming the same Betrokkene the same way.
			for _, pi := range subjects {
				if subjectIDType == "pseudonym" {
					req.Header.Set(ldv.HeaderSubjectID, pi)
					req.Header.Set(ldv.HeaderSubjectIDType, ldv.SubjectTypePI)
				}
				break
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "upstream unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// The forward has happened; log it before the response leaves this
		// process. If the logbook does not confirm, the caller gets an error
		// instead of data — the strongest ordering available without a
		// two-phase commit, and the reason this is fail-closed rather than
		// best-effort.
		if logbook != nil {
			for variable, subject := range subjects {
				subjectID, subjectType := subject, ldv.SubjectTypePI
				if subjectIDType != "pseudonym" {
					subjectID, subjectType = logbook.LocalPseudonym(subject), ldv.SubjectTypePseudonym
				}
				record := ldv.Record{
					TraceID:   forward.traceID,
					SpanID:    forward.spanID,
					Name:      "dataverwerking.bronquery-doorgifte",
					Status:    ldv.StatusFromHTTP(resp.StatusCode),
					StartTime: forward.startTime,
					EndTime:   time.Now().UTC(),
					Attributes: ldv.Attributes(cfg.LDVForwardActivity, subjectID, subjectType, forward.processor, map[string]any{
						"gbo.graphql.variable":        variable,
						"gbo.sidecar.subject_id_type": subjectIDType,
						"gbo.upstream.status":         resp.StatusCode,
						"gbo.scope":                   r.Header.Get("X-GBO-Scope"),
					}),
				}
				if writeErr := logbook.Write(r.Context(), record); writeErr != nil {
					ldv.LogFailure(record.Name, writeErr)
					http.Error(w, "the forward could not be logged; withholding the response", http.StatusInternalServerError)
					return
				}
				// One forward, one Betrokkene: the demo's queries are
				// single-subject, and a multi-subject body would need child
				// records rather than a reused span id.
				break
			}
		}

		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// ldvOperation is the identity and timing of one Dataverwerking while it is
// still in progress.
type ldvOperation struct {
	traceID   string
	spanID    string
	startTime time.Time
	processor string
}

func initTracer(ctx context.Context) (func(context.Context) error, error) {
	endpoint := getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	serviceName := getEnv("OTEL_SERVICE_NAME", "bron-sidecar")

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
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
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// newMux builds the routing tree for the sidecar. Extracted from main so
// integration tests can wire the handlers to an httptest.Server (with
// stub upstream + BSNk URLs in cfg) without starting the real listener.
func newMux(cfg config, client *http.Client, logbook *ldv.Client) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	// All non-health paths → forward.
	mux.HandleFunc("/", forwardHandler(cfg, client, logbook))
	return mux
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	// One image, one instance per bron (bron-sidecar for the BD bron,
	// brp-sidecar for the BRP bron). Take the identity from the environment so
	// logs and spans name the instance that ran, not the image — the
	// dev-portal matches the sidecar-span on it.
	serviceName := getEnv("OTEL_SERVICE_NAME", "bron-sidecar")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))
	cfg := loadConfig()

	ctx := context.Background()
	shutdown, err := initTracer(ctx)
	if err != nil {
		slog.Warn("tracer init failed", "err", err.Error())
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(shutdownCtx); err != nil {
				slog.Error("tracer shutdown error", "err", err.Error())
			}
		}()
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// Either this bron is part of a Verantwoordelijke's LDV chain, or it is
	// not and writes no records. The activity references are configuration:
	// the same image runs in front of every bron, and each bron's register
	// names its processings in its own terms. A reference the logbook does not
	// know is refused at write time, which fails the request.
	logbook, err := ldv.New(ldv.Config{
		ServiceName:  serviceName,
		LogbookURL:   os.Getenv("LDV_LOGBOOK_URL"),
		WriteToken:   os.Getenv("LDV_WRITE_TOKEN"),
		PseudonymKey: os.Getenv("LDV_SUBJECT_PSEUDONYM_KEY"),
	})
	if err != nil {
		fatal("configuring the logboek client", err)
	}
	if logbook == nil {
		slog.Warn("no LDV_LOGBOOK_URL configured; this bron writes no Logboek Dataverwerkingen records")
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(newMux(cfg, client, logbook), serviceName),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	slog.Info("sidecar starting",
		"addr", srv.Addr,
		"upstream", cfg.UpstreamURL,
		"bsnk", cfg.BSNkURL,
		"pseudonym_vars", cfg.PseudonymVars,
	)
	serve(srv)
}

// serve runs the server until the process is asked to stop, then drains it.
// Without this a SIGTERM (docker compose down, a Kubernetes rollout) killed
// in-flight requests outright.
func serve(srv *http.Server) {
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			fatal("listen and serve", err)
		}
	}()
	slog.Info("listening", "addr", srv.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	slog.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("drain did not finish; closing remaining connections", "err", err.Error())
		_ = srv.Close()
	}
}
