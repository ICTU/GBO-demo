package httpapi

// The test-person chooser of #523 (cases S1-S10). S2 is in
// TestFullFlowIssuesDPoPBoundAccessToken, S11 in the testpersons package,
// S12 and S13 in e2e_test.go.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChooserPage(t *testing.T) { // S1
	as := newTestAS(t)
	params := authorizeParams(newPKCE())
	params.Del("login_hint")
	tx := as.startAuthorization(t, params)
	for _, want := range []string{"Testomgeving — geen echte identificatie", "Frouke Jansen", "Tom Willems", testClientID, "laten verifiëren", `value="approve"`, `value="deny"`} {
		if !strings.Contains(tx.page, want) {
			t.Errorf("chooser lacks %q", want)
		}
	}
	if strings.Contains(tx.page, "checked") {
		t.Errorf("a test person is preselected without login_hint")
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "X-Frame-Options": "DENY"} {
		if got := tx.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := tx.header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", csp)
	}
	if c := tx.cookie; !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/authorize" {
		t.Errorf("transaction cookie = %+v, want HttpOnly, SameSite=Strict, Path /authorize", c)
	}
}

func TestDenyIsAccessDenied(t *testing.T) { // S3
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	assertAuthorizeError(t, as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"deny"}}), "access_denied")
}

func TestDecisionThatCannotBeRedirected(t *testing.T) { // S5-S9
	approve := func(person string) url.Values { return url.Values{"person": {person}, "decision": {"approve"}} }
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, as *testAS) *http.Response
	}{
		{"S5: unknown transaction", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, "unknown", tx.cookie, approve("frouke"))
		}},
		{"S6: transaction used before", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			if resp := as.decide(t, tx.id, tx.cookie, approve("frouke")); resp.StatusCode != http.StatusFound {
				t.Fatalf("first decision = %d, want 302", resp.StatusCode)
			}
			return as.decide(t, tx.id, tx.cookie, approve("frouke"))
		}},
		{"S7: transaction expired", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			as.offset = 5*time.Minute + time.Second
			return as.decide(t, tx.id, tx.cookie, approve("frouke"))
		}},
		{"S8: no transaction cookie", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, nil, approve("frouke"))
		}},
		{"S8b: the cookie of another transaction", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			other := as.startAuthorization(t, authorizeParams(newPKCE()))
			c := *other.cookie
			c.Name = tx.cookie.Name
			return as.decide(t, tx.id, &c, approve("frouke"))
		}},
		{"S9: unknown test person", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, tx.cookie, approve("nobody"))
		}},
		{"decision other than approve or deny", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, tx.cookie, url.Values{"person": {"frouke"}, "decision": {"maybe"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.run(t, newTestAS(t))
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Fatalf("decision = %d, Location %q; want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

func TestLoginHintOnlyPreselects(t *testing.T) { // S10
	as := newTestAS(t)
	params := authorizeParams(newPKCE())
	params.Set("login_hint", "tom")
	tx := as.startAuthorization(t, params)
	if !strings.Contains(tx.page, `value="tom" required checked`) || strings.Contains(tx.page, `value="frouke" required checked`) {
		t.Fatalf("chooser does not preselect only Tom:\n%s", tx.page)
	}
}
