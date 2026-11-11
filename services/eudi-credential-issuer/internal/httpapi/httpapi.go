// Package httpapi is the HTTP adapter of the issuer: the wallet-facing
// endpoints of OpenID4VCI and OAuth on the public port, and the offer and
// revocation API on the internal port.
package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/clientattest"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// maxBody limits request bodies.
const maxBody = 1 << 20

// Public returns the wallet-facing handler.
func Public(svc *issuance.Service, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	// Issuer identifiers are <public URL>/{provider}/{profile}; their
	// well-known URLs insert the well-known path before them (RFC 8414
	// section 3, OpenID4VCI 1.0 section 12.2.2).
	mux.HandleFunc("GET /.well-known/openid-credential-issuer/{provider}/{profile}", h.issuerMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/{provider}/{profile}", h.asMetadata)
	mux.HandleFunc("POST /{provider}/{profile}/challenge", h.challenge)
	mux.HandleFunc("POST /{provider}/{profile}/token", h.token)
	mux.HandleFunc("POST /{provider}/{profile}/nonce", h.nonce)
	mux.HandleFunc("POST /{provider}/{profile}/credential", h.credential)
	mux.HandleFunc("POST /{provider}/{profile}/credential_preview", h.preview)
	mux.HandleFunc("POST /{provider}/{profile}/notification", h.notification)
	mux.HandleFunc("POST /{provider}/{profile}/deferred_credential", h.deferredCredential)
	mux.HandleFunc("GET /status/{provider}/{list}", h.statusList)
	// Disclosure-based issuance: the wallet fetches the OpenID4VP request
	// of an offer by reference and posts its response (direct_post.jwt).
	mux.HandleFunc("GET /{provider}/{profile}/vp/{offer}/request", h.disclosureRequest)
	mux.HandleFunc("GET /{provider}/{profile}/vp/{offer}/no-data", h.noDataPage)
	mux.HandleFunc("POST /{provider}/{profile}"+issuance.DisclosureResponsePath+"{session}", h.disclosureResponse)
	mux.HandleFunc("GET /disclosure-offers", h.disclosureOffers)
	// The authorization code flow with a login at GBO's authorization
	// server (eudi profile).
	mux.HandleFunc("POST /{provider}/{profile}/par", h.par)
	// One pattern for both browser endpoints: two would conflict with
	// GET /status/{provider}/{list}, this one is less specific than it.
	mux.HandleFunc("GET /{provider}/{profile}/{endpoint}", h.browser)
	return mux
}

// Internal returns the handler for the internal port. It has no
// authentication of its own. Demo shortcut: it must only be reachable from
// trusted callers on the internal network; a real deployment authenticates
// each caller.
func Internal(svc *issuance.Service, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /offers", h.createOffer)
	mux.HandleFunc("POST /revocations", h.revoke)
	return mux
}

type handler struct {
	svc *issuance.Service
	log *slog.Logger
}

// target is the provider and profile of a request path.
func target(r *http.Request) issuance.Target {
	return issuance.Target{Provider: r.PathValue("provider"), Profile: r.PathValue("profile")}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *handler) issuerMetadata(w http.ResponseWriter, r *http.Request) {
	// Signed when asked for (section 12.2.3; ARF ISSU_32; the NL Wallet
	// asks for it), unsigned JSON otherwise (section 12.2.2 requires both).
	w.Header().Set("Vary", "Accept")
	if !accepts(r, "application/jwt") {
		md, err := h.svc.IssuerMetadataJSON(target(r))
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, md)
		return
	}
	md, err := h.svc.IssuerMetadata(target(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/jwt")
	_, _ = io.WriteString(w, md)
}

func (h *handler) asMetadata(w http.ResponseWriter, r *http.Request) {
	md, err := h.svc.AuthorizationServerMetadata(target(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, md)
}

func (h *handler) challenge(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Challenge(r.Context(), target(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"attestation_challenge": c})
}

func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidRequest, Description: "the body must be a form"})
		return
	}
	resp, err := h.svc.Token(r.Context(), issuance.TokenRequest{
		Target:               target(r),
		GrantType:            r.PostForm.Get("grant_type"),
		PreAuthorizedCode:    r.PostForm.Get("pre-authorized_code"),
		TxCode:               r.PostForm.Get("tx_code"),
		Code:                 r.PostForm.Get("code"),
		CodeVerifier:         r.PostForm.Get("code_verifier"),
		RedirectURI:          r.PostForm.Get("redirect_uri"),
		RefreshToken:         r.PostForm.Get("refresh_token"),
		ClientID:             r.PostForm.Get("client_id"),
		ClientAttestation:    r.Header.Get(clientattest.AttestationHeader),
		ClientAttestationPoP: r.Header.Get(clientattest.PoPHeader),
		DPoP:                 r.Header.Values("DPoP"),
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) nonce(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.Nonce(r.Context(), target(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, oid4vci.NonceResponse{CNonce: n})
}

func (h *handler) credential(w http.ResponseWriter, r *http.Request) {
	call := issuance.CredentialCall{
		Target:        target(r),
		Authorization: r.Header.Get("Authorization"),
		DPoP:          r.Header.Values("DPoP"),
	}
	body := http.MaxBytesReader(w, r.Body, maxBody)
	// An encrypted request is a JWE served as application/jwt (section 10).
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/jwt" {
		raw, err := io.ReadAll(body)
		if err != nil {
			h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidCredentialRequest, Description: "the body could not be read"})
			return
		}
		call.Encrypted, call.EncryptedRequest = true, strings.TrimSpace(string(raw))
	} else if err := json.NewDecoder(body).Decode(&call.Request); err != nil {
		h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidCredentialRequest, Description: "the body must be a JSON credential request"})
		return
	}
	res, err := h.svc.Credential(r.Context(), call)
	if err != nil {
		h.failResource(w, err, "DPoP")
		return
	}
	writeCredentialResult(w, res)
}

