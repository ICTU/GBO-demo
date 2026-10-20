// Package issuancecallback adapts the issuance-server's HTTPS callback to the
// transport-independent pending-attestation store.
package issuancecallback

import (
	"encoding/json"
	"io"
	"net/http"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/login"
)

const AttestationPath = "/attestations"

// Handler exposes only the callback and a health check on the internal TLS
// listener. Unknown paths retain net/http's 404/405 behaviour.
func Handler(store *issuance.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+AttestationPath, func(w http.ResponseWriter, r *http.Request) { attestations(store, w, r) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func attestations(store *issuance.Store, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var disclosed disclosureRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&disclosed); err != nil {
		http.Error(w, "invalid disclosure", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid disclosure", http.StatusBadRequest)
		return
	}
	identity, ok := disclosed.identity()
	if !ok {
		http.Error(w, "invalid PID disclosure", http.StatusBadRequest)
		return
	}
	document := store.Take(identity)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if document == nil {
		_, _ = w.Write([]byte("[]\n"))
		return
	}
	_ = json.NewEncoder(w).Encode([]issuance.Document{*document})
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
