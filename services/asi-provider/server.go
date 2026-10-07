package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// provider is the provider object of the ETSI dataservice schema
// (19478-dataservice-schema.json#/definitions/provider).
type provider struct {
	LegalName string `json:"legalName"`
}

type serverConfig struct {
	Source    *memorySource
	Catalogue catalogue
	Sealer    *sealer
	// Tokens maps a stub bearer token to the BSN of a test person. Slice 2
	// replaces this with access tokens from the authorization server.
	Tokens          map[string]string
	Provider        provider
	AuthenticSource provider
}

type server struct{ cfg serverConfig }

// basePath is the default basePath of the server template in ETSI TS 119 478
// Annex B.
const basePath = "/authsrc-api"

func newServer(cfg serverConfig) http.Handler {
	s := &server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+basePath+"/verify", s.authenticated(s.verify))
	mux.HandleFunc("POST "+basePath+"/retrieve", s.authenticated(s.retrieve))
	mux.HandleFunc("GET /openapi.json", serveJSON(publishedContract))
	mux.HandleFunc("GET /"+dataserviceFileName, serveJSON(mustReadOpenAPIFile(dataserviceFile)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func serveJSON(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
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
	Provider                     provider                      `json:"provider"`
	AuthenticSource              provider                      `json:"authenticSource"`
}

type retrieveRequest struct {
	AttributeIdentifiers []struct {
		AttributeIdentifier string `json:"attributeIdentifier"`
	} `json:"attributeIdentifiers"`
	Mandate json.RawMessage `json:"mandate"`
}

type retrieveResponse struct {
	Attributes      []attribute `json:"attributes"`
	Provider        provider    `json:"provider"`
	AuthenticSource provider    `json:"authenticSource"`
}

// --- authentication (stub for slice 1) ---------------------------------------

type handlerWithSubject func(w http.ResponseWriter, r *http.Request, bsn string)

func (s *server) authenticated(next handlerWithSubject) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		bsn, known := s.cfg.Tokens[token]
		if !ok || token == "" || !known {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid access token is required")
			return
		}
		next(w, r, bsn)
	}
}

// --- /verify -------------------------------------------------------------------

