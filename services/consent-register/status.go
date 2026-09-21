package main

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	ldv "gbo-demo/ldv-client"
)

// The status lookup has a listener of its own, and it is the only thing that
// listener serves. It is published as an FSC service: the PDP of a source
// reaches it through its Outway, the register's Inway authenticates the peer
// and checks the contract. An Inway forwards every path under a service's
// endpoint, so putting the lookup on the portal's listener would have made
// the citizen's consents reachable over FSC too.
//
// The FSC contract is the authorization: any peer with a connection to this
// service may ask the status of a consent whose id it holds. Whether that
// peer is the PDP of the source the consent is for is not checked here; the
// id comes from a signed token, and the answer is a single status (#383).
//
// This listener trusts the Fsc-Authorization header the Inway forwards, so
// it must be reachable only through that Inway.

// fscPeerIDPattern is the 20-character alphanumeric FSC Peer ID.
var fscPeerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// callingPeer is the peer the FSC access token was issued to: the `sub` of
// the token the Inway validated and forwarded. Empty when the request did
// not come through FSC. `iss` is not a fallback: it names the peer that
// issued the token, which is this register's own side.
func callingPeer(r *http.Request) string {
	sub, _ := ldv.Claims(r.Header.Get("Fsc-Authorization"))["sub"].(string)
	if !fscPeerIDPattern.MatchString(sub) {
		return ""
	}
	return sub
}

func newStatusMux(store ConsentStore, logbook *registerLogbook) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/consents/", handleConsentStatus(store, logbook))
	return mux
}

// handleConsentStatus serves GET /consents/{id}/status.
func handleConsentStatus(store ConsentStore, logbook *registerLogbook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now().UTC()

		consentID, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/consents/"), "/status")
		if !ok || consentID == "" || strings.Contains(consentID, "/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if callingPeer(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the status lookup is only available over FSC"})
			return
		}

		c, found, err := store.Get(r.Context(), consentID)
		if err != nil {
			slog.Error("get consent status", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not get consent status"})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})
			return
		}
		// The PDP asks this on every request. Confirming a consent's status
		// is a processing of that Betrokkene's data, and it is the step that
		// makes a revocation take effect — so it is logged like any other,
		// not treated as a read-only lookup. The processor is the calling
		// peer.
		if err := logbook.logConsentOperation(r.Context(), store, r,
			consentStatusActivity, "dataverwerking.toestemming-status",
			c.SubjectRef, start, http.StatusOK,
			map[string]any{
				"dpl.gbo.consentId": c.ConsentID,
			}); err != nil {
			refuseUnlogged(w)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"consent_id": c.ConsentID,
			"status":     c.Status,
		})
	}
}
