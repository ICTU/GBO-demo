package main

// The request API of the web side (slice 6b-3 of #533): the developer portal
// lays a request down, opens the start URL in a browser, and follows the
// outcome. Cases Q1-Q8.

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"testing"

	"gbo-demo/qtsp-reference/internal/web"
)

// idShoeSize is an attribute the QTSP offers but no data service provides
// (Q3).
const idShoeSize = "https://gbo.example/attributes/shoe_size/1"

type apiRequest struct {
	ID       string `json:"id"`
	StartURL string `json:"start_url"`
	Status   string `json:"status"`
	Started  bool   `json:"started"`
	Seal     *struct {
		Valid bool `json:"valid"`
	} `json:"seal"`
	Results []struct {
		Identifier string          `json:"identifier"`
		Result     string          `json:"result"`
		Value      json.RawMessage `json:"value"`
	} `json:"results"`
	Error   string `json:"error"`
	Detail  string `json:"detail"`
	TraceID string `json:"trace_id"`
}

// apiEnv is the QTSP's web side on a local port, in front of the stack.
type apiEnv struct{ base string }

func newAPIEnv(t *testing.T) apiEnv {
	t.Helper()
	s := startStack(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	attributes := append(append([]web.Attribute{}, web.DemoAttributes...), web.Attribute{Identifier: idShoeSize, Label: "Schoenmaat", Example: `{"shoe_size":42}`})
	srv := &http.Server{Handler: web.NewHandler(s.client(t, base+"/callback", s.sealAnchor, registryURI), attributes, base, nil)}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return apiEnv{base: base}
}

func (e apiEnv) api(t *testing.T, method, path, body string) (int, apiRequest) {
	t.Helper()
	req, _ := http.NewRequest(method, e.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out apiRequest
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e apiEnv) create(t *testing.T, body string) apiRequest {
	t.Helper()
	status, created := e.api(t, http.MethodPost, "/api/requests", body)
	if status != http.StatusCreated || created.ID == "" || created.Status != "pending" {
		t.Fatalf("Q1: POST /api/requests = %d %+v", status, created)
	}
	return created
}

// run opens the start URL in a new browser, logs in at the QTSP with the
// mock login, then at the authorization server with the DigiD mock with bsn
// and approves (decision "approve") or denies, and returns the request as
// the API then shows it. While the user is at the authorization server, the
// request is pending (Q5).
func (e apiEnv) run(t *testing.T, created apiRequest, bsn, decision string) apiRequest {
	t.Helper()
	browser := newBrowser()
	authorization := e.loginAndStart(t, browser, created)
	if authorization == "" {
		t.Fatal("start URL: no redirect to the authorization server")
	}
	if _, pending := e.api(t, http.MethodGet, "/api/requests/"+created.ID, ""); pending.Status != "pending" || !pending.Started {
		t.Fatalf("Q5: while the user is at the authorization server: %+v", pending)
	}
	if resp, _ := fetch(t, browser, http.MethodGet, login(t, e.base+"/callback", authorization, bsn, decision == "approve"), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("callback = %d", resp.StatusCode)
	}
	_, got := e.api(t, http.MethodGet, "/api/requests/"+created.ID, "")
	return got
}

func newBrowser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// loginAndStart opens the start URL without a login: the login page, the
// request not yet started (N3). It logs in with the mock and follows back to
// the start URL, which sends the browser to the authorization server; it
// returns that URL.
func (e apiEnv) loginAndStart(t *testing.T, browser *http.Client, created apiRequest) string {
	t.Helper()
	resp, page := fetch(t, browser, http.MethodGet, created.StartURL, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `action="/login/mock"`) || !strings.Contains(page, `value="`+created.ID+`"`) {
		t.Fatalf("N3: start URL without a login = %d\n%s", resp.StatusCode, page)
	}
	if _, view := e.api(t, http.MethodGet, "/api/requests/"+created.ID, ""); view.Started {
		t.Fatal("N3: the login page started the request")
	}
	form := mockLogin()
	form.Set("request", created.ID)
	resp, _ = fetch(t, browser, http.MethodPost, e.base+"/login/mock", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/requests/"+created.ID+"/start" {
		t.Fatalf("N3: mock login = %d to %q, want the start URL", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = fetch(t, browser, http.MethodGet, created.StartURL, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start URL after the login = %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// N3: test data laid down with a request prefills the mock login; it is not
// in the start URL.
func TestRequestAPILoginHint(t *testing.T) {
	e := newAPIEnv(t)
	body := `{"login_hint":{"family_name":"Willems","given_name":"Tom","birthdate":"1982-05-08","nationalities":["BE"]},` +
		strings.TrimPrefix(verifyFamilyName(`{"family_name":"Jansen"}`), "{")
	created := e.create(t, body)
	if strings.Contains(created.StartURL, "Tom") {
		t.Fatalf("start URL %q carries test data", created.StartURL)
	}
	resp, page := fetch(t, newBrowser(), http.MethodGet, created.StartURL, nil)
	// The request's test data, not the default.
	for _, want := range []string{`value="Willems"`, `value="Tom"`, `value="1982-05-08"`, `value="BE"`} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Errorf("login page lacks %s:\n%s", want, page)
		}
	}
	if strings.Contains(page, `value="Frouke"`) {
		t.Errorf("login page shows the default instead of the request's test data:\n%s", page)
	}
	// A browser that is logged in goes straight to the authorization server.
	browser := newBrowser()
	if resp, _ := fetch(t, browser, http.MethodPost, e.base+"/login/mock", mockLogin()); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("mock login = %d", resp.StatusCode)
	}
	if resp, _ := fetch(t, browser, http.MethodGet, e.create(t, verifyFamilyName(`{"family_name":"Jansen"}`)).StartURL, nil); resp.StatusCode != http.StatusFound {
		t.Errorf("start URL when logged in = %d, want a redirect to the authorization server", resp.StatusCode)
	}
}

func verifyFamilyName(value string) string {
	b, _ := json.Marshal(map[string]any{"operation": "verify", "attributes": []map[string]any{{"identifier": idFamilyName, "value": json.RawMessage(value)}}})
	return string(b)
}

// Q1: a request is created; its start URL carries only an id.
func TestRequestAPICreate(t *testing.T) {
	e := newAPIEnv(t)
	created := e.create(t, verifyFamilyName(`{"family_name":"Jansen"}`))
	if created.StartURL != e.base+"/requests/"+created.ID+"/start" || strings.Contains(created.StartURL, "Jansen") {
		t.Errorf("start URL %q", created.StartURL)
	}
}

// Q2: invalid requests are refused with a reason.
func TestRequestAPIInvalid(t *testing.T) {
	e := newAPIEnv(t)
	for name, body := range map[string]string{
		"not JSON":           `{"operation":`,
		"JSON after JSON":    verifyFamilyName(`{"family_name":"Jansen"}`) + ` {}`,
		"unknown field":      `{"operation":"verify","attributes":[],"bsn":"1"}`,
		"no operation":       `{"attributes":[{"identifier":"` + idFamilyName + `","value":{"family_name":"Jansen"}}]}`,
		"no attributes":      `{"operation":"verify","attributes":[]}`,
		"unknown attribute":  `{"operation":"verify","attributes":[{"identifier":"https://gbo.example/attributes/unknown/1","value":{}}]}`,
		"verify, no value":   `{"operation":"verify","attributes":[{"identifier":"` + idFamilyName + `"}]}`,
		"the same attribute": `{"operation":"verify","attributes":[{"identifier":"` + idFamilyName + `","value":{"family_name":"Jansen"}},{"identifier":"` + idFamilyName + `","value":{"family_name":"Jansen"}}]}`,
	} {
		if status, got := e.api(t, http.MethodPost, "/api/requests", body); status != http.StatusBadRequest || got.Detail == "" {
			t.Errorf("%s: %d %+v, want 400 with a reason", name, status, got)
		}
	}
}

// Q3: no data service provides the attribute: refused before the
// authorization server.
func TestRequestAPINoDataService(t *testing.T) {
	status, got := newAPIEnv(t).api(t, http.MethodPost, "/api/requests", `{"operation":"verify","attributes":[{"identifier":"`+idShoeSize+`","value":{"shoe_size":42}}]}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(got.Detail, "Schoenmaat") {
		t.Errorf("%d %+v, want 422 naming the attribute", status, got)
	}
}

// Q4: the start URL works once.
func TestRequestAPIStartsOnce(t *testing.T) {
	e := newAPIEnv(t)
	created := e.create(t, verifyFamilyName(`{"family_name":"Jansen"}`))
	browser := newBrowser()
	if e.loginAndStart(t, browser, created) == "" {
		t.Fatal("first open: no redirect to the authorization server")
	}
	if resp, _ := fetch(t, browser, http.MethodGet, created.StartURL, nil); resp.StatusCode != http.StatusGone {
		t.Errorf("second open = %d, want 410", resp.StatusCode)
	}
}

// Q5 and Q6: a verify gives the outcome per attribute.
func TestRequestAPIVerify(t *testing.T) {
	e := newAPIEnv(t)
	for _, tc := range []struct{ value, want string }{
		{`{"family_name":"Jansen"}`, "Match"},
		{`{"family_name":"jansen"}`, "MatchWithVariation"},
		{`{"family_name":"Janssen"}`, "NoMatch"},
	} {
		got := e.run(t, e.create(t, verifyFamilyName(tc.value)), bsnFrouke, "approve")
		if got.Status != "done" || got.Seal == nil || !got.Seal.Valid || len(got.Results) != 1 || got.Results[0].Result != tc.want {
			t.Errorf("%s: %+v, want done, a valid seal and %s", tc.value, got, tc.want)
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got.TraceID) {
			t.Errorf("%s: trace_id %q, want the trace of the call at the ASIP", tc.value, got.TraceID)
		}
	}
	created := e.create(t, `{"operation":"verify","attributes":[{"identifier":"`+idAddress+`","value":{"street":"Coolsingel"}}]}`)
	if got := e.run(t, created, bsnTom, "approve"); got.Status != "done" || len(got.Results) != 1 || got.Results[0].Result != "Unknown" {
		t.Errorf("address of Tom: %+v, want Unknown", got)
	}
}

// Q6: a retrieve gives the value.
func TestRequestAPIRetrieve(t *testing.T) {
	e := newAPIEnv(t)
	got := e.run(t, e.create(t, `{"operation":"retrieve","attributes":[{"identifier":"https://gbo.example/attributes/nationality/1"}]}`), bsnFrouke, "approve")
	if got.Status != "done" || len(got.Results) != 1 || !bytes.Contains(got.Results[0].Value, []byte(`"BE"`)) {
		t.Errorf("%+v, want the nationalities", got)
	}
}

// Q7: a denial asks nothing of the source.
func TestRequestAPIDenied(t *testing.T) {
	e := newAPIEnv(t)
	created := e.create(t, verifyFamilyName(`{"family_name":"Jansen"}`))
	before := brpCalls.Load()
	got := e.run(t, created, bsnFrouke, "deny")
	if got.Status != "denied" || got.Error != "access_denied" || got.Results != nil || got.TraceID != "" {
		t.Errorf("%+v, want denied with access_denied", got)
	}
	if brpCalls.Load() != before {
		t.Error("the source was asked after a denial")
	}
}

// Q8: an unknown id, for the API and the start URL.
func TestRequestAPIUnknownID(t *testing.T) {
	e := newAPIEnv(t)
	if status, _ := e.api(t, http.MethodGet, "/api/requests/not-a-request", ""); status != http.StatusNotFound {
		t.Errorf("GET = %d, want 404", status)
	}
	if resp, _ := fetch(t, http.DefaultClient, http.MethodGet, e.base+"/requests/not-a-request/start", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("start = %d, want 404", resp.StatusCode)
	}
}

// The attributes the form offers, for the portal.
func TestRequestAPIAttributes(t *testing.T) {
	e := newAPIEnv(t)
	resp, body := fetch(t, http.DefaultClient, http.MethodGet, e.base+"/api/attributes", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, idFamilyName) || !strings.Contains(body, `"label":"Familienaam"`) {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}
