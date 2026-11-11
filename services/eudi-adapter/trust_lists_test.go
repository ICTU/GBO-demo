package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/lotefiles"
	"gbo-demo/eudi-adapter/internal/lotesigning"
	"gbo-demo/eudi-adapter/internal/nlconfig"
	"gbo-demo/eudi-adapter/internal/trustlists"
	"github.com/go-jose/go-jose/v4"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type loteSourceFunc func(context.Context) (trustlists.Snapshot, error)

func (f loteSourceFunc) Load(ctx context.Context) (trustlists.Snapshot, error) { return f(ctx) }

type loteFixture struct {
	options  trustListOptions
	now      time.Time
	snapshot trustlists.Snapshot
	signer   lotesigning.Signer
	nlKey    *ecdsa.PrivateKey
	nlPID    []byte
	config   map[string]any
}

func newLoTEFixture(t *testing.T) *loteFixture {
	t.Helper()
	f := newWalletRegistrationFixture(t)
	options := trustListOptions{rootDir: t.TempDir(), publicDir: t.TempDir(), publishDir: t.TempDir(), secretsDir: f.options.secretsDir, publicURL: testTrustURL, validDays: 30}
	if err := provisionTrustListSigner(options, f.now); err != nil {
		t.Fatal(err)
	}
	signer, err := readTrustListSigner(options, f.now, f.now.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	root := func(name string) []byte {
		dir := t.TempDir()
		if err := createDevelopmentRoot(dir, "fixture", pkix.Name{CommonName: name}, f.now, rand.Reader); err != nil {
			t.Fatal(err)
		}
		cert, err := lotefiles.ReadCertificate(filepath.Join(dir, "fixture-ca-cert.pem"))
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	gbo := map[trustlists.Role][]byte{trustlists.PID: root("GBO test-PID root"), trustlists.PubEAA: root("ca.gbo-issuer"),
		trustlists.WRPAC: f.caCert(t, "reader").Raw, trustlists.WRPRC: f.caCert(t, "registrar").Raw}
	for role, name := range map[trustlists.Role]string{trustlists.PID: "test-pid", trustlists.PubEAA: "issuer", trustlists.WRPAC: "reader", trustlists.WRPRC: "registrar"} {
		dir := filepath.Join(options.publicDir, "development-ca")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(gbo[role])
		if err := os.WriteFile(filepath.Join(dir, name+"-ca-cert.pem"), pemCertificate(cert.Raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nlPID, nlIssuer, nlReader := root("issuance-ca.nonprod.wallet.edi.bzk.nl"), root("NL attestation test CA"), root("NL reader test CA")
	b64 := base64.StdEncoding.EncodeToString
	config := map[string]any{"version": 49, "environment": "preproductie", "issuer_trust_anchors": []string{b64(nlPID), b64(nlIssuer), b64(gbo[trustlists.PubEAA])},
		"disclosure": map[string]any{"rp_trust_anchors": []string{b64(nlReader), b64(gbo[trustlists.WRPAC])}}}
	fixture := &loteFixture{options: options, now: f.now, signer: signer, nlKey: key, nlPID: nlPID, config: config}
	verified, err := fixture.nlSource().Verify(fixture.signed(t, key))
	if err != nil {
		t.Fatal(err)
	}
	verified.GBO = gbo
	fixture.snapshot = verified
	return fixture
}

func pemCertificate(der []byte) []byte {
	return []byte("-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(der) + "\n-----END CERTIFICATE-----\n")
}

func (f *loteFixture) nlSource() nlconfig.Source {
	return nlconfig.Source{URL: nlconfig.DefaultURL, Key: &f.nlKey.PublicKey, PIDFingerprints: []string{trustlists.Fingerprint(f.nlPID)}, Client: &http.Client{Timeout: time.Second}}
}

func (f *loteFixture) signed(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType("jwt"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(f.config)
	if err != nil {
		t.Fatal(err)
	}
	jws, err := s.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return []byte(raw)
}

func (f *loteFixture) publish(source trustlists.Source, renew bool, now time.Time) (trustlists.Result, error) {
	if source == nil {
		source = loteSourceFunc(func(context.Context) (trustlists.Snapshot, error) { return f.snapshot, nil })
	}
	return trustlists.Publish(context.Background(), trustlists.Options{PublicURL: f.options.publicURL, Validity: 30 * 24 * time.Hour, Now: now, Renew: renew}, source, f.signer, lotefiles.Publications{Directory: f.options.publishDir})
}

func (f *loteFixture) files(t *testing.T) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, role := range trustlists.Roles {
		body, err := os.ReadFile(filepath.Join(f.options.publishDir, "lote", role.Filename()))
		if err != nil {
			t.Fatal(err)
		}
		result[role.Filename()] = string(body)
	}
	return result
}

type loteDocument struct {
	LoTE struct {
		Information struct {
			Sequence uint64    `json:"LoTESequenceNumber"`
			Issue    time.Time `json:"ListIssueDateTime"`
			Next     time.Time `json:"NextUpdate"`
		} `json:"ListAndSchemeInformation"`
		Entities []struct {
			Information struct {
				Origins []struct {
					URI string `json:"uriValue"`
				} `json:"TEInformationURI"`
			} `json:"TrustedEntityInformation"`
			Services []struct {
				Info struct {
					Type     string `json:"ServiceTypeIdentifier"`
					Identity struct {
						Certificates []struct {
							Value string `json:"val"`
						} `json:"X509Certificates"`
					} `json:"ServiceDigitalIdentity"`
				} `json:"ServiceInformation"`
			} `json:"TrustedEntityServices"`
		} `json:"TrustedEntitiesList"`
	}
}

func decodeLoTE(t *testing.T, raw string, signer lotesigning.Signer) loteDocument {
	t.Helper()
	jws, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := jws.Signatures[0].Header.Certificates(x509.VerifyOptions{Roots: certPool(signer.Root), CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil || len(chain) == 0 || !bytes.Equal(chain[0][0].Raw, signer.Certificate.Raw) {
		t.Fatalf("T7 signer chain: %v", err)
	}
	body, err := jws.Verify(chain[0][0].PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("internal/trustlists/testdata/etsi/1960201_json_schema.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("T1 official ETSI JSON binding: %v", err)
	}
	if jws.Signatures[0].Header.Algorithm != "ES256" || jws.Signatures[0].Header.ExtraHeaders["typ"] != "JOSE+JSON" {
		t.Fatal("T1 header")
	}
	var doc loteDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// checkLoTERole checks the first publication of role: its times, its
// anchors (GBO and NL, with provenance) and the role separation.
func checkLoTERole(t *testing.T, f *loteFixture, role trustlists.Role) {
	t.Helper()
	doc := decodeLoTE(t, f.files(t)[role.Filename()], f.signer)
	info := doc.LoTE.Information
	if info.Sequence != 1 || !info.Issue.Equal(f.now) || !info.Next.Equal(f.now.Add(30*24*time.Hour)) {
		t.Fatalf("T6 times: %+v", info)
	}
	expected := map[string]bool{trustlists.Fingerprint(f.snapshot.GBO[role]): true}
	for _, anchor := range f.snapshot.NL[role] {
		expected[trustlists.Fingerprint(anchor.DER)] = true
	}
	actual := map[string]bool{}
	for _, entity := range doc.LoTE.Entities {
		if len(entity.Information.Origins) == 0 {
			t.Fatal("T2f missing provenance")
		}
		if len(entity.Services) != 2 {
			t.Fatal("issuance and revocation services required")
		}
		for _, service := range entity.Services {
			if !strings.HasPrefix(service.Info.Type, "http://uri.etsi.org/19602/SvcType/"+string(role)+"/") {
				t.Fatal("service role")
			}
			if len(service.Info.Identity.Certificates) != 1 {
				t.Fatal("one certificate per service")
			}
			der, err := base64.StdEncoding.DecodeString(service.Info.Identity.Certificates[0].Value)
			if err != nil {
				t.Fatal(err)
			}
			actual[trustlists.Fingerprint(der)] = true
		}
	}
	if len(doc.LoTE.Entities) != len(expected) || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("T2 role %s: %v != %v", role, actual, expected)
	}
	if role == trustlists.PubEAA && actual[trustlists.Fingerprint(f.nlPID)] {
		t.Fatal("T2b PID CA in PubEAA list")
	}
}

func TestLoTEPublication(t *testing.T) {
	f := newLoTEFixture(t)
	result, err := f.publish(nil, false, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Sequence != 1 {
		t.Fatalf("T1 result: %+v", result)
	}
	for _, role := range trustlists.Roles {
		checkLoTERole(t, f, role)
	}
	before := f.files(t)
	result, err = f.publish(nil, false, f.now.Add(time.Hour))
	if err != nil || result.Changed || result.Sequence != 1 || !reflect.DeepEqual(before, f.files(t)) {
		t.Fatalf("T5 identical run: %+v %v", result, err)
	}
	result, err = f.publish(nil, true, f.now.Add(2*time.Hour))
	if err != nil || !result.Changed || result.Sequence != 2 {
		t.Fatalf("T5 renewal: %+v %v", result, err)
	}
	for _, raw := range f.files(t) {
		if !decodeLoTE(t, raw, f.signer).LoTE.Information.Issue.Equal(f.now.Add(2 * time.Hour)) {
			t.Fatal("renewal time")
		}
	}
	// Replace the test PID root by another root of the same role.
	newRoot := t.TempDir()
	if err := createDevelopmentRoot(newRoot, "replacement", pkix.Name{CommonName: "GBO test-PID root"}, f.now, rand.Reader); err != nil {
		t.Fatal(err)
	}
	f.snapshot.GBO[trustlists.PID], err = lotefiles.ReadCertificate(filepath.Join(newRoot, "replacement-ca-cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	result, err = f.publish(nil, false, f.now.Add(3*time.Hour))
	if err != nil || result.Sequence != 3 {
		t.Fatalf("T5 changed anchors: %+v %v", result, err)
	}
	result, err = f.publish(nil, false, f.now.Add(31*24*time.Hour))
	if err != nil || result.Sequence != 4 {
		t.Fatalf("expiry renewal: %+v %v", result, err)
	}
}

func TestLoTERefusalsLeavePublicationIntact(t *testing.T) {
	f := newLoTEFixture(t)
	if _, err := f.publish(nil, false, f.now); err != nil {
		t.Fatal(err)
	}
	before := f.files(t)
	for _, name := range []string{"missing root", "wrong role", "NL unavailable", "NL rollback"} {
		t.Run(name, func(t *testing.T) {
			snapshot := f.snapshot
			snapshot.GBO = map[trustlists.Role][]byte{}
			for role, der := range f.snapshot.GBO {
				snapshot.GBO[role] = der
			}
			var failure error
			switch name {
			case "missing root":
				delete(snapshot.GBO, trustlists.PID)
			case "wrong role":
				snapshot.GBO[trustlists.PID] = snapshot.GBO[trustlists.PubEAA]
			case "NL unavailable":
				failure = errors.New("unreachable")
			case "NL rollback":
				snapshot.NLVersion--
			}
			source := loteSourceFunc(func(context.Context) (trustlists.Snapshot, error) { return snapshot, failure })
			if _, err := f.publish(source, false, f.now); err == nil {
				t.Fatal("T3/T4/T2d must refuse")
			}
			if !reflect.DeepEqual(before, f.files(t)) {
				t.Fatal("refusal changed publication")
			}
		})
	}
	t.Run("first run without NL", func(t *testing.T) {
		f.options.publishDir = t.TempDir()
		if _, err := f.publish(loteSourceFunc(func(context.Context) (trustlists.Snapshot, error) {
			return trustlists.Snapshot{}, errors.New("offline")
		}), false, f.now); err == nil {
			t.Fatal("T2d")
		}
		if _, err := os.Lstat(filepath.Join(f.options.publishDir, "lote")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("no partial publication permitted")
		}
	})
}

func TestLoTENLSignatureAndV06Anchors(t *testing.T) {
	f := newLoTEFixture(t)
	if _, err := f.publish(nil, false, f.now); err != nil {
		t.Fatal(err)
	}
	before := f.files(t)
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := f.nlSource().Verify(f.signed(t, otherKey)); err == nil {
		t.Fatal("T2c forged configuration accepted")
	}
	forged := f.signed(t, otherKey)
	if _, err := f.publish(loteSourceFunc(func(context.Context) (trustlists.Snapshot, error) { return f.nlSource().Verify(forged) }), false, f.now); err == nil {
		t.Fatal("T2c publication accepted")
	}
	if !reflect.DeepEqual(before, f.files(t)) {
		t.Fatal("T2c replaced the previous lists")
	}
	f.config["environment"] = "dev"
	if _, err := f.nlSource().Verify(f.signed(t, f.nlKey)); err == nil {
		t.Fatal("wrong environment accepted")
	}
	f.config["environment"] = "preproductie"
	base64DER := base64.StdEncoding.EncodeToString
	f.config["disclosure"] = map[string]any{"wrpac_trust_anchors": []string{base64DER(f.snapshot.GBO[trustlists.WRPAC])}, "wrprc_trust_anchors": []string{base64DER(f.snapshot.GBO[trustlists.WRPRC])}}
	got, err := f.nlSource().Verify(f.signed(t, f.nlKey))
	if err != nil || len(got.NL[trustlists.WRPAC]) != 1 || len(got.NL[trustlists.WRPRC]) != 1 {
		t.Fatalf("T2e: %+v %v", got, err)
	}
	// The same v0.6 fields are also accepted at top level.
	f.config["disclosure"] = map[string]any{}
	f.config["wrpac_trust_anchors"] = []string{base64DER(f.snapshot.GBO[trustlists.WRPAC])}
	f.config["wrprc_trust_anchors"] = []string{base64DER(f.snapshot.GBO[trustlists.WRPRC])}
	got, err = f.nlSource().Verify(f.signed(t, f.nlKey))
	if err != nil || len(got.NL[trustlists.WRPRC]) != 1 {
		t.Fatalf("T2e top-level: %v", err)
	}
	s := f.nlSource()
	s.PIDFingerprints = []string{strings.Repeat("0", 64)}
	if _, err := s.Verify(f.signed(t, f.nlKey)); err == nil {
		t.Fatal("unreviewed PID CA accepted")
	}
}

func TestLoTENLFetchFailure(t *testing.T) {
	f := newLoTEFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	source := f.nlSource()
	source.URL = server.URL
	if _, err := source.Load(context.Background()); err == nil {
		t.Fatal("T2d")
	}
}

// The request still addresses the fixed official URL. Only its network dial
// is routed to a local fixture with a normally verified test certificate.
func officialNLHTTPSFixture(t *testing.T, f *loteFixture, handler http.Handler) *http.Client {
	t.Helper()
	official, err := url.Parse(nlconfig.DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: "NL HTTPS test fixture"},
		DNSNames: []string{official.Hostname()}, NotBefore: f.now.Add(-time.Hour), NotAfter: f.now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &f.nlKey.PublicKey, f.nlKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: f.nlKey}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != official.Hostname()+":443" {
			return nil, errors.New("unexpected external dial in test")
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 2 * time.Second}
}

func TestLoTENLHTTPSDemo(t *testing.T) {
	f := newLoTEFixture(t)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// An intentionally unverifiable JWT shows exactly what the shortcut skips.
	raw := f.signed(t, otherKey)
	client := officialNLHTTPSFixture(t, f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	nl := f.nlSource()
	nl.Key = nil
	nl.TrustOfficialHTTPS = true
	nl.Client = client
	source := loteSourceFunc(func(ctx context.Context) (trustlists.Snapshot, error) {
		snapshot, err := nl.Load(ctx)
		snapshot.GBO = f.snapshot.GBO
		return snapshot, err
	})
	result, err := f.publish(source, false, f.now)
	if err != nil || result.NLAuthentication != trustlists.NLHTTPSDemo {
		t.Fatalf("HTTPS shortcut: %+v %v", result, err)
	}
	sawNLOrigin := false
	for _, list := range f.files(t) {
		decodeLoTE(t, list, f.signer) // Includes the official JSON schema and our signature.
		jws, _ := jose.ParseSignedCompact(list, []jose.SignatureAlgorithm{jose.ES256})
		payload, _ := jws.Verify(f.signer.Certificate.PublicKey)
		if !bytes.Contains(payload, []byte("JWT signature was not verified")) {
			t.Fatal("HTTPS shortcut is not disclosed in signed output")
		}
		sawNLOrigin = sawNLOrigin || bytes.Contains(payload, []byte("authentication=https-only-demo"))
	}
	if !sawNLOrigin {
		t.Fatal("NL provenance does not disclose the HTTPS shortcut")
	}
	manifest, err := os.ReadFile(filepath.Join(f.options.publishDir, "lote", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var publication trustlists.Publication
	if err := json.Unmarshal(manifest, &publication); err != nil {
		t.Fatal(err)
	}
	if publication.NLAuthentication != trustlists.NLHTTPSDemo {
		t.Fatal("manifest hides authentication mode")
	}
	before := f.files(t)
	nl.Key = &f.nlKey.PublicKey
	if _, err := f.publish(source, false, f.now); err == nil {
		t.Fatal("configured key fell back after JWT verification failed")
	}
	if !reflect.DeepEqual(before, f.files(t)) {
		t.Fatal("failed verification replaced publication")
	}
}

func TestLoTENLHTTPSDemoRefusals(t *testing.T) {
	f := newLoTEFixture(t)
	raw := f.signed(t, f.nlKey)
	client := officialNLHTTPSFixture(t, f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	base := f.nlSource()
	base.Key = nil
	base.TrustOfficialHTTPS = true
	base.Client = client
	for _, address := range []string{strings.Replace(nlconfig.DefaultURL, "https:", "http:", 1), "https://other.example/config", nlconfig.DefaultURL + "/"} {
		t.Run(address, func(t *testing.T) {
			s := base
			s.URL = address
			if _, err := s.Load(context.Background()); err == nil {
				t.Fatal("non-official URL accepted")
			}
		})
	}
	t.Run("no explicit shortcut", func(t *testing.T) {
		s := base
		s.TrustOfficialHTTPS = false
		if _, err := s.Load(context.Background()); err == nil {
			t.Fatal("implicit shortcut accepted")
		}
	})
	t.Run("untrusted TLS", func(t *testing.T) {
		s := base
		transport := client.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.RootCAs = x509.NewCertPool()
		defer transport.CloseIdleConnections()
		s.Client = &http.Client{Transport: transport, Timeout: time.Second}
		if _, err := s.Load(context.Background()); err == nil {
			t.Fatal("untrusted TLS accepted")
		}
	})
	t.Run("TLS verification disabled", func(t *testing.T) {
		s := base
		transport := client.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.InsecureSkipVerify = true // Intentional negative fixture.
		defer transport.CloseIdleConnections()
		s.Client = &http.Client{Transport: transport, Timeout: time.Second}
		if _, err := s.Load(context.Background()); err == nil {
			t.Fatal("disabled TLS verification accepted")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		s := base
		s.Client = officialNLHTTPSFixture(t, f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, nlconfig.DefaultURL+"/redirect", http.StatusFound)
		}))
		if _, err := s.Load(context.Background()); err == nil {
			t.Fatal("redirect accepted")
		}
	})
}

func TestLoTEConcurrentPublication(t *testing.T) {
	f := newLoTEFixture(t)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := f.publish(nil, true, f.now); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, raw := range f.files(t) {
		if decodeLoTE(t, raw, f.signer).LoTE.Information.Sequence != 6 {
			t.Fatal("concurrent publications reused a sequence")
		}
	}
}

func TestLoTESignerKeysAreIsolatedAndStable(t *testing.T) {
	f := newLoTEFixture(t)
	before, err := os.ReadFile(filepath.Join(f.options.rootDir, "lote-ca-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionTrustListSigner(f.options, f.now); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(f.options.rootDir, "lote-ca-key.pem"))
	if !bytes.Equal(before, after) {
		t.Fatal("root was replaced")
	}
	for _, name := range []string{"lote-ca-key.pem", "signer-key.pem"} {
		info, err := os.Stat(filepath.Join(f.options.rootDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("T8 private key permissions")
		}
		if fileExists(filepath.Join(f.options.publicDir, "development-ca", name)) {
			t.Fatal("private key projected publicly")
		}
	}
	for _, dir := range []string{f.options.secretsDir, f.options.publicDir, f.options.publishDir} {
		options := f.options
		options.rootDir = filepath.Join(dir, "lote-keys")
		if err := checkLoTEKeyDirectory(options); err == nil {
			t.Fatal("T8 key directory under a container mount accepted")
		}
	}
	link := filepath.Join(t.TempDir(), "mounted")
	if err := os.Symlink(f.options.secretsDir, link); err != nil {
		t.Fatal(err)
	}
	options := f.options
	options.rootDir = filepath.Join(link, "new-keys")
	if err := checkLoTEKeyDirectory(options); err == nil {
		t.Fatal("T8 symlink bypass")
	}
}

func TestLoTENLPublicKeyFormats(t *testing.T) {
	f := newLoTEFixture(t)
	der, err := x509.MarshalPKIXPublicKey(&f.nlKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(der)
	for name, body := range map[string][]byte{
		"PEM":                pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}),
		"base64 DER":         []byte(b64),
		"preproduction JSON": []byte(`{"environment":"preproductie","signing_public_key":"` + b64 + `"}`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "public-key")
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			key, err := nlconfig.ReadKey(path)
			if err != nil || !f.nlKey.PublicKey.Equal(key) {
				t.Fatalf("public key: %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "dev-key.json")
	if err := os.WriteFile(path, []byte(`{"environment":"dev","signing_public_key":"`+b64+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := nlconfig.ReadKey(path); err == nil {
		t.Fatal("dev configuration accepted as preproduction")
	}
}

type failingLoTESigner struct {
	lotesigning.Signer
	calls int
}

func (s *failingLoTESigner) Sign(ctx context.Context, body []byte, issue time.Time) ([]byte, error) {
	s.calls++
	if s.calls == 3 {
		return nil, errors.New("signer unavailable")
	}
	return s.Signer.Sign(ctx, body, issue)
}

func TestLoTEPartialSigningFailure(t *testing.T) {
	f := newLoTEFixture(t)
	if _, err := f.publish(nil, false, f.now); err != nil {
		t.Fatal(err)
	}
	before := f.files(t)
	source := loteSourceFunc(func(context.Context) (trustlists.Snapshot, error) { return f.snapshot, nil })
	signer := &failingLoTESigner{Signer: f.signer}
	_, err := trustlists.Publish(context.Background(), trustlists.Options{PublicURL: f.options.publicURL, Validity: 30 * 24 * time.Hour, Now: f.now, Renew: true}, source, signer, lotefiles.Publications{Directory: f.options.publishDir})
	if err == nil || signer.calls != 3 {
		t.Fatal("partial signing failure expected")
	}
	if !reflect.DeepEqual(before, f.files(t)) {
		t.Fatal("partial signing replaced a list")
	}
}

func TestLoTETrustHost(t *testing.T) {
	if os.Getenv("GBO_TEST_NGINX") != "1" {
		t.Skip("set GBO_TEST_NGINX=1 for the real nginx HTTP test (Docker required)")
	}
	f := newLoTEFixture(t)
	if _, err := f.publish(nil, false, f.now); err != nil {
		t.Fatal(err)
	}
	config, err := filepath.Abs("../../trust-host/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "--pull=never", "-p", "127.0.0.1::8080", "-v", config+":/etc/nginx/conf.d/default.conf:ro", "-v", f.options.publishDir+":/usr/share/trust:ro", "nginx:1.27-alpine").CombinedOutput()
	if err != nil {
		t.Fatalf("nginx: %s %v", out, err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	port, err := exec.Command("docker", "port", id, "8080/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + strings.TrimSpace(string(port))
	client := http.Client{Timeout: time.Second}
	for _, role := range trustlists.Roles {
		var resp *http.Response
		for range 40 {
			resp, err = client.Get(base + "/lote/" + role.Filename())
			if err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("Cache-Control") != "public, max-age=3600" {
			t.Fatalf("T9 headers: %v %v", resp, err)
		}
		if string(body) != f.files(t)[role.Filename()] {
			t.Fatal("T9 wrong publication")
		}
	}
	for _, path := range []string{"/lote/manifest.json", "/.lote-generations/", "/lote/Other.jwt"} {
		response, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatalf("internal publication exposed: %s %d", path, response.StatusCode)
		}
	}
}
