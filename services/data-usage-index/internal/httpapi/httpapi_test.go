package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"data-usage-index/internal/sqlite"
	"data-usage-index/internal/usage"
)

const (
	bdPeer       = "99999999900000000200"
	registerPeer = "99999999900000000700"
)

// fscToken builds an unsigned token with the given sub, the way the Inway
// forwards one; the listener reads its claims only.
func fscToken(sub string) string {
	enc := base64.RawURLEncoding
	claims, _ := json.Marshal(map[string]string{"sub": sub})
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(claims) + ".sig"
}

func newServers(t *testing.T) (fsc, lookup http.Handler) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	index := usage.New(store, nil)
	return NewFSCHandler(index, store), NewLookupHandler(index, store, []string{registerPeer})
}

func register(t *testing.T, h http.Handler, peer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/registrations", strings.NewReader(body))
	if peer != "" {
		req.Header.Set("Fsc-Authorization", fscToken(peer))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"logbook_id":"99999999900000000200/bd","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","grondslag_type":"consent","grondslag_ref":"c-1"}`

func TestRegisterThenRetryThenLookup(t *testing.T) {
	fsc, lookup := newServers(t)

	if rec := register(t, fsc, bdPeer, validBody); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	if rec := register(t, fsc, bdPeer, validBody); rec.Code != http.StatusOK {
		t.Fatalf("retry: want 200, got %d %s", rec.Code, rec.Body)
	}

	req := httptest.NewRequest(http.MethodGet, "/registrations?grondslag_type=consent&grondslag_ref=c-1", nil)
	req.Header.Set("Fsc-Authorization", fscToken(registerPeer))
	rec := httptest.NewRecorder()
	lookup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Registrations []registrationJSON `json:"registrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Registrations) != 1 || out.Registrations[0].LogbookID != bdPeer+"/bd" || out.Registrations[0].RegisteredAt == "" {
		t.Fatalf("want one registration with a time, got %+v", out.Registrations)
	}
}

func TestAPeerWithoutFSCTokenCannotRegister(t *testing.T) {
	fsc, _ := newServers(t)
	if rec := register(t, fsc, "", validBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAPeerCannotRegisterForAnotherPeersLogbook(t *testing.T) {
	fsc, _ := newServers(t)
	if rec := register(t, fsc, "99999999900000000400", validBody); rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestAnUnknownFieldIsRefused(t *testing.T) {
	fsc, _ := newServers(t)
	body := strings.Replace(validBody, `"grondslag_ref"`, `"data_subject_id":"x","grondslag_ref"`, 1)
	if rec := register(t, fsc, bdPeer, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestTheLookupIsNotOnTheFSCListener(t *testing.T) {
	fsc, _ := newServers(t)
	req := httptest.NewRequest(http.MethodGet, "/registrations?grondslag_type=consent&grondslag_ref=c-1", nil)
	req.Header.Set("Fsc-Authorization", fscToken(bdPeer))
	rec := httptest.NewRecorder()
	fsc.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

// The lookup is for the register: a request that did not come over FSC is
// refused, and so is a source peer, even with a connection.
func TestTheLookupIsForTheRegisterOverFSC(t *testing.T) {
	_, lookup := newServers(t)
	for peer, want := range map[string]int{"": http.StatusUnauthorized, bdPeer: http.StatusForbidden, registerPeer: http.StatusOK} {
		req := httptest.NewRequest(http.MethodGet, "/registrations?grondslag_type=consent&grondslag_ref=c-1", nil)
		if peer != "" {
			req.Header.Set("Fsc-Authorization", fscToken(peer))
		}
		rec := httptest.NewRecorder()
		lookup.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("peer %q: status %d, want %d", peer, rec.Code, want)
		}
	}
}

type downStore struct{}

func (downStore) Ping(context.Context) error { return context.DeadlineExceeded }

func TestHealthFailsWhenTheStoreDoesNotAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	health(downStore{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}
