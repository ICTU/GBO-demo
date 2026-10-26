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
// authorization server itself carry its government look (the coat of arms)
// and the test environment banner. The DigiD mock imitates DigiD and has its own style sheet.
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
		`<link rel="stylesheet" href="/static/azsp.css">`,
		`<img class="as-wapen" src="/static/rijkswapen.png" alt="Rijkswapen">`,
		"Inloggen en toestemming geven",
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
	for _, path := range []string{"/static/azsp.css", "/static/digid-mock.css"} {
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
	for _, path := range []string{"/static/", "/static/missing.css", "/static/nlds/voorbeeld-theme.css", "/static/../pages.go"} {
		if resp, _ := as.get(t, path, nil); resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want no file", path)
		}
	}
}

// Z2, T1, T3, T4: the request page names the QTSP that gets consent, says in
// plain language what it asks at which source, and that the user logs in at
// this service, not at the QTSP.
func TestRequestPageInPlainLanguage(t *testing.T) {
	as := newTestAS(t)
	for _, tc := range []struct {
		scope, lead, what, note string
	}{
		{"verify", "bij Basisregistratie Personen (test) laten controleren of uw gegevens kloppen.", "<dd>Controleren of uw gegevens kloppen</dd>",
			testClientName + " hoort alleen of uw gegevens kloppen. Uw gegevens uit Basisregistratie Personen (test) krijgt " + testClientName + " niet te zien."},
		{"retrieve", "bij Basisregistratie Personen (test) gegevens over u opvragen.", "<dd>Gegevens over u opvragen</dd>",
			testClientName + " krijgt deze gegevens te zien."},
		{"verify retrieve", "bij Basisregistratie Personen (test) laten controleren of uw gegevens kloppen en gegevens over u opvragen.",
			"<dd>Controleren of uw gegevens kloppen en gegevens over u opvragen</dd>", testClientName + " krijgt deze gegevens te zien."},
	} {
		params := authorizeParams(newPKCE())
		params.Set("scope", tc.scope)
		setAuthorizationDetails(params, "https://gbo.example/attributes/family_name/1")
		tx := as.startAuthorization(t, params)
		for _, want := range []string{
			"<h1>Toestemming voor " + testClientName + "</h1>",
			"U vraagt bij " + testClientName + " een verklaring aan. Daarvoor wil " + testClientName + " " + tc.lead,
			"<dt>Wie vraagt het</dt><dd>" + testClientName + ", een vertrouwensdienst</dd>",
			"<dt>Wat</dt>" + tc.what,
			"<dt>Bij</dt><dd>Basisregistratie Personen (test)</dd>",
			"<dt>Welke gegevens</dt><dd><ul><li>Achternaam</li></ul></dd>",
			tc.note,
			"<h2>Bevestig wie u bent</h2>",
			"Log in om te bevestigen dat het om uw gegevens gaat. Daarmee geeft u " + testClientName + " toestemming voor alleen dit verzoek. U logt in bij deze inlogvoorziening, niet bij " + testClientName + ". Daarna gaat u terug naar " + testClientName + ".",
			"Er wordt dan niets bij Basisregistratie Personen (test) gevraagd en u gaat terug naar " + testClientName + ".",
		} {
			if !strings.Contains(tx.page, want) {
				t.Errorf("scope %q: request page lacks %q:\n%s", tc.scope, want, tx.page)
			}
		}
	}
}

// T2: without authorization details the page names no attributes and makes
// no promise about them.
func TestRequestPageWithoutAuthorizationDetailsOmitsAttributes(t *testing.T) {
	as := newTestAS(t)
	for _, scope := range []string{"verify", "retrieve", "verify retrieve"} {
		params := authorizeParams(newPKCE())
		params.Set("scope", scope)
		params.Del(authorization.AuthorizationDetailsClaim)
		page := as.startAuthorization(t, params).page
		for _, want := range []string{"<h1>Toestemming voor " + testClientName + "</h1>", "Basisregistratie Personen (test)", "<dt>Wat</dt>", "Bevestig wie u bent"} {
			if !strings.Contains(page, want) {
				t.Errorf("scope %q: request page lacks %q:\n%s", scope, want, page)
			}
		}
		for _, unwanted := range []string{"<dt>Welke gegevens</dt>", "hoort alleen of uw gegevens kloppen", "krijgt deze gegevens te zien"} {
			if strings.Contains(page, unwanted) {
				t.Errorf("scope %q: operation-only request page has %q:\n%s", scope, unwanted, page)
			}
		}
	}
}
