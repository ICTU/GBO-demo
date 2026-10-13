package stubregister

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

func TestLoad(t *testing.T) {
	r, err := Load(filepath.Join("..", "..", "testdata", "register.json"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.Lookup(context.Background(), "https://register.gbo.example/wrp/test-qtsp")
	if err != nil || len(e.Identifiers) != 1 || e.Entitlements[0] != authorization.QEAAProviderEntitlement {
		t.Fatalf("entry = %+v, %v; want the test QTSP with QEAA_Provider", e, err)
	}
	if _, err := r.Lookup(context.Background(), "https://register.gbo.example/wrp/unknown"); !errors.Is(err, authorization.ErrNotRegistered) {
		t.Fatalf("unknown registryURI: err = %v, want ErrNotRegistered", err)
	}
	for name, doc := range map[string]string{
		"no registryURI": `{"entries":[{"identifiers":["NTRNL-1"]}]}`,
		"no identifiers": `{"entries":[{"registryURI":"https://r.test/x"}]}`,
		"not JSON":       `entries`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "register.json")
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("the register file was accepted")
			}
		})
	}
}

// The file is read again on every lookup: a withdrawn entitlement is seen
// without a restart, and a file that cannot be read is an unavailable
// register.
func TestLookupReadsTheFileAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "register.json")
	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"entries":[{"registryURI":"https://r.test/q","identifiers":["NTRNL-1"],"entitlements":["` + authorization.QEAAProviderEntitlement + `"]}]}`)
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := r.Lookup(context.Background(), "https://r.test/q"); err != nil || len(e.Entitlements) != 1 {
		t.Fatalf("entry = %+v, %v; want QEAA_Provider", e, err)
	}
	write(`{"entries":[{"registryURI":"https://r.test/q","identifiers":["NTRNL-1"],"entitlements":[]}]}`)
	if e, err := r.Lookup(context.Background(), "https://r.test/q"); err != nil || len(e.Entitlements) != 0 {
		t.Fatalf("entry after withdrawal = %+v, %v; want no entitlements", e, err)
	}
	write(`{"entries":`)
	if _, err := r.Lookup(context.Background(), "https://r.test/q"); !errors.Is(err, authorization.ErrRegisterUnavailable) {
		t.Fatalf("corrupt file: err = %v, want ErrRegisterUnavailable", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Lookup(context.Background(), "https://r.test/q"); !errors.Is(err, authorization.ErrRegisterUnavailable) {
		t.Fatalf("missing file: err = %v, want ErrRegisterUnavailable", err)
	}
}
