// Package issuancecallback adapts the issuance-server's HTTPS callback to the
// transport-independent pending-attestation store.
package issuancecallback

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/login"
)

const attestationPath = "/attestations/"

// Handler exposes only the callback and a health check on the internal TLS
// listener. Unknown paths retain net/http's 404/405 behaviour.
func Handler(store *issuance.Store, callbackToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path(callbackToken), func(w http.ResponseWriter, r *http.Request) { attestations(store, w, r) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// Path is the unguessable callback path shared through the mounted secret.
func Path(callbackToken string) string { return attestationPath + callbackToken }

// attestations serves both issuers. eudi-issuance-server posts the PID the
// wallet disclosed, an array: the result of the same citizen is issued. The
// own credential issuer posts {"subject": {"qtsp_request": ref}} when the
// QTSP asked it for an offer: the result with that reference is issued, and
// an unknown, used or expired one is 404, which the issuer reports as no
// data.
func attestations(store *issuance.Store, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var raw json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&raw); err != nil {
		http.Error(w, "invalid disclosure", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid disclosure", http.StatusBadRequest)
		return
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '{' {
		var offered offerRequest
		if err := json.Unmarshal(raw, &offered); err != nil || offered.Subject.QTSPRequest == "" {
			http.Error(w, "invalid offer request", http.StatusBadRequest)
			return
		}
		document := store.TakeRef(offered.Subject.QTSPRequest)
		if document == nil {
			http.Error(w, "no pending result", http.StatusNotFound)
			return
		}
		writeDocuments(w, []issuance.Document{*document})
		return
	}
	var disclosed disclosureRequest
	if err := json.Unmarshal(raw, &disclosed); err != nil {
		http.Error(w, "invalid disclosure", http.StatusBadRequest)
		return
	}
	identity, ok := disclosed.identity()
	if !ok {
		http.Error(w, "invalid PID disclosure", http.StatusBadRequest)
		return
	}
	var documents []issuance.Document
	if document := store.Take(identity); document != nil {
		documents = append(documents, *document)
	}
	writeDocuments(w, documents)
}

func writeDocuments(w http.ResponseWriter, documents []issuance.Document) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if documents == nil {
		documents = []issuance.Document{}
	}
	_ = json.NewEncoder(w).Encode(documents)
}

// offerRequest is what the own credential issuer posts for an offer the
// QTSP made: the subject the QTSP named, its reference to the result.
type offerRequest struct {
	Subject struct {
		QTSPRequest string `json:"qtsp_request"`
	} `json:"subject"`
}

type disclosureRequest []struct {
	Attestations []struct {
		AttestationType string         `json:"attestation_type"`
		Attributes      map[string]any `json:"attributes"`
	} `json:"attestations"`
}

func (request disclosureRequest) identity() (login.Identity, bool) {
	for _, disclosed := range request {
		for _, attestation := range disclosed.Attestations {
			if attestation.AttestationType != "urn:eudi:pid:nl:1" {
				continue
			}
			attributes := attestation.Attributes
			if nested, ok := attributes["urn:eudi:pid:nl:1"].(map[string]any); ok {
				attributes = nested
			}
			identity := login.Identity{
				FamilyName: stringValue(attributes["family_name"]), GivenName: stringValue(attributes["given_name"]),
				Birthdate: stringValue(attributes["birthdate"]), Nationalities: stringSlice(attributes["nationalities"]),
			}
			if identity.FamilyName != "" && identity.GivenName != "" && identity.Birthdate != "" && len(identity.Nationalities) > 0 {
				return identity, true
			}
		}
	}
	return login.Identity{}, false
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func stringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil
		}
		result = append(result, text)
	}
	return result
}
