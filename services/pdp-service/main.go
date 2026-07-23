// Package main implements the PDP context-handler — the XACML "P3"
// role that sits between the PEP and the OPA decision engine. The PEP
// sends a standard AuthZEN evaluation request; the PDP parses the
// GraphQL query (action.properties.query), resolves the requested
// fields against the source-schema SDL, and forwards an enriched
// `input.resolved = {fields, args, coverage_unverifiable}` to OPA.
// OPA returns a single Decision which the PDP passes back verbatim.
//
// The AuthZEN wire shape is unchanged: the PEP still sends the raw
// query, the PDP still returns OPA's response as-is. The split is
// internal — the PEP does not need to know parsing exists, and OPA
// does not need to know about GraphQL.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	gqlparser "github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
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

// tokenAdditionalClaimsFromHeaders extracts additional claims from the FSC
// access-token. FSC-Inway forwards all incoming request-headers in
// AuthZen Context.headers; the Fsc-Authorization: Bearer <token>
// header carries the FSC-Manager-issued token, with claims returned by
// the Additional Claims API in the official 'add' claim. We read the
// token unsafely: FSC-Inway validated the signature before this handler
// was invoked (chain-of-trust).
// Returns nil for missing or malformed tokens so callers can fall
// back to header-based context (e.g. X-GBO-Flow).
//
// Token format: three base64url-encoded parts separated by "." (JWT-
// like); the payload sits in the middle segment. The legacy 'prp'
// claim remains a temporary fallback for older local tokens.
func tokenAdditionalClaimsFromHeaders(headers map[string]string) map[string]any {
	auth := headers["Fsc-Authorization"]
	if auth == "" {
		auth = headers["fsc-authorization"]
	}
	if auth == "" {
		return nil
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer"))
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some encoders still add padding — try the standard variant.
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

// withFscTraceContextFromRequestID reconstructs a Traceparent header
// from FSC's X-Request-Id so this service's spans join the caller's
// OTel trace. FSC-Inway's AuthZen-plugin invokes downstream services
// with a fresh context.Background(), which drops the traceparent, but
// the same plugin does propagate Fsc-Transaction-Id as X-Request-Id.
// The upstream adapter mints its OTel trace-id from that same UUID v7,
// so mapping X-Request-Id back onto a Traceparent header re-attaches
// this service's spans to the adapter's and GraphQL's trace without
// needing an upstream patch to FSC.
func withFscTraceContextFromRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Traceparent") == "" {
			xr := r.Header.Get("X-Request-Id")
			traceHex := strings.ReplaceAll(xr, "-", "")
			if len(traceHex) == 32 {
				var sb [8]byte
				if _, err := rand.Read(sb[:]); err == nil {
					r.Header.Set("Traceparent", "00-"+traceHex+"-"+hex.EncodeToString(sb[:])+"-01")
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type config struct {
	Port        string
	OPAURL      string
	SchemaDir   string
	ConsentURL  string
	TLSCertPath string
	TLSKeyPath  string
}

func loadConfig() config {
	return config{
		Port:        getEnv("PORT", "4008"),
		OPAURL:      getEnv("OPA_URL", "http://opa:8181"),
		SchemaDir:   getEnv("SCHEMA_DIR", "/schemas"),
		ConsentURL:  getEnv("CONSENT_URL", "http://consent-register:4002"),
		TLSCertPath: getEnv("TLS_CERT_PATH", ""),
		TLSKeyPath:  getEnv("TLS_KEY_PATH", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadSchemas reads both consumer-schemas: DvTP (consent-based) and EUDI
// (wallet-based). Both mirror the same BD bron-schema (bd.graphql); the
// PDP handler dispatches on action.name to pick the schema to use.
func loadSchemas(dir string) (map[string]*ast.Schema, error) {
	schemas := map[string]*ast.Schema{}
	dvtpSrc, err := os.ReadFile(filepath.Join(dir, "bd.graphql"))
	if err != nil {
		return nil, fmt.Errorf("dvtp schema: %w", err)
	}
	dvtp, err := gqlparser.LoadSchema(&ast.Source{Name: "bd.graphql", Input: string(dvtpSrc)})
	if err != nil {
		return nil, fmt.Errorf("parse dvtp schema: %w", err)
	}
	schemas["dvtp:query"] = dvtp

	eudiSrc, err := os.ReadFile(filepath.Join(dir, "eudi", "bd.graphql"))
	if err != nil {
		// The EUDI schema may be absent; fall back to the DvTP schema so
		// the service still starts.
		slog.Warn("eudi schema not found, EUDI-flow will fall back to dvtp schema", "err", err.Error())
		schemas["eudi:attestation"] = dvtp
	} else {
		eudi, err := gqlparser.LoadSchema(&ast.Source{Name: "eudi/bd.graphql", Input: string(eudiSrc)})
		if err != nil {
			return nil, fmt.Errorf("parse eudi schema: %w", err)
		}
		schemas["eudi:attestation"] = eudi
	}
	return schemas, nil
}

// authzInput reads the GraphQL query + variables from the incoming
// input. Query and variables live in input.resource.*; dispatch happens
// on input.action.name — dvtp:query vs eudi:attestation vs default.
type authzInput struct {
	Input struct {
		Action struct {
			Name string `json:"name"`
		} `json:"action"`
		Resource struct {
			ConsentID string         `json:"consent_id"`
			Scope     string         `json:"scope"`
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
			// EUDI-flow: the adapter places the BSN directly in resource
			// because there is no consent-ID. In the DvTP-flow this stays
			// empty.
			BSN string `json:"bsn"`
		} `json:"resource"`
	} `json:"input"`
}

// pipConsent is the policy-relevant subset of a consent-record. Mirrors
// the shape OPA's lib.evaluate expects under input.pip.consent.
type pipConsent struct {
	Exists        bool     `json:"exists"`
	Withdrawn     bool     `json:"withdrawn"`
	GrantedScopes []string `json:"granted_scopes"`
	ValidUntil    string   `json:"valid_until"`
	// PI field is required for the binding-check rule
	// (input.bsn == pip.consent.pi) after a by-PI lookup.
	PI string `json:"pi,omitempty"`
}

// pipPID is the policy-relevant shape for the EUDI flow. Carries the
// BSN that the adapter extracted from the wallet's disclosed PID.
// Signature verification is deliberately not performed here — that
// concern lives upstream in the adapter/wallet-attestation path.
type pipPID struct {
	BSN string `json:"bsn"`
}

// consentRecord is the relevant subset of consent-register's
// /consents/<id> response.
type consentRecord struct {
	Status     string   `json:"status"`
	Scopes     []string `json:"scopes"`
	ValidUntil string   `json:"valid_until"`
	// consent-register also returns the PI — pdp-service copies it to
	// pip.consent.pi for the binding-check rule.
	PI string `json:"pi,omitempty"`
}

// handleAuthz parses the AuthZEN request, dispatches on action.name to
// the appropriate enrichment (DvTP: consent-fetch from the consent
// register; EUDI: BSN from resource → input.pip.pid), builds resolved-
// fields against the schema for that flow, and forwards the enriched
// copy to OPA. The PEP is dumb with respect to policy-attributes; PIP
// lookups are the PDP's responsibility (the XACML "context handler"
// P3 role).
func handleAuthz(cfg config, client *http.Client, schemas map[string]*ast.Schema) http.HandlerFunc {
	target := cfg.OPAURL + "/v1/data/dvtp/authz"
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Expose the incoming AuthZEN-request (S/A/R/C) on the current
		// span so the dev-portal can render it inline in the PDP-node
		// popover — no Jaeger deep-dive needed for the demo scenario.
		if s := trace.SpanFromContext(r.Context()); s.IsRecording() {
			s.SetAttributes(attribute.String("gbo.authzen.request", string(body)))
		}

		enriched, err := enrichInput(r.Context(), body, schemas, cfg.ConsentURL, client)
		if err != nil {
			// Don't fail the whole request — fall back to passthrough so we
			// don't break the demo on a parse glitch. The runtime will deny
			// later via COVERAGE_UNVERIFIABLE / missing-pip if applicable.
			slog.Warn("enrichment failed, falling back to passthrough", "err", err.Error())
			enriched = body
		}

		if s := trace.SpanFromContext(r.Context()); s.IsRecording() {
			s.SetAttributes(attribute.String("gbo.opa.input", string(enriched)))
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(enriched))
		if err != nil {
			http.Error(w, "build opa request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		otel.GetTextMapPropagator().Inject(r.Context(), propagation.HeaderCarrier(req.Header))
		resp, err := client.Do(req)
		if err != nil {
			slog.Error("opa unreachable", "err", err.Error())
			http.Error(w, "opa unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
	}
}

// handleFSCAuthZen receives AuthZen-envelopes from FSC-Inway.
// Translates Subject/Resource/Action/Context into the OPA-input shape
// the EUDI rule expects:
//   - subject.id = FSC peer-OIN (from subject.properties.outway_peer_id)
//   - resource.scope = X-GBO-Scope header
//   - action.properties.body (JSON or base64-encoded {query, variables})
//     is decoded into resource.query + resource.variables + resource.bsn
//   - pip.pid.bsn is inferred from resource.variables.bsn so the
//     PID-present check in the EUDI rule fires
//
// The response is the AuthZen 1.0 decision-shape:
// {decision: bool, context: {...}}. FSC-Inway reads .Allowed from it.
func handleFSCAuthZen(cfg config, client *http.Client, schemas map[string]*ast.Schema) http.HandlerFunc {
	target := cfg.OPAURL + "/v1/data/dvtp/authz"
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("fsc-authzen request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Helper for the deny-response — always 200 with an AuthZen-shape
		// body, because FSC-Inway's generated client panics on non-200
		// responses with an unexpected body-shape.
		denyResp := func(code string) {
			resp := map[string]any{
				"decision": false,
				"context":  map[string]any{"reason_admin": map[string]any{"code": code}},
			}
			out, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(out)
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			slog.Error("fsc-authzen read body", "err", err.Error())
			denyResp("PDP_READ_BODY_ERROR")
			return
		}
		slog.Info("fsc-authzen envelope", "body_len", len(raw), "body_preview", string(raw[:min(200, len(raw))]))
		// FSC-Inway's AuthZen-plugin propagates Fsc-Transaction-Id as
		// X-Request-Id. The traceparent-context breaks at FSC-Inway (no
		// OTel support in the FSC version we run), so we expose the FSC
		// transaction-id as a span-attribute (gbo.fsc.transaction_id) to
		// correlate the adapter-trace with the pdp-trace. Empty means FSC
		// did not send one (e.g. tests, direct calls) — the attribute
		// then stays empty without error.
		fscTxID := r.Header.Get("X-Request-Id")
		if s := trace.SpanFromContext(r.Context()); s.IsRecording() {
			s.SetAttributes(
				attribute.String("gbo.fsc.authzen.request", string(raw)),
				attribute.String("gbo.fsc.transaction_id", fscTxID),
			)
		}

		var env struct {
			Subject struct {
				ID         string         `json:"id"`
				Properties map[string]any `json:"properties"`
			} `json:"subject"`
			Action struct {
				Name       string         `json:"name"`
				Properties map[string]any `json:"properties"`
			} `json:"action"`
			Context struct {
				Headers map[string]string `json:"headers"`
			} `json:"context"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			slog.Error("fsc-authzen parse envelope", "err", err.Error())
			denyResp("PDP_PARSE_ENVELOPE_ERROR")
			return
		}

		// The body can arrive as a plain-JSON string (Content-Type
		// application/json, as FSC-Inway stringifies it) or as base64.
		// Try raw JSON first, then fall back to base64-decode.
		var query string
		var variables map[string]any
		if bodyProp, ok := env.Action.Properties["body"].(string); ok && bodyProp != "" {
			decoded := []byte(bodyProp)
			var innerBody struct {
				Query     string                     `json:"query"`
				Variables map[string]json.RawMessage `json:"variables"`
			}
			if err := json.Unmarshal(decoded, &innerBody); err != nil {
				// Fallback: try base64-decode.
				b64, err2 := base64.StdEncoding.DecodeString(bodyProp)
				if err2 != nil {
					http.Error(w, "parse body (json+base64 failed): "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal(b64, &innerBody); err != nil {
					http.Error(w, "parse decoded body: "+err.Error(), http.StatusBadRequest)
					return
				}
			}
			query = innerBody.Query
			variables = map[string]any{}
			for k, v := range innerBody.Variables {
				variables[k] = v
			}
		}

		// Peer-OIN from Subject.properties.outway_peer_id (trusted; comes
		// from the FSC-token). Falls back to Subject.id.
		peerID, _ := env.Subject.Properties["outway_peer_id"].(string)
		if peerID == "" {
			peerID = env.Subject.ID
		}

		scope := env.Context.Headers["X-Gbo-Scope"]
		if scope == "" {
			scope = env.Context.Headers["X-GBO-Scope"]
		}
		// Flow dispatch: prefer the trusted additional claim from the FSC
		// token, then fall back to the untrusted X-GBO-Flow header for
		// deployments that do not yet configure the Additional Claims
		// API. The additional claim is signed into the access token by
		// the service provider's FSC Manager; the header is not.
		flow := ""
		tokenProps := tokenAdditionalClaimsFromHeaders(env.Context.Headers)
		if tokenProps != nil {
			if f, ok := tokenProps["flow"].(string); ok {
				flow = f
			}
		}
		if flow == "" {
			flow = env.Context.Headers["X-Gbo-Flow"]
		}
		if flow == "" {
			flow = env.Context.Headers["X-GBO-Flow"]
		}

		// Envelope → OPA-input mapping. Flow-agnostic here: no PIP
		// population, no BSN extraction, no default action.name.
		// enrichInput (the P3 context-handler) dispatches on action.name
		// and populates flow-specific PIP fields (pip.pid.bsn for EUDI,
		// pip.consent for DvTP) from resource.variables or an external
		// fetch.
		// input.trace_id links the OPA decision-log to our OTel trace so
		// the dev-portal can look decisions up by trace-id. This trace-id
		// equals the Fsc-Transaction-Id — one identifier through the
		// whole chain.
		traceIDStr := ""
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			traceIDStr = sc.TraceID().String()
		}
		opaInput := map[string]any{
			"input": map[string]any{
				"subject":  map[string]any{"type": "org", "id": peerID},
				"resource": map[string]any{"scope": scope, "query": query, "variables": variables},
				"action":   map[string]any{"name": flow},
				"trace_id": traceIDStr,
				// The FSC-transaction-id also lives on the OPA input so
				// it appears in the decision-log — enabling correlation
				// with both our traces and the FSC transaction log.
				"fsc": map[string]any{"transaction_id": fscTxID},
			},
		}
		envelopeBytes, _ := json.Marshal(opaInput)
		// Reuse the existing enrichInput — it parses the query-AST
		// against the schema and populates input.resolved.fields, which
		// OPA needs for per-field rule selection.
		enriched, err := enrichInput(r.Context(), envelopeBytes, schemas, cfg.ConsentURL, client)
		if err != nil {
			slog.Warn("fsc-authzen enrichment fallback (passthrough)", "err", err.Error())
			enriched = envelopeBytes
		}

		slog.Info("fsc-authzen opa input", "input", string(enriched))
		if s := trace.SpanFromContext(r.Context()); s.IsRecording() {
			s.SetAttributes(attribute.String("gbo.opa.input", string(enriched)))
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(enriched))
		if err != nil {
			http.Error(w, "build opa request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		otel.GetTextMapPropagator().Inject(r.Context(), propagation.HeaderCarrier(req.Header))
		resp, err := client.Do(req)
		if err != nil {
			slog.Error("opa unreachable", "err", err.Error())
			http.Error(w, "opa unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)

		slog.Info("fsc-authzen opa response", "status", resp.StatusCode, "body", string(respBody))

		// OPA-response shape: {"result": {"decision": bool, "context": {...}}}
		// Translate to AuthZen: {"decision": bool, "context": {...}}
		var opaResp struct {
			Result struct {
				Decision bool           `json:"decision"`
				Context  map[string]any `json:"context"`
			} `json:"result"`
		}
		_ = json.Unmarshal(respBody, &opaResp)

		authzenResp := map[string]any{
			"decision": opaResp.Result.Decision,
			"context":  opaResp.Result.Context,
		}
		out, _ := json.Marshal(authzenResp)

		if s := trace.SpanFromContext(r.Context()); s.IsRecording() {
			s.SetAttributes(
				attribute.Bool("gbo.fsc.authzen.decision", opaResp.Result.Decision),
				// Expose the OPA response (context contains denied_fields
				// + reason_admin) as a span-attribute so the dev-portal
				// can render the OPA popover via cross-trace-lookup on
				// gbo.fsc.transaction_id, even when traceparent is broken.
				attribute.String("gbo.opa.output", string(respBody)),
			)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
}

func enrichInput(ctx context.Context, body []byte, schemas map[string]*ast.Schema, consentURL string, client *http.Client) ([]byte, error) {
	var envelope struct {
		Input map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var ai authzInput
	if err := json.Unmarshal(body, &ai); err != nil {
		return nil, err
	}

	// Dispatch on action.name. Default = dvtp:query for backwards
	// compatibility with PEP-callers that do not yet set action.name.
	flowType := ai.Input.Action.Name
	if flowType == "" {
		flowType = "dvtp:query"
	}

	pipData := map[string]any{}
	switch flowType {
	case "eudi:attestation":
		// EUDI-flow: BSN comes from resource.bsn or resource.variables["bsn"].
		// The PDP-handler stays flow-agnostic and only forwards the raw
		// variables; the EUDI-specific BSN extraction happens here. PID
		// signature-verification lives upstream.
		bsn := ai.Input.Resource.BSN
		if bsn == "" {
			if v, ok := ai.Input.Resource.Variables["bsn"]; ok {
				switch t := v.(type) {
				case string:
					bsn = t
				case json.RawMessage:
					_ = json.Unmarshal(t, &bsn)
				}
			}
		}
		pipData["pid"] = pipPID{BSN: bsn}
	default:
		// DvTP-flow: fetch consent from the consent-register. Two paths
		// for backwards compatibility:
		//   1. New: PI in resource.variables.bsn — lookup by (PI, scope).
		//   2. Old (pep-service path): resource.consent_id — lookup by ID.
		// Fail-soft → exists=false so OPA denies fail-closed with
		// CONSENT_NOT_FOUND.
		pip := pipConsent{Exists: false}
		var c *consentRecord
		var err error
		if pi := extractStringVar(ai.Input.Resource.Variables, "bsn"); pi != "" && looksLikePI(pi) {
			c, err = fetchConsentByPI(ctx, client, consentURL, pi, ai.Input.Resource.Scope)
			if err != nil {
				slog.Info("consent by-PI fetch failed", "pi", pi, "scope", ai.Input.Resource.Scope, "err", err.Error())
			}
		} else if ai.Input.Resource.ConsentID != "" {
			c, err = fetchConsent(ctx, client, consentURL, ai.Input.Resource.ConsentID)
			if err != nil {
				slog.Info("consent by-ID fetch failed", "consent_id", ai.Input.Resource.ConsentID, "err", err.Error())
			}
		}
		if c != nil {
			pip = pipConsent{
				Exists:        true,
				Withdrawn:     c.Status == "REVOKED",
				GrantedScopes: c.Scopes,
				ValidUntil:    c.ValidUntil,
				PI:            c.PI,
			}
			// Binding-support: lib.constraint_binding reads
			// resource[<field>], so mirror pip.consent.pi to resource.pi
			// so the rule's constraint (bsn-arg ==
			// resource.pi) is evaluable without a lib-refactor.
			if c.PI != "" {
				var res map[string]json.RawMessage
				if resJSON, ok := envelope.Input["resource"]; ok {
					_ = json.Unmarshal(resJSON, &res)
				}
				if res == nil {
					res = map[string]json.RawMessage{}
				}
				res["pi"], _ = json.Marshal(c.PI)
				envelope.Input["resource"], _ = json.Marshal(res)
			}
		}
		pipData["consent"] = pip
	}
	pipJSON, _ := json.Marshal(pipData)

	// Pick schema-key by flow-type. loadSchemas always guarantees a
	// fallback map-entry, so this lookup cannot fail.
	schema, ok := schemas[flowType]
	if !ok {
		schema = schemas["dvtp:query"]
	}
	res := buildResolved(ai.Input.Resource.Query, ai.Input.Resource.Variables, schema)
	resJSON, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}

	if envelope.Input == nil {
		envelope.Input = map[string]json.RawMessage{}
	}
	envelope.Input["resolved"] = resJSON
	envelope.Input["pip"] = pipJSON
	return json.Marshal(envelope)
}

// extractStringVar pulls a string value out of a variables-map. The
// concrete type depends on how the JSON was decoded (may be either
// string or json.RawMessage).
func extractStringVar(vars map[string]any, key string) string {
	v, ok := vars[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.RawMessage:
		var s string
		if err := json.Unmarshal(t, &s); err == nil {
			return s
		}
	}
	return ""
}

// looksLikePI is a quick heuristic: a BSN is 9 numeric chars, a PI
// carries the "PI-" prefix or has some other shape. Sufficient for
// the demo. In production this would use type-inference from the
// query-shape, or an additional claim like subject_id_type would be
// authoritative and remove the need for the heuristic altogether.
func looksLikePI(s string) bool {
	if strings.HasPrefix(s, "PI-") {
		return true
	}
	// Anything that is not 9 digits is not a BSN → treat as PI.
	if len(s) != 9 {
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

// fetchConsentByPI fetches the first ACTIVE consent for (PI, scope).
// Returns nil without error when no match is found — enrichInput
// handles the fail-closed path.
func fetchConsentByPI(ctx context.Context, client *http.Client, baseURL, pi, scope string) (*consentRecord, error) {
	u := fmt.Sprintf("%s/consents?pi=%s&scope=%s&status=ACTIVE",
		baseURL, url.QueryEscape(pi), url.QueryEscape(scope))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpErr{status: resp.StatusCode}
	}
	var list []consentRecord
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

func fetchConsent(ctx context.Context, client *http.Client, baseURL, consentID string) (*consentRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/consents/"+consentID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpErr{status: resp.StatusCode}
	}
	var c consentRecord
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

type httpErr struct{ status int }

func (e *httpErr) Error() string { return http.StatusText(e.status) }

func initTracer(ctx context.Context) (func(context.Context) error, error) {
	endpoint := getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")
	serviceName := getEnv("OTEL_SERVICE_NAME", "pdp-service")
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
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// newMux builds the routing tree with the given config, HTTP client and
// parsed schemas. Extracted from main so integration tests can wire the
// handlers to an httptest.Server without loading schemas from disk or
// starting the real TLS listener.
func newMux(cfg config, client *http.Client, schemas map[string]*ast.Schema) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/data/dvtp/authz", handleAuthz(cfg, client, schemas))
	// FSC-Inway's AuthZen-plugin calls /evaluation with an AuthZen 1.0
	// access-evaluation envelope. Translates to the existing OPA-input
	// shape and returns an AuthZen-decision.
	mux.HandleFunc("/evaluation", handleFSCAuthZen(cfg, client, schemas))
	return mux
}

func main() {
	cfg := loadConfig()
	ctx := context.Background()
	shutdown, err := initTracer(ctx)
	if err != nil {
		slog.Error("tracer init failed", "err", err.Error())
		os.Exit(1)
	}
	defer func() { _ = shutdown(ctx) }()

	schemas, err := loadSchemas(cfg.SchemaDir)
	if err != nil {
		slog.Error("schema load failed", "err", err.Error(), "dir", cfg.SchemaDir)
		os.Exit(1)
	}
	for name, s := range schemas {
		slog.Info("schema loaded", "flow_type", name, "types", len(s.Types))
	}

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	mux := newMux(cfg, client, schemas)

	addr := ":" + cfg.Port
	handler := withFscTraceContextFromRequestID(otelhttp.NewHandler(mux, "pdp-service"))

	// FSC-Inway's AuthZen-plugin requires HTTPS for the /evaluation
	// call. With no plain-HTTP caller left in the chain, we run HTTPS-
	// only when TLS_CERT_PATH is set. Without TLS: plain HTTP for
	// stand-alone dev scenarios that do not involve FSC-Inway.
	if cfg.TLSCertPath != "" && cfg.TLSKeyPath != "" {
		slog.Info("pdp-service starting (TLS)", "addr", addr, "opa", cfg.OPAURL, "cert", cfg.TLSCertPath)
		if err := http.ListenAndServeTLS(addr, cfg.TLSCertPath, cfg.TLSKeyPath, handler); err != nil {
			slog.Error("server stopped", "err", err.Error())
		}
		return
	}
	slog.Info("pdp-service starting", "addr", addr, "opa", cfg.OPAURL)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "err", err.Error())
	}
}