// writeCredentialResult writes a credential response: 202 when it defers
// the issuance, as a JWE when the wallet asked for encryption.
func writeCredentialResult(w http.ResponseWriter, res issuance.CredentialResult) {
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if res.Deferred {
		status = http.StatusAccepted
	}
	if res.Encrypted != "" {
		w.Header().Set("Content-Type", "application/jwt")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, res.Encrypted)
		return
	}
	writeJSON(w, status, res.Response)
}

// deferredCredential handles a deferred credential request (section 9), as
// JSON or as a JWE.
func (h *handler) deferredCredential(w http.ResponseWriter, r *http.Request) {
	call := issuance.DeferredCall{Target: target(r), Authorization: r.Header.Get("Authorization"), DPoP: r.Header.Values("DPoP")}
	body := http.MaxBytesReader(w, r.Body, maxBody)
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/jwt" {
		raw, err := io.ReadAll(body)
		if err != nil {
			h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidCredentialRequest, Description: "the body could not be read"})
			return
		}
		call.Encrypted, call.EncryptedRequest = true, strings.TrimSpace(string(raw))
	} else if err := json.NewDecoder(body).Decode(&call.Request); err != nil {
		h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidCredentialRequest, Description: "the body must be a JSON deferred credential request"})
		return
	}
	res, err := h.svc.Deferred(r.Context(), call)
	if err != nil {
		h.failResource(w, err, "DPoP")
		return
	}
	if res.Deferred {
		h.log.Info("issuance still deferred")
	}
	writeCredentialResult(w, res)
}

