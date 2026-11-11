package statuslist_test

import (
	"context"
	"encoding/asn1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const uri = "https://status.test/pid"

func signed(t *testing.T, signer testpki.Cert, claims map[string]any) string {
	t.Helper()
	return testwallet.Sign(t, signer.Key, statuslist.Type, map[jose.HeaderKey]any{"x5c": x5c.Header(signer.Chain)}, false, claims)
}

func TestVerify(t *testing.T) {
	root := testpki.NewRoot(t, "pid root")
	signer := root.Issue(t, testpki.Options{EKU: []asn1.ObjectIdentifier{statuslist.SignerEKU}})
	anchors := []x5c.Anchors{x5c.NewAnchors("pid", root.Cert)}
	now := time.Now()

	t.Run("a valid token gives the status of each entry", func(t *testing.T) {
		token, err := statuslist.Sign(statuslist.Token{URI: uri, IssuedAt: now, Expires: now.Add(time.Hour), Size: 16, Revoked: []int{5}}, signer.Key, x5c.Header(signer.Chain))
		if err != nil {
			t.Fatal(err)
		}
		list, err := statuslist.Verify(token, uri, anchors, now)
		if err != nil {
			t.Fatal(err)
		}
		if v, err := list.Status(5); err != nil || v != 1 {
			t.Fatalf("entry 5: %d %v", v, err)
		}
		if v, err := list.Status(4); err != nil || v != 0 {
			t.Fatalf("entry 4: %d %v", v, err)
		}
		if _, err := list.Status(16); err == nil {
			t.Fatal("an index outside the list must be refused")
		}
	})
	t.Run("two bits per entry", func(t *testing.T) {
		// Entries 0..3 in one byte, least significant first: 0, 2, 1, 3.
		lst := encodeBytes(t, []byte{0b11_01_10_00})
		list, err := statuslist.Verify(signed(t, signer, map[string]any{
			"sub": uri, "iat": now.Unix(), "status_list": map[string]any{"bits": 2, "lst": lst},
		}), uri, anchors, now)
		if err != nil {
			t.Fatal(err)
		}
		for idx, want := range []byte{0, 2, 1, 3} {
			if v, err := list.Status(idx); err != nil || v != want {
				t.Fatalf("entry %d: %d %v, want %d", idx, v, err, want)
			}
		}
	})
	t.Run("four and eight bits, up to the last entry", func(t *testing.T) {
		for bits, raw := range map[int][]byte{4: {0x21, 0x0f}, 8: {0, 7}} {
			list, err := statuslist.Verify(signed(t, signer, map[string]any{
				"sub": uri, "iat": now.Unix(), "status_list": map[string]any{"bits": bits, "lst": encodeBytes(t, raw)},
			}), uri, anchors, now)
			if err != nil {
				t.Fatal(err)
			}
			last := len(raw)*8/bits - 1
			want := map[int][]byte{4: {1, 2, 15, 0}, 8: {0, 7}}[bits]
			for idx, w := range want {
				if v, err := list.Status(idx); err != nil || v != w {
					t.Fatalf("%d bits, entry %d: %d %v, want %d", bits, idx, v, err, w)
				}
			}
			if _, err := list.Status(last + 1); err == nil {
				t.Fatalf("%d bits: an index past the last entry must be refused", bits)
			}
		}
	})
	lst := encodeBytes(t, []byte{0})
	claims := func(extra map[string]any) map[string]any {
		c := map[string]any{"sub": uri, "iat": now.Unix(), "status_list": map[string]any{"bits": 1, "lst": lst}}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	if _, err := statuslist.Verify(signed(t, signer, claims(map[string]any{"exp": now.Add(-statuslist.ExpiryLeeway + time.Second).Unix()})), uri, anchors, now); err != nil {
		t.Errorf("expired within the leeway: %v", err)
	}
	for name, tc := range map[string]struct{ token, want string }{
		"another root": {signed(t, testpki.NewRoot(t, "other").Issue(t, testpki.Options{EKU: []asn1.ObjectIdentifier{statuslist.SignerEKU}}), claims(nil)), "chain does not verify"},
		"no EKU":       {signed(t, root.Issue(t, testpki.Options{}), claims(nil)), "extended key usage"},
		"another sub":  {signed(t, signer, claims(map[string]any{"sub": uri + "/x"})), "sub is not the uri"},
		"no iat":       {signed(t, signer, claims(map[string]any{"iat": nil})), "iat is missing"},
		"expired":      {signed(t, signer, claims(map[string]any{"exp": now.Add(-statuslist.ExpiryLeeway - time.Second).Unix()})), "has expired"},
		"3 bits":       {signed(t, signer, claims(map[string]any{"status_list": map[string]any{"bits": 3, "lst": lst}})), "bits must be"},
		"another typ":  {testwallet.Sign(t, signer.Key, "jwt", map[jose.HeaderKey]any{"x5c": x5c.Header(signer.Chain)}, false, claims(nil)), "typ must be"},
	} {
		if _, err := statuslist.Verify(tc.token, uri, anchors, now); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a refusal with %q", name, err, tc.want)
		}
	}
}

func encodeBytes(t *testing.T, raw []byte) string {
	t.Helper()
	// Encode with one bit per entry gives the same bytes for a chosen layout.
	var revoked []int
	for i := range len(raw) * 8 {
		if raw[i/8]&(1<<(i%8)) != 0 {
			revoked = append(revoked, i)
		}
	}
	lst, err := statuslist.Encode(len(raw)*8, revoked)
	if err != nil {
		t.Fatal(err)
	}
	return lst
}

func TestFetch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != statuslist.MediaType {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("token\n"))
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat("a", 1<<20+1)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if token, err := statuslist.Fetch(ctx, srv.Client(), srv.URL+"/ok"); err != nil || token != "token" {
		t.Fatalf("%q %v", token, err)
	}
	for _, path := range []string{"/missing", "/large"} {
		if _, err := statuslist.Fetch(ctx, srv.Client(), srv.URL+path); err == nil {
			t.Errorf("%s: must fail", path)
		}
	}
}

// A list that decompresses beyond the bound is refused.
func TestDecodeBound(t *testing.T) {
	lst, err := statuslist.Encode((8<<20+1)*8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statuslist.Decode(lst); err == nil {
		t.Fatal("an oversized list must be refused")
	}
}
