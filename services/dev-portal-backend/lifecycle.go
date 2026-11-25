package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── Credential lifecycle: batch settings and the timeline of the own
// credential issuer ─────────────────────────────────────────────────────────
//
//	GET    /credential-lifecycle/settings                       → the settings in force
//	PUT    /credential-lifecycle/settings/providers/{p}          {batch_size}
//	DELETE /credential-lifecycle/settings/providers/{p}
//	PUT    /credential-lifecycle/settings/configurations/{id}    {reuse_policy}
//	DELETE /credential-lifecycle/settings/configurations/{id}
//	GET    /credential-lifecycle/events                         → the events of the last 50 minutes
//
// The portal passes these on to the issuer's demo API (/demo/ on its
// internal port) and changes nothing: the issuer decides what is valid and
// answers with its own codes. Without CREDENTIAL_ISSUER_INTERNAL_URL none
// of these exist, so the portal hides its block. When the issuer cannot be
// reached the answer is 502 {error: issuer_unavailable}.

// lifecycleBody bounds what the portal passes on and takes back.
const lifecycleBody = 1 << 20

func registerLifecycle(mux *http.ServeMux, issuerInternalURL string) {
	if issuerInternalURL == "" {
		return
	}
	base := strings.TrimRight(issuerInternalURL, "/")
	client := &http.Client{Timeout: 10 * time.Second}
	forward := func(issuerPath func(*http.Request) string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			passOn(w, r, client, base+issuerPath(r))
		}
	}
	fixed := func(p string) func(*http.Request) string { return func(*http.Request) string { return p } }
	provider := func(r *http.Request) string {
		return "/demo/settings/providers/" + url.PathEscape(r.PathValue("provider"))
	}
	configuration := func(r *http.Request) string {
		return "/demo/settings/configurations/" + url.PathEscape(r.PathValue("configuration"))
	}
	mux.HandleFunc("GET /credential-lifecycle/settings", forward(fixed("/demo/settings")))
	mux.HandleFunc("PUT /credential-lifecycle/settings/providers/{provider}", forward(provider))
	mux.HandleFunc("DELETE /credential-lifecycle/settings/providers/{provider}", forward(provider))
	mux.HandleFunc("PUT /credential-lifecycle/settings/configurations/{configuration}", forward(configuration))
	mux.HandleFunc("DELETE /credential-lifecycle/settings/configurations/{configuration}", forward(configuration))
	mux.HandleFunc("GET /credential-lifecycle/events", forward(fixed("/demo/events")))
}

// passOn sends r's method and JSON body to target and copies the answer
// back.
func passOn(w http.ResponseWriter, r *http.Request, client *http.Client, target string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, lifecycleBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "issuer_error"})
		return
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("credential lifecycle: issuer unreachable", "err", err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "issuer_unavailable"})
		return
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, lifecycleBody))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "issuer_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(answer)
}

// ── Test relying party (#737) ──────────────────────────────────────────────
//
//	GET  /test-rp/catalog        → the relying parties, attestation types and claims
//	POST /test-rp/presentations  {relying_party, credential, claims} → 201 {link, request_uri, session, expires_at}
//	GET  /test-rp/events         → what wallets presented, declined or failed to present, of the last 50 minutes
//
// Passed on to the internal API of test-relying-party as they are. Without
// TEST_RP_INTERNAL_URL none of these exist, so the block hides its form.

func registerTestRP(mux *http.ServeMux, internalURL string) {
	if internalURL == "" {
		return
	}
	base := strings.TrimRight(internalURL, "/")
	client := &http.Client{Timeout: 10 * time.Second}
	for route, target := range map[string]string{
		"GET /test-rp/catalog":        "/catalog",
		"POST /test-rp/presentations": "/presentations",
		"GET /test-rp/events":         "/events",
	} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			passOn(w, r, client, base+target)
		})
	}
}
