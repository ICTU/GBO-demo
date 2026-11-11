package login

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheURLMapRewritesOnlyMappedHosts(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "internal "+r.URL.Path)
	}))
	defer internal.Close()
	transport, err := NewURLMap("http://localhost:9425="+internal.URL, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: transport}).Get("http://localhost:9425/token")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "internal /token" {
		t.Fatalf("mapped request reached %q", body)
	}
	for _, spec := range []string{"http://localhost:9425", "http://localhost:9425=not a url", "a=/relative"} {
		if _, err := NewURLMap(spec, http.DefaultTransport); err == nil {
			t.Errorf("NewURLMap(%q) accepted", spec)
		}
	}
}
