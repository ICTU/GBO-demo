// Package web is the reference QTSP's web side, where the user starts: a
// page to request the verification or retrieval of attributes, the redirect
// to the authorization server, and the return with the code, after which the
// QTSP takes a token, asks the ASIP and shows the result with its seal check.
package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gbo-demo/qtsp-reference/internal/qtsp"
)

// Attribute is an attribute the form offers, with an example value.
type Attribute struct {
	Identifier string
	Label      string
	Example    string
}

// DemoAttributes are the BRP attributes of the demo's ASIP, with the values
// of test person Frouke Jansen as examples.
var DemoAttributes = []Attribute{
	{"https://gbo.example/attributes/family_name/1", "Familienaam", `{"family_name":"Jansen"}`},
	{"https://gbo.example/attributes/given_name/1", "Voornamen", `{"given_name":"Frouke"}`},
	{"https://gbo.example/attributes/birth_date/1", "Geboortedatum", `{"birth_date":"2000-03-24"}`},
	{"https://gbo.example/attributes/nationality/1", "Nationaliteit", `{"nationalities":["NL","BE"]}`},
	{"https://gbo.example/attributes/address/1", "Adres", `{"street":"Coolsingel","house_number":"12 B","postal_code":"3011 AD","city":"Rotterdam","country":"NL"}`},
}

const (
	sessionCookie   = "qtsp_session"
	sessionLifetime = 10 * time.Minute
)

// session is a request waiting for the user to return from the
// authorization server.
type session struct {
	state, verifier, operation string
	plan                       qtsp.Plan
	reg                        qtsp.Registration
	attributes                 []qtsp.Attribute
	expires                    time.Time
}

type Handler struct {
	client     *qtsp.Client
	attributes []Attribute
	mu         sync.Mutex
	sessions   map[string]*session
	now        func() time.Time
}

func NewHandler(client *qtsp.Client, attributes []Attribute) http.Handler {
	h := &Handler{client: client, attributes: attributes, sessions: map[string]*session{}, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.form)
	mux.HandleFunc("POST /start", h.start)
	mux.HandleFunc("GET /callback", h.callback)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func (h *Handler) form(w http.ResponseWriter, _ *http.Request) {
	render(w, http.StatusOK, formPage, map[string]any{"Attributes": h.attributes})
}

// start plans the request, registers when needed, and sends the browser to
// the authorization server.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		errorPage(w, http.StatusBadRequest, "Het formulier kon niet worden gelezen.")
		return
	}
	operation := r.PostForm.Get("operation")
	if operation != "verify" && operation != "retrieve" {
		errorPage(w, http.StatusBadRequest, "Kies verifiëren of ophalen.")
		return
	}
	var attrs []qtsp.Attribute
	var ids []string
	for _, a := range h.attributes {
		if r.PostForm.Get("use:"+a.Identifier) == "" {
			continue
		}
		value := strings.TrimSpace(r.PostForm.Get("value:" + a.Identifier))
		if operation == "verify" && !json.Valid([]byte(value)) {
			errorPage(w, http.StatusBadRequest, "De waarde van "+a.Label+" is geen JSON.")
			return
		}
		attrs = append(attrs, qtsp.Attribute{Identifier: a.Identifier, Value: json.RawMessage(value)})
		ids = append(ids, a.Identifier)
	}
	if len(ids) == 0 {
		errorPage(w, http.StatusBadRequest, "Kies ten minste één attribuut.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	plan, err := h.client.Plan(ctx, ids)
	var none *qtsp.NoDataServiceError
	switch {
	case errors.Is(err, qtsp.ErrSeveralDataServices):
		errorPage(w, http.StatusBadRequest, "Deze attributen horen bij verschillende bronnen. Vraag ze apart aan: een toestemming geldt voor één bron.")
		return
	case errors.As(err, &none):
		errorPage(w, http.StatusBadRequest, "Voor "+none.Attribute+" is geen dataservice gevonden.")
		return
	case err != nil:
		slog.Warn("discovery failed", "err", err)
		errorPage(w, http.StatusBadGateway, "De dataservice kon niet worden gevonden.")
		return
	}
	reg, err := h.client.Register(ctx, plan.Server)
	if err != nil {
		slog.Warn("registration failed", "err", err)
		errorPage(w, http.StatusBadGateway, "De QTSP kon zich niet registreren bij de autorisatieserver.")
		return
	}
	verifier, challenge := qtsp.PKCE()
	s := &session{state: qtsp.State(), verifier: verifier, operation: operation, plan: plan, reg: reg, attributes: attrs, expires: h.now().Add(sessionLifetime)}
	id := randomID()
	h.mu.Lock()
	h.sessions[id] = s
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLifetime.Seconds())})
	http.Redirect(w, r, h.client.AuthorizationURL(plan, reg, operation, s.state, challenge), http.StatusFound)
}

