// Package web is the reference QTSP's web side, where the user starts: a
// page to request the verification or retrieval of attributes, the redirect
// to the authorization server, and the return with the code, after which the
// QTSP takes a token, asks the ASIP and shows the result with its seal check.
// The developer portal can lay a request down through the request API
// (api.go) and follow its outcome. The citizen logs in first (login.go).
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
	"slices"
	"strings"
	"sync"
	"time"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/login"
	"gbo-demo/qtsp-reference/internal/qtsp"
	"rsc.io/qr"
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
	prepared
	state, verifier string
	// request is the request of the request API this session runs, if any:
	// the callback records its outcome there.
	request string
	// identity is the citizen who logged in at the QTSP and started the
	// request, for the issuance of the attestation (slice 13).
	identity login.Identity
	expires  time.Time
}

// prepared is a checked request whose data service is found and at whose
// authorization server the QTSP is registered.
type prepared struct {
	operation  string
	attributes []qtsp.Attribute
	plan       qtsp.Plan
	reg        qtsp.Registration
}

// refusal is a request the QTSP does not start, with the reason for the user.
type refusal struct {
	status  int
	message string
}

func (r *refusal) Error() string { return r.message }

type Handler struct {
	client     *qtsp.Client
	attributes []Attribute
	// products are what the request page offers; the request API keeps
	// offering every attribute.
	products []Product
	// secure marks the session cookie Secure: the QTSP is served over https.
	secure bool
	// publicURL is where the user reaches the QTSP: the base of start URLs.
	publicURL string
	// wallet is the NL Wallet as login means; nil when it is not
	// configured, and then only the mock login is offered.
	wallet   login.Wallet
	issuance *issuance.Store
	// issuanceLink is the static cross-device universal link for the QTSP
	// usecase. The issuance server creates the actual session when scanned.
	issuanceLink string
	mu           sync.Mutex
	sessions     map[string]*session
	requests     map[string]*request
	logins       map[string]*loginState
	now          func() time.Time
}

// NewHandler serves the web side. publicURL is where the user reaches it;
// over https the cookies are Secure. On http it can only be loopback (the
// authorization server allows no other http redirect URI), where a browser
// may refuse a Secure cookie. wallet is the NL Wallet as login means, or nil.
func NewHandler(client *qtsp.Client, attributes []Attribute, publicURL string, wallet login.Wallet) http.Handler {
	return newHandler(client, attributes, publicURL, wallet).routes()
}

// NewHandlerWithIssuance adds the test-attestation action to accepted result
// pages. The store is shared with the internal HTTPS callback listener.
func NewHandlerWithIssuance(client *qtsp.Client, attributes []Attribute, publicURL string, wallet login.Wallet, store *issuance.Store, link string) http.Handler {
	handler := newHandler(client, attributes, publicURL, wallet)
	handler.issuance, handler.issuanceLink = store, link
	return handler.routes()
}

func newHandler(client *qtsp.Client, attributes []Attribute, publicURL string, wallet login.Wallet) *Handler {
	return &Handler{
		client: client, attributes: attributes, products: DemoProducts, secure: strings.HasPrefix(publicURL, "https://"), publicURL: strings.TrimRight(publicURL, "/"),
		wallet: wallet, sessions: map[string]*session{}, requests: map[string]*request{}, logins: map[string]*loginState{}, now: time.Now,
	}
}

func (h *Handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.form)
	mux.HandleFunc("POST /start", h.start)
	mux.HandleFunc("GET /callback", h.callback)
	mux.HandleFunc("GET /api/attributes", h.listAttributes)
	mux.HandleFunc("POST /api/requests", h.createRequest)
	mux.HandleFunc("GET /api/requests/{id}", h.getRequest)
	mux.HandleFunc("GET /requests/{id}/start", h.startRequest)
	mux.HandleFunc("POST /login/mock", h.mockLogin)
	mux.HandleFunc("POST /login/wallet", h.walletStart)
	mux.HandleFunc("GET /login/wallet", h.walletPage)
	mux.HandleFunc("POST /logout", h.logout)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// form is the request form for a logged-in citizen, and otherwise the login
// page: no request without a login.
func (h *Handler) form(w http.ResponseWriter, r *http.Request) {
	id := h.loggedIn(r)
	if id == nil {
		h.loginPage(w, http.StatusOK, "", "", loginHint{})
		return
	}
	h.renderForm(w, http.StatusOK, defaultForm(h.products), id)
}

// start reads the chosen product, plans the verification, registers when
// needed, and sends the browser to the authorization server.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	id := h.loggedIn(r)
	if id == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		errorPage(w, http.StatusBadRequest, "Het formulier kon niet worden gelezen.")
		return
	}
	// The page offers products to verify; retrieval stays with the request
	// API.
	attr, f, ok := readProduct(h.products, r.PostForm)
	if !ok {
		h.renderForm(w, http.StatusBadRequest, f, id)
		return
	}
	p, err := h.prepare(r.Context(), "verify", []qtsp.Attribute{attr})
	var refused *refusal
	if errors.As(err, &refused) {
		errorPage(w, refused.status, refused.message)
		return
	}
	h.begin(w, r, p, "", *id)
}