func (s *server) verify(w http.ResponseWriter, r *http.Request, bsn string) {
	var req verifyRequest
	if err := decodeStrict(r.Body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := validateVerifyRequest(req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if hasMandate(req.Mandate) {
		writeProblem(w, http.StatusNotImplemented, "Not Implemented", "requests on behalf of another person (mandate) are not supported")
		return
	}
	for _, id := range requestedIdentifiers(req) {
		if _, ok := s.cfg.Catalogue.lookup(id); !ok {
			writeProblem(w, http.StatusNotFound, "Not Found", "unknown attribute identifier: "+id)
			return
		}
	}

	resp := verifyResponse{Provider: s.cfg.Provider, AuthenticSource: s.cfg.AuthenticSource}
	for _, a := range req.Attributes {
		resp.AttributeVerificationResults = append(resp.AttributeVerificationResults, attributeVerificationResult{
			AttributeIdentifier:         a.AttributeIdentifier,
			AttributeVerificationResult: s.verifyAttribute(bsn, a),
		})
	}
	for _, f := range req.AttributeFragments {
		result, err := s.verifyFragment(bsn, f)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		resp.FragmentVerificationResults = append(resp.FragmentVerificationResults, fragmentVerificationResult{
			AttributeIdentifier:        f.AttributeIdentifier,
			FragmentVerificationResult: result,
		})
	}
	s.writeSealed(w, resp)
}

// verifyAttribute never returns the stored value: Match and
// MatchWithVariation carry no attributeValue in this mock.
func (s *server) verifyAttribute(bsn string, a attribute) string {
	held, ok := s.heldValue(bsn, a.AttributeIdentifier)
	if !ok {
		return resultURIUnknown
	}
	var claimed any
	_ = json.Unmarshal(a.AttributeValue, &claimed)
	return compareValues(claimed, held)
}

// verifyFragment compares a fragment. The ETSI schema requires the fragment
// value to be an object; this mock reads it as {"<last member name>": value},
// for example location "$.city" with value {"city": "Rotterdam"}.
func (s *server) verifyFragment(bsn string, f attributeFragment) (string, error) {
	held, ok := s.heldValue(bsn, f.AttributeIdentifier)
	if !ok {
		return resultURIUnknown, nil
	}
	selected, member, err := jsonPath(held, f.Location)
	if errors.Is(err, errPathEmpty) {
		return resultURIUnknown, nil
	}
	if err != nil {
		return "", err
	}
	var claimed map[string]any
	_ = json.Unmarshal(f.Value, &claimed)
	if member == "" {
		return "", errors.New("a fragment location must end in a member name")
	}
	value, ok := claimed[member]
	if !ok || len(claimed) != 1 {
		return "", errors.New(`fragment value must be an object with the single member "` + member + `"`)
	}
	return compareValues(value, selected), nil
}

// heldValue returns the decoded value the source holds, and false when there
// is no authentic source data for it: the attribute is not ours, or the source
// holds no value for this person. Both yield Unknown
// (ETSI TS 119 478 REQ-ASIP-6.1.1.2-04).
func (s *server) heldValue(bsn, id string) (any, bool) {
	entry, _ := s.cfg.Catalogue.lookup(id)
	if !entry.Ours {
		return nil, false
	}
	raw, ok := s.cfg.Source.value(bsn, entry.Name)
	if !ok {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

// --- /retrieve -----------------------------------------------------------------

func (s *server) retrieve(w http.ResponseWriter, r *http.Request, bsn string) {
	var req retrieveRequest
	if err := decodeStrict(r.Body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if len(req.AttributeIdentifiers) == 0 {
		writeProblem(w, http.StatusBadRequest, "Bad Request", "attributeIdentifiers must contain at least one attribute")
		return
	}
	if hasMandate(req.Mandate) {
		writeProblem(w, http.StatusNotImplemented, "Not Implemented", "requests on behalf of another person (mandate) are not supported")
		return
	}
	resp := retrieveResponse{Attributes: []attribute{}, Provider: s.cfg.Provider, AuthenticSource: s.cfg.AuthenticSource}
	for _, a := range req.AttributeIdentifiers {
		entry, ok := s.cfg.Catalogue.lookup(a.AttributeIdentifier)
		if !ok {
			writeProblem(w, http.StatusNotFound, "Not Found", "unknown attribute identifier: "+a.AttributeIdentifier)
			return
		}
		if !entry.Ours {
			continue
		}
		if raw, ok := s.cfg.Source.value(bsn, entry.Name); ok {
			resp.Attributes = append(resp.Attributes, attribute{AttributeIdentifier: a.AttributeIdentifier, AttributeValue: raw})
		}
	}
	s.writeSealed(w, resp)
}

// --- helpers -------------------------------------------------------------------

func decodeStrict(body io.Reader, v any) error {
	dec := json.NewDecoder(io.LimitReader(body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("request body is not valid JSON for this operation: " + err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body contains data after the JSON object")
	}
	return nil
}

// validateVerifyRequest enforces the required members of the ETSI schema and
// REQ-ASIP-6.1.1.1-05 (attributes or attributeFragments, or both).
func validateVerifyRequest(req verifyRequest) error {
	if len(req.Attributes) == 0 && len(req.AttributeFragments) == 0 {
		return errors.New("verifyRequest must contain attributes or attributeFragments")
	}
	for _, a := range req.Attributes {
		if a.AttributeIdentifier == "" {
			return errors.New("attribute without attributeIdentifier")
		}
		if !isJSONObject(a.AttributeValue) {
			return errors.New("attributeValue of " + a.AttributeIdentifier + " must be a JSON object")
		}
	}
	for _, f := range req.AttributeFragments {
		if f.AttributeIdentifier == "" || f.Location == "" {
			return errors.New("attributeFragment requires attributeIdentifier and location")
		}
		if !isJSONObject(f.Value) {
			return errors.New("value of fragment " + f.Location + " must be a JSON object")
		}
	}
	return nil
}

func requestedIdentifiers(req verifyRequest) []string {
	var ids []string
	for _, a := range req.Attributes {
		ids = append(ids, a.AttributeIdentifier)
	}
	for _, f := range req.AttributeFragments {
		ids = append(ids, f.AttributeIdentifier)
	}
	return ids
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var m map[string]any
	return json.Unmarshal(trimmed, &m) == nil
}

func hasMandate(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func (s *server) writeSealed(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "could not encode the response")
		return
	}
	sig, err := s.cfg.Sealer.seal(body)
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
