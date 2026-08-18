package consent

// Core tests. These drive Portal directly against in-memory ports, so they
// need no httptest.Server and no network. main_test.go still exercises the
// real HTTP adapters end-to-end; the two are complementary.
//
// The fakes live here rather than in a mock package: they are ten lines each
// and only this package needs them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePI mirrors bsnk-mock: the PI is a hash of the BSN, so it does not embed
// the BSN itself. Deriving it as "PI-"+bsn would make the leak test vacuous.
func fakePI(bsn BSN) PI {
	sum := sha256.Sum256([]byte("test-salt|" + string(bsn)))
	return PI("PI-" + hex.EncodeToString(sum[:8]))
}

func fakeSubjectRef(bsn BSN, recipientOIN string) SubjectRef {
	sum := sha256.Sum256([]byte("subject-ref|" + string(bsn) + "|" + recipientOIN))
	return SubjectRef("EP-" + hex.EncodeToString(sum[:8]))
}

// testPortalOIN stands in for the portal's own OIN, which main supplies in
// production.
const testPortalOIN = "00000000000000000002"

// ── Fakes ─────────────────────────────────────────────────────────────────

type fakePseudo struct {
	err       error
	gotBSN    []BSN
	gotOIN    []string
	mu        sync.Mutex
	pseudonym string
}

func (f *fakePseudo) Pseudonymize(_ context.Context, bsn BSN, recipientOIN string) (Pseudonyms, error) {
	f.mu.Lock()
	f.gotBSN = append(f.gotBSN, bsn)
	f.gotOIN = append(f.gotOIN, recipientOIN)
	f.mu.Unlock()
	if f.err != nil {
		return Pseudonyms{}, f.err
	}
	p := f.pseudonym
	if p == "" {
		p = string(fakeSubjectRef(bsn, recipientOIN))
	}
	// PI is deterministic per BSN and independent of the recipient.
	return Pseudonyms{Pseudonym: p, PI: fakePI(bsn)}, nil
}

type memStore struct {
	mu      sync.Mutex
	recs    map[string]Record
	created []Draft
	seq     int
	revoked []string
}

func newMemStore() *memStore {
	return &memStore{recs: make(map[string]Record)}
}

func (m *memStore) Create(_ context.Context, d Draft) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := "c-" + string(rune('0'+m.seq))
	m.created = append(m.created, d)
	rec := Record{
		ID:         id,
		SubjectRef: d.SubjectRef,
		Token:      "signed-consent-token",
		Status:     "ACTIVE",
		Raw:        map[string]any{"consent_id": id, "subject_ref": string(d.SubjectRef), "status": "ACTIVE"},
	}
	if d.ValiditySeconds > 0 {
		rec.ValidUntil = time.Now().Add(time.Duration(d.ValiditySeconds) * time.Second)
	}
	m.recs[id] = rec
	return rec, nil
}

func (m *memStore) ListBySubject(_ context.Context, subject SubjectRef) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.recs {
		if r.SubjectRef == subject {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) Get(_ context.Context, consentID string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[consentID]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (m *memStore) Revoke(_ context.Context, consentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[consentID]
	if !ok {
		return ErrNotFound
	}
	r.Status = "REVOKED"
	m.recs[consentID] = r
	m.revoked = append(m.revoked, consentID)
	return nil
}

// recorder captures every event the core emits.
type recorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *recorder) Observe(_ context.Context, e Event) {
	if e.Step == "" {
		return
	}
	r.mu.Lock()
	r.steps = append(r.steps, e.Step)
	r.mu.Unlock()
}

func testPortal(t *testing.T, watch Observer) (*Portal, *fakePseudo, *memStore) {
	t.Helper()
	bsnk := &fakePseudo{}
	store := newMemStore()
	return &Portal{
		Pseudonyms: bsnk,
		Consents:   store,
		Watch:      watch,
		OwnOIN:     testPortalOIN,
	}, bsnk, store
}

// ── Ownership ─────────────────────────────────────────────────────────────

// The check that makes revoke safe: without it, any token holder could revoke
// any consent_id they guess. Previously unreachable without two stub servers.
func TestRevokeDeniedForOtherCitizen(t *testing.T) {
	p, _, store := testPortal(t, nil)
	ctx := context.Background()

	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}

	err = p.RevokeConsent(ctx, BSN("222222222"), granted.ConsentID)
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("revoke by other citizen = %v, want ErrNotOwned", err)
	}
	if len(store.revoked) != 0 {
		t.Fatalf("record was revoked despite failed ownership check: %v", store.revoked)
	}
}

