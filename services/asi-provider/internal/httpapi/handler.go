// Package httpapi is the HTTP interface of the ASIP: per data service, POST
// /verify and /retrieve of ETSI TS 119 478 clause 6.1.1 and 6.1.2 under
// /<data service>/authsrc-api (the base path of Annex B), and its published
// contract. It validates every request against the contract, has the access
// token checked, calls the verification core, and seals every result.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/getkin/kin-openapi/routers"
	otelattribute "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/seal"
	"gbo-demo/asi-provider/internal/verification"
)

// BasePath is the default basePath of the server template in ETSI TS 119 478
// Annex B.
const BasePath = "/authsrc-api"

// Provider is the provider object of the ETSI dataservice schema
// (19478-dataservice-schema.json#/definitions/provider).
type Provider struct {
	LegalName string `json:"legalName"`
}

// DataService is one authentic source behind the ASIP.
type DataService struct {
	// ID is the path segment of the data service: /<ID>/authsrc-api.
	ID           string
	Verification *verification.Service
	// Tokens validates the access tokens of the authorization server for
	// this data service: its base URL is their audience.
	Tokens   *accesstoken.Validator
	Provider Provider
	// AuthenticSource is nil when the ASIP is the authentic source: the
	// responses then name none (REQ-ASIP-6.1.1.2-08, REQ-ASIP-6.1.2.2-04).
	AuthenticSource *Provider
	// Attributes are the identifiers of the attributes the data service
	// provides, and Country its Member State: its discovery records.
	Attributes []string
	Country    string
}

type Config struct {
	// PublicURL is the scheme and host QTSPs use; with the base path of a
	// data service it is the server of that data service's contract.
	PublicURL string
	// Issuer is the authorization server: the contracts name its endpoints.
	Issuer       string
	Sealer       *seal.Sealer
	DataServices []DataService
}

// BasePathOf is the base path of a data service.
func BasePathOf(id string) string { return "/" + id + BasePath }

type handler struct {
	ds     DataService
	sealer *seal.Sealer
}

