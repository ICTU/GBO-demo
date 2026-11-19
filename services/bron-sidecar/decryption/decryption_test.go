package decryption

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// A source keeps the keys of an earlier version next to the current ones, so
// every key file in the directory is loaded.
func TestLoadKeysReadsEveryKeyFileAndTheSchemeKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ei-decryption-20260101.pem", "key of january")
	writeFile(t, dir, "ei-decryption-20260314.pem", "key of march")
	writeFile(t, dir, "notes.txt", "not a key")
	writeFile(t, dir, schemeKeysFile, `{"urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1":"c2NoZW1lIGtleQ=="}`)

	keys, schemeKeys, err := LoadKeys(dir)
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	if len(keys) != 2 || keys[0] != "key of january" || keys[1] != "key of march" {
		t.Errorf("keys = %v, want the two key files", keys)
	}
	if schemeKeys["urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1"] != "c2NoZW1lIGtleQ==" {
		t.Errorf("scheme keys = %v", schemeKeys)
	}
}

func TestLoadKeysRefusesAnIncompleteDirectory(t *testing.T) {
	withoutKeys := t.TempDir()
	writeFile(t, withoutKeys, schemeKeysFile, `{}`)
	if _, _, err := LoadKeys(withoutKeys); err == nil {
		t.Error("a directory without key files was accepted")
	}

	withoutSchemeKeys := t.TempDir()
	writeFile(t, withoutSchemeKeys, "ei-decryption.pem", "key")
	if _, _, err := LoadKeys(withoutSchemeKeys); err == nil {
		t.Error("a directory without scheme keys was accepted")
	}
}

// The component says why it could not read a value, as plain text. That
// reason is kept, so that a missing key for a new key set version is
// recognisable as such.
func TestARefusalCarriesTheComponentsReason(t *testing.T) {
	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no matching key: need an \"EI Decryption\" key for key set version 20260314", http.StatusInternalServerError)
	}))
	defer component.Close()

	c := Component{URL: component.URL, Client: &http.Client{Timeout: 5 * time.Second}, Keys: []string{"key"}}
	if _, err := c.Identity(context.Background(), "value"); err == nil || !strings.Contains(err.Error(), "key set version 20260314") {
		t.Errorf("err = %v, want the component's reason", err)
	}
}

// Without keys there is nothing to hand to the component, so it is not asked.
func TestASourceWithoutKeysDoesNotCallTheComponent(t *testing.T) {
	called := false
	component := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer component.Close()

	c := Component{URL: component.URL, Client: &http.Client{Timeout: 5 * time.Second}}
	if _, err := c.Identity(context.Background(), "value"); err == nil {
		t.Error("want an error for a source without keys")
	}
	if called {
		t.Error("the component was called without keys")
	}
}

// A source's pseudonym of a citizen is the pseudonym value alone, so it stays
// the same when the source gets new keys. The component's whole decoded
// structure names the key set version too and differs between the two.
func TestAPseudonymIsTheSameAcrossKeySetVersions(t *testing.T) {
	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SignedEncryptedPseudonym string `json:"signedEncryptedPseudonym"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		version := req.SignedEncryptedPseudonym // the test names the version as the value
		decoded := map[string]string{
			"recipient":              "99999999900000000200",
			"recipientKeySetVersion": version,
			"pseudonymValue":         "the-same-pseudonym",
		}
		whole, _ := json.Marshal(decoded)
		_ = json.NewEncoder(w).Encode(map[string]any{"pseudonym": string(whole), "decodedPseudonym": decoded})
	}))
	defer component.Close()

	c := Component{URL: component.URL, Client: &http.Client{Timeout: 5 * time.Second}, Keys: []string{"key"}}
	january, err := c.Pseudonym(context.Background(), "20260101")
	if err != nil {
		t.Fatalf("Pseudonym 20260101: %v", err)
	}
	nextYear, err := c.Pseudonym(context.Background(), "20270101")
	if err != nil {
		t.Fatalf("Pseudonym 20270101: %v", err)
	}
	if january.Value != "the-same-pseudonym" || nextYear.Value != january.Value {
		t.Errorf("pseudonyms = %q and %q, want the pseudonym value under both key sets", january.Value, nextYear.Value)
	}
	if january.Recipient != "99999999900000000200" {
		t.Errorf("recipient = %q", january.Recipient)
	}
}
