// Package main implements the bron-sidecar — a gateway sitting between the
// FSC-Inway and the source service. The Inway proxies a request only after the
// PDP has allowed it, so everything here runs after the authorization
// decision — provided nothing but the Inway can reach the sidecar. The
// deployment has to make sure of that: a caller that reaches the sidecar
// directly skips the PDP. In the demo the sidecar shares a network with the
// Inways and with what serves the source, and with nothing else. It is part
// of the source: the request arrives here exactly as the PDP judged it. Its
// role:
//
//  0. Refuse every request outside the FTV GraphQL profile's transport
//     subset: POST with a JSON body on the one GraphQL path (transport.go).
//  1. Look at the evidence the request carries:
//     - a consent token → the query names its subject with a placeholder.
//     Take the value of the form it asks for, made for this source, out of
//     the token, have the source's own decryption component read it, and put
//     the BSN or the source's own pseudonym in the placeholder's place. No
//     call to BSNk.
//     - no consent token → pass-through (the BSN is already in the query)
//  2. The source service (behind the sidecar) stays unchanged — it gets the
//     form its API takes, whatever the consumer sent.
//
// What this gives:
//   - In the DvTP flow the BSN stays out of the authorization envelope: the
//     consumer holds no identifier of the citizen, and only this sidecar
//     turns the token's value into a BSN. The EUDI flow sends a plain BSN,
//     which the PDP does see (#364; keeping it out of the PDP's decision logs
//     is #368).
//   - The sidecar is source-owned; the PDP does not perform data transformation
//     (gateway responsibility, not policy responsibility)
//
// The substitution itself is in the substitution package, apart from this
// handler, and reaches the decryption component through the decryption
// package.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"gbo-demo/bron-sidecar/decryption"
	"gbo-demo/bron-sidecar/substitution"
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

// ldvDeliveryInterval is how often the LDV spool is drained. Records are
// durable the moment they are written, so this governs only how quickly they
// reach the logbook, not whether they do.
const ldvDeliveryInterval = 2 * time.Second

type config struct {
	Port        string
	UpstreamURL string // source service (e.g. http://graphql-server:4000)
	// OwnPeerOIN is this source's OIN: the party an encrypted identity must
	// be made for.
	OwnPeerOIN string
	// PseudonymVars are the comma-separated names of the GraphQL variables
	// that name the subject (default: "bsn").
	PseudonymVars string
	// DecryptionURL is the source's own decryption component.
	DecryptionURL string
	// SubjectKeysDir holds the source's decryption keys and BSNk's scheme
	// keys. Empty means this source has none and answers no consent-based
	// request.
	SubjectKeysDir string
	// LDVDecryptionActivity and LDVForwardActivity name this sidecar's two
	// Dataverwerkingen in its Verantwoordelijke's register. They are
	// configuration because the same image runs in front of every bron, and
	// each bron's register names its activities in its own terms.
	LDVDecryptionActivity string
	LDVForwardActivity    string
	// GraphQLPath is the one path GraphQL is served on (transport.go).
	GraphQLPath string
}

func loadConfig() config {
	return config{
		Port:           getEnv("PORT", "4011"),
		UpstreamURL:    getEnv("UPSTREAM_URL", "http://graphql-server:4000"),
		OwnPeerOIN:     getEnv("OWN_PEER_OIN", "99999999900000000200"),
		PseudonymVars:  getEnv("PSEUDONYM_VARS", "bsn"),
		DecryptionURL:  getEnv("DECRYPTION_URL", "http://decryption-component:4003"),
		SubjectKeysDir: getEnv("SUBJECT_KEYS_DIR", ""),

		LDVDecryptionActivity: getEnv("LDV_DECRYPTION_ACTIVITY", ""),
		LDVForwardActivity:    getEnv("LDV_FORWARD_ACTIVITY", ""),
		GraphQLPath:           getEnv("GRAPHQL_PATH", defaultGraphQLPath),
	}
}

func getEnv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// consentTokenHeader carries the signed consent token. Its presence is what
// puts a request under the consent regime, here as in the PDP.
const consentTokenHeader = "X-GBO-Consent-Token"

