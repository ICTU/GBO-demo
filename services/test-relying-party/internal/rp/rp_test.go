package rp_test

// Cases D1 to D8 of the test relying party (#737): a wallet asked through
// the portal presents a copy of an attestation to one of two relying
// parties, and the timeline records which copy, to whom, with which status.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
	"gbo-demo/test-relying-party/internal/rp"
)

const (
	vct       = "https://source.test/types/income"
	statusURI = "https://issuer.test/status/tax/income"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

type fixture struct {
	clock   *clock
	issuer  testpki.Cert
	status  testpki.Cert
	svc     *rp.Service
	public  *httptest.Server
	revoked []int
	parties map[string]testpki.Cert
}

var signerName = pkix.Name{CommonName: "Tax", Organization: []string{"Tax"}}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{clock: &clock{now: time.Now().Truncate(time.Second)}, parties: map[string]testpki.Cert{}}
	root := testpki.NewRoot(t, "issuer root")
	f.issuer = root.Issue(t, testpki.Options{Subject: signerName, EKU: []asn1.ObjectIdentifier{oid4vp.CredentialSignerEKU}})
	f.status = root.Issue(t, testpki.Options{Subject: signerName, EKU: []asn1.ObjectIdentifier{statuslist.SignerEKU}})
	readerRoot := testpki.NewRoot(t, "reader root")
	registrar := testpki.NewRoot(t, "registrar root").Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "registrar"}})
	cfg := rp.Config{
		Credentials: []rp.Credential{{ID: "income", Name: "Income", VCT: vct,
			Claims: [][]string{{"year"}, {"income"}, {"address", "city"}}}},
		Anchors: []x5c.Anchors{x5c.NewAnchors("issuers", root.Cert)},
	}
	for _, id := range []string{"a", "b"} {
		wrpac := readerRoot.Issue(t, testpki.Options{Subject: testpki.OrganizationSubject("rp-"+id, "Test party "+id, "NTRNL-9999150"+id)})
		signer, err := oid4vp.NewRequestSigner(wrpac.Key, wrpac.Chain)
		if err != nil {
			t.Fatal(err)
		}
		reg := testwallet.VerifierRegistrationCertificate(t, registrar, "NTRNL-9999150"+id, vct, []string{"year"}, []string{"income"}, []string{"address", "city"})
		cfg.Parties = append(cfg.Parties, rp.Party{ID: id, Name: "Test party " + id, Signer: signer, Registration: string(reg)})
		f.parties[id] = wrpac
	}
	f.public = httptest.NewServer(nil)
	t.Cleanup(f.public.Close)
	cfg.PublicURL = f.public.URL
	f.svc = rp.New(cfg, func(_ context.Context, uri string) (string, error) {
		if uri != statusURI {
			return "", errors.New("unreachable")
		}
		return statuslist.Sign(statuslist.Token{URI: statusURI, IssuedAt: f.clock.Now(), Expires: f.clock.Now().Add(time.Hour),
			Size: 64, Revoked: f.revoked}, f.status.Key, x5c.Header(f.status.Chain))
	}, f.clock.Now)
	f.public.Config.Handler = rp.Public(f.svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return f
}

