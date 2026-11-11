package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gbo-demo/dev-portal-backend/internal/testpid"
)

// ── Test PID: a pre-authorized offer for an EUDI wallet ─────────────────
//
//	GET  /test-pid/persons  → the test persons
//	POST /test-pid/offers   {bsn} → 201 {link}, or {error: <code>}
//
// Without TEST_PID_OFFERS_URL neither route exists, so the portal hides
// its block. No CORS headers: the portal reaches these through its own
// origin, and another site has no business reading an offer link.

var testPIDStatus = map[testpid.Error]int{
	testpid.ErrUnknownPerson:     http.StatusBadRequest,
	testpid.ErrNoData:            http.StatusNotFound,
	testpid.ErrNotConfigured:     http.StatusServiceUnavailable,
	testpid.ErrSourceUnavailable: http.StatusServiceUnavailable,
	testpid.ErrIssuerUnavailable: http.StatusBadGateway,
	testpid.ErrBadSourceData:     http.StatusBadGateway,
	testpid.ErrIssuer:            http.StatusBadGateway,
}

func registerTestPID(mux *http.ServeMux, svc *testpid.Service) {
	if svc == nil {
		return
	}
	mux.HandleFunc("/test-pid/persons", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, svc.Persons())
	})
	mux.HandleFunc("/test-pid/offers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			BSN string `json:"bsn"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.BSN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		link, err := svc.Offer(r.Context(), req.BSN)
		if err != nil {
			var why testpid.Error
			if !errors.As(err, &why) {
				why = testpid.ErrIssuer
			}
			slog.Warn("no test PID offer", "reason", string(why), "err", err.Error())
			writeJSON(w, testPIDStatus[why], map[string]string{"error": string(why)})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"link": link})
	})
}
