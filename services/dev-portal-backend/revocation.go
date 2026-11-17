package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gbo-demo/dev-portal-backend/internal/revocation"
	"gbo-demo/dev-portal-backend/internal/testpid"
)

// ── Revocation: offers with a reference, their revocation, status lists ─
//
//	GET  /revocation/offers         → the offers the portal made
//	POST /revocation/offers         {source_id, key, bsn} → 201 {link, offer}
//	POST /revocation/revoke         {reference} → 200 {offer}
//	POST /revocation/revoke-entry   {provider, list, index} → 204
//	GET  /revocation/status-lists   → the status lists of those offers
//
// Without CREDENTIAL_ISSUER_INTERNAL_URL none of these exist, so the portal
// hides its panel. Errors are {error: <code>}.

var revocationStatus = map[string]int{
	string(revocation.ErrInvalidRequest):     http.StatusBadRequest,
	string(revocation.ErrUnknownOffer):       http.StatusBadRequest,
	string(revocation.ErrUnknownReference):   http.StatusNotFound,
	string(revocation.ErrUnknownEntry):       http.StatusNotFound,
	string(revocation.ErrNotConfigured):      http.StatusServiceUnavailable,
	string(revocation.ErrCatalogUnavailable): http.StatusBadGateway,
	string(testpid.ErrNoData):                http.StatusNotFound,
	string(testpid.ErrSourceUnavailable):     http.StatusServiceUnavailable,
	string(testpid.ErrIssuerUnavailable):     http.StatusBadGateway,
	string(testpid.ErrBadSourceData):         http.StatusBadGateway,
	string(testpid.ErrIssuer):                http.StatusBadGateway,
}

// revocationCode is the code of err for the portal.
func revocationCode(err error) string {
	var own revocation.Error
	if errors.As(err, &own) {
		return string(own)
	}
	var why testpid.Error
	if errors.As(err, &why) {
		return string(why)
	}
	return string(testpid.ErrIssuer)
}

func writeRevocationError(w http.ResponseWriter, msg string, err error) {
	code := revocationCode(err)
	status, ok := revocationStatus[code]
	if !ok {
		status = http.StatusBadGateway
	}
	slog.Warn(msg, "reason", code, "err", err.Error())
	writeJSON(w, status, map[string]string{"error": code})
}

// decodeRevocationRequest reads a small JSON body into v; false after
// answering invalid_request.
func decodeRevocationRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": string(revocation.ErrInvalidRequest)})
		return false
	}
	return true
}

func registerRevocation(mux *http.ServeMux, svc *revocation.Service) {
	if svc == nil {
		return
	}
	mux.HandleFunc("GET /revocation/offers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, svc.Offers())
	})
	mux.HandleFunc("POST /revocation/offers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SourceID string `json:"source_id"`
			Key      string `json:"key"`
			BSN      string `json:"bsn"`
		}
		if !decodeRevocationRequest(w, r, &req) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		offer, link, err := svc.OfferAttestation(r.Context(), req.SourceID, req.Key, req.BSN)
		if err != nil {
			writeRevocationError(w, "no attestation offer", err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"link": link, "offer": offer})
	})
	mux.HandleFunc("POST /revocation/revoke", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reference string `json:"reference"`
		}
		if !decodeRevocationRequest(w, r, &req) {
			return
		}
		offer, err := svc.RevokeOffer(r.Context(), req.Reference)
		if err != nil {
			writeRevocationError(w, "no revocation", err)
			return
		}
		slog.Info("offer revoked", "reference", offer.Reference, "entries", offer.RevokedEntries)
		writeJSON(w, http.StatusOK, map[string]any{"offer": offer})
	})
	mux.HandleFunc("POST /revocation/revoke-entry", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Provider string `json:"provider"`
			List     string `json:"list"`
			Index    *int   `json:"index"`
		}
		if !decodeRevocationRequest(w, r, &req) {
			return
		}
		if req.Index == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": string(revocation.ErrInvalidRequest)})
			return
		}
		if err := svc.RevokeEntry(r.Context(), req.Provider, req.List, *req.Index); err != nil {
			writeRevocationError(w, "no revocation", err)
			return
		}
		slog.Info("status list entry revoked", "provider", req.Provider, "list", req.List, "index", *req.Index)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /revocation/status-lists", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, svc.StatusLists(r.Context()))
	})
}
