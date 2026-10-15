package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/harness"
	"gbo-demo/qtsp-reference/internal/qtsp"
	"gbo-demo/qtsp-reference/internal/seal"
)

var sealCheck = seal.Check{Signer: "CN=seal"}

func TestParse(t *testing.T) {
	o, err := Parse("verify", []string{"--attr", `https://a.test/x/1={"x":"y"}`, "--bsn", "555555555", "--mandate"}, io.Discard)
	if err != nil || o.BSN != "555555555" || !o.Mandate || len(o.Attributes) != 1 || string(o.Attributes[0].Value) != `{"x":"y"}` {
		t.Fatalf("Parse = %+v, %v", o, err)
	}
	for _, tc := range []struct {
		op   string
		args []string
	}{
		{"verify", nil},
		{"verify", []string{"--attr", "https://a.test/x/1"}},
		{"verify", []string{"--attr", "https://a.test/x/1=not json"}},
		{"retrieve", []string{"--attr", `https://a.test/x/1={}`}},
	} {
		if _, err := Parse(tc.op, tc.args, io.Discard); err == nil {
			t.Errorf("Parse(%s, %v): err = nil", tc.op, tc.args)
		}
	}
}

// While the authorization server is unavailable the QTSP tries again
// (Q-C8).
func TestRegisterRetriesWhileUnavailable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"client_id":"c1","jwks":{"keys":[{"kid":"k1"}]}}`))
	}))
	defer srv.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c, _ := qtsp.New(qtsp.Config{Key: key, RedirectURI: "https://qtsp.test/callback"})
	reg, err := Register(context.Background(), c, qtsp.AuthorizationServer{Issuer: srv.URL, RegistrationEndpoint: srv.URL}, 10*time.Second)
	if err != nil || reg.ClientID != "c1" || calls.Load() != 3 {
		t.Fatalf("Register = %+v, %v after %d calls", reg, err, calls.Load())
	}
}

// The report names results and the seal, never the token or a BSN (Q-C10).
func TestPrint(t *testing.T) {
	var out bytes.Buffer
	code := Print(&out, Outcome{
		Plan: qtsp.Plan{EndpointURI: "https://asip.test/brp/authsrc-api", Provider: "GBO demo ASIP"},
		Response: qtsp.Response{Status: 200, Seal: &sealCheck,
			Body: []byte(`{"attributeVerificationResults":[{"attributeIdentifier":"https://a.test/family_name/1","attributeVerificationResult":"http://uri.etsi.org/19478/VerificationResult/Match"}],"authenticSource":{"legalName":"BRP"}}`)},
	}, nil)
	if code != 0 || !strings.Contains(out.String(), "Match") || !strings.Contains(out.String(), "sealed by CN=seal") {
		t.Fatalf("Print = %d\n%s", code, out.String())
	}
	out.Reset()
	if code := Print(&out, Outcome{Response: qtsp.Response{Status: 200, SealError: io.ErrUnexpectedEOF}}, nil); code != 3 || !strings.Contains(out.String(), "INVALID") {
		t.Fatalf("invalid seal: %d %s", code, out.String())
	}
	out.Reset()
	if code := Print(&out, Outcome{Denied: &harness.Callback{Error: "access_denied"}}, nil); code != 4 {
		t.Fatalf("denied: %d %s", code, out.String())
	}
	out.Reset()
	if code := Print(&out, Outcome{Response: qtsp.Response{Status: 404, Body: []byte(`{"title":"Not Found","detail":"unknown attribute"}`)}}, nil); code != 2 || !strings.Contains(out.String(), "unknown attribute") {
		t.Fatalf("problem: %d %s", code, out.String())
	}
}
