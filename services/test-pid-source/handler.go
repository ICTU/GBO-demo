package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// maxBody limits request bodies.
const maxBody = 1 << 16

// issuableDocument is one entry of the issuer's IssuableDocument contract.
type issuableDocument struct {
	Format          string         `json:"format"`
	AttestationType string         `json:"attestation_type"`
	Attributes      map[string]any `json:"attributes"`
}

func newHandler(persons map[string]person, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /attestations", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Subject struct {
				BSN string `json:"bsn"`
			} `json:"subject"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil || req.Subject.BSN == "" {
			http.Error(w, `the body must be {"subject": {"bsn": "..."}}`, http.StatusBadRequest)
			return
		}
		// An unknown BSN is an empty list: the issuer answers no_data.
		docs := []issuableDocument{}
		if p, ok := persons[req.Subject.BSN]; ok {
			attrs, err := pidAttributes(p, now())
			if err != nil {
				slog.Error("mapping person", "err", err)
				http.Error(w, "the mock person cannot be mapped", http.StatusInternalServerError)
				return
			}
			docs = append(docs, issuableDocument{Format: "dc+sd-jwt", AttestationType: PIDType, Attributes: attrs})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(docs)
	})
	return mux
}