// callback takes the user back from the authorization server: a session and
// a matching state, or nothing happens.
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	s := h.take(r)
	q := r.URL.Query()
	if s == nil || q.Get("state") == "" || q.Get("state") != s.state {
		errorPage(w, http.StatusBadRequest, "Dit antwoord hoort niet bij een aanvraag van deze browser.")
		return
	}
	if e := q.Get("error"); e != "" {
		render(w, http.StatusOK, deniedPage, map[string]any{"Error": e, "Description": q.Get("error_description")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := h.client.Token(ctx, s.plan, s.reg, q.Get("code"), s.verifier)
	if err != nil {
		slog.Warn("token request failed", "err", err)
		errorPage(w, http.StatusBadGateway, "De autorisatieserver gaf geen token.")
		return
	}
	body := qtsp.VerifyBody(s.attributes, false)
	if s.operation == "retrieve" {
		ids := make([]string, 0, len(s.attributes))
		for _, a := range s.attributes {
			ids = append(ids, a.Identifier)
		}
		body = qtsp.RetrieveBody(ids, false)
	}
	resp, err := h.client.Call(ctx, s.plan, s.operation, token, body)
	if err != nil {
		slog.Warn("call to the ASIP failed", "err", err)
		errorPage(w, http.StatusBadGateway, "De ASIP was niet bereikbaar.")
		return
	}
	render(w, http.StatusOK, resultPage, h.result(s, resp))
}

// take returns and removes the browser's session, if it is still valid.
func (h *Handler) take(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[c.Value]
	delete(h.sessions, c.Value)
	for id, other := range h.sessions {
		if h.now().After(other.expires) {
			delete(h.sessions, id)
		}
	}
	if s == nil || h.now().After(s.expires) {
		return nil
	}
	return s
}

type row struct{ Label, Result string }

func (h *Handler) result(s *session, resp qtsp.Response) map[string]any {
	out := map[string]any{"Operation": s.operation, "Status": resp.Status, "DataService": s.plan.EndpointURI, "Provider": s.plan.Provider}
	if resp.Status != http.StatusOK {
		var p struct{ Title, Detail string }
		_ = json.Unmarshal(resp.Body, &p)
		out["Problem"] = p.Title + ": " + p.Detail
		return out
	}
	if resp.SealError != nil {
		out["SealError"] = resp.SealError.Error()
		return out
	}
	out["Signer"] = resp.Seal.Signer
	var res struct {
		Verify []struct {
			Identifier string `json:"attributeIdentifier"`
			Result     string `json:"attributeVerificationResult"`
		} `json:"attributeVerificationResults"`
		Retrieve []struct {
			Identifier string          `json:"attributeIdentifier"`
			Value      json.RawMessage `json:"attributeValue"`
		} `json:"attributes"`
		AuthenticSource *struct {
			LegalName string `json:"legalName"`
		} `json:"authenticSource"`
	}
	_ = json.Unmarshal(resp.Body, &res)
	if res.AuthenticSource != nil {
		out["Source"] = res.AuthenticSource.LegalName
	}
	var rows []row
	for _, v := range res.Verify {
		rows = append(rows, row{h.label(v.Identifier), v.Result[strings.LastIndex(v.Result, "/")+1:]})
	}
	for _, v := range res.Retrieve {
		rows = append(rows, row{h.label(v.Identifier), string(v.Value)})
	}
	out["Rows"] = rows
	return out
}

func (h *Handler) label(id string) string {
	for _, a := range h.attributes {
		if a.Identifier == id {
			return a.Label
		}
	}
	return id
}

func randomID() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func render(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = t.Execute(w, data)
}

func errorPage(w http.ResponseWriter, status int, message string) {
	render(w, status, errPage, map[string]any{"Message": message})
}