func NewHandler(cfg Config) (http.Handler, error) {
	if len(cfg.DataServices) == 0 {
		return nil, errors.New("the ASIP serves no data service")
	}
	doc, err := loadContractDoc()
	if err != nil {
		return nil, fmt.Errorf("published contract: %w", err)
	}
	verifyRoute, err := contractRoute(doc, "/verify")
	if err != nil {
		return nil, err
	}
	retrieveRoute, err := contractRoute(doc, "/retrieve")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	seen := map[string]bool{}
	for _, ds := range cfg.DataServices {
		if seen[ds.ID] {
			return nil, fmt.Errorf("data service %q is named twice", ds.ID)
		}
		seen[ds.ID] = true
		h := &handler{ds: ds, sealer: cfg.Sealer}
		base := BasePathOf(ds.ID)
		published, err := contractFor(cfg.PublicURL+base, cfg.Issuer)
		if err != nil {
			return nil, err
		}
		mux.HandleFunc("POST "+base+"/verify", h.authenticated("verify", conformant(verifyRoute, h.verify)))
		mux.HandleFunc("POST "+base+"/retrieve", h.authenticated("retrieve", conformant(retrieveRoute, h.retrieve)))
		// The endpointDescription of the data service (REQ-DIP-5.3.3-03-02),
		// with the schema it references next to it.
		mux.HandleFunc("GET /"+ds.ID+"/openapi.json", serveJSON(published))
		records, err := json.MarshalIndent(dataServiceRecords(cfg.PublicURL, ds), "", "  ")
		if err != nil {
			return nil, err
		}
		mux.HandleFunc("GET /"+ds.ID+"/dataservices", serveJSON(records))
		mux.HandleFunc("GET /"+ds.ID+"/"+dataserviceFileName, serveJSON(mustReadOpenAPIFile(dataserviceFile)))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux, nil
}

func serveJSON(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// --- discovery records (ETSI TS 119 478 clause 5.3.3) -------------------------

// dataServiceRecord is a dataService of the Data Service Directory
// (19478-dataservice-schema.json#/definitions/dataService): where a QTSP
// verifies or retrieves one attribute.
type dataServiceRecord struct {
	AttributeIdentifier string   `json:"attributeIdentifier"`
	EndpointDescription string   `json:"endpointDescription"`
	EndpointURI         string   `json:"endpointURI"`
	Provider            Provider `json:"provider"`
	Country             string   `json:"country"`
}

// dataServiceRecords are the records of a data service, one per attribute:
// what the ASIP hands to the Data Service Directory. The endpoint is the
// data service's base URL, for verify and retrieve; its description is its
// contract. The provider carries no identifiers or legal basis yet: there is
// no EU identifier type for an OIN, and the demo ASIP has none other
// (REQ-DIP-5.3.3-03-04-04 is a should).
func dataServiceRecords(publicURL string, ds DataService) map[string][]dataServiceRecord {
	records := []dataServiceRecord{}
	for _, id := range ds.Attributes {
		records = append(records, dataServiceRecord{
			AttributeIdentifier: id,
			EndpointDescription: publicURL + "/" + ds.ID + "/openapi.json",
			EndpointURI:         publicURL + BasePathOf(ds.ID),
			Provider:            ds.Provider,
			Country:             ds.Country,
		})
	}
	return map[string][]dataServiceRecord{"dataServices": records}
}

// --- request and response shapes (ETSI TS 119 478 Annex B) ------------------

type attribute struct {
	AttributeIdentifier string          `json:"attributeIdentifier"`
	AttributeValue      json.RawMessage `json:"attributeValue"`
}

type attributeFragment struct {
	AttributeIdentifier string          `json:"attributeIdentifier"`
	Location            string          `json:"location"`
	Value               json.RawMessage `json:"value"`
}

type verifyRequest struct {
	Attributes         []attribute         `json:"attributes"`
	AttributeFragments []attributeFragment `json:"attributeFragments"`
	Mandate            json.RawMessage     `json:"mandate"`
}

type attributeVerificationResult struct {
	AttributeIdentifier         string `json:"attributeIdentifier"`
	AttributeVerificationResult string `json:"attributeVerificationResult"`
}

type fragmentVerificationResult struct {
	AttributeIdentifier        string `json:"attributeIdentifier"`
	FragmentVerificationResult string `json:"fragmentVerificationResult"`
}

type verifyResponse struct {
	AttributeVerificationResults []attributeVerificationResult `json:"attributeVerificationResults,omitempty"`
	FragmentVerificationResults  []fragmentVerificationResult  `json:"fragmentVerificationResults,omitempty"`
	Provider                     Provider                      `json:"provider"`
	AuthenticSource              *Provider                     `json:"authenticSource,omitempty"`
}

type retrieveRequest struct {
	AttributeIdentifiers []struct {
		AttributeIdentifier string `json:"attributeIdentifier"`
	} `json:"attributeIdentifiers"`
	Mandate json.RawMessage `json:"mandate"`
}

type retrieveResponse struct {
	Attributes      []attribute `json:"attributes"`
	Provider        Provider    `json:"provider"`
	AuthenticSource *Provider   `json:"authenticSource,omitempty"`
}

// --- authentication --------------------------------------------------------------

type handlerWithSubject func(w http.ResponseWriter, r *http.Request, bsn string)

// authenticated validates the access token for operation before anything
// else; the BSN of the user comes from the token.
func (h *handler) authenticated(operation string, next handlerWithSubject) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).SetAttributes(otelattribute.String("gbo.asi.data_service", h.ds.ID))
		bsn, failure := h.ds.Tokens.Authenticate(r.Context(), accesstoken.Request{
			Method:        r.Method,
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			DPoP:          r.Header.Values("DPoP"),
		}, operation)
		if failure != nil {
			writeAuthFailure(w, failure)
			return
		}
		next(w, r, bsn)
	}
}

// writeAuthFailure answers with 401, the WWW-Authenticate challenge of
// RFC 9449 section 7.1 and a problem body. Every failure is 401, as ETSI
// TS 119 478 lists for "an authentication or authorization error"
// (REQ-ASIP-6.1.1.2-12), also insufficient_scope, for which RFC 6750 would
// use 403. When no token can be validated at all, the answer is 503.
func writeAuthFailure(w http.ResponseWriter, f *accesstoken.Failure) {
	if f.Unavailable {
		writeProblem(w, http.StatusServiceUnavailable, "Service Unavailable", f.Description)
		return
	}
	challenge := `DPoP algs="ES256"`
	if f.Code != "" {
		challenge = fmt.Sprintf(`DPoP error="%s", error_description="%s", algs="ES256"`, f.Code, f.Description)
		if f.Scope != "" {
			challenge += fmt.Sprintf(`, scope="%s"`, f.Scope)
		}
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeProblem(w, http.StatusUnauthorized, "Unauthorized", f.Description)
}

// conformant rejects a request that does not conform to the request schema of
// the published contract with 400, before any other check of its content.
func conformant(route *routers.Route, next handlerWithSubject) handlerWithSubject {
	return func(w http.ResponseWriter, r *http.Request, bsn string) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		if err := validateRequest(r, route); err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		next(w, r, bsn)
	}
}

