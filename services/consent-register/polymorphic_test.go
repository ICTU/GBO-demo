package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func polymorphicServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	issuer, err := NewConsentIssuer(config{SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	srv := httptest.NewServer(newMux(store, issuer, nil))
	t.Cleanup(srv.Close)
	return srv, store
}

func put(t *testing.T, url, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The portal keeps the values from its first activation of a citizen here,
// and finds them again under the same reference.
func TestPolymorphicValuesAreKeptUnderTheSubjectReference(t *testing.T) {
	srv, _ := polymorphicServer(t)
	url := srv.URL + "/subjects/SR1-abc/polymorphic"

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("before keeping: status %d, want 404", resp.StatusCode)
	}

	if status := put(t, url, `{"pi":"signed-PI","pp":"signed-PP"}`); status != http.StatusNoContent {
		t.Fatalf("keep: status %d, want 204", status)
	}
	// A second pair for the same reference does not replace the first.
	if status := put(t, url, `{"pi":"other-PI","pp":"other-PP"}`); status != http.StatusNoContent {
		t.Fatalf("keep again: status %d, want 204", status)
	}

	resp, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got Polymorphic
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got != (Polymorphic{PI: "signed-PI", PP: "signed-PP"}) {
		t.Errorf("values = %+v, want the first pair", got)
	}
}

func TestPolymorphicValuesNeedBothHalves(t *testing.T) {
	srv, _ := polymorphicServer(t)
	for _, body := range []string{`{"pi":"signed-PI"}`, `{"pp":"signed-PP"}`, `not json`} {
		if status := put(t, srv.URL+"/subjects/SR1-abc/polymorphic", body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, status)
		}
	}
}

// The values belong to the portal's activation, not to any consent: nothing
// the register says about consents carries them.
func TestNoConsentAnswerCarriesThePolymorphicValues(t *testing.T) {
	srv, _ := polymorphicServer(t)
	if status := put(t, srv.URL+"/subjects/"+testSubjectRef+"/polymorphic", `{"pi":"signed-PI","pp":"signed-PP"}`); status != http.StatusNoContent {
		t.Fatalf("keep: status %d", status)
	}
	body, _ := json.Marshal(map[string]any{
		"encrypted_subject": testEncryptedSubject, "subject_ref": testSubjectRef,
		"dienstverlener_oin": "00000001234567890000", "scopes": []string{"bd:ib:2025"},
	})
	created, err := http.Post(srv.URL+"/consents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(created.Body)
	_ = created.Body.Close()

	listed, err := http.Get(srv.URL + "/consents?subject_ref=" + testSubjectRef)
	if err != nil {
		t.Fatal(err)
	}
	listedBody, _ := io.ReadAll(listed.Body)
	_ = listed.Body.Close()

	for name, answer := range map[string][]byte{"create": createdBody, "list": listedBody} {
		if bytes.Contains(answer, []byte("signed-PI")) || bytes.Contains(answer, []byte("signed-PP")) {
			t.Errorf("%s answer carries the polymorphic values: %s", name, answer)
		}
	}
}

func TestPolymorphicRoutes(t *testing.T) {
	srv, _ := polymorphicServer(t)
	for path, want := range map[string]int{
		"/subjects/SR1-abc":           http.StatusNotFound,
		"/subjects//polymorphic":      http.StatusNotFound,
		"/subjects/SR1-abc/something": http.StatusNotFound,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/subjects/SR1-abc/polymorphic", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status %d, want 405", resp.StatusCode)
	}
}
