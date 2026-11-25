package democontrol

import (
	"encoding/json"
	"errors"
	"net/http"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// reusePolicy is issuance.ReusePolicy in JSON.
type reusePolicy struct {
	Method               string `json:"method"`
	BatchSize            int    `json:"batch_size"`
	ReissueTriggerUnused int    `json:"reissue_trigger_unused"`
}

func fromPolicy(p issuance.ReusePolicy) *reusePolicy {
	return &reusePolicy{Method: p.Method, BatchSize: p.BatchSize, ReissueTriggerUnused: p.ReissueTriggerUnused}
}

// maxBody limits request bodies.
const maxBody = 1 << 16

// Handler returns the settings and timeline API for the internal port,
// under /demo/. Like the rest of the internal port it has no
// authentication of its own; only the developer portal reaches it.
func Handler(o *Overrides, events *EventLog) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo/events", func(w http.ResponseWriter, r *http.Request) {
		list, err := events.Events(r.Context())
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "events_unavailable", "the timeline could not be read")
			return
		}
		if list == nil {
			list = []issuance.Event{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": list})
	})
	mux.HandleFunc("GET /demo/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, o.View())
	})
	mux.HandleFunc("PUT /demo/settings/providers/{provider}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BatchSize *int `json:"batch_size"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.BatchSize == nil {
			fail(w, http.StatusBadRequest, "invalid_setting", "batch_size is required")
			return
		}
		answer(w, o, o.SetBatchSize(r.PathValue("provider"), *body.BatchSize))
	})
	mux.HandleFunc("DELETE /demo/settings/providers/{provider}", func(w http.ResponseWriter, r *http.Request) {
		answer(w, o, o.ResetBatchSize(r.PathValue("provider")))
	})
	mux.HandleFunc("PUT /demo/settings/configurations/{configuration}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReusePolicy *reusePolicy `json:"reuse_policy"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.ReusePolicy == nil {
			fail(w, http.StatusBadRequest, "invalid_setting", "reuse_policy is required")
			return
		}
		p := issuance.ReusePolicy{Method: body.ReusePolicy.Method, BatchSize: body.ReusePolicy.BatchSize,
			ReissueTriggerUnused: body.ReusePolicy.ReissueTriggerUnused}
		answer(w, o, o.SetReusePolicy(r.PathValue("configuration"), p))
	})
	mux.HandleFunc("DELETE /demo/settings/configurations/{configuration}", func(w http.ResponseWriter, r *http.Request) {
		answer(w, o, o.ResetReusePolicy(r.PathValue("configuration")))
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", "the body must be a JSON object with the setting")
		return false
	}
	return true
}

// answer returns the settings in force after a change, or why the change
// was refused.
func answer(w http.ResponseWriter, o *Overrides, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, o.View())
	case errors.Is(err, ErrUnknownProvider):
		fail(w, http.StatusNotFound, "unknown_provider", err.Error())
	case errors.Is(err, ErrUnknownConfiguration):
		fail(w, http.StatusNotFound, "unknown_configuration", err.Error())
	default:
		fail(w, http.StatusBadRequest, "invalid_setting", err.Error())
	}
}

func fail(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
