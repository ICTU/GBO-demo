package main

import (
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

// ── ASI: the reference QTSP's request API ───────────────────────────────
//
// The ASI tab lays a request down at the reference QTSP and follows its
// outcome; the user still goes through the QTSP and the authorization
// server in a browser. The backend relays three calls to the QTSP on the
// compose network:
//
//	GET  /asi/attributes      → GET  <QTSP>/api/attributes
//	POST /asi/requests        → POST <QTSP>/api/requests
//	GET  /asi/requests/{id}   → GET  <QTSP>/api/requests/{id}
//
// The QTSP's answer, status and body, goes to the portal unchanged.

var asiRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var asiClient = &http.Client{Timeout: 20 * time.Second}

func handleAsi(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var target string
		switch path := r.URL.Path; {
		case path == "/asi/attributes" && r.Method == http.MethodGet:
			target = "/api/attributes"
		case path == "/asi/requests" && r.Method == http.MethodPost:
			target = "/api/requests"
		case len(path) > len("/asi/requests/") && path[:len("/asi/requests/")] == "/asi/requests/" && r.Method == http.MethodGet:
			id := path[len("/asi/requests/"):]
			if !asiRequestID.MatchString(id) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown request"})
				return
			}
			target = "/api/requests/" + id
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if cfg.QtspURL == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no QTSP configured (QTSP_URL)"})
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.QtspURL+target, http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := asiClient.Do(req)
		if err != nil {
			slog.Warn("QTSP unreachable", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the QTSP is unreachable"})
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
	}
}
