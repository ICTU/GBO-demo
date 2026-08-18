// Package httpapi exposes the DvTP onboarding use cases over HTTP.
package httpapi

import (
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
	Saved        bool
	Error        string
}

func NewHandler(service *onboarding.Service) http.Handler {
	sources := service.Sources()
	page := template.Must(template.New("index.html").Funcs(template.FuncMap{
		"sourceName": func(key string) string {
			for _, source := range sources {
				if source.Key == key {
					return source.Name
				}
			}
			return key
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
		participants, err := service.List(r.Context())
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		setBrowserSecurityHeaders(w)
		_ = page.Execute(w, pageData{
			Participants: participants,
			Sources:      sources,
			Saved:        r.URL.Query().Get("saved") == "1",
			Error:        r.URL.Query().Get("error"),
		})
	})
	mux.HandleFunc("POST /participants", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			redirectError(w, r, "Formulier kon niet worden gelezen")
			return
		}
		participant := onboarding.Participant{
			OIN:            r.FormValue("oin"),
			Name:           r.FormValue("name"),
			Active:         r.FormValue("active") == "on",
			AllowedSources: r.Form["sources"],
		}
		if err := service.Save(r.Context(), participant); err != nil {
			redirectError(w, r, err.Error())
			return
		}
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /participants/{oin}/toggle", func(w http.ResponseWriter, r *http.Request) {
		found, err := service.ToggleActive(r.Context(), r.PathValue("oin"))
		if err != nil || !found {
			redirectError(w, r, "Deelnemer niet gevonden")
			return
		}
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
	})
	return withAccessLog(mux)
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
	w.Header().Set("Referrer-Policy", "no-referrer")
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
