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
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gbo-demo/qtsp-reference/internal/web"
)

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
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func TestRequestAPI(t *testing.T) {
	s := startStack(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	// An attribute the QTSP offers but no data service provides (Q3).
	const idShoeSize = "https://gbo.example/attributes/shoe_size/1"
	attributes := append(append([]web.Attribute{}, web.DemoAttributes...), web.Attribute{Identifier: idShoeSize, Label: "Schoenmaat", Example: `{"shoe_size":42}`})
	srv := &http.Server{Handler: web.NewHandler(s.client(t, base+"/callback", s.sealAnchor, registryURI), attributes, base)}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	api := func(t *testing.T, method, path, body string) (int, apiRequest) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
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
	// run opens the start URL in a new browser, picks person at the chooser
	// and decides, and returns the request as the API then shows it.
	run := func(t *testing.T, created apiRequest, person, decision string) apiRequest {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, _ := fetch(t, browser, http.MethodGet, created.StartURL, nil)
		authorization := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusFound || authorization == "" {
			t.Fatalf("start URL = %d", resp.StatusCode)
		}
		if _, pending := api(t, http.MethodGet, "/api/requests/"+created.ID, ""); pending.Status != "pending" || !pending.Started {
			t.Fatalf("Q5: while the user is at the authorization server: %+v", pending)
		}
		_, page := fetch(t, browser, http.MethodGet, authorization, nil)
		tx := regexp.MustCompile(`name="transaction" value="([^"]+)"`).FindStringSubmatch(page)
		action := regexp.MustCompile(`<form method="post" action="([^"]+)"`).FindStringSubmatch(page)
		if tx == nil || action == nil {
			t.Fatalf("no chooser:\n%s", page)
		}
		u, _ := url.Parse(authorization)
		post, _ := u.Parse(action[1])
		resp, _ = fetch(t, browser, http.MethodPost, post.String(), url.Values{"transaction": {tx[1]}, "person": {person}, "decision": {decision}})
		if resp, _ := fetch(t, browser, http.MethodGet, resp.Header.Get("Location"), nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("callback = %d", resp.StatusCode)
		}
		_, got := api(t, http.MethodGet, "/api/requests/"+created.ID, "")
		return got
	}
	create := func(t *testing.T, body string) apiRequest {
		t.Helper()
		status, created := api(t, http.MethodPost, "/api/requests", body)
		if status != http.StatusCreated || created.ID == "" || created.Status != "pending" {
			t.Fatalf("Q1: POST /api/requests = %d %+v", status, created)
		}
		return created
	}
	verifyFamilyName := func(value string) string {
		b, _ := json.Marshal(map[string]any{"operation": "verify", "attributes": []map[string]any{{"identifier": idFamilyName, "value": json.RawMessage(value)}}})
		return string(b)
	}

	t.Run("Q1 created, the start URL carries only an id", func(t *testing.T) {
		created := create(t, verifyFamilyName(`{"family_name":"Jansen"}`))
		if created.StartURL != base+"/requests/"+created.ID+"/start" || strings.Contains(created.StartURL, "Jansen") {
			t.Errorf("start URL %q", created.StartURL)
		}
	})
	t.Run("Q2 invalid requests", func(t *testing.T) {
		for name, body := range map[string]string{
			"not JSON":           `{"operation":`,
			"unknown field":      `{"operation":"verify","attributes":[],"bsn":"1"}`,
			"no operation":       `{"attributes":[{"identifier":"` + idFamilyName + `","value":{"family_name":"Jansen"}}]}`,
			"no attributes":      `{"operation":"verify","attributes":[]}`,
			"unknown attribute":  `{"operation":"verify","attributes":[{"identifier":"https://gbo.example/attributes/unknown/1","value":{}}]}`,
			"verify, no value":   `{"operation":"verify","attributes":[{"identifier":"` + idFamilyName + `"}]}`,
			"the same attribute": `{"operation":"verify","attributes":[{"identifier":"` + idFamilyName + `","value":{"family_name":"Jansen"}},{"identifier":"` + idFamilyName + `","value":{"family_name":"Jansen"}}]}`,
		} {
			if status, got := api(t, http.MethodPost, "/api/requests", body); status != http.StatusBadRequest || got.Detail == "" {
				t.Errorf("%s: %d %+v, want 400 with a reason", name, status, got)
			}
		}
	})
	t.Run("Q3 no data service, refused before the authorization server", func(t *testing.T) {
		status, got := api(t, http.MethodPost, "/api/requests", `{"operation":"verify","attributes":[{"identifier":"`+idShoeSize+`","value":{"shoe_size":42}}]}`)
		if status != http.StatusUnprocessableEntity || !strings.Contains(got.Detail, "Schoenmaat") {
			t.Errorf("%d %+v, want 422 naming the attribute", status, got)
		}
	})
	t.Run("Q4 the start URL works once", func(t *testing.T) {
		created := create(t, verifyFamilyName(`{"family_name":"Jansen"}`))
		browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if resp, _ := fetch(t, browser, http.MethodGet, created.StartURL, nil); resp.StatusCode != http.StatusFound {
			t.Fatalf("first open = %d, want a redirect to the authorization server", resp.StatusCode)
		}
		if resp, _ := fetch(t, browser, http.MethodGet, created.StartURL, nil); resp.StatusCode != http.StatusGone {
			t.Errorf("second open = %d, want 410", resp.StatusCode)
		}
	})
	t.Run("Q5 Q6 verify: the outcome per attribute", func(t *testing.T) {
		for _, tc := range []struct{ person, value, want string }{
			{"frouke", `{"family_name":"Jansen"}`, "Match"},
			{"frouke", `{"family_name":"jansen"}`, "MatchWithVariation"},
			{"frouke", `{"family_name":"Janssen"}`, "NoMatch"},
		} {
			got := run(t, create(t, verifyFamilyName(tc.value)), tc.person, "approve")
			if got.Status != "done" || got.Seal == nil || !got.Seal.Valid || len(got.Results) != 1 || got.Results[0].Result != tc.want {
				t.Errorf("%s: %+v, want done, a valid seal and %s", tc.value, got, tc.want)
			}
		}
		created := create(t, `{"operation":"verify","attributes":[{"identifier":"`+idAddress+`","value":{"street":"Coolsingel"}}]}`)
		if got := run(t, created, "tom", "approve"); got.Status != "done" || len(got.Results) != 1 || got.Results[0].Result != "Unknown" {
			t.Errorf("address of Tom: %+v, want Unknown", got)
		}
	})
	t.Run("Q6 retrieve: the value", func(t *testing.T) {
		created := create(t, `{"operation":"retrieve","attributes":[{"identifier":"https://gbo.example/attributes/nationality/1"}]}`)
		got := run(t, created, "frouke", "approve")
		if got.Status != "done" || len(got.Results) != 1 || !bytes.Contains(got.Results[0].Value, []byte(`"BE"`)) {
			t.Errorf("%+v, want the nationalities", got)
		}
	})
	t.Run("Q7 denied, nothing asked of the source", func(t *testing.T) {
		created := create(t, verifyFamilyName(`{"family_name":"Jansen"}`))
		before := brpCalls.Load()
		got := run(t, created, "frouke", "deny")
		if got.Status != "denied" || got.Error != "access_denied" || got.Results != nil {
			t.Errorf("%+v, want denied with access_denied", got)
		}
		if brpCalls.Load() != before {
			t.Error("the source was asked after a denial")
		}
	})
	t.Run("Q8 unknown id", func(t *testing.T) {
		if status, _ := api(t, http.MethodGet, "/api/requests/not-a-request", ""); status != http.StatusNotFound {
			t.Errorf("GET = %d, want 404", status)
		}
		if resp, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/requests/not-a-request/start", nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("start = %d, want 404", resp.StatusCode)
		}
	})
	t.Run("the attributes the form offers", func(t *testing.T) {
		resp, body := fetch(t, http.DefaultClient, http.MethodGet, base+"/api/attributes", nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, idFamilyName) || !strings.Contains(body, `"label":"Familienaam"`) {
			t.Errorf("%d %s", resp.StatusCode, body)
		}
	})
}
