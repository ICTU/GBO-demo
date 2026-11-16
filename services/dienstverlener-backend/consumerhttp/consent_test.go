package consumerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
	"gbo-demo/dienstverlener-backend/consumer"
	"gbo-demo/dienstverlener-backend/sessionstore"
)

type fakePortal struct {
	pushErr  error
	exchErr  error
	token    string
	pushed   []consentrequest.PushedRequest
	verifier string
}

func (p *fakePortal) Push(_ context.Context, req consentrequest.PushedRequest) (string, error) {
	p.pushed = append(p.pushed, req)
	return "urn:ietf:params:oauth:request_uri:ref-1", p.pushErr
}

func (p *fakePortal) Exchange(_ context.Context, req consentrequest.CodeExchange) (consentrequest.Consent, error) {
	p.verifier = req.CodeVerifier
	if p.exchErr != nil {
		return consentrequest.Consent{}, p.exchErr
	}
	return consentrequest.Consent{Token: p.token, ConsentID: "c-1", Scopes: []string{"bd:ib:2025"}}, nil
}

func consentFlow(portal *fakePortal, secure bool) *ConsentFlow {
	return &ConsentFlow{
		Requester: &consentrequest.Requester{
			ClientID:    "99999999900000000300",
			RedirectURI: "http://localhost:9001/return",
			PortalURL:   "http://localhost:9002",
			Portal:      portal,
			Sessions:    &sessionstore.Memory{Lifetime: time.Hour},
		},
		CookieName: "consent_session_99999999900000000300",
		Secure:     secure,
		MaxAge:     time.Hour,
	}
}

// browser is a client with a cookie jar against handler.
type browser struct {
	t      *testing.T
	url    string
	client *http.Client
}

func newBrowser(t *testing.T, handler http.Handler) *browser {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, url: srv.URL, client: &http.Client{Jar: jar}}
}

func (b *browser) post(path string, body any) (*http.Response, string, map[string]any) {
	b.t.Helper()
	encoded, _ := json.Marshal(body)
	resp, err := b.client.Post(b.url+path, "application/json", strings.NewReader(string(encoded)))
	if err != nil {
		b.t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, string(raw), out
}

// send posts a raw body with the given content type and extra headers.
func (b *browser) send(path, contentType, body string, header map[string]string) *http.Response {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("post %s: %v", path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

// sessionCookie is the value of the session cookie in b's jar.
func (b *browser) sessionCookie() string {
	u, _ := url.Parse(b.url + "/api/")
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == "consent_session_99999999900000000300" {
			return c.Value
		}
	}
	return ""
}

func (b *browser) start() {
	b.t.Helper()
	resp, _, out := b.post("/api/consent/start", map[string]any{"service": "aangiftegegevens", "scopes": []string{"bd:ib:2025"}})
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("start: %d %v", resp.StatusCode, out)
	}
}

func TestStartSetsTheSessionCookie(t *testing.T) {
	for _, secure := range []bool{false, true} {
		portal := &fakePortal{}
		b := newBrowser(t, NewMux(&consumer.Consumer{}, consentFlow(portal, secure)))
		resp, _, out := b.post("/api/consent/start", map[string]any{
			"service": "aangiftegegevens", "scopes": []string{"bd:ib:2025"}, "valid_until": "2027-01-01T00:00:00Z",
		})
		if resp.StatusCode != http.StatusOK || len(out) != 1 {
			t.Fatalf("start: %d %v, want only redirect_to", resp.StatusCode, out)
		}
		redirect, _ := url.Parse(out["redirect_to"].(string))
		if redirect.Path != "/toestemming/start" || redirect.Query().Get("request_uri") == "" {
			t.Errorf("redirect_to %v", out["redirect_to"])
		}
		cookies := resp.Cookies()
		if len(cookies) != 1 {
			t.Fatalf("cookies %v", cookies)
		}
		c := cookies[0]
		if c.Name != "consent_session_99999999900000000300" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
			c.Path != "/api" || c.Secure != secure || c.MaxAge != 3600 {
			t.Errorf("secure=%v: cookie %+v", secure, c)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Error("start answer may be cached")
		}
		if portal.pushed[0].ValidUntil != "2027-01-01T00:00:00Z" {
			t.Errorf("pushed %+v", portal.pushed[0])
		}
	}
}

// After a granted return the session holds the token: no answer to the
// browser carries it, and a query without a token uses it.
func TestTheTokenStaysInTheSession(t *testing.T) {
	token := testToken("c-1", "bd:ib:2025")
	portal := &fakePortal{token: token}
	source := &fakeSource{answer: consumer.Answer{Allowed: true, Data: json.RawMessage(`{"data":{}}`)}}
	b := newBrowser(t, NewHandler(&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: source}, consentFlow(portal, false), "test"))

	b.start()
	before := b.sessionCookie()
	resp, raw, out := b.post("/api/consent/complete", map[string]any{"code": "code-1", "state": portal.pushed[0].State})
	if resp.StatusCode != http.StatusOK || out["outcome"] != "granted" || out["consent_id"] != "c-1" {
		t.Fatalf("complete: %d %s", resp.StatusCode, raw)
	}
	if after := b.sessionCookie(); after == "" || after == before {
		t.Errorf("the session id was not renewed on the grant: %q", after)
	}
	if strings.Contains(raw, token) || portal.verifier == "" {
		t.Errorf("complete answered %s (verifier %q)", raw, portal.verifier)
	}

	resp, raw, out = b.post("/api/dvtp/query", map[string]any{"belastingjaren": []int{2025}})
	if resp.StatusCode != http.StatusOK || out["allowed"] != true {
		t.Fatalf("query: %d %s", resp.StatusCode, raw)
	}
	if source.asked[0].ConsentToken != token {
		t.Errorf("asked with %q, want the session's token", source.asked[0].ConsentToken)
	}
	if strings.Contains(raw, token) {
		t.Error("the query answer carries the token")
	}

	other := testToken("c-2", "bd:ib:2025")
	b.post("/api/dvtp/query", map[string]any{"consent_token": other, "belastingjaren": []int{2025}})
	if source.asked[1].ConsentToken != other {
		t.Error("a token in the body did not win over the session's")
	}
}

