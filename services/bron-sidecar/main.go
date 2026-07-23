// Package main implements the bron-sidecar — a gateway sitting between the
// FSC-Inway and the source service. Its role:
//
//  1. Take the FSC-Authorization access-token from the incoming request and
//     read the additional claim 'subject_id_type':
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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

type config struct {
	Port          string
	UpstreamURL   string // source service (e.g. http://graphql-server:4000)
	BSNkURL       string // http://bsnk-mock:4003
	OwnPeerOIN    string // passed to BSNk /transform as recipient_oin
	PseudonymVars string // comma-separated variable names that carry PI values (default: "bsn")
}

func loadConfig() config {
	return config{
		Port:          getEnv("PORT", "4011"),
		UpstreamURL:   getEnv("UPSTREAM_URL", "http://graphql-server:4000"),
		BSNkURL:       getEnv("BSNK_URL", "http://bsnk-mock:4003"),
		OwnPeerOIN:    getEnv("OWN_PEER_OIN", "99999999900000000200"),
		PseudonymVars: getEnv("PSEUDONYM_VARS", "bsn"),
	}
}

func getEnv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// additionalClaimsFromAuth decodes the Fsc-Authorization token and returns the
// official 'add' claim populated by the provider Manager's Additional Claims
// API. Unsafe decode: chain-of-trust is on the FSC-Inway that already
// validated this token before the request reached us.
// Returns nil for a missing/invalid token — the caller treats that as the
// 'direct' flow (no data transformation). The legacy 'prp' claim remains a
// temporary fallback for older local tokens.
func additionalClaimsFromAuth(auth string) map[string]any {
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer"))
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims struct {
		Add map[string]any `json:"add"`
		Prp map[string]any `json:"prp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	if len(claims.Add) > 0 {
		return claims.Add
	}
	return claims.Prp
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

// forwardHandler inspects Fsc-Authorization, substitutes PI variables when
// needed, and forwards to the upstream. GraphQL body shape: {"query": "...",
// "variables": {...}}. We only rewrite variables listed in cfg.PseudonymVars;
// the query itself stays unchanged (source schema unaffected).
func forwardHandler(cfg config, client *http.Client) http.HandlerFunc {
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

		// Determine binding from the trusted additional claim.
		auth := r.Header.Get("Fsc-Authorization")
		props := additionalClaimsFromAuth(auth)
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
				bsn, err := resolvePI(r.Context(), client, cfg, piVal)
				if err != nil {
					slog.Error("PI resolve failed", "var", varName, "err", err.Error())
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

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "upstream unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
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
func newMux(cfg config, client *http.Client) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	// All non-health paths → forward.
	mux.HandleFunc("/", forwardHandler(cfg, client))
	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "bron-sidecar"))
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
	mux := newMux(cfg, client)

	addr := ":" + cfg.Port
	slog.Info("bron-sidecar starting",
		"addr", addr,
		"upstream", cfg.UpstreamURL,
		"bsnk", cfg.BSNkURL,
		"pseudonym_vars", cfg.PseudonymVars,
	)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(mux, "bron-sidecar")); err != nil {
		slog.Error("server stopped", "err", err.Error())
		os.Exit(1)
	}
}