// notification takes the wallet's notification (section 11); the issuer
// logs the event and the id, nothing about the person.
func (h *handler) notification(w http.ResponseWriter, r *http.Request) {
	call := issuance.NotificationCall{Target: target(r), Authorization: r.Header.Get("Authorization"), DPoP: r.Header.Values("DPoP")}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&call.Notification); err != nil {
		h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidNotificationRequest, Description: "the body must be a JSON notification"})
		return
	}
	if err := h.svc.Notify(r.Context(), call); err != nil {
		h.failResource(w, err, "DPoP")
		return
	}
	h.log.Info("notification", "event", call.Notification.Event, "notification_id", call.Notification.NotificationID,
		"event_description", description(call.Notification.EventDescription))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.CredentialPreview(r.Context(), target(r), r.Header.Get("Authorization"))
	if err != nil {
		h.failResource(w, err, "Bearer")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) statusList(w http.ResponseWriter, r *http.Request) {
	tok, err := h.svc.StatusList(r.Context(), r.PathValue("provider"), r.PathValue("list"))
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/statuslist+jwt")
	_, _ = io.WriteString(w, tok)
}

type offerRequest struct {
	Profile                    string            `json:"profile"`
	CredentialConfigurationIDs []string          `json:"credential_configuration_ids"`
	Subject                    map[string]any    `json:"subject"`
	Parameters                 map[string]string `json:"parameters"`
	TxCode                     bool              `json:"tx_code"`
	Reference                  string            `json:"reference"`
}

type offerResponse struct {
	CredentialOffer oid4vci.CredentialOffer `json:"credential_offer"`
	Links           map[string]string       `json:"links"`
	// TxCodeValue is for the caller to deliver through another channel.
	TxCodeValue string `json:"tx_code_value,omitempty"`
}

