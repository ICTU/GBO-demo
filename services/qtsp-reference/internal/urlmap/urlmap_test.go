package urlmap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMapsOnlyTheTransport(t *testing.T) {
	var gotHost, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	tr, err := New("http://localhost:9424="+srv.URL+", http://unused:1=http://x:2", http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://localhost:9424/brp/authsrc-api/verify", nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotPath != "/brp/authsrc-api/verify" || !strings.HasPrefix(srv.URL, "http://"+gotHost) || req.URL.Host != "localhost:9424" {
		t.Fatalf("host %s path %s, request URL %s", gotHost, gotPath, req.URL)
	}
	for _, bad := range []string{"http://a", "http://a=notaurl"} {
		if _, err := New(bad, nil); err == nil {
			t.Errorf("New(%q): err = nil", bad)
		}
	}
}
