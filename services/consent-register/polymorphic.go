package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// handlePolymorphic serves the polymorphic values the portal keeps here, at
// /subjects/{subject_ref}/polymorphic: GET returns them, PUT keeps them.
//
// These are for the portal alone. The browser never calls them, so there are
// no CORS headers. Keeping the values is part of the portal's
// pseudonymisation, which the portal records in the same logbook; the
// register writes no record of its own for it.
func handlePolymorphic(store ConsentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subjectRef, rest, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/subjects/"), "/")
		if !found || rest != "polymorphic" || subjectRef == "" {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodGet:
			values, ok, err := store.PolymorphicFor(r.Context(), subjectRef)
			if err != nil {
				slog.Error("read polymorphic values", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the polymorphic values"})
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no polymorphic values for this subject"})
				return
			}
			writeJSON(w, http.StatusOK, values)
		case http.MethodPut:
			var values Polymorphic
			if err := json.NewDecoder(r.Body).Decode(&values); err != nil || values.PI == "" || values.PP == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pi and pp are required"})
				return
			}
			if err := store.KeepPolymorphic(r.Context(), subjectRef, values); err != nil {
				slog.Error("keep polymorphic values", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not keep the polymorphic values"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}