func TestAQueryWithoutSessionOrTokenIsABadRequest(t *testing.T) {
	b := newBrowser(t, NewMux(&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: &fakeSource{}}, consentFlow(&fakePortal{}, false)))
	if resp, _, _ := b.post("/api/dvtp/query", map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}

func TestDeniedAndFailedComeThroughAsOutcomes(t *testing.T) {
	for portalError, want := range map[string]string{"access_denied": "denied", "server_error": "failed"} {
		portal := &fakePortal{}
		b := newBrowser(t, NewMux(&consumer.Consumer{}, consentFlow(portal, false)))
		b.start()
		resp, raw, out := b.post("/api/consent/complete", map[string]any{"error": portalError, "state": portal.pushed[0].State})
		if resp.StatusCode != http.StatusOK || out["outcome"] != want || len(out) != 1 {
			t.Errorf("%s: %d %s, want outcome %s", portalError, resp.StatusCode, raw, want)
		}
	}
}

func TestCompleteRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		exchErr error
		started bool
		body    map[string]any
		status  int
		error   string
	}{
		"state mismatch":     {started: true, body: map[string]any{"code": "c", "state": "other"}, status: 400, error: "state_mismatch"},
		"no pending request": {body: map[string]any{"code": "c", "state": "s"}, status: 400, error: "no_pending_request"},
		"no state":           {started: true, body: map[string]any{"code": "c"}, status: 400, error: "invalid_request"},
		"refused exchange":   {started: true, exchErr: &consentrequest.Refusal{Code: "invalid_grant"}, status: 502, error: "code_exchange_failed"},
		"portal unreachable": {started: true, exchErr: errors.New("dial tcp: refused"), status: 502, error: "code_exchange_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			portal := &fakePortal{exchErr: tc.exchErr, token: "a.b.c"}
			b := newBrowser(t, NewMux(&consumer.Consumer{}, consentFlow(portal, false)))
			body := tc.body
			if tc.started {
				b.start()
				if body == nil {
					body = map[string]any{"code": "c", "state": portal.pushed[0].State}
				}
			}
			resp, raw, out := b.post("/api/consent/complete", body)
			if resp.StatusCode != tc.status || out["error"] != tc.error {
				t.Errorf("%d %s, want %d %s", resp.StatusCode, raw, tc.status, tc.error)
			}
		})
	}
}

func TestStartRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		pushErr error
		scopes  []string
		status  int
		error   string
		reason  string
	}{
		"refused":              {pushErr: &consentrequest.Refusal{Code: "invalid_scope", Reason: "UNKNOWN_SCOPE"}, status: 400, error: "invalid_scope", reason: "UNKNOWN_SCOPE"},
		"unregistered service": {pushErr: &consentrequest.Refusal{Code: "invalid_request", Reason: "INITIATION_MISMATCH"}, status: 400, error: "invalid_request", reason: "INITIATION_MISMATCH"},
		"inactive provider":    {pushErr: &consentrequest.Refusal{Code: "unauthorized_client", Reason: "PARTY_NOT_ACTIVE"}, status: 502, error: "unauthorized_client", reason: "PARTY_NOT_ACTIVE"},
		"unknown client":       {pushErr: &consentrequest.Refusal{Code: "invalid_client"}, status: 502, error: "invalid_client"},
		"return location":      {pushErr: &consentrequest.Refusal{Code: "invalid_request", Reason: "RETURN_URL_NOT_REGISTERED"}, status: 502, error: "invalid_request", reason: "RETURN_URL_NOT_REGISTERED"},
		"register down":        {pushErr: &consentrequest.Refusal{Code: "temporarily_unavailable", Reason: "ADMISSION_REGISTER_UNAVAILABLE"}, status: 503, error: "temporarily_unavailable", reason: "ADMISSION_REGISTER_UNAVAILABLE"},
		"unreachable":          {pushErr: errors.New("dial tcp: refused"), status: 502, error: "consent_portal_unavailable"},
		"white space set":      {scopes: []string{"a b"}, status: 400, error: "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBrowser(t, NewMux(&consumer.Consumer{}, consentFlow(&fakePortal{pushErr: tc.pushErr}, false)))
			scopes := tc.scopes
			if scopes == nil {
				scopes = []string{"bd:ib:2025"}
			}
			resp, raw, out := b.post("/api/consent/start", map[string]any{"service": "s", "scopes": scopes})
			if resp.StatusCode != tc.status || out["error"] != tc.error || (tc.reason != "" && out["reason"] != tc.reason) {
				t.Errorf("%d %s", resp.StatusCode, raw)
			}
			if len(resp.Cookies()) != 0 {
				t.Error("a refused start set a cookie")
			}
		})
	}
}

func TestWithoutAFlowTheConsentRoutesAreOff(t *testing.T) {
	b := newBrowser(t, NewMux(&consumer.Consumer{}, nil))
	if resp, _, _ := b.post("/api/consent/start", map[string]any{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

// Session fixation: an attacker starts a session, plants its id in the
// citizen's browser and waits for the citizen to grant. The planted id must
// not give the citizen's token.
func TestAPlantedSessionCannotReceiveTheConsent(t *testing.T) {
	token := testToken("c-1", "bd:ib:2025")
	portal := &fakePortal{token: token}
	source := &fakeSource{answer: consumer.Answer{Allowed: true, Data: json.RawMessage(`{"data":{}}`)}}
	handler := NewMux(&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: source}, consentFlow(portal, false))
	attacker := newBrowser(t, handler)
	attacker.start()
	planted := attacker.sessionCookie()

	citizen := newBrowser(t, handler)
	citizen.url = attacker.url
	u, _ := url.Parse(citizen.url + "/api/")
	citizen.client.Jar.SetCookies(u, []*http.Cookie{{Name: "consent_session_99999999900000000300", Value: planted, Path: "/api"}})
	citizen.start()
	if resp, raw, _ := citizen.post("/api/consent/complete", map[string]any{"code": "code-1", "state": portal.pushed[1].State}); resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, raw)
	}
	if citizen.sessionCookie() == planted {
		t.Fatal("the citizen kept the planted session id")
	}

	resp, raw, _ := attacker.post("/api/dvtp/query", map[string]any{"belastingjaren": []int{2025}})
	if resp.StatusCode != http.StatusBadRequest || len(source.asked) != 0 {
		t.Fatalf("the planted session asked the source: %d %s", resp.StatusCode, raw)
	}
	if resp, raw, _ := citizen.post("/api/dvtp/query", map[string]any{"belastingjaren": []int{2025}}); resp.StatusCode != http.StatusOK || source.asked[0].ConsentToken != token {
		t.Errorf("the citizen's query: %d %s", resp.StatusCode, raw)
	}
}

