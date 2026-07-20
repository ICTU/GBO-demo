package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/graphql-go/graphql"
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

// ── Data model ────────────────────────────────────────────────────────────────

type CodeOmschrijving struct {
	Code         string `json:"code"`
	Omschrijving string `json:"omschrijving"`
}

type InkomensgegevensPerJaar struct {
	Belastingjaar   int               `json:"belastingjaar"`
	Verzamelinkomen *int              `json:"verzamelinkomen"`
	InkomenUitBox1  *int              `json:"inkomenUitBox1"`
	InkomenUitBox2  *int              `json:"inkomenUitBox2"`
	InkomenUitBox3  *int              `json:"inkomenUitBox3"`
	Grondslag       *CodeOmschrijving `json:"grondslag"`
	Status          *CodeOmschrijving `json:"status"`
	PeilDatum       *string           `json:"peilDatum"`
}

type Citizen struct {
	BSN              string                    `json:"bsn"`
	Inkomensgegevens []InkomensgegevensPerJaar `json:"inkomensgegevens"`
}

// ── Mock data store ───────────────────────────────────────────────────────────

var citizenStore map[string][]InkomensgegevensPerJaar

func loadMockData(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var citizens []Citizen
	if err := json.Unmarshal(data, &citizens); err != nil {
		return err
	}
	citizenStore = make(map[string][]InkomensgegevensPerJaar, len(citizens))
	for _, c := range citizens {
		citizenStore[c.BSN] = c.Inkomensgegevens
	}
	slog.Info("mock data loaded", "citizens", len(citizenStore))
	return nil
}

// ── GraphQL schema ────────────────────────────────────────────────────────────

var codeOmschrijvingType = graphql.NewObject(graphql.ObjectConfig{
	Name: "CodeOmschrijving",
	Fields: graphql.Fields{
		"code":         {Type: graphql.NewNonNull(graphql.String)},
		"omschrijving": {Type: graphql.NewNonNull(graphql.String)},
	},
})

var inkomensgegevensPerJaarType = graphql.NewObject(graphql.ObjectConfig{
	Name: "InkomensgegevensPerJaar",
	Fields: graphql.Fields{
		"belastingjaar":   {Type: graphql.NewNonNull(graphql.Int)},
		"verzamelinkomen": {Type: graphql.Int},
		"inkomenUitBox1":  {Type: graphql.Int},
		"inkomenUitBox2":  {Type: graphql.Int},
		"inkomenUitBox3":  {Type: graphql.Int},
		"grondslag":       {Type: codeOmschrijvingType},
		"status":          {Type: codeOmschrijvingType},
		"peilDatum":       {Type: graphql.String},
	},
})

func buildSchema(tracer trace.Tracer) (graphql.Schema, error) {
	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"inkomensgegevens": {
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(inkomensgegevensPerJaarType))),
				Args: graphql.FieldConfigArgument{
					"input": {Type: graphql.NewNonNull(graphql.NewInputObject(graphql.InputObjectConfig{
						Name: "InkomensgegevensInput",
						Fields: graphql.InputObjectConfigFieldMap{
							"burgerservicenummer": {Type: graphql.NewNonNull(graphql.String)},
							"belastingjaren":      {Type: graphql.NewList(graphql.NewNonNull(graphql.Int))},
						},
					}))},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					ctx := p.Context
					_, span := tracer.Start(ctx, "resolve.inkomensgegevens")
					defer span.End()

					input, ok := p.Args["input"].(map[string]interface{})
					if !ok {
						return nil, nil
					}
					bsn, _ := input["burgerservicenummer"].(string)
					jarenRaw, _ := input["belastingjaren"].([]interface{})

					records, exists := citizenStore[bsn]
					if !exists {
						return []interface{}{}, nil
					}

					// Filter by requested years if specified
					if len(jarenRaw) > 0 {
						yearSet := make(map[int]bool, len(jarenRaw))
						for _, y := range jarenRaw {
							if yr, ok := y.(int); ok {
								yearSet[yr] = true
							}
						}
						var filtered []InkomensgegevensPerJaar
						for _, r := range records {
							if yearSet[r.Belastingjaar] {
								filtered = append(filtered, r)
							}
						}
						return filtered, nil
					}
					return records, nil
				},
			},
		},
	})

	return graphql.NewSchema(graphql.SchemaConfig{Query: queryType})
}

// ── OTel setup ────────────────────────────────────────────────────────────────

func initTracer(ctx context.Context) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317"
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "graphql-server"
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
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

// ── HTTP server ───────────────────────────────────────────────────────────────

// newMux builds the routing tree for the GraphQL server. Extracted from
// main so integration tests can wire the handlers to an httptest.Server
// without starting the real listener. Schema + tracer are constructed by
// main and injected here; loading mock data + env-reads stay in main.
func newMux(schema *graphql.Schema, tracer trace.Tracer) *http.ServeMux {
	mux := http.NewServeMux()

	gqlHandler := handler.New(&handler.Config{
		Schema:   schema,
		Pretty:   true,
		GraphiQL: true,
	})

	// Wrap with OTel span
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, span := tracer.Start(r.Context(), "graphql.query")
		defer span.End()
		// The FSC-Inway proxies the Fsc-Transaction-Id through to the
		// backend. Store it as a span attribute so the dev-portal can use a
		// tag lookup to correlate the graphql trace with the adapter and pdp
		// traces (traceparent propagation is broken across the FSC hop).
		if txID := r.Header.Get("Fsc-Transaction-Id"); txID != "" {
			span.SetAttributes(attribute.String("gbo.fsc.transaction_id", txID))
		}
		gqlHandler.ServeHTTP(w, r.WithContext(r.Context()))
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "graphql-server"))

	ctx := context.Background()

	// OTel
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

	tracer := otel.Tracer("graphql-server")

	// Load mock data
	dataPath := "mockdata/citizens.json"
	if p := os.Getenv("MOCKDATA_PATH"); p != "" {
		dataPath = p
	}
	if err := loadMockData(dataPath); err != nil {
		slog.Error("failed to load mock data", "err", err.Error())
		os.Exit(1)
	}

	// Build schema
	schema, err := buildSchema(tracer)
	if err != nil {
		slog.Error("failed to build schema", "err", err.Error())
		os.Exit(1)
	}

	mux := newMux(&schema, tracer)

	port := "4000"
	slog.Info("listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, otelhttp.NewHandler(withAccessLog(mux), "graphql-server")); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}