func (h *handler) createOffer(w http.ResponseWriter, r *http.Request) {
	var req offerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	offer, err := h.svc.CreateOffer(r.Context(), issuance.OfferRequest{
		Profile: req.Profile, ConfigurationIDs: req.CredentialConfigurationIDs,
		Subject: req.Subject, Parameters: req.Parameters, TxCode: req.TxCode, Reference: req.Reference,
	})
	if err != nil {
		h.failOffer(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, offerResponse{CredentialOffer: offer.CredentialOffer, Links: offer.Links, TxCodeValue: offer.TxCodeValue})
}

func (h *handler) failOffer(w http.ResponseWriter, err error) {
	var status int
	var code string
	switch {
	case errors.Is(err, issuance.ErrUnknownProfile):
		status, code = http.StatusBadRequest, "unknown_profile"
	case errors.Is(err, issuance.ErrUnknownConfiguration):
		status, code = http.StatusBadRequest, "unknown_configuration"
	case errors.Is(err, issuance.ErrMixedProviders):
		status, code = http.StatusBadRequest, "mixed_providers"
	case errors.Is(err, issuance.ErrTxCodeUnsupported):
		status, code = http.StatusBadRequest, "tx_code_unsupported"
	case errors.Is(err, issuance.ErrNoData):
		status, code = http.StatusNotFound, "no_data"
	case errors.Is(err, issuance.ErrSourceUnavailable):
		status, code = http.StatusServiceUnavailable, "source_unavailable"
	case errors.Is(err, issuance.ErrBadSourceData):
		status, code = http.StatusBadGateway, "bad_source_data"
	default:
		h.log.Error("offer failed", "error", err)
		status, code = http.StatusInternalServerError, "server_error"
	}
	h.log.Info("offer refused", "error", code)
	writeJSON(w, status, map[string]string{"error": code, "error_description": err.Error()})
}

// revocationRequest names either the offer reference, which revokes all
// credentials issued for it, or one status list entry.
type revocationRequest struct {
	Reference string `json:"reference"`
	Provider  string `json:"provider"`
	List      string `json:"list"`
	Index     *int   `json:"index"`
}

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	var req revocationRequest
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req)
	byReference := req.Reference != "" && req.Provider == "" && req.List == "" && req.Index == nil
	byEntry := req.Reference == "" && req.Provider != "" && req.List != "" && req.Index != nil
	if err != nil || byReference == byEntry {
		// A revocation cannot be undone, so nothing is guessed.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
			"error_description": "give either reference, or provider, list and index"})
		return
	}
	if byReference {
		n, err := h.svc.RevokeReference(r.Context(), req.Reference)
		if err != nil {
			h.log.Error("revocation failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
		return
	}
	switch err := h.svc.Revoke(r.Context(), req.Provider, req.List, *req.Index); {
	case errors.Is(err, issuance.ErrUnknownStatusList), errors.Is(err, issuance.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_entry"})
	case err != nil:
		h.log.Error("revocation failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// failResource writes an error of a protected resource with the given
// authentication scheme: a refused token or DPoP proof is a 401 with a
// challenge of that scheme (RFC 6750 section 3, RFC 9449 section 7.1).
func (h *handler) failResource(w http.ResponseWriter, err error, scheme string) {
	var pe *issuance.Error
	if errors.As(err, &pe) && (pe.Code == oid4vci.ErrInvalidDPoPProof || pe.Code == oid4vci.ErrInvalidToken) {
		w.Header().Set("WWW-Authenticate", scheme+` error="`+pe.Code+`"`)
		h.log.Info("request refused", "error", pe.Code, "error_description", pe.Description)
		writeJSON(w, http.StatusUnauthorized, oid4vci.ErrorResponse{Error: pe.Code, Description: description(pe.Description)})
		return
	}
	h.fail(w, err)
}

// fail writes a protocol error with the status its code implies.
func (h *handler) fail(w http.ResponseWriter, err error) {
	var pe *issuance.Error
	switch {
	case errors.As(err, &pe):
		// 400 by default, also for use_attestation_challenge and
		// use_fresh_attestation (attestation-based client auth draft -12
		// sections 6.1 and 7.4).
		status := http.StatusBadRequest
		switch pe.Code {
		case oid4vci.ErrInvalidClientAttestation, oid4vci.ErrInvalidToken:
			status = http.StatusUnauthorized
		case oid4vci.ErrServerError:
			status = http.StatusInternalServerError
		}
		if pe.Challenge != "" {
			w.Header().Set(clientattest.ChallengeHeader, pe.Challenge)
			w.Header().Set("Cache-Control", "no-store")
		}
		h.log.Info("request refused", "error", pe.Code, "error_description", pe.Description)
		writeJSON(w, status, oid4vci.ErrorResponse{Error: pe.Code, Description: description(pe.Description)})
	case errors.Is(err, issuance.ErrUnknownProvider), errors.Is(err, issuance.ErrUnknownProfile),
		errors.Is(err, issuance.ErrUnknownStatusList):
		http.NotFound(w, nil)
	default:
		h.log.Error("request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, oid4vci.ErrorResponse{Error: oid4vci.ErrServerError})
	}
}

func (h *handler) disclosureRequest(w http.ResponseWriter, r *http.Request) {
	request, err := h.svc.DisclosureRequest(r.Context(), target(r), r.PathValue("offer"))
	switch {
	case errors.Is(err, issuance.ErrUnknownProvider), errors.Is(err, issuance.ErrUnknownProfile), errors.Is(err, issuance.ErrUnknownOffer):
		http.NotFound(w, r)
		return
	case errors.Is(err, issuance.ErrPIDTrustUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, oid4vci.ErrorResponse{Error: oid4vci.ErrServerError})
		return
	case err != nil:
		h.log.Error("disclosure request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, oid4vci.ErrorResponse{Error: oid4vci.ErrServerError})
		return
	}
	w.Header().Set("Content-Type", "application/oauth-authz-req+jwt")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, request)
}

// disclosureOffers lists the fixed links of the disclosure offers. They are
// public, so any page may read them (a frontend that shows the QR codes).
func (h *handler) disclosureOffers(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, h.svc.DisclosureOfferLinks())
}

// disclosureResponse answers the wallet's direct_post.jwt: the
// redirect_uri with the Credential Offer, {} after a wallet error, the
// redirect_uri of the no-data page on the eudi profile, or an error object
// the wallet shows (no_issuable_attestations as the NL Wallet expects it).
func (h *handler) disclosureResponse(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, oid4vci.ErrorResponse{Error: oid4vci.ErrInvalidRequest})
		return
	}
	form := issuance.DisclosureForm{Response: r.PostForm.Get("response"), Error: r.PostForm.Get("error"), State: r.PostForm.Get("state")}
	redirect, err := h.svc.DisclosureResponse(r.Context(), target(r), r.PathValue("session"), form)
	w.Header().Set("Cache-Control", "no-store")
	var pe *issuance.Error
	var noData *issuance.NoDataError
	switch {
	case err == nil && redirect == "":
		h.log.Info("disclosure ended by the wallet", "error", description(form.Error))
		writeJSON(w, http.StatusOK, struct{}{})
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"redirect_uri": redirect})
	case errors.Is(err, issuance.ErrPIDTrustUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, oid4vci.ErrorResponse{Error: oid4vci.ErrServerError})
	case errors.As(err, &pe):
		h.log.Info("disclosure refused", "error", pe.Code, "error_description", pe.Description)
		writeJSON(w, http.StatusBadRequest, oid4vci.ErrorResponse{Error: pe.Code, Description: description(pe.Description)})
	case errors.As(err, &noData):
		h.log.Info("disclosure refused", "error", "no_data", "page", noData.Page)
		writeJSON(w, http.StatusOK, map[string]string{"redirect_uri": noData.Page})
	case errors.Is(err, issuance.ErrNoData):
		h.log.Info("disclosure refused", "error", errNoIssuableAttestations)
		writeJSON(w, http.StatusNotFound, oid4vci.ErrorResponse{Error: errNoIssuableAttestations})
	case errors.Is(err, issuance.ErrUnknownProvider), errors.Is(err, issuance.ErrUnknownProfile):
		// The NL Wallet reads a JSON error body from every refusal.
		writeJSON(w, http.StatusNotFound, oid4vci.ErrorResponse{Error: oid4vci.ErrInvalidRequest})
	default:
		// An unreachable source or PID status list, or unusable source
		// data: the wallet may try again with a new scan.
		h.log.Error("disclosure failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, oid4vci.ErrorResponse{Error: oid4vci.ErrServerError})
	}
}

// noDataPage tells the citizen, in the browser an EUDI wallet opens, that
// the source has no data for them. It shows no personal data: the URL
// names the offer only.
func (h *handler) noDataPage(w http.ResponseWriter, r *http.Request) {
	page, err := h.svc.NoDataPage(target(r), r.PathValue("offer"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := noDataTemplate.Execute(w, page); err != nil {
		h.log.Error("no-data page failed", "error", err)
	}
}

var noDataTemplate = template.Must(template.New("no-data").Parse(`<!doctype html>
<html lang="nl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Geen gegevens gevonden</title>
<style>
body { font-family: system-ui, sans-serif; margin: 0; padding: 2rem 1rem; background: #f5f7fa; color: #1a1a1a; }
main { max-width: 32rem; margin: 0 auto; background: #fff; border-radius: 8px; padding: 1.5rem; }
h1 { font-size: 1.4rem; margin-top: 0; }
</style>
</head>
<body>
<main>
<h1>Geen gegevens gevonden</h1>
<p>{{.Organization}} heeft geen gegevens voor u voor: <strong>{{.Attestation}}</strong>.</p>
<p>Er is niets aan uw wallet toegevoegd. U kunt dit venster sluiten.</p>
</main>
</body>
</html>
`))

// errNoIssuableAttestations is the error the NL Wallet reads as "nothing
// to issue" after a disclosure.
const errNoIssuableAttestations = "no_issuable_attestations"

// description keeps only the characters RFC 6749 section 5.2 allows in
// error_description: printable ASCII without the double quote and the
// backslash.
func description(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, s)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// accepts reports whether the Accept header lists media type t.
func accepts(r *http.Request, t string) bool {
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || mt != t {
			continue
		}
		if q, ok := params["q"]; ok {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v == 0 {
				continue
			}
		}
		return true
	}
	return false
}
