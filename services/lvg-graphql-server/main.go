// Package main is the mock LVG (Landelijke Voorziening Gebouwen) source.
//
// It answers one verification question for the LVG/IR pilot: is this
// verblijfsobject one the citizen owns? The query names both the citizen and
// the VBO-id, and the answer is that same VBO-id or null, so the caller learns
// yes or no and nothing about the citizen's other buildings.
//
// Like every source it speaks BSN; the lvg-sidecar in front of it turns the
// PI the Installatie Register sends into one.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	ldv "gbo-demo/ldv-client"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/handler"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain after SIGTERM: stop accepting, let
// in-flight requests finish, then close whatever is left.
const shutdownTimeout = 15 * time.Second

// ldvDeliveryInterval is how often the LDV spool is drained.
const ldvDeliveryInterval = 2 * time.Second

type config struct {
	Port         string
	MockDataPath string
	LDVQuery     ldvQueryConfig
}

func loadConfig() config {
	return config{
		Port:         getEnv("PORT", "4008"),
		MockDataPath: getEnv("MOCKDATA_PATH", "mockdata/owners.json"),
		LDVQuery: ldvQueryConfig{
			ScopeActivityBase: os.Getenv("LDV_SCOPE_ACTIVITY_BASE"),
		},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Mock data ────────────────────────────────────────────────────────────────

type owner struct {
	BSN               string   `json:"bsn"`
	Verblijfsobjecten []string `json:"verblijfsobjecten"`
}

// ownership answers whether a BSN owns a VBO-id.
type ownership map[string]map[string]bool

func (o ownership) owns(bsn, vboID string) bool {
	return o[bsn][vboID]
}

func loadMockData(path string) (ownership, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var owners []owner
	if err := json.Unmarshal(data, &owners); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	store := make(ownership, len(owners))
	for _, o := range owners {
		vbos := make(map[string]bool, len(o.Verblijfsobjecten))
		for _, id := range o.Verblijfsobjecten {
			vbos[id] = true
		}
		store[o.BSN] = vbos
	}
	slog.Info("mock data loaded", "owners", len(store))
	return store, nil
}

// ── GraphQL schema ───────────────────────────────────────────────────────────

var bsnScalar = graphql.NewScalar(graphql.ScalarConfig{
	Name:        "BSN",
	Description: "Burgerservicenummer: 9 cijfers met geldige elfproef.",
	Serialize:   func(value interface{}) interface{} { return value },
	ParseValue:  func(value interface{}) interface{} { return value },
	ParseLiteral: func(valueAST ast.Value) interface{} {
		if sv, ok := valueAST.(*ast.StringValue); ok {
			return sv.Value
		}
		return nil
	},
})

// verblijfsobjectType is deliberately the only object type: the PDP's field
// map is global across sources, so its name must not collide with BD's or
// RvIG's.
var verblijfsobjectType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Verblijfsobject",
	Fields: graphql.Fields{
		"vboId": {Type: graphql.NewNonNull(graphql.String)},
	},
})

type verblijfsobject struct {
	VboID string `json:"vboId"`
}

func buildSchema(tracer trace.Tracer, store ownership) (graphql.Schema, error) {
	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"vbo": {
				Type:        verblijfsobjectType,
				Description: "Het verblijfsobject als de burger er eigenaar van is, anders null.",
				Args: graphql.FieldConfigArgument{
					"bsn":   {Type: graphql.NewNonNull(bsnScalar)},
					"vboId": {Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					_, span := tracer.Start(p.Context, "resolve.vbo")
					defer span.End()

					bsn, _ := p.Args["bsn"].(string)
					vboID, _ := p.Args["vboId"].(string)
					// Checking ownership is a processing of this citizen's
					// data whatever the answer, so the subject is noted
					// before the lookup rather than only on a match.
					queryFactsFrom(p.Context).noteSubject(bsn)
					if !store.owns(bsn, vboID) {
						return nil, nil
					}
					return verblijfsobject{VboID: vboID}, nil
				},
			},
		},
	})
	return graphql.NewSchema(graphql.SchemaConfig{Query: queryType})
}

// ── OTel setup ───────────────────────────────────────────────────────────────

func initTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")),
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

// ── HTTP server ──────────────────────────────────────────────────────────────

func newMux(schema *graphql.Schema, tracer trace.Tracer, logbook *sourceLogbook) *http.ServeMux {
	mux := http.NewServeMux()
	gqlHandler := handler.New(&handler.Config{Schema: schema, Pretty: true})

	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "graphql.query")
		defer span.End()
		if txID := r.Header.Get("Fsc-Transaction-Id"); txID != "" {
			span.SetAttributes(attribute.String("gbo.fsc.transaction_id", txID))
		}

		if logbook == nil {
			gqlHandler.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Buffered until the record is spooled: an unrecorded processing must
		// not be answered with data.
		start := time.Now().UTC()
		factsCtx, facts := withQueryFacts(ctx)
		buffered := newBufferedResponse()
		gqlHandler.ServeHTTP(buffered, r.WithContext(factsCtx))
		if err := logbook.logQuery(factsCtx, r, facts, start, buffered.status); err != nil {
			http.Error(w, "the source query could not be logged; withholding the response", http.StatusInternalServerError)
			return
		}
		buffered.flushTo(w)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	serviceName := getEnv("OTEL_SERVICE_NAME", "lvg-graphql-server")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))
	cfg := loadConfig()

	ctx := context.Background()
	if shutdown, err := initTracer(ctx, serviceName); err != nil {
		slog.Warn("tracer init failed", "err", err.Error())
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(shutdownCtx)
		}()
	}
	tracer := otel.Tracer(serviceName)

	store, err := loadMockData(cfg.MockDataPath)
	if err != nil {
		fatal("loading mock data", err)
	}
	schema, err := buildSchema(tracer, store)
	if err != nil {
		fatal("building schema", err)
	}

	// Either this source is part of an LDV chain and cannot start without its
	// logbook, or it is not and writes no records.
	client, err := ldv.New(ldv.Config{
		ServiceName:  serviceName,
		LogbookURL:   os.Getenv("LDV_LOGBOOK_URL"),
		WriteToken:   os.Getenv("LDV_WRITE_TOKEN"),
		PseudonymKey: os.Getenv("LDV_SUBJECT_PSEUDONYM_KEY"),
	})
	if err != nil {
		fatal("configuring the logboek client", err)
	}
	if client == nil {
		slog.Warn("no LDV_LOGBOOK_URL configured; this bron writes no Logboek Dataverwerkingen records")
	} else {
		outbox, err := ldv.OpenOutbox(getEnv("LDV_OUTBOX_PATH", "/data/ldv-outbox.jsonl"), client)
		if err != nil {
			fatal("opening the LDV outbox", err)
		}
		defer func() { _ = outbox.Close() }()
		client.UseOutbox(outbox)
		go outbox.Run(ctx, ldvDeliveryInterval)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(withAccessLog(newMux(&schema, tracer, newSourceLogbook(client, cfg.LDVQuery))), serviceName),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	serve(srv)
}

// serve runs the server until the process is asked to stop, then drains it.
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
