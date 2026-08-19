// Package httpapi exposes the DvTP onboarding use cases over HTTP.
package httpapi

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"dvtp-onboarding-register/internal/onboarding"
)

//go:embed web/*
var webFiles embed.FS

type pageData struct {
	Participants []onboarding.Participant
	Sources      []onboarding.Source
	Editing      *onboarding.Participant
	CSRFToken    string
	Saved        bool
	Error        string
}

func NewHandler(service *onboarding.Service, csrfToken string) http.Handler {
	if csrfToken == "" {
		panic("CSRF token is required")
	}
	sources := service.Sources()
	page := template.Must(template.New("index.html").Funcs(template.FuncMap{
		"sourceName": func(peerID string) string {
			for _, source := range sources {
				if source.PeerID == peerID {
					return source.Name
				}
			}
			return peerID
		},
		"containsSource": func(sourcePeerIDs []string, wanted string) bool {
			for _, sourcePeerID := range sourcePeerIDs {
				if sourcePeerID == wanted {
					return true
				}
			}
			return false
		},
	}).ParseFS(webFiles, "web/index.html"))
	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /internal/openftv/participants", func(w http.ResponseWriter, r *http.Request) {
		participants, err := service.ListForPolicy(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "register unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, participants)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		participants, err := service.List(r.Context())
		if err != nil {
			http.Error(w, "register unavailable", http.StatusInternalServerError)
			return
		}
		var editing *onboarding.Participant
		if editPeerID := r.URL.Query().Get("edit"); editPeerID != "" {
			for index := range participants {
				if participants[index].PeerID == editPeerID {
					editing = &participants[index]
					break
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		setBrowserSecurityHeaders(w)
		_ = page.Execute(w, pageData{
			Participants: participants,
			Sources:      sources,
			Editing:      editing,
			CSRFToken:    csrfToken,
			Saved:        r.URL.Query().Get("saved") == "1",
			Error:        r.URL.Query().Get("error"),
		})
	})
	mux.HandleFunc("POST /participants/{peer_id}", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			redirectError(w, r, "Formulier kon niet worden gelezen")
			return
		}
		if !validCSRFRequest(r, csrfToken) {
			slog.Warn("rejected cross-site participant mutation", "origin", r.Header.Get("Origin"), "fetch_site", r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		found, err := service.UpdateDetails(r.Context(), r.PathValue("peer_id"), r.FormValue("name"), r.Form["source_peer_ids"])
		if err != nil {
			redirectError(w, r, err.Error())
			return
		}
		if !found {
			redirectError(w, r, "Deelnemer niet gevonden")
			return
		}
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /participants", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			redirectError(w, r, "Formulier kon niet worden gelezen")
			return
		}
		if !validCSRFRequest(r, csrfToken) {
			slog.Warn("rejected cross-site participant mutation", "origin", r.Header.Get("Origin"), "fetch_site", r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		participant := onboarding.Participant{
			PeerID:               r.FormValue("peer_id"),
			Name:                 r.FormValue("name"),
			Active:               r.FormValue("active") == "on",
			AllowedSourcePeerIDs: r.Form["source_peer_ids"],
		}
		if err := service.Save(r.Context(), participant); err != nil {
			redirectError(w, r, err.Error())
			return
		}
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /participants/{peer_id}/toggle", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			redirectError(w, r, "Formulier kon niet worden gelezen")
			return
		}
		if !validCSRFRequest(r, csrfToken) {
			slog.Warn("rejected cross-site participant mutation", "origin", r.Header.Get("Origin"), "fetch_site", r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		found, err := service.ToggleActive(r.Context(), r.PathValue("peer_id"))
		if err != nil {
			slog.Error("toggling participant", "peer_id", r.PathValue("peer_id"), "err", err)
			http.Error(w, "register unavailable", http.StatusInternalServerError)
			return
		}
		if !found {
			redirectError(w, r, "Deelnemer niet gevonden")
			return
		}
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
	})
	return withAccessLog(mux)
}

func validCSRFRequest(r *http.Request, expectedToken string) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host != r.Host {
		return false
	}
	if fetchSite := r.Header.Get("Sec-Fetch-Site"); fetchSite != "" && fetchSite != "same-origin" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(expectedToken)) == 1
}

func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func setBrowserSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func redirectError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/?error="+url.QueryEscape(message), http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
