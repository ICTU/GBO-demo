package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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

// ── Data model ────────────────────────────────────────────────────────────────
// Bronprofiel BD (subset): the mock bron serves the IH-aangiften of an
// IngeschrevenPersoon. Amounts are Bedrag objects (waarde + valuta), as in
// the upstream schema (gbo-semantiek v0.3/graphql/bd.graphql).

type Bedrag struct {
	Waarde float64 `json:"waarde"`
	Valuta *string `json:"valuta"`
}

type AangifteIH struct {
	AangifteIdentificatie string  `json:"aangifteIdentificatie"`
	Belastingsoort        string  `json:"belastingsoort"`
	Belastingjaar         int     `json:"belastingjaar"`
	Status                string  `json:"status"`
	Indieningsdatum       *string `json:"indieningsdatum"`
	IngangsdatumAangifte  *string `json:"ingangsdatumAangifte"`
	EinddatumAangifte     *string `json:"einddatumAangifte"`
	Verzamelinkomen       *Bedrag `json:"verzamelinkomen"`
	Box1Inkomen           *Bedrag `json:"box1Inkomen"`
	Box2Inkomen           *Bedrag `json:"box2Inkomen"`
	Box3Inkomen           *Bedrag `json:"box3Inkomen"`
}

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

type config struct {
	Port                   string
	MockDataPath           string
	SourceMetadataPath     string
	MetadataSigningJWKPath string
}

func loadConfig() (config, error) {
	return config{
		Port:                   getEnv("PORT", "4000"),
		MockDataPath:           getEnv("MOCKDATA_PATH", "mockdata/citizens.json"),
		SourceMetadataPath:     os.Getenv("GBO_ATTESTATIONS_PATH"),
		MetadataSigningJWKPath: os.Getenv("GBO_METADATA_SIGNING_JWK_PATH"),
	}, nil
}

func loadSourceMetadataPublisher(cfg config) (*sourceMetadataPublisher, error) {
	if cfg.SourceMetadataPath == "" && cfg.MetadataSigningJWKPath == "" {
		return nil, nil
	}
	if cfg.SourceMetadataPath == "" || cfg.MetadataSigningJWKPath == "" {
		return nil, fmt.Errorf("GBO_ATTESTATIONS_PATH and GBO_METADATA_SIGNING_JWK_PATH must be configured together")
	}
	payload, err := os.ReadFile(cfg.SourceMetadataPath)
	if err != nil {
		return nil, fmt.Errorf("read source metadata: %w", err)
	}
	privateJWK, err := os.ReadFile(cfg.MetadataSigningJWKPath)
	if err != nil {
		return nil, fmt.Errorf("read source metadata signing JWK: %w", err)
	}
	return newSourceMetadataPublisher(payload, privateJWK)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type Citizen struct {
	BSN                        string       `json:"bsn"`
	HeeftBelastingjaarAangifte []AangifteIH `json:"heeftBelastingjaarAangifte"`
}

// ── Mock data store ───────────────────────────────────────────────────────────

// loadMockData returns the citizen records indexed by BSN. The store is
// returned rather than assigned to a package variable so that main can hand
// it to the schema explicitly and tests can build one without touching
// process-wide state.
func loadMockData(path string) (map[string]Citizen, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var citizens []Citizen
	if err := json.Unmarshal(data, &citizens); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	store := make(map[string]Citizen, len(citizens))
	for _, c := range citizens {
		store[c.BSN] = c
	}
	slog.Info("mock data loaded", "citizens", len(store))
	return store, nil
}

// ── GraphQL schema ────────────────────────────────────────────────────────────

// stringScalar builds a custom scalar that behaves like a String (the mock
// does not enforce the upstream formaat-restricties). Serialize accepts
// both string and *string — the mock data model uses pointers for optional
// fields.
func stringScalar(name, description string) *graphql.Scalar {
	asString := func(value interface{}) interface{} {
		switch v := value.(type) {
		case string:
			return v
		case *string:
			if v != nil {
				return *v
			}
		}
		return nil
	}
	return graphql.NewScalar(graphql.ScalarConfig{
		Name:        name,
		Description: description,
		Serialize:   asString,
		ParseValue:  asString,
		ParseLiteral: func(valueAST ast.Value) interface{} {
			if sv, ok := valueAST.(*ast.StringValue); ok {
				return sv.Value
			}
			return nil
		},
	})
}

var bsnScalar = stringScalar("BSN", "Burgerservicenummer: 9 cijfers met geldige elfproef.")
var datumScalar = stringScalar("Datum", "Kalenderdatum in ISO 8601, precisie tot op de dag (jjjj-mm-dd).")
var valutaScalar = stringScalar("CodelijstISO4217", "Valuta-aanduiding conform ISO 4217 (drieletterige code, EUR).")

var bedragType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Bedrag",
	Fields: graphql.Fields{
		"waarde": {Type: graphql.NewNonNull(graphql.Float)},
		"valuta": {Type: valutaScalar},
	},
})

// aangifteIHType is assigned in init() to break the var-init cycle:
// the interface's ResolveType points at the object, the object's
// Interfaces list points back at the interface.
var aangifteIHType *graphql.Object