// --- /verify -------------------------------------------------------------------

func (h *handler) verify(w http.ResponseWriter, r *http.Request, bsn string) {
	var req verifyRequest
	if err := decodeStrict(r.Body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	in := verification.VerifyRequest{Mandate: hasMandate(req.Mandate)}
	for _, a := range req.Attributes {
		in.Attributes = append(in.Attributes, verification.Attribute{Identifier: a.AttributeIdentifier, Value: a.AttributeValue})
	}
	for _, f := range req.AttributeFragments {
		in.Fragments = append(in.Fragments, verification.Fragment{Identifier: f.AttributeIdentifier, Location: f.Location, Value: f.Value})
	}
	result, err := h.ds.Verification.Verify(r.Context(), bsn, in)
	if err != nil {
		writeUseCaseError(w, err)
		return
	}
	resp := verifyResponse{Provider: h.ds.Provider, AuthenticSource: h.ds.AuthenticSource}
	for _, a := range result.Attributes {
		resp.AttributeVerificationResults = append(resp.AttributeVerificationResults, attributeVerificationResult{
			AttributeIdentifier: a.Identifier, AttributeVerificationResult: a.Result,
		})
	}
	for _, f := range result.Fragments {
		resp.FragmentVerificationResults = append(resp.FragmentVerificationResults, fragmentVerificationResult{
			AttributeIdentifier: f.Identifier, FragmentVerificationResult: f.Result,
		})
	}
	h.writeSealed(w, resp)
}

// --- /retrieve -----------------------------------------------------------------

func (h *handler) retrieve(w http.ResponseWriter, r *http.Request, bsn string) {
	var req retrieveRequest
	if err := decodeStrict(r.Body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ids := make([]string, 0, len(req.AttributeIdentifiers))
	for _, a := range req.AttributeIdentifiers {
		ids = append(ids, a.AttributeIdentifier)
	}
	attrs, err := h.ds.Verification.Retrieve(r.Context(), bsn, ids, hasMandate(req.Mandate))
	if err != nil {
		writeUseCaseError(w, err)
		return
	}
	resp := retrieveResponse{Attributes: []attribute{}, Provider: h.ds.Provider, AuthenticSource: h.ds.AuthenticSource}
	for _, a := range attrs {
		resp.Attributes = append(resp.Attributes, attribute{AttributeIdentifier: a.Identifier, AttributeValue: a.Value})
	}
	h.writeSealed(w, resp)
}

// --- helpers -------------------------------------------------------------------

const maxRequestBody = 1 << 20

// writeUseCaseError maps an error of the verification core to the status
// codes of REQ-ASIP-6.1.1.2-12, plus 503 for a source that cannot be reached
// and 500 for one that refuses: neither is a missing value (Unknown). The
// errors name no BSN, so they can be logged.
func writeUseCaseError(w http.ResponseWriter, err error) {
	var malformed *verification.MalformedError
	var unknown *verification.UnknownAttributeError
	switch {
	case errors.As(err, &malformed):
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
	case errors.Is(err, verification.ErrMandate):
		writeProblem(w, http.StatusNotImplemented, "Not Implemented", err.Error())
	case errors.As(err, &unknown):
		writeProblem(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, verification.ErrSourceUnavailable):
		slog.Warn("authentic source unavailable", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Service Unavailable", "the authentic source is unavailable")
	default:
		slog.Error("authentic source query failed", "err", err)
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "the request could not be processed")
	}
}

func decodeStrict(body io.Reader, v any) error {
	dec := json.NewDecoder(io.LimitReader(body, maxRequestBody))
	if err := dec.Decode(v); err != nil {
		return errors.New("request body is not valid JSON for this operation: " + err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body contains data after the JSON object")
	}
	return nil
}

func hasMandate(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func (h *handler) writeSealed(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "could not encode the response")
		return
	}
	sig, err := h.sealer.Seal(body)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "could not seal the response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-JWS-Signature", sig)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeProblem writes an RFC 9457 problem as the ETSI OpenAPI requires.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  title,
		"status": status,
		"detail": detail,
	})
}
