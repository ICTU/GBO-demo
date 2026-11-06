package httpapi_test

// The authorization code flow against the real authorization server (#614,
// cases E2 and E3): asi-authorization-server is built and started as a
// process; the issuer registers the provider there with its access
// certificate, the citizen logs in with the DigiD mock, and the wallet gets
// its credential.

import (
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/login"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// loginFixture is a fixture whose eudi profile logs in at a started
// authorization server.
func loginFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and starts the authorization server")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "asi-authorization-server")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("..", "..", "..", "asi-authorization-server")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the authorization server: %v\n%s", err, out)
	}
	addr := freeAddr(t)
	server := "http://" + addr
	var logs syncBuffer
	f := newFixtureWith(t, func(f *fixture, cfg *issuance.Config) {
		anchors := filepath.Join(dir, "anchors.pem")
		if err := os.WriteFile(anchors, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.accessRoot.Cert.Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		register := filepath.Join(dir, "register.json")
		entry := `{"entries":[{"registryURI":"https://register.test/wrp/tax","identifiers":["NTRNL-tax"],"legalName":"Belastingdienst","entitlements":["https://uri.etsi.org/19475/Entitlement/PUB_EAA_Provider"]}]}`
		if err := os.WriteFile(register, []byte(entry), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin)
		cmd.Dir = build.Dir
		cmd.Env = append(os.Environ(), "AZSP_ADDR="+addr, "AZSP_ISSUER="+server,
			"AZSP_RESOURCES=http://asip.test/brp/authsrc-api=Basisregistratie Personen (mock)",
			"AZSP_ISSUANCE_RESOURCES="+f.public.URL+"/tax/eudi=NTRNL-tax=Belastingdienst",
			"AZSP_TRUST_ANCHORS_FILE="+anchors, "AZSP_REGISTER_FILE="+register)
		cmd.Stdout, cmd.Stderr = &logs, &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		if err := waitHealthy(server + "/healthz"); err != nil {
			t.Fatalf("authorization server: %v\n%s", err, logs.String())
		}
		var providers []login.Provider
		for _, p := range cfg.Providers {
			providers = append(providers, login.Provider{Name: p.Name, Key: p.AccessKey, Chain: p.AccessChain,
				RegistryURI: "https://register.test/wrp/" + p.Name, RedirectURI: f.public.URL + "/" + p.Name + "/eudi/callback"})
		}
		client, err := login.New(server, &http.Client{Timeout: 10 * time.Second}, providers)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Login = client
		f.loginBase = server + "/authorize?"
	})
	return f, server
}

var transactionField = regexp.MustCompile(`name="transaction" value="([^"]+)"`)

// digidLogin follows the login at the authorization server from loginURL:
// the request page, the choice of the DigiD mock and its login with bsn. It
// returns the redirect back to the issuer.
func digidLogin(t *testing.T, loginURL, bsn string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "Toestemming voor Belastingdienst") {
		t.Fatalf("request page = %d:\n%s", resp.StatusCode, page)
	}
	m := transactionField.FindStringSubmatch(string(page))
	if m == nil {
		t.Fatalf("no transaction on the request page:\n%s", page)
	}
	base, _ := url.Parse(loginURL)
	post := func(path string, form url.Values) *http.Response {
		form.Set("transaction", m[1])
		r, err := browser.PostForm(base.Scheme+"://"+base.Host+path, form)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r
	}
	if r := post("/authorize/decision", url.Values{"decision": {"digid"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("choosing DigiD = %d", r.StatusCode)
	}
	r := post("/authorize/digid", url.Values{"username": {"j.dejong"}, "bsn": {bsn}})
	if r.StatusCode != http.StatusFound {
		t.Fatalf("DigiD login = %d", r.StatusCode)
	}
	return r.Header.Get("Location")
}

func TestTheFlowWithTheRealAuthorizationServer(t *testing.T) { // E2
	f, _ := loginFixture(t)
	w := f.acfWallet()
	back := digidLogin(t, w.authorize(w.requestURI(nil)), loginBSN)
	if !strings.HasPrefix(back, f.id("eudi")+"/callback?") {
		t.Fatalf("the login returned to %q, want the issuer's callback", back)
	}
	status, location, body := browse(t, back)
	q := walletReturn(t, status, location)
	if q.Get("code") == "" || q.Get("state") != w.state {
		t.Fatalf("callback = %d %q %s", status, location, body)
	}
	// The source got the BSN from the login.
	if b := string(f.lastSourceBody()); !strings.Contains(b, loginBSN) || !strings.Contains(b, `"origin":"login"`) {
		t.Fatalf("source request = %s", b)
	}
	status, tokBody := w.tokenForCode(q.Get("code"), nil, nil)
	if status != http.StatusOK {
		t.Fatalf("token = %d %v", status, tokBody)
	}
	var tok oid4vci.TokenResponse
	remarshal(t, tokBody, &tok)
	if len(tok.AuthorizationDetails) != 1 {
		t.Fatalf("token = %+v", tok)
	}

	// A second login reuses the registration.
	w2 := f.acfWallet()
	back = digidLogin(t, w2.authorize(w2.requestURI(nil)), loginBSN)
	if status, location, _ := browse(t, back); status != http.StatusFound || !strings.Contains(location, "code=") {
		t.Fatalf("second login callback = %d %q", status, location)
	}
}

func TestNoDataWithTheRealAuthorizationServer(t *testing.T) { // E3
	f, _ := loginFixture(t)
	f.setSource(http.StatusNotFound, nil)
	w := f.acfWallet()
	back := digidLogin(t, w.authorize(w.requestURI(nil)), "123456789")
	status, location, body := browse(t, back)
	if status != http.StatusOK || location != "" || !strings.Contains(body, "Geen gegevens gevonden") {
		t.Fatalf("callback = %d %q:\n%s", status, location, body)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitHealthy(u string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(u); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s did not become healthy", u)
}
