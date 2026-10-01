package httpapi

import (
	"encoding/json"
	"net/http"

	"bsnk-mock/internal/legacy"
)

// handlePseudonymize turns a BSN into a pseudonym for a recipient.
func handlePseudonymize() http.HandlerFunc {
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
		writeJSON(w, http.StatusOK, map[string]string{"pseudonym": legacy.Pseudonym(req.BSN, req.RecipientOIN)})
	}
}
