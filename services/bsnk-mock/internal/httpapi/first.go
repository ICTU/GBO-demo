package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"bsnk-mock/internal/legacy"
)

// handlePseudonymize turns a BSN into a PI and a pseudonym for a recipient.
func handlePseudonymize(store *legacy.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, http.MethodPost) {
			return
		}
		var req struct {
			BSN          string `json:"bsn"`
			RecipientOIN string `json:"recipient_oin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.BSN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bsn is required"})
			return
		}
		pi, pseudonym := store.Pseudonymize(req.BSN, req.RecipientOIN)
		writeJSON(w, http.StatusOK, map[string]string{"pseudonym": pseudonym, "pi": pi})
	}
}

// handleResolve turns a PI back into the BSN. It collapses what BSNk does in
// two steps, a transformation and the recipient's own decryption, into one
// answer. recipient_oin is required and not used; it appears in traces.
func handleResolve(store *legacy.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, http.MethodPost) {
			return
		}
		var req struct {
			PI           string `json:"pi"`
			RecipientOIN string `json:"recipient_oin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.PI == "" || req.RecipientOIN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pi and recipient_oin are required"})
			return
		}
		bsn, ok := store.Resolve(req.PI)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("PI not found: %s", req.PI)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"bsn": bsn})
	}
}
