package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/digidmock"
)

// Z1, Z3: every page of the authorization server takes its style sheets
// from this server, has no script and no inline style, and the pages of the
// authorization server itself carry the theme and the test environment
// banner. The DigiD mock imitates DigiD and has its own style sheet.
func TestPagesUseOwnStyleSheetsAndNoScript(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	walletPath, session := as.startWallet(t, tx)
	_, wallet := as.get(t, walletPath, tx.cookie)
	as.wallet.set(session, func(s *fakeWalletSession) { s.progressErr = errors.New("connection refused") })
	unavailableResp, unavailable := as.get(t, walletPath, tx.cookie)
	digidResp, digid := as.get(t, digidmock.Path+"?transaction="+tx.id, tx.cookie)
	params := authorizeParams(newPKCE())
	params.Set("client_id", "unknown-client")
	errorResp, errorBody := as.get(t, authorization.AuthorizePath+"?"+params.Encode(), nil)

	theme := []string{
		`<html lang="nl" class="voorbeeld-theme">`,
		`<link rel="stylesheet" href="/static/nlds/voorbeeld-theme.css">`,
		`<link rel="stylesheet" href="/static/nlds/utrecht-components.css">`,
		`<link rel="stylesheet" href="/static/azsp.css">`,
		"Testomgeving — geen echte identificatie",
	}
	for _, p := range []struct {
		name   string
		header http.Header
		page   string
		want   []string
	}{
		{"request", tx.header, tx.page, theme},
		{"wallet", nil, wallet, theme},
		{"unavailable", unavailableResp.Header, unavailable, theme},
		{"error", errorResp.Header, errorBody, theme},
		{"digid", digidResp.Header, digid, []string{`<link rel="stylesheet" href="/static/digid-mock.css">`, "Mock van DigiD — testomgeving"}},
	} {
		for _, want := range p.want {
			if !strings.Contains(p.page, want) {
				t.Errorf("%s page lacks %q:\n%s", p.name, want, p.page)
			}
		}
		for _, banned := range []string{"<script", "<style", "style=", "http://", "https://"} {
			if strings.Contains(p.page, banned) {
				t.Errorf("%s page has %q:\n%s", p.name, banned, p.page)
			}
		}
		if p.header == nil {
			continue
		}
		csp := p.header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "style-src 'self';") ||
			strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "script-src") || strings.Contains(csp, "http") {
			t.Errorf("%s page Content-Security-Policy = %q, want style sheets from this server only and no script", p.name, csp)
		}
	}
}

// Z1: the style sheets are served from this server, as CSS, and nothing
// else is: no directory listing, no other file.
func TestStaticStyleSheets(t *testing.T) {
	as := newTestAS(t)
	for _, path := range []string{"/static/nlds/voorbeeld-theme.css", "/static/nlds/utrecht-components.css", "/static/azsp.css", "/static/digid-mock.css"} {
		resp, body := as.get(t, path, nil)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") ||
			resp.Header.Get("X-Content-Type-Options") != "nosniff" || len(body) == 0 {
			t.Errorf("GET %s = %d, %q, nosniff %q, %d bytes; want CSS", path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Content-Type-Options"), len(body))
		}
	}
	// A changed file reaches the browser at once: no-cache with an ETag of
	// the content, and 304 while it is unchanged.
	resp, _ := as.get(t, "/static/digid-mock.css", nil)
	etag := resp.Header.Get("ETag")
	if resp.Header.Get("Cache-Control") != "no-cache" || len(etag) < 10 {
		t.Errorf("Cache-Control %q, ETag %q; want no-cache and an ETag", resp.Header.Get("Cache-Control"), etag)
	}
	req, _ := http.NewRequest(http.MethodGet, as.srv.URL+"/static/digid-mock.css", nil)
	req.Header.Set("If-None-Match", etag)
	if again, err := http.DefaultClient.Do(req); err != nil || again.StatusCode != http.StatusNotModified {
		t.Errorf("unchanged with If-None-Match = %v, %v; want 304", again, err)
	} else {
		_ = again.Body.Close()
	}
	for _, path := range []string{"/static/rijkswapen.png", "/static/Logo_of_DigiD.png"} {
		if resp, body := as.get(t, path, nil); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || len(body) == 0 {
			t.Errorf("GET %s = %d, %q; want the DigiD mock's image", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	for _, path := range []string{"/static/", "/static/nlds/", "/static/missing.css", "/static/nlds/README.md", "/static/../pages.go"} {
		if resp, _ := as.get(t, path, nil); resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want no file", path)
		}
	}
}

// Z2: the request page says in plain language who asks what at which
// source, for each operation the client may ask.
func TestRequestPageInPlainLanguage(t *testing.T) {
	as := newTestAS(t)
	for _, tc := range []struct {
		scope         string
		lead, request string
	}{
		{"verify", "wil bij Basisregistratie Personen (test) laten controleren of gegevens over u kloppen.", "<dd>Controleren of gegevens kloppen</dd>"},
		{"retrieve", "wil bij Basisregistratie Personen (test) gegevens over u opvragen.", "<dd>Gegevens opvragen</dd>"},
		{"verify retrieve", "wil bij Basisregistratie Personen (test) laten controleren of gegevens over u kloppen en gegevens over u opvragen.", "<dd>Controleren of gegevens kloppen en gegevens opvragen</dd>"},
	} {
		params := authorizeParams(newPKCE())
		params.Set("scope", tc.scope)
		setAuthorizationDetails(params, "https://gbo.example/attributes/family_name/1")
		tx := as.startAuthorization(t, params)
		for _, want := range []string{
			"<strong>" + testClientName + "</strong> " + tc.lead,
			"<dd>" + testClientName + ", een vertrouwensdienst</dd>",
			tc.request,
			"<dd>Basisregistratie Personen (test)</dd>",
			"<li>Achternaam</li>",
			"U logt in bij de bron. Daarmee geeft u " + testClientName + " toestemming voor dit verzoek.",
		} {
			if !strings.Contains(tx.page, want) {
				t.Errorf("scope %q: request page lacks %q:\n%s", tc.scope, want, tx.page)
			}
		}
	}
}

func TestRequestPageWithoutAuthorizationDetailsOmitsAttributes(t *testing.T) {
	as := newTestAS(t)
	for _, scope := range []string{"verify", "retrieve", "verify retrieve"} {
		params := authorizeParams(newPKCE())
		params.Set("scope", scope)
		params.Del(authorization.AuthorizationDetailsClaim)
		page := as.startAuthorization(t, params).page
		for _, want := range []string{testClientName, "Basisregistratie Personen (test)", "<dt>Verzoek</dt>"} {
			if !strings.Contains(page, want) {
				t.Errorf("scope %q: request page lacks %q:\n%s", scope, want, page)
			}
		}
		if strings.Contains(page, "<dt>Gegevens</dt>") || strings.Contains(page, "azsp-attributes") {
			t.Errorf("scope %q: operation-only request page shows an attribute list:\n%s", scope, page)
		}
	}
}
