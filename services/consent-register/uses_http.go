package main

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"consent-register/uses"
)

// consentUsesView is the name of the processing that shows a citizen when a
// consent was used. It is logged under the existing inzage activity: showing
// the uses is part of viewing the consent.
const consentUsesView = "dataverwerking.toestemming-gebruik-inzage"

type useJSON struct {
	UsedAt             string `json:"used_at"`
	ServiceProviderOIN string `json:"service_provider_oin"`
	ServiceProvider    string `json:"service_provider"`
	Source             string `json:"source"`
	LogbookID          string `json:"logbook_id"`
}

type noticeJSON struct {
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
}

// handleConsentUses serves GET /consents/{id}/uses: one use per delivery the
// index holds for the consent, newest first, and a notice when the index
// cannot be reached (D2). The register does not check who asks; the portal
// backend checks that the consent is the caller's before it calls this.
func handleConsentUses(store ConsentStore, logbook *registerLogbook, service uses.Service) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, consentID string) {
		start := time.Now().UTC()
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		c, found, err := store.Get(r.Context(), consentID)
		if err != nil {
			slog.Error("get consent", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not get consent"})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})
			return
		}

		report := service.For(r.Context(), c.ConsentID, c.DienstverlenrOIN)
		if report.IndexErr != nil {
			slog.Warn("the consent usage index did not answer; the uses are shown as unavailable", "err", report.IndexErr.Error())
		}

		// No trace IDs in the record: a log of which traces a person looked
		// at would re-create the link the index avoids (TransparantieApp).
		if err := logbook.logConsentOperation(r.Context(), store, r,
			consentListActivity, consentUsesView,
			c.SubjectRef, start, http.StatusOK,
			map[string]any{
				"dpl.gbo.consentId": c.ConsentID,
				"dpl.gbo.useCount":  len(report.Uses),
			}); err != nil {
			refuseUnlogged(w)
			return
		}

		out := struct {
			ConsentID string       `json:"consent_id"`
			Uses      []useJSON    `json:"uses"`
			Notices   []noticeJSON `json:"notices"`
		}{ConsentID: c.ConsentID, Uses: []useJSON{}, Notices: []noticeJSON{}}
		for _, use := range report.Uses {
			out.Uses = append(out.Uses, useJSON{
				UsedAt:             use.UsedAt.UTC().Format(time.RFC3339Nano),
				ServiceProviderOIN: use.ServiceProviderOIN,
				ServiceProvider:    use.ServiceProvider,
				Source:             use.Source,
				LogbookID:          use.LogbookID,
			})
		}
		for _, notice := range report.Notices {
			out.Notices = append(out.Notices, noticeJSON(notice))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// parseSources reads CONSENT_USAGE_SOURCES and
// CONSENT_USAGE_SERVICE_PROVIDERS: key=name pairs separated by semicolons,
// for example "99999999900000000200/bd=Belastingdienst".
func parseSources(value string) map[string]string {
	sources := map[string]string{}
	for _, pair := range strings.Split(value, ";") {
		logbookID, name, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(logbookID) != "" && strings.TrimSpace(name) != "" {
			sources[strings.TrimSpace(logbookID)] = strings.TrimSpace(name)
		}
	}
	return sources
}
