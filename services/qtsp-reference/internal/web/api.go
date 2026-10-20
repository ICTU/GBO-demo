package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"gbo-demo/qtsp-reference/internal/qtsp"
)

// The request API lets the developer portal lay a request down and follow
// its outcome, while the user still goes through the QTSP: the portal opens
// the start URL in a browser, the QTSP sends that browser to the
// authorization server, and the user returns to the QTSP's result page.
//
// The start URL carries only an opaque id, so no attribute value appears in
// a URL. The API has no access control, like the other mocks of the demo.

// requestLifetime is how long a request, and its outcome once it has one,
// can be fetched.
const requestLifetime = 10 * time.Minute

// request is a request laid down through the API.
type request struct {
	prepared
	// started is set when the start URL begins the request: it works once.
	started bool
	// hint is test data for the mock login, if the request carried it.
	hint    *loginHint
	outcome *outcome
	expires time.Time
}

type apiAttribute struct {
	Identifier string          `json:"identifier"`
	Label      string          `json:"label"`
	Example    json.RawMessage `json:"example"`
}

// listAttributes returns the attributes the QTSP can request, with an
// example value, so the portal offers the same ones as the form.
func (h *Handler) listAttributes(w http.ResponseWriter, _ *http.Request) {
	out := make([]apiAttribute, 0, len(h.attributes))
	for _, a := range h.attributes {
		out = append(out, apiAttribute{Identifier: a.Identifier, Label: a.Label, Example: json.RawMessage(a.Example)})
	}
	writeJSON(w, http.StatusOK, out)
}

type requestBody struct {
	// LoginHint is test data that prefills the mock login, so a demo runs
	// without a phone. It stays on the server; the start URL has only an id.
	LoginHint  *loginHint `json:"login_hint"`
	Operation  string     `json:"operation"`
	Attributes []struct {
		Identifier string          `json:"identifier"`
		Value      json.RawMessage `json:"value"`
	} `json:"attributes"`
}

type requestView struct {
	ID        string    `json:"id"`
	StartURL  string    `json:"start_url"`
	ExpiresAt time.Time `json:"expires_at"`
	Started   bool      `json:"started"`
	outcome
}

// createRequest checks the request, finds its data service and registers at
// its authorization server, as the form does, and returns the start URL.
func (h *Handler) createRequest(w http.ResponseWriter, r *http.Request) {
	var body requestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	// One JSON object and nothing after it.
	if err := dec.Decode(&body); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeProblem(w, http.StatusBadRequest, "De aanvraag is geen geldige JSON.")
		return
	}
	attrs := make([]qtsp.Attribute, 0, len(body.Attributes))
	for _, a := range body.Attributes {
		attrs = append(attrs, qtsp.Attribute{Identifier: a.Identifier, Value: a.Value})
	}
	p, err := h.prepare(r.Context(), body.Operation, attrs)
	var refused *refusal
	if errors.As(err, &refused) {
		writeProblem(w, refused.status, refused.message)
		return
	}
	id := randomID()
	req := &request{prepared: p, hint: body.LoginHint, expires: h.now().Add(requestLifetime)}
	h.mu.Lock()
	h.purge()
	h.requests[id] = req
	view := h.view(id, req)
	h.mu.Unlock()
	w.Header().Set("Location", "/api/requests/"+id)
	writeJSON(w, http.StatusCreated, view)
}

// getRequest returns the request: pending until the user returns from the
// authorization server, then done, denied or failed.
func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.purge()
	req := h.requests[r.PathValue("id")]
	var view requestView
	if req != nil {
		view = h.view(r.PathValue("id"), req)
	}
	h.mu.Unlock()
	if req == nil {
		writeProblem(w, http.StatusNotFound, "Deze aanvraag bestaat niet of is verlopen.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// startRequest is the start URL: without a login the login page, after
// which the citizen returns here; with a login it sends the browser to the
// authorization server, once.
func (h *Handler) startRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	citizen := h.loggedIn(r)
	h.mu.Lock()
	h.purge()
	req := h.requests[id]
	started := req != nil && req.started
	if req != nil && citizen != nil {
		req.started = true
	}
	h.mu.Unlock()
	switch {
	case req == nil:
		errorPage(w, http.StatusNotFound, "Deze aanvraag bestaat niet of is verlopen.")
	case started:
		errorPage(w, http.StatusGone, "Deze aanvraag is al gestart. Leg een nieuwe aanvraag neer.")
	case citizen == nil:
		h.loginPage(w, http.StatusOK, id, "", loginHint{})
	default:
		h.begin(w, r, req.prepared, id, *citizen)
	}
}

// finish records the outcome of the request with id, if the session ran one.
func (h *Handler) finish(id string, o outcome) {
	if id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if req := h.requests[id]; req != nil {
		req.outcome, req.expires = &o, h.now().Add(requestLifetime)
	}
}

// view is the API's form of a request; h.mu is held.
func (h *Handler) view(id string, req *request) requestView {
	o := outcome{Status: statusPending, Operation: req.operation, DataService: req.plan.EndpointURI, Provider: req.plan.Provider}
	if req.outcome != nil {
		o = *req.outcome
	}
	return requestView{ID: id, StartURL: h.publicURL + "/requests/" + id + "/start", ExpiresAt: req.expires, Started: req.started, outcome: o}
}

// purge removes expired requests; h.mu is held.
func (h *Handler) purge() {
	for id, req := range h.requests {
		if h.now().After(req.expires) {
			delete(h.requests, id)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeProblem writes an RFC 9457 problem with the reason for the user.
func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "title": http.StatusText(status), "detail": detail})
}