// A page on another origin of the same site (another port, a sibling
// subdomain) gets the session cookie sent along. Without a preflight it can
// only send a simple request, such as a text/plain body; a browser that says
// where the request comes from is refused unless it is the same origin.
func TestTheSessionIsOnlyUsedFromTheSameOrigin(t *testing.T) {
	token := testToken("c-1", "bd:ib:2025")
	portal := &fakePortal{token: token}
	source := &fakeSource{answer: consumer.Answer{Allowed: true, Data: json.RawMessage(`{"data":{}}`)}}
	b := newBrowser(t, NewMux(&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: source}, consentFlow(portal, false)))
	b.start()
	state := portal.pushed[0].State
	if resp, raw, _ := b.post("/api/consent/complete", map[string]any{"code": "code-1", "state": state}); resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, raw)
	}
	b.start()
	state = portal.pushed[1].State

	query := `{"belastingjaren":[2025]}`
	start := `{"service":"aangiftegegevens","scopes":["bd:ib:2025"]}`
	complete := `{"error":"access_denied","state":"` + state + `"}`
	for name, tc := range map[string]struct {
		path, contentType, body string
		header                  map[string]string
		status                  int
	}{
		"query as text/plain":        {"/api/dvtp/query", "text/plain", query, nil, http.StatusUnsupportedMediaType},
		"query as a form":            {"/api/dvtp/query", "application/x-www-form-urlencoded", query, nil, http.StatusUnsupportedMediaType},
		"query cross-site":           {"/api/dvtp/query", "application/json", query, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"query from another port":    {"/api/dvtp/query", "application/json", query, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"start as text/plain":        {"/api/consent/start", "text/plain", start, nil, http.StatusUnsupportedMediaType},
		"start from another port":    {"/api/consent/start", "application/json", start, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"complete as text/plain":     {"/api/consent/complete", "text/plain;charset=UTF-8", complete, nil, http.StatusUnsupportedMediaType},
		"complete from another port": {"/api/consent/complete", "application/json", complete, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
	} {
		if resp := b.send(tc.path, tc.contentType, tc.body, tc.header); resp.StatusCode != tc.status {
			t.Errorf("%s: %d, want %d", name, resp.StatusCode, tc.status)
		}
	}
	if len(source.asked) != 0 || len(portal.pushed) != 2 {
		t.Fatalf("a refused request reached the source (%d) or the portal (%d pushes)", len(source.asked), len(portal.pushed))
	}

	// The refused complete left the request pending; the same origin still works.
	sameOrigin := map[string]string{"Sec-Fetch-Site": "same-origin"}
	if resp := b.send("/api/consent/complete", "application/json; charset=utf-8", complete, sameOrigin); resp.StatusCode != http.StatusOK {
		t.Errorf("same-origin complete: %d", resp.StatusCode)
	}
	if resp := b.send("/api/dvtp/query", "application/json", query, sameOrigin); resp.StatusCode != http.StatusOK || source.asked[0].ConsentToken != token {
		t.Errorf("same-origin query: %d", resp.StatusCode)
	}
	// A token in the body does not use the session: no change for callers
	// that send their own token, whatever their origin.
	body := `{"consent_token":"` + token + `","belastingjaren":[2025]}`
	if resp := b.send("/api/dvtp/query", "text/plain", body, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusOK {
		t.Errorf("query with its own token: %d", resp.StatusCode)
	}
}

func TestALargeBodyIsRefused(t *testing.T) {
	portal := &fakePortal{}
	b := newBrowser(t, NewMux(&consumer.Consumer{}, consentFlow(portal, false)))
	big := `{"service":"s","scopes":["bd:ib:2025"],"valid_until":"` + strings.Repeat("x", 70<<10) + `"}`
	for _, path := range []string{"/api/consent/start", "/api/consent/complete"} {
		if resp := b.send(path, "application/json", big, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", path, resp.StatusCode)
		}
	}
	if len(portal.pushed) != 0 {
		t.Error("a large start was pushed")
	}
}
