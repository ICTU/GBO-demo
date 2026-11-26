package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"gbo-demo/ldv-client/ldvtest"
)

// revoke sends DELETE /consents/{id} and returns the status and the body.
func revoke(t *testing.T, url, consentID string) (int, map[string]string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, url+"/consents/"+consentID, nil)
	if err != nil {
		t.Fatalf("build delete: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var body map[string]string
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.StatusCode, body
}

// Only an active consent can be revoked (#600). An expired or revoked one
// keeps its status, and no withdrawal is logged for it.
func TestRevokingAConsentThatIsNotActiveIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, url string, store *Store, consentID string)
		status  string
	}{
		{
			name: "expired",
			prepare: func(_ *testing.T, _ string, store *Store, consentID string) {
				store.mu.Lock()
				store.consents[consentID].ValidUntil = time.Now().Add(-time.Hour)
				store.mu.Unlock()
			},
			status: "ACTIVE",
		},
		{
			name: "revoked",
			prepare: func(t *testing.T, url string, _ *Store, consentID string) {
				if status, body := revoke(t, url, consentID); status != http.StatusOK {
					t.Fatalf("first revoke = %d %v", status, body)
				}
			},
			status: "REVOKED",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logbook := ldvtest.New(t, allGBOActivities()...)
			url, store, client := registerUnderTest(t, logbook)
			consentID := grant(t, url)
			tc.prepare(t, url, store.(*Store), consentID)
			drain(t, client, store)
			before := len(ldvtest.ByName(logbook.Written(), "dataverwerking.toestemming-intrekken"))

			status, body := revoke(t, url, consentID)
			if status != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %v", status, body)
			}
			if body["reason"] != "CONSENT_NOT_REVOCABLE" {
				t.Errorf("reason = %q, want CONSENT_NOT_REVOCABLE", body["reason"])
			}

			pending, err := store.PendingRecords(t.Context(), 10)
			if err != nil {
				t.Fatalf("pending: %v", err)
			}
			if len(pending) != 0 {
				t.Fatalf("a refused revocation put %d records in the outbox", len(pending))
			}
			drain(t, client, store)
			if got := len(ldvtest.ByName(logbook.Written(), "dataverwerking.toestemming-intrekken")); got != before {
				t.Errorf("a refused revocation wrote %d withdrawal records", got-before)
			}
			c, _, _ := store.Get(t.Context(), consentID)
			if c.Status != tc.status {
				t.Errorf("status = %q, want %q", c.Status, tc.status)
			}
		})
	}
}

func TestRevokingAnUnknownConsentIsNotFound(t *testing.T) {
	url, _, _ := registerUnderTest(t, ldvtest.New(t, allGBOActivities()...))
	if status, body := revoke(t, url, "no-such-consent"); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %v", status, body)
	}
}

// The store's own rule, without HTTP: a consent is active until it is revoked
// or its validity has passed, and only then is the record written.
func TestStoreRevokesOnlyAnActiveConsent(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		status     string
		validUntil time.Time
		wantErr    error
	}{
		{"active", "ACTIVE", now.Add(time.Hour), nil},
		{"expired", "ACTIVE", now.Add(-time.Hour), errNotRevocable},
		{"expires now", "ACTIVE", now, errNotRevocable},
		{"revoked", "REVOKED", now.Add(time.Hour), errNotRevocable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore()
			if err := store.Create(t.Context(), &Consent{ConsentID: "c-1", Status: tc.status, ValidUntil: tc.validUntil}, nil); err != nil {
				t.Fatalf("create: %v", err)
			}
			recorded := 0
			_, found, err := store.Revoke(t.Context(), "c-1", now, func(*Consent) ([]byte, error) {
				recorded++
				return []byte(`{}`), nil
			})
			if !found || !errors.Is(err, tc.wantErr) {
				t.Fatalf("Revoke = found %v, err %v; want found, err %v", found, err, tc.wantErr)
			}
			c, _, _ := store.Get(t.Context(), "c-1")
			if tc.wantErr == nil {
				if recorded != 1 || c.Status != "REVOKED" {
					t.Fatalf("recorded %d, status %q; want 1, REVOKED", recorded, c.Status)
				}
				return
			}
			if recorded != 0 || c.Status != tc.status {
				t.Fatalf("recorded %d, status %q; want 0, %q", recorded, c.Status, tc.status)
			}
		})
	}
}