func TestRevokeSucceedsForOwner(t *testing.T) {
	p, _, store := testPortal(t, nil)
	ctx := context.Background()

	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	if err := p.RevokeConsent(ctx, BSN("111111111"), granted.ConsentID); err != nil {
		t.Fatalf("revoke by owner: %v", err)
	}
	if len(store.revoked) != 1 || store.revoked[0] != granted.ConsentID {
		t.Fatalf("revoked = %v, want [%s]", store.revoked, granted.ConsentID)
	}
}

func TestRevokeUnknownConsentIsNotFound(t *testing.T) {
	p, _, _ := testPortal(t, nil)
	err := p.RevokeConsent(context.Background(), BSN("111111111"), "c-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown = %v, want ErrNotFound", err)
	}
}

// ── Effective status ──────────────────────────────────────────────────────

func TestEffectiveStatus(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rec  Record
		want Status
	}{
		{"active without expiry", Record{Status: "ACTIVE"}, StatusActive},
		{"active before expiry", Record{Status: "ACTIVE", ValidUntil: now.Add(time.Hour)}, StatusActive},
		{"expired", Record{Status: "ACTIVE", ValidUntil: now.Add(-time.Hour)}, StatusExpired},
		{"revoked wins over expiry", Record{Status: "REVOKED", ValidUntil: now.Add(time.Hour)}, StatusRevoked},
		{"revoked lowercase", Record{Status: "revoked"}, StatusRevoked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.EffectiveStatus(now); got != tc.want {
				t.Errorf("EffectiveStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// ListConsents must annotate against the injected clock, not the wall clock.
func TestListAnnotatesExpiredAgainstInjectedClock(t *testing.T) {
	p, _, _ := testPortal(t, nil)
	ctx := context.Background()

	if _, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{
		DienstverlenerOIN: "DV",
		ValiditySeconds:   60,
	}); err != nil {
		t.Fatalf("give consent: %v", err)
	}

	// Freeze the clock well past the validity window.
	p.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	recs, err := p.ListConsents(ctx, BSN("111111111"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(recs) = %d, want 1", len(recs))
	}
	if recs[0].Effective != StatusExpired {
		t.Errorf("effective = %q, want %q", recs[0].Effective, StatusExpired)
	}
}

// A citizen must never see another citizen's consents: isolation is by the
// portal-specific subject reference.
func TestListIsolatesByCitizen(t *testing.T) {
	p, _, _ := testPortal(t, nil)
	ctx := context.Background()

	if _, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"}); err != nil {
		t.Fatalf("give consent: %v", err)
	}
	recs, err := p.ListConsents(ctx, BSN("222222222"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("other citizen saw %d consents, want 0", len(recs))
	}
}

// ── The privacy invariant ─────────────────────────────────────────────────

// The register receives PI only as transient signing material and receives a
// portal-scoped pseudonym as its persistent subject. Plain BSN must never
// cross the port.
func TestBSNNeverReachesTheRegister(t *testing.T) {
	p, bsnk, store := testPortal(t, nil)
	const bsn = "987654321"

	if _, err := p.GiveConsent(context.Background(), BSN(bsn), GiveInput{
		DienstverlenerOIN: "DV",
		Scopes:            []string{"bd:ib:2025"},
	}); err != nil {
		t.Fatalf("give consent: %v", err)
	}

	if len(store.created) != 1 {
		t.Fatalf("created %d consents, want 1", len(store.created))
	}
	payload, err := json.Marshal(store.created[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), bsn) {
		t.Fatalf("BSN leaked to the consent register: %s", payload)
	}
	if store.created[0].PI != fakePI(BSN(bsn)) {
		t.Errorf("transient PI = %q, want derived PI", store.created[0].PI)
	}
	if store.created[0].SubjectRef != fakeSubjectRef(BSN(bsn), testPortalOIN) {
		t.Errorf("subject_ref = %q, want portal-scoped pseudonym", store.created[0].SubjectRef)
	}
	// The BSN is not merely absent downstream — it did reach the one port
	// that is allowed to see it.
	if len(bsnk.gotBSN) != 2 || bsnk.gotBSN[0] != BSN(bsn) || bsnk.gotBSN[1] != BSN(bsn) {
		t.Errorf("BSNk saw %v, want two derivations for %s", bsnk.gotBSN, bsn)
	}
}

// Pseudonymisation must use the dienstverlener as recipient when granting,
// and the portal's own OIN for the persistent subject reference.
func TestRecipientOINPerFlow(t *testing.T) {
	p, bsnk, _ := testPortal(t, nil)
	ctx := context.Background()

	if _, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV-OIN"}); err != nil {
		t.Fatalf("give consent: %v", err)
	}
	if _, err := p.ListConsents(ctx, BSN("111111111")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := bsnk.gotOIN; len(got) != 3 || got[0] != "DV-OIN" || got[1] != testPortalOIN || got[2] != testPortalOIN {
		t.Errorf("recipient OINs = %v, want [DV-OIN %s %s]", got, testPortalOIN, testPortalOIN)
	}
}

// ── Observers cannot break the flow ───────────────────────────────────────

// Observe returns no error, so a failing watcher cannot fail a request. Even
// a panicking one must not: FanOut contains it.
func TestPanickingObserverDoesNotFailGiveConsent(t *testing.T) {
	boom := ObserverFunc(func(context.Context, Event) { panic("observer exploded") })
	p, _, _ := testPortal(t, FanOut{boom})

	granted, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent failed because of an observer: %v", err)
	}
	if granted.ConsentID == "" {
		t.Fatal("no consent id returned")
	}
}

// The architecture panel depends on this exact narrative; lock it down.
func TestGiveConsentEmitsPanelSteps(t *testing.T) {
	rec := &recorder{}
	p, _, _ := testPortal(t, rec)

	if _, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"}); err != nil {
		t.Fatalf("give consent: %v", err)
	}
	want := []string{"portal_received", "pseudonymizing", "pseudonym_generated", "consent_granting", "consent_granted"}
	if strings.Join(rec.steps, ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, want %v", rec.steps, want)
	}
}

func TestRevokeEmitsPanelSteps(t *testing.T) {
	rec := &recorder{}
	p, _, _ := testPortal(t, rec)
	ctx := context.Background()

	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	rec.steps = nil

	if err := p.RevokeConsent(ctx, BSN("111111111"), granted.ConsentID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	want := []string{"portal_received", "consent_revoking", "consent_revoked"}
	if strings.Join(rec.steps, ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, want %v", rec.steps, want)
	}
}

// A denied revoke must not narrate a successful revocation.
func TestDeniedRevokeDoesNotEmitRevoked(t *testing.T) {
	rec := &recorder{}
	p, _, _ := testPortal(t, rec)
	ctx := context.Background()

	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	rec.steps = nil

	if err := p.RevokeConsent(ctx, BSN("222222222"), granted.ConsentID); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("want ErrNotOwned, got %v", err)
	}
	for _, s := range rec.steps {
		if s == "consent_revoked" || s == "consent_revoking" {
			t.Errorf("denied revoke emitted %q", s)
		}
	}
}

// ── Upstream failures ─────────────────────────────────────────────────────

func TestGiveConsentFailsWhenPseudonymizerFails(t *testing.T) {
	p, bsnk, store := testPortal(t, nil)
	bsnk.err = errors.New("bsnk down")

	if _, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"}); err == nil {
		t.Fatal("want an error when BSNk fails")
	}
	// Nothing may be registered when we could not derive a PI.
	if len(store.created) != 0 {
		t.Fatalf("registered %d consents despite BSNk failure", len(store.created))
	}
}