// prepare checks a request, finds the data service that provides all its
// attributes, and registers at its authorization server when needed. Every
// error is a refusal.
func (h *Handler) prepare(ctx context.Context, operation string, attrs []qtsp.Attribute) (prepared, error) {
	if operation != "verify" && operation != "retrieve" {
		return prepared{}, &refusal{http.StatusBadRequest, "Kies verifiëren of ophalen."}
	}
	if len(attrs) == 0 {
		return prepared{}, &refusal{http.StatusBadRequest, "Kies ten minste één attribuut."}
	}
	ids := make([]string, 0, len(attrs))
	for i, a := range attrs {
		label, known := h.lookup(a.Identifier)
		switch {
		case !known:
			return prepared{}, &refusal{http.StatusBadRequest, "Onbekend attribuut " + a.Identifier + "."}
		case slices.Contains(ids, a.Identifier):
			return prepared{}, &refusal{http.StatusBadRequest, "Attribuut " + label + " staat er twee keer in."}
		case operation == "verify" && !json.Valid(a.Value):
			return prepared{}, &refusal{http.StatusBadRequest, "De waarde van " + label + " is geen JSON."}
		case operation == "retrieve":
			attrs[i].Value = nil
		}
		ids = append(ids, a.Identifier)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	plan, err := h.client.Plan(ctx, ids)
	var none *qtsp.NoDataServiceError
	switch {
	case errors.Is(err, qtsp.ErrNoCommonDataService):
		return prepared{}, &refusal{http.StatusUnprocessableEntity, "Geen enkele bron levert al deze attributen. Vraag ze apart aan: een toestemming geldt voor één bron."}
	case errors.As(err, &none):
		return prepared{}, &refusal{http.StatusUnprocessableEntity, "Voor " + h.label(none.Attribute) + " is geen dataservice gevonden."}
	case err != nil:
		slog.Warn("discovery failed", "err", err)
		return prepared{}, &refusal{http.StatusBadGateway, "De dataservice kon niet worden gevonden."}
	}
	reg, err := h.client.Register(ctx, plan.Server)
	if err != nil {
		slog.Warn("registration failed", "err", err)
		return prepared{}, &refusal{http.StatusBadGateway, "De QTSP kon zich niet registreren bij de autorisatieserver."}
	}
	return prepared{operation: operation, attributes: attrs, plan: plan, reg: reg}, nil
}

// begin keeps state, the PKCE verifier and the logged-in citizen in a
// session of this browser and sends it to the authorization server.
func (h *Handler) begin(w http.ResponseWriter, r *http.Request, p prepared, requestID string, citizen login.Identity) {
	verifier, challenge := qtsp.PKCE()
	s := &session{prepared: p, state: qtsp.State(), verifier: verifier, request: requestID, identity: citizen, expires: h.now().Add(sessionLifetime)}
	id := randomID()
	h.mu.Lock()
	h.sessions[id] = s
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLifetime.Seconds())})
	http.Redirect(w, r, h.client.AuthorizationURL(p.plan, p.reg, p.operation, s.state, challenge), http.StatusFound)
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
		h.finish(s.request, outcome{Status: statusDenied, Operation: s.operation, Error: e, ErrorDescription: q.Get("error_description")})
		render(w, http.StatusOK, deniedPage, map[string]any{"Error": e, "Description": q.Get("error_description")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := h.client.Token(ctx, s.plan, s.reg, q.Get("code"), s.verifier)
	if err != nil {
		slog.Warn("token request failed", "err", err)
		h.finish(s.request, outcome{Status: statusFailed, Operation: s.operation, Error: "De autorisatieserver gaf geen token."})
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
		h.finish(s.request, outcome{Status: statusFailed, Operation: s.operation, Error: "De ASIP was niet bereikbaar."})
		errorPage(w, http.StatusBadGateway, "De ASIP was niet bereikbaar.")
		return
	}
	o := h.result(s.prepared, resp)
	h.finish(s.request, o)
	view := resultView{outcome: o, Who: h.loggedIn(r)}
	view.WalletQR = h.offerIssuance(s.identity, s.attributes, o)
	render(w, http.StatusOK, resultPage, view)
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

// The status of a request of the request API.
const (
	statusPending = "pending"
	statusDone    = "done"
	statusDenied  = "denied"
	statusFailed  = "failed"
)

// outcome is how a request ended: the result page shows it, and the request
// API returns it.
type outcome struct {
	Status      string `json:"status"`
	Operation   string `json:"operation"`
	DataService string `json:"data_service,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Source      string `json:"source,omitempty"`
	// ASIPStatus and Problem are an error answer of the ASIP.
	ASIPStatus int    `json:"asip_status,omitempty"`
	Problem    string `json:"problem,omitempty"`
	// Seal is the check of the seal on a 200; without a valid seal the
	// result is not accepted and Results is empty.
	Seal    *sealView `json:"seal,omitempty"`
	Results []row     `json:"results,omitempty"`
	// Evidence is private issuance input. In particular, a verify value is
	// what the QTSP submitted, not a value returned by the source, so it must
	// not acquire the public API meaning of results[].value.
	Evidence []issuance.Evidence `json:"-"`
	// TraceID is the trace of the call at the ASIP, so the portal shows the
	// chain of this request and no other.
	TraceID string `json:"trace_id,omitempty"`
	// Error is a denial (an OAuth error code) or why the request failed.
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// resultView is the result page: the outcome, and who is logged in.
type resultView struct {
	outcome
	Who      *login.Identity
	WalletQR template.URL
}

// offerIssuance stores no result unless every requested verification
// succeeded (or every retrieval returned a value). It returns the QR only
// after the pending result exists, so the page never offers a dead action.
func (h *Handler) offerIssuance(identity login.Identity, requested []qtsp.Attribute, out outcome) template.URL {
	if h.issuance == nil || h.issuanceLink == "" || out.Status != statusDone || out.Seal == nil || !out.Seal.Valid || !completeEvidence(requested, out.Results) {
		return ""
	}
	code, err := qr.Encode(h.issuanceLink, qr.M)
	if err != nil {
		return ""
	}
	if !h.issuance.Add(issuance.Request{
		Identity: identity, Operation: out.Operation, Source: out.Source,
		VerifiedAt: h.now().UTC(), Evidence: out.Evidence, ExpectedAttributes: len(requested),
	}) {
		return ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
}

func completeEvidence(requested []qtsp.Attribute, results []row) bool {
	if len(requested) == 0 || len(results) != len(requested) {
		return false
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if result.Identifier == "" {
			return false
		}
		seen[result.Identifier] = struct{}{}
	}
	if len(seen) != len(requested) {
		return false
	}
	for _, attribute := range requested {
		if _, found := seen[attribute.Identifier]; !found {
			return false
		}
	}
	return true
}

type sealView struct {
	Valid  bool   `json:"valid"`
	Signer string `json:"signer,omitempty"`
	Error  string `json:"error,omitempty"`
}

// row is one attribute: the verification result (Match, NoMatch,
// MatchWithVariation, Unknown) or the retrieved value.
type row struct {
	Identifier string          `json:"identifier"`
	Label      string          `json:"label"`
	Result     string          `json:"result,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	// Submitted is the value the QTSP had verified, for the result page
	// only: the request API does not return it.
	Submitted json.RawMessage `json:"-"`
}

// Display is what the result page shows for the attribute.
func (r row) Display() string {
	if r.Result != "" {
		return r.Result
	}
	return string(r.Value)
}

func (h *Handler) result(p prepared, resp qtsp.Response) outcome {
	out := outcome{Status: statusDone, Operation: p.operation, DataService: p.plan.EndpointURI, Provider: p.plan.Provider, TraceID: resp.TraceID}
	if resp.Status != http.StatusOK {
		var problem struct{ Title, Detail string }
		_ = json.Unmarshal(resp.Body, &problem)
		out.Status, out.ASIPStatus, out.Problem = statusFailed, resp.Status, problem.Title+": "+problem.Detail
		return out
	}
	if resp.SealError != nil {
		out.Seal = &sealView{Error: resp.SealError.Error()}
		return out
	}
	out.Seal = &sealView{Valid: true, Signer: resp.Seal.Signer}
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
		out.Source = res.AuthenticSource.LegalName
	}
	for _, v := range res.Verify {
		value := requestedValue(p.attributes, v.Identifier)
		out.Results = append(out.Results, row{
			Identifier: v.Identifier, Label: h.label(v.Identifier), Result: v.Result[strings.LastIndex(v.Result, "/")+1:], Submitted: value,
		})
		out.Evidence = append(out.Evidence, issuance.Evidence{Name: h.label(v.Identifier), Outcome: v.Result[strings.LastIndex(v.Result, "/")+1:], Value: value})
	}
	for _, v := range res.Retrieve {
		out.Results = append(out.Results, row{Identifier: v.Identifier, Label: h.label(v.Identifier), Value: v.Value})
		out.Evidence = append(out.Evidence, issuance.Evidence{Name: h.label(v.Identifier), Value: v.Value})
	}
	return out
}

func requestedValue(attributes []qtsp.Attribute, identifier string) json.RawMessage {
	for _, attribute := range attributes {
		if attribute.Identifier == identifier {
			return append(json.RawMessage(nil), attribute.Value...)
		}
	}
	return nil
}

func (h *Handler) lookup(id string) (string, bool) {
	for _, a := range h.attributes {
		if a.Identifier == id {
			return a.Label, true
		}
	}
	return "", false
}

func (h *Handler) label(id string) string {
	if label, ok := h.lookup(id); ok {
		return label
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