var belastingjaarAangifteInterface = graphql.NewInterface(graphql.InterfaceConfig{
	Name: "BelastingjaarAangifte",
	Fields: graphql.Fields{
		"aangifteIdentificatie": {Type: graphql.NewNonNull(graphql.String)},
		"indieningsdatum":       {Type: datumScalar},
		"status":                {Type: graphql.NewNonNull(graphql.String)},
		"belastingjaar":         {Type: graphql.NewNonNull(graphql.Int)},
		"belastingsoort":        {Type: graphql.NewNonNull(graphql.String)},
		"ingangsdatumAangifte":  {Type: datumScalar},
		"einddatumAangifte":     {Type: datumScalar},
	},
	ResolveType: func(p graphql.ResolveTypeParams) *graphql.Object {
		// The mock bron only serves IH-aangiften.
		return aangifteIHType
	},
})

func init() {
	aangifteIHType = graphql.NewObject(graphql.ObjectConfig{
		Name: "AangifteIH",
		Interfaces: []*graphql.Interface{
			belastingjaarAangifteInterface,
		},
		Fields: graphql.Fields{
			"aangifteIdentificatie": {Type: graphql.NewNonNull(graphql.String)},
			"indieningsdatum":       {Type: datumScalar},
			"status":                {Type: graphql.NewNonNull(graphql.String)},
			"belastingjaar":         {Type: graphql.NewNonNull(graphql.Int)},
			"belastingsoort":        {Type: graphql.NewNonNull(graphql.String)},
			"ingangsdatumAangifte":  {Type: datumScalar},
			"einddatumAangifte":     {Type: datumScalar},
			"verzamelinkomen":       {Type: bedragType},
			"box1Inkomen":           {Type: bedragType},
			"box2Inkomen":           {Type: bedragType},
			"box3Inkomen":           {Type: bedragType},
		},
	})
}

var ingeschrevenPersoonType = graphql.NewObject(graphql.ObjectConfig{
	Name: "IngeschrevenPersoon",
	Fields: graphql.Fields{
		"bsn": {Type: graphql.NewNonNull(bsnScalar)},
		"heeftBelastingjaarAangifte": {
			Type: graphql.NewList(graphql.NewNonNull(belastingjaarAangifteInterface)),
			Args: graphql.FieldConfigArgument{
				// Demo-bron extension of the upstream BD schema: a year
				// filter. The PDP can only enforce per-year consent when
				// the selector travels inside the query.
				"belastingjaren": {Type: graphql.NewList(graphql.NewNonNull(graphql.Int))},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				persoon, ok := p.Source.(Citizen)
				if !ok {
					return nil, nil
				}
				jarenRaw, _ := p.Args["belastingjaren"].([]interface{})
				if len(jarenRaw) == 0 {
					return persoon.HeeftBelastingjaarAangifte, nil
				}
				yearSet := make(map[int]bool, len(jarenRaw))
				for _, y := range jarenRaw {
					if yr, ok := y.(int); ok {
						yearSet[yr] = true
					}
				}
				var filtered []AangifteIH
				for _, a := range persoon.HeeftBelastingjaarAangifte {
					if yearSet[a.Belastingjaar] {
						filtered = append(filtered, a)
					}
				}
				return filtered, nil
			},
		},
	},
})

func buildSchema(tracer trace.Tracer, store map[string]Citizen) (graphql.Schema, error) {
	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"ingeschrevenPersoon": {
				Type: ingeschrevenPersoonType,
				Args: graphql.FieldConfigArgument{
					"bsn": {Type: graphql.NewNonNull(bsnScalar)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					ctx := p.Context
					_, span := tracer.Start(ctx, "resolve.ingeschrevenPersoon")
					defer span.End()

					bsn, _ := p.Args["bsn"].(string)
					citizen, exists := store[bsn]
					if !exists {
						return nil, nil
					}
					return citizen, nil
				},
			},
		},
	})

	// AangifteIH is only reachable through the interface's ResolveType, so
	// register it explicitly — otherwise `... on AangifteIH` fragments fail
	// with 'Unknown type'.
	return graphql.NewSchema(graphql.SchemaConfig{
		Query: queryType,
		Types: []graphql.Type{aangifteIHType},
	})
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
func newMux(schema *graphql.Schema, tracer trace.Tracer, publishers ...http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	if len(publishers) > 0 && publishers[0] != nil {
		mux.Handle("/.well-known/gbo-attestations", publishers[0])
	}

	// Both UIs off: the ones graphql-go/handler bundles are GraphiQL 0.11 and
	// the retired Prisma GraphQL Playground, neither of which can build a
	// query by clicking or draw the schema. The playground lives in the
	// developer-portal now (/playground there, one page for every bron), so
	// this server serves GraphQL and nothing else. Introspection needs no
	// flag — graphql-go answers __schema/__type queries unconditionally, which
	// is what the portal's Schema tab reads.
	gqlHandler := handler.New(&handler.Config{
		Schema:     schema,
		Pretty:     true,
		GraphiQL:   false,
		Playground: false,
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

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "graphql-server"))
	if handled, err := runLocalMetadataKeyCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		if err != nil {
			fatal("initializing local metadata key", err)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fatal("loading configuration from environment", err)
	}

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

	tracer := otel.Tracer("graphql-server")

	store, err := loadMockData(cfg.MockDataPath)
	if err != nil {
		fatal("loading mock data", err)
	}

	schema, err := buildSchema(tracer, store)
	if err != nil {
		fatal("building schema", err)
	}
	publisher, err := loadSourceMetadataPublisher(cfg)
	if err != nil {
		fatal("loading source metadata publisher", err)
	}
	var mux *http.ServeMux
	if publisher == nil {
		mux = newMux(&schema, tracer)
	} else {
		mux = newMux(&schema, tracer, publisher)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(withAccessLog(mux), "graphql-server"),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	slog.Info("listening", "addr", srv.Addr)
	fatal("listen and serve", srv.ListenAndServe())
}