// credential issues an income credential bound to holder, with status
// index idx, valid until exp.
func (f *fixture) credential(t *testing.T, holder *ecdsa.PrivateKey, idx int, exp time.Time) string {
	t.Helper()
	s, err := sdjwtvc.NewSigner(f.issuer.Key, x5c.Header(f.issuer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.Issue(sdjwtvc.Credential{VCT: vct, IssuedAt: f.clock.Now().Add(-time.Hour), NotBefore: f.clock.Now().Add(-time.Hour),
		Expires: exp, HolderKey: &jose.JSONWebKey{Key: &holder.PublicKey}, Status: &sdjwtvc.StatusReference{Index: idx, URI: statusURI},
		Claims: map[string]any{"year": "2025", "income": 41234, "address": map[string]any{"city": "Utrecht"}}})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

func (f *fixture) create(t *testing.T, party string, claims ...[]string) rp.Created {
	t.Helper()
	c, err := f.svc.Create(rp.PresentationRequest{RelyingParty: party, Credential: "income", Claims: claims})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// request is what a wallet reads from the request object.
type request struct {
	ClientID    string `json:"client_id"`
	Nonce       string `json:"nonce"`
	State       string `json:"state"`
	ResponseURI string `json:"response_uri"`
	DCQL        struct {
		Credentials []struct {
			ID   string `json:"id"`
			Meta struct {
				VCTValues []string `json:"vct_values"`
			} `json:"meta"`
			Claims []struct {
				Path []string `json:"path"`
			} `json:"claims"`
		} `json:"credentials"`
	} `json:"dcql_query"`
	ClientMetadata struct {
		JWKS struct {
			Keys []jose.JSONWebKey `json:"keys"`
		} `json:"jwks"`
	} `json:"client_metadata"`
	VerifierInfo []struct{ Format, Data string } `json:"verifier_info"`
}

// fetch does what a wallet does with the link: GET request_uri.
func fetch(t *testing.T, c rp.Created) (request, int) {
	t.Helper()
	u, err := url.Parse(c.Link)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(u.Query().Get("request_uri"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return request{}, resp.StatusCode
	}
	parts := strings.Split(string(raw), ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var r request
	if err := json.Unmarshal(payload, &r); err != nil {
		t.Fatal(err)
	}
	return r, resp.StatusCode
}

// post sends a form to the response_uri.
func post(t *testing.T, uri string, form url.Values) int {
	t.Helper()
	resp, err := http.PostForm(uri, form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// present answers r with cred, disclosing keep, bound with holder and the
// given nonce.
func present(t *testing.T, r request, cred string, holder *ecdsa.PrivateKey, nonce string, keep ...string) int {
	t.Helper()
	pres := testwallet.Present(t, testwallet.Select(t, cred, keep...), holder, r.ClientID, nonce, time.Now())
	body, err := json.Marshal(map[string]any{"vp_token": map[string]any{"attestation": []string{pres}}, "state": r.State})
	if err != nil {
		t.Fatal(err)
	}
	jwe := testwallet.EncryptRequest(t, body, r.ClientMetadata.JWKS.Keys[0], jose.A128GCM, nil)
	return post(t, r.ResponseURI, url.Values{"response": {jwe}})
}

func fingerprint(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	jwk := jose.JSONWebKey{Key: &key.PublicKey}
	tp, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(tp)[:16]
}

func last(t *testing.T, f *fixture) rp.Event {
	t.Helper()
	events := f.svc.Events()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	return events[len(events)-1]
}

// D1: the catalog lists both parties and the registered claims; a request
// names the party's client_id and asks for exactly the chosen claims.
func TestCatalogAndRequest(t *testing.T) {
	f := newFixture(t)
	cat := f.svc.Catalog()
	if len(cat.RelyingParties) != 2 || len(cat.Credentials) != 1 || len(cat.Credentials[0].Claims) != 3 {
		t.Fatalf("catalog = %+v", cat)
	}
	c := f.create(t, "b", []string{"year"}, []string{"address", "city"})
	if !strings.HasPrefix(c.Link, "openid4vp://?client_id=") || c.Session == "" || !c.ExpiresAt.Equal(f.clock.Now().Add(rp.RequestTTL)) {
		t.Errorf("created = %+v", c)
	}
	r, status := fetch(t, c)
	if status != http.StatusOK {
		t.Fatalf("request object: %d", status)
	}
	if r.ClientID != cat.RelyingParties[1].ClientID || !strings.HasPrefix(r.ClientID, oid4vp.ClientIDPrefixHash) {
		t.Errorf("client_id = %q, want party b's %q", r.ClientID, cat.RelyingParties[1].ClientID)
	}
	q := r.DCQL.Credentials[0]
	if q.Meta.VCTValues[0] != vct || len(q.Claims) != 2 || q.Claims[1].Path[1] != "city" || len(r.VerifierInfo) != 1 {
		t.Errorf("dcql_query = %+v, verifier_info = %d", q, len(r.VerifierInfo))
	}
	for name, req := range map[string]rp.PresentationRequest{
		"unknown party":        {RelyingParty: "z", Credential: "income", Claims: [][]string{{"year"}}},
		"unknown credential":   {RelyingParty: "a", Credential: "pid", Claims: [][]string{{"year"}}},
		"no claims":            {RelyingParty: "a", Credential: "income"},
		"unregistered claim":   {RelyingParty: "a", Credential: "income", Claims: [][]string{{"bsn"}}},
		"the same claim twice": {RelyingParty: "a", Credential: "income", Claims: [][]string{{"year"}, {"year"}}},
	} {
		if _, err := f.svc.Create(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// D2, D3, D6: two copies presented to the two parties; each event names
// the party, the claims asked and the copy's fingerprint, with no values.
func TestPresentedCopies(t *testing.T) {
	f := newFixture(t)
	key1, key2 := testpki.NewKey(t), testpki.NewKey(t)
	exp := f.clock.Now().Add(24 * time.Hour)
	for _, tc := range []struct {
		party string
		key   *ecdsa.PrivateKey
	}{{"a", key1}, {"b", key2}} {
		c := f.create(t, tc.party, []string{"year"})
		r, _ := fetch(t, c)
		if status := present(t, r, f.credential(t, tc.key, 3, exp), tc.key, r.Nonce, "year"); status != http.StatusOK {
			t.Fatalf("%s: response %d", tc.party, status)
		}
		e := last(t, f)
		if e.Kind != rp.EventPresented || e.RelyingParty != tc.party || e.RelyingPartyName != "Test party "+tc.party ||
			e.Configuration != "income" || e.VCT != vct || strings.Join(e.Claims, ",") != "year" ||
			e.Status != oid4vp.StatusValid || e.KeyFingerprint != fingerprint(t, tc.key) || e.Session != c.Session {
			t.Errorf("%s: event = %+v", tc.party, e)
		}
	}
	raw, _ := json.Marshal(f.svc.Events())
	for _, secret := range []string{"2025", "41234", "Utrecht"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("D6: the events hold the value %q", secret)
		}
	}
}

// D4: a revoked or expired copy is recorded as presented, with its status.
func TestPresentedStatus(t *testing.T) {
	f := newFixture(t)
	key := testpki.NewKey(t)
	f.revoked = []int{5}
	c := f.create(t, "a", []string{"year"})
	r, _ := fetch(t, c)
	if status := present(t, r, f.credential(t, key, 5, f.clock.Now().Add(time.Hour)), key, r.Nonce, "year"); status != http.StatusOK {
		t.Fatalf("revoked: response %d", status)
	}
	if e := last(t, f); e.Kind != rp.EventPresented || e.Status != oid4vp.StatusRevoked {
		t.Errorf("revoked: %+v", e)
	}
	c = f.create(t, "a", []string{"year"})
	r, _ = fetch(t, c)
	if status := present(t, r, f.credential(t, key, 6, f.clock.Now().Add(-time.Hour)), key, r.Nonce, "year"); status != http.StatusOK {
		t.Fatalf("expired: response %d", status)
	}
	if e := last(t, f); e.Kind != rp.EventPresented || e.Status != oid4vp.StatusExpired {
		t.Errorf("expired: %+v", e)
	}
}

// D5: the wallet declines.
func TestDeclined(t *testing.T) {
	f := newFixture(t)
	c := f.create(t, "a", []string{"year"})
	r, _ := fetch(t, c)
	if status := post(t, r.ResponseURI, url.Values{"error": {"access_denied"}, "state": {r.State}}); status != http.StatusOK {
		t.Fatalf("decline: %d", status)
	}
	if e := last(t, f); e.Kind != rp.EventDeclined || e.Error != "access_denied" || e.KeyFingerprint != "" {
		t.Errorf("declined: %+v", e)
	}
}

// D7: after five minutes the request is gone: the link fails and nothing
// is recorded. A request is answered once.
func TestExpiredAndAnsweredRequests(t *testing.T) {
	f := newFixture(t)
	c := f.create(t, "a", []string{"year"})
	r, _ := fetch(t, c)
	f.clock.add(rp.RequestTTL)
	if _, status := fetch(t, c); status != http.StatusNotFound {
		t.Errorf("expired request object: %d, want 404", status)
	}
	if status := post(t, r.ResponseURI, url.Values{"error": {"access_denied"}}); status != http.StatusBadRequest {
		t.Errorf("expired response: %d, want 400", status)
	}
	if n := len(f.svc.Events()); n != 0 {
		t.Errorf("D7: %d events recorded for an expired request", n)
	}
	c = f.create(t, "a", []string{"year"})
	r, _ = fetch(t, c)
	post(t, r.ResponseURI, url.Values{"error": {"access_denied"}})
	if status := post(t, r.ResponseURI, url.Values{"error": {"access_denied"}}); status != http.StatusBadRequest {
		t.Errorf("second answer: %d, want 400", status)
	}
	if n := len(f.svc.Events()); n != 1 {
		t.Errorf("answered twice: %d events, want 1", n)
	}
}

// D8: a presentation that does not verify is rejected with the reason.
func TestRejected(t *testing.T) {
	f := newFixture(t)
	key := testpki.NewKey(t)
	cred := f.credential(t, key, 3, f.clock.Now().Add(time.Hour))
	for name, tc := range map[string]struct {
		nonce  func(request) string
		keep   []string
		reason string
	}{
		"another nonce":       {func(request) string { return "other" }, []string{"year"}, "nonce"},
		"more than was asked": {func(r request) string { return r.Nonce }, []string{"year", "income"}, "income"},
	} {
		t.Run(name, func(t *testing.T) {
			c := f.create(t, "a", []string{"year"})
			r, _ := fetch(t, c)
			if status := present(t, r, cred, key, tc.nonce(r), tc.keep...); status != http.StatusBadRequest {
				t.Errorf("response %d, want 400", status)
			}
			e := last(t, f)
			if e.Kind != rp.EventRejected || e.Error != "invalid_presentation" || !strings.Contains(e.ErrorDescription, tc.reason) || e.KeyFingerprint != "" {
				t.Errorf("rejected: %+v, want a reason with %q", e, tc.reason)
			}
		})
	}
}

// The timeline keeps 50 minutes.
func TestRetention(t *testing.T) {
	f := newFixture(t)
	c := f.create(t, "a", []string{"year"})
	r, _ := fetch(t, c)
	post(t, r.ResponseURI, url.Values{"error": {"access_denied"}})
	f.clock.add(rp.Retention + time.Second)
	if n := len(f.svc.Events()); n != 0 {
		t.Errorf("%d events after the retention", n)
	}
}