// subjectVariables splits the configured list of subject variable names.
func subjectVariables(list string) []string {
	var names []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// subjectFromBody reads the subject-carrying GraphQL variables out of the
// request body without changing it. LDV needs them because a record is per
// Betrokkene and the sidecar has to know who that is before it can log the
// forward.
//
// A body that is not a GraphQL request, or that names no subject variable,
// yields nothing. That is not a gap: a request that identifies no Betrokkene
// is not a Dataverwerking of personal data, so there is no record to write.
func subjectFromBody(body []byte, subjectVars []string) map[string]string {
	var gql struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &gql); err != nil {
		return nil
	}
	subjects := map[string]string{}
	for _, name := range subjectVars {
		if value, ok := gql.Variables[name].(string); ok && value != "" {
			subjects[name] = value
		}
	}
	return subjects
}

// forwardHandler puts the citizen of the consent in the placeholder's place
// when the request carries a consent token, and forwards to the upstream.
// GraphQL body shape: {"query": "...", "variables": {...}}. Only the variables
// listed in cfg.PseudonymVars are rewritten; the query itself stays unchanged
// (source schema unaffected).
//
// It is also where two of this Verantwoordelijke's Dataverwerkingen are
// logged to its Logboek Dataverwerkingen: the decryption of the identity and
// the forward itself. When a logbook is configured, a record that the logbook
// does not confirm fails the request — the response is withheld rather than
// returned unlogged.
func forwardHandler(cfg config, client *http.Client, logbook *ldv.Client, substitute substitution.Substituter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())

		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// The evidence decides, not a contract property: a consent token
		// means the subject is to be taken from it.
		consentToken := r.Header.Get(consentTokenHeader)
		underConsent := consentToken != ""
		span.SetAttributes(attribute.Bool("gbo.sidecar.consent_token", underConsent))

		slog.Info("sidecar request",
			"method", r.Method, "path", r.URL.Path,
			"consent_token", underConsent,
			"body_len", len(body),
		)

		// The forward is the outer Dataverwerking; its span is the parent of
		// the decryption below and of whatever the source logs downstream, so
		// the whole request reads as one tree in the logboek.
		traceID := ldv.TraceID(r.Context(), r.Header)
		forward := ldvOperation{
			traceID: traceID,
			spanID:  ldv.SpanID(),
			// §3.3.1: when the caller was in the same trace, its span is this
			// record's parent — across FSC, in the caller's own logbook.
			parentSpanID: ldv.ParentSpanFor(r.Header, traceID),
			startTime:    time.Now().UTC(),
		}
		// A bron without a logbook is a supported configuration, so the
		// client is legitimately nil here and must not be dereferenced.
		if logbook != nil {
			forward.processor = logbook.ForeignProcessor(r)
		}

		// Who the request is about, as it arrived: a placeholder under a
		// consent, a BSN otherwise. Neither goes into a record. The sidecar
		// names the Betrokkene in its own Verantwoordelijke's pseudonym space —
		// a logbook-local pseudonym derived, with this source's key, from the
		// BSN or from this source's BSNk pseudonym of the citizen — so a record
		// here shares no identifier with the caller's logbook, and logbooks
		// join on the trace id alone.
		subjects := subjectFromBody(body, substitute.Variables)
		// filled maps a subject as it arrived onto what it stands for: the
		// BSN, or this source's pseudonym of the citizen.
		filled := map[string]string{}
		if !underConsent {
			for _, subject := range subjects {
				filled[subject] = subject
			}
		}

		if underConsent {
			decryptionStart := time.Now().UTC()
			result, substituteErr := substitute.Apply(r.Context(), body, consentToken)

			// Reading the identity is itself a Dataverwerking, and it is
			// logged whether or not it succeeded. A request that named no
			// subject decrypted nothing, so there is nothing to log for it.
			if result.BSN != "" || result.Pseudonym != "" || (substituteErr != nil && result.ConsentID != "") {
				if err := logDecryption(r.Context(), logbook, cfg, forward, result, substituteErr, decryptionStart); err != nil {
					ldv.LogFailure("dataverwerking.identiteit-ontsleuteling", err)
					http.Error(w, "the decryption could not be logged; refusing the request", http.StatusInternalServerError)
					return
				}
			}
			if substituteErr != nil {
				slog.Error("subject substitution failed", "err", substituteErr.Error())
				http.Error(w, "the subject of the consent could not be filled in", http.StatusBadRequest)
				return
			}
			if result.BSN != "" {
				filled[substitution.IdentityPlaceholder] = result.BSN
			}
			if result.Pseudonym != "" {
				filled[substitution.PseudonymPlaceholder] = result.Pseudonym
			}
			span.SetAttributes(attribute.Bool("gbo.sidecar.subject_substituted", result.BSN != "" || result.Pseudonym != ""))
			body = result.Body
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

		// Hand the source its position in the trace, so it files its records
		// under the same trace and below this one. On the standard
		// traceparent (§3.1), which also replaces whatever OTel injected
		// above: the LDV trace id is the one the whole chain shares, and two
		// different trace ids on one request is the problem this avoids.
		if logbook != nil {
			ldv.InjectTraceparent(req.Header, ldv.TraceContext{
				TraceID: forward.traceID, SpanID: forward.spanID, Sampled: true,
			})
			passSubject(req.Header, logbook, subjects, filled)
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
		if err := logForward(r.Context(), logbook, cfg, forward, subjects, filled, resp.StatusCode); err != nil {
			ldv.LogFailure("dataverwerking.bronquery-doorgifte", err)
			http.Error(w, "the forward could not be logged; withholding the response", http.StatusInternalServerError)
			return
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

// logDecryption records one reading of an encrypted value. The record names
// the Betrokkene by this source's logbook-local pseudonym, derived from what
// the reading produced — the BSN or the source's BSNk pseudonym, never either
// itself. A value that could not be read produced nothing to derive from; the
// record is then named by a pseudonym of the consent, which is still local
// and still names one citizen.
//
// The record points to no other logbook: the source read the value itself,
// with its own keys.
func logDecryption(ctx context.Context, logbook *ldv.Client, cfg config, forward ldvOperation, result substitution.Result, decryptErr error, start time.Time) error {
	if logbook == nil {
		return nil
	}
	named := result.BSN
	if named == "" {
		named = result.Pseudonym
	}
	if named == "" {
		named = "consent:" + result.ConsentID
	}
	subjectID, err := logbook.LocalPseudonym(named)
	if err != nil {
		return err
	}
	return logbook.Write(ctx, ldv.Record{
		TraceID:      forward.traceID,
		SpanID:       ldv.SpanID(),
		ParentSpanID: forward.spanID,
		Name:         "dataverwerking.identiteit-ontsleuteling",
		Status:       ldv.Status(decryptErr),
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes:   ldv.Attributes(cfg.LDVDecryptionActivity, subjectID, ldv.SubjectTypePseudonym, forward.processor, map[string]any{}),
	})
}

// passSubject hands the source the sidecar's name for the Betrokkene. The
// source receives a BSN or its own pseudonym and would otherwise derive a
// reference of its own; passing this one on keeps both components naming the
// Betrokkene alike.
func passSubject(header http.Header, logbook *ldv.Client, subjects, filled map[string]string) {
	for _, subject := range subjects {
		if named := filled[subject]; named != "" {
			if pseudonym, err := logbook.LocalPseudonym(named); err == nil {
				header.Set(ldv.HeaderSubjectID, pseudonym)
				header.Set(ldv.HeaderSubjectIDType, ldv.SubjectTypePseudonym)
			}
		}
		return
	}
}

// logForward records the forward once it has happened, before the response
// leaves this process. If the logbook does not confirm, the caller gets an
// error instead of data — the strongest ordering available without a
// two-phase commit, and the reason this is fail-closed rather than
// best-effort.
//
// One forward, one Betrokkene: the demo's queries are single-subject, and a
// multi-subject body would need child records rather than a reused span id.
func logForward(ctx context.Context, logbook *ldv.Client, cfg config, forward ldvOperation, subjects, filled map[string]string, statusCode int) error {
	if logbook == nil {
		return nil
	}
	for _, subject := range subjects {
		named := subject
		if value := filled[subject]; value != "" {
			named = value
		}
		subjectID, err := logbook.LocalPseudonym(named)
		if err != nil {
			return err
		}
		return logbook.Write(ctx, ldv.Record{
			TraceID:      forward.traceID,
			SpanID:       forward.spanID,
			ParentSpanID: forward.parentSpanID,
			Name:         "dataverwerking.bronquery-doorgifte",
			Status:       ldv.StatusFromHTTP(statusCode),
			StartTime:    forward.startTime,
			EndTime:      time.Now().UTC(),
			Attributes:   ldv.Attributes(cfg.LDVForwardActivity, subjectID, ldv.SubjectTypePseudonym, forward.processor, map[string]any{}),
		})
	}
	return nil
}

// ldvOperation is the identity and timing of one Dataverwerking while it is
// still in progress.
type ldvOperation struct {
	traceID      string
	spanID       string
	parentSpanID string
	startTime    time.Time
	processor    string
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

// newSubstituter wires the substitution to the source's decryption component
// and keys. A source without keys gets a component that refuses, so that a
// consent-based request fails instead of reaching the source unsubstituted.
func newSubstituter(cfg config, client *http.Client) (substitution.Substituter, error) {
	component := decryption.Component{URL: cfg.DecryptionURL, Client: client}
	if cfg.SubjectKeysDir != "" {
		keys, schemeKeys, err := decryption.LoadKeys(cfg.SubjectKeysDir)
		if err != nil {
			return substitution.Substituter{}, err
		}
		component.Keys, component.SchemeKeys = keys, schemeKeys
	}
	return substitution.Substituter{
		OwnOIN:    cfg.OwnPeerOIN,
		Variables: subjectVariables(cfg.PseudonymVars),
		Decrypter: component,
	}, nil
}

// newMux builds the routing tree for the sidecar. Extracted from main so
// integration tests can wire the handlers to an httptest.Server (with stub
// upstream and decryption component URLs in cfg) without starting the real
// listener.
//
// The transport check sits in front of the ServeMux, which would otherwise
// clean the path (//graphql → /graphql) before anything could compare it.
func newMux(cfg config, client *http.Client, logbook *ldv.Client) (http.Handler, error) {
	substitute, err := newSubstituter(cfg, client)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	graphQLPath := cfg.GraphQLPath
	if graphQLPath == "" {
		graphQLPath = defaultGraphQLPath
	}
	forward := transportOnly(graphQLPath, forwardHandler(cfg, client, logbook, substitute))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			mux.ServeHTTP(w, r)
			return
		}
		forward.ServeHTTP(w, r)
	}), nil
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
	if logbook != nil {
		// A local spool: durable here, delivered afterwards. The logbook is
		// no longer on the critical path of every forwarded request.
		outbox, err := ldv.OpenOutbox(getEnv("LDV_OUTBOX_PATH", "/data/ldv-outbox.jsonl"), logbook)
		if err != nil {
			fatal("opening the LDV outbox", err)
		}
		defer func() { _ = outbox.Close() }()
		logbook.UseOutbox(outbox)
		go outbox.Run(ctx, ldvDeliveryInterval)
	}

	// A source that answers consent-based requests cannot start without its
	// keys: it would pass the placeholder on to the source instead.
	mux, err := newMux(cfg, client, logbook)
	if err != nil {
		fatal("reading the decryption keys", err)
	}
	if cfg.SubjectKeysDir == "" {
		slog.Warn("no SUBJECT_KEYS_DIR configured; this bron refuses consent-based requests")
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(mux, serviceName),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	slog.Info("sidecar starting",
		"addr", srv.Addr,
		"upstream", cfg.UpstreamURL,
		"decryption_component", cfg.DecryptionURL,
		"subject_vars", cfg.PseudonymVars,
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
