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

// fakeValue stands in for what BSNk makes for one party: a hash of the
// polymorphic identity and the party, so it does not embed the BSN. Deriving
// it from the BSN plus the OIN would make the leak test vacuous.
func fakeValue(values Polymorphic, party Party) string {
	sum := sha256.Sum256([]byte("value|" + values.PI + "|" + party.OIN))
	return "VI-" + hex.EncodeToString(sum[:8])
}

// fakePI stands in for an activation: like a real one, it does not carry the
// BSN in the clear.
func fakePI(bsn BSN) string {
	sum := sha256.Sum256([]byte("pi|" + string(bsn)))
	return "PI-" + hex.EncodeToString(sum[:8])
}

// testSource is the one source the test portal gives consent for.
var testSource = Party{OIN: "00000000000000000200", KeySetVersion: 20260101}

// testSubjectRefs derives references the way the portal does in production,
// under a test key.
var testSubjectRefs = SubjectRefs{Key: []byte("test-subject-ref-key-of-32-bytes!"), Version: "1"}

func subjectRefOf(t *testing.T, bsn BSN) SubjectRef {
	t.Helper()
	ref, err := testSubjectRefs.For(bsn)
	if err != nil {
		t.Fatalf("subject ref: %v", err)
	}
	return ref
}

// ── Fakes ─────────────────────────────────────────────────────────────────

// fakeBSNk is BSNk's two steps: activate and transform.
type fakeBSNk struct {
	err         error
	activated   []BSN
	transformed []Polymorphic
	gotParties  [][]Party
	mu          sync.Mutex
}

func (f *fakeBSNk) Activate(_ context.Context, bsn BSN) (Polymorphic, error) {
	f.mu.Lock()
	f.activated = append(f.activated, bsn)
	f.mu.Unlock()
	if f.err != nil {
		return Polymorphic{}, f.err
	}
	pi := fakePI(bsn)
	return Polymorphic{PI: pi, PP: "PP-of-" + pi}, nil
}

func (f *fakeBSNk) Transform(_ context.Context, values Polymorphic, parties []Party) ([]EncryptedSubject, error) {
	f.mu.Lock()
	f.transformed = append(f.transformed, values)
	f.gotParties = append(f.gotParties, parties)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	subjects := make([]EncryptedSubject, len(parties))
	for i, party := range parties {
		subjects[i] = EncryptedSubject{Party: party, IdentifierType: "Identity", Value: fakeValue(values, party)}
	}
	return subjects, nil
}

func (f *fakeBSNk) calls() (activated int, transformed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.activated), len(f.transformed)
}

type memStore struct {
	mu          sync.Mutex
	recs        map[string]Record
	created     []Draft
	seq         int
	revoked     []string
	polymorphic map[SubjectRef]Polymorphic
}

func newMemStore() *memStore {
	return &memStore{recs: make(map[string]Record), polymorphic: make(map[SubjectRef]Polymorphic)}
}

func (m *memStore) PolymorphicFor(_ context.Context, subject SubjectRef) (Polymorphic, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	values, ok := m.polymorphic[subject]
	if !ok {
		return Polymorphic{}, ErrNotFound
	}
	return values, nil
}

func (m *memStore) KeepPolymorphic(_ context.Context, subject SubjectRef, values Polymorphic) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, kept := m.polymorphic[subject]; !kept {
		m.polymorphic[subject] = values
	}
	return nil
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

func testPortal(t *testing.T, watch Observer) (*Portal, *fakeBSNk, *memStore) {
	t.Helper()
	bsnk := &fakeBSNk{}
	store := newMemStore()
	return &Portal{
		Identities:  bsnk,
		SubjectRefs: testSubjectRefs,
		Consents:    store,
		Watch:       watch,
		Sources:     []Party{testSource},
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

// The register receives the value per source only as transient signing
// material and the portal's own reference as its persistent subject. Plain
// BSN must never cross the port.
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
	payload, err := json.Marshal(struct {
		Draft       Draft
		Polymorphic map[SubjectRef]Polymorphic
	}{store.created[0], store.polymorphic})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), bsn) {
		t.Fatalf("BSN leaked to the consent register: %s", payload)
	}
	kept := store.polymorphic[subjectRefOf(t, BSN(bsn))]
	want := EncryptedSubject{Party: testSource, IdentifierType: "Identity", Value: fakeValue(kept, testSource)}
	if got := store.created[0].Subjects; len(got) != 1 || got[0] != want {
		t.Errorf("subjects = %+v, want the value BSNk made for the source", got)
	}
	if store.created[0].SubjectRef != subjectRefOf(t, BSN(bsn)) {
		t.Errorf("subject_ref = %q, want the portal's own reference", store.created[0].SubjectRef)
	}
	// The BSN is not merely absent downstream — it did reach the one port
	// that is allowed to see it.
	if len(bsnk.activated) != 1 || bsnk.activated[0] != BSN(bsn) {
		t.Errorf("BSNk activated %v, want %s once", bsnk.activated, bsn)
	}
}

// A citizen is activated once, at their first consent. Every consent after it
// uses the values kept under the portal's reference and is only transformed.
func TestACitizenIsActivatedOnce(t *testing.T) {
	p, bsnk, store := testPortal(t, nil)
	ctx := context.Background()

	for range 2 {
		if _, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"}); err != nil {
			t.Fatalf("give consent: %v", err)
		}
	}
	if activated, transformed := bsnk.calls(); activated != 1 || transformed != 2 {
		t.Errorf("activated %d and transformed %d times, want 1 and 2", activated, transformed)
	}
	kept, ok := store.polymorphic[subjectRefOf(t, BSN("111111111"))]
	if !ok {
		t.Fatal("the values of the activation were not kept")
	}
	for i, values := range bsnk.transformed {
		if values != kept {
			t.Errorf("transform %d used %+v, want the kept values", i, values)
		}
	}

	// Another citizen is a first consent again.
	if _, err := p.GiveConsent(ctx, BSN("222222222"), GiveInput{DienstverlenerOIN: "DV"}); err != nil {
		t.Fatalf("give consent: %v", err)
	}
	if activated, _ := bsnk.calls(); activated != 2 {
		t.Errorf("activated %d times after a second citizen, want 2", activated)
	}
}

// Showing and revoking consents only need the portal's own reference, which
// the portal derives itself: BSNk is not asked.
func TestListingAndRevokingDoNotCallBSNk(t *testing.T) {
	p, bsnk, _ := testPortal(t, nil)
	ctx := context.Background()

	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	activated, transformed := bsnk.calls()

	if _, err := p.ListConsents(ctx, BSN("111111111")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := p.RevokeConsent(ctx, BSN("111111111"), granted.ConsentID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if a, tr := bsnk.calls(); a != activated || tr != transformed {
		t.Errorf("listing and revoking called BSNk: %d activations and %d transformations more", a-activated, tr-transformed)
	}
}

// A value is made for the sources, and for nobody else: the service provider
// gets no identifier of the citizen.
func TestValuesAreMadeForTheSourcesOnly(t *testing.T) {
	p, bsnk, _ := testPortal(t, nil)

	if _, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV-OIN"}); err != nil {
		t.Fatalf("give consent: %v", err)
	}
	if got := bsnk.gotParties; len(got) != 1 || len(got[0]) != 1 || got[0][0] != testSource {
		t.Errorf("BSNk made values for %v, want the source alone", got)
	}
}

// BSNk makes a value only while the citizen is present, so a consent is not
// given when there is no source to make one for.
func TestGiveConsentNeedsASource(t *testing.T) {
	p, bsnk, store := testPortal(t, nil)
	p.Sources = nil

	if _, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"}); err == nil {
		t.Fatal("want an error when no source is configured")
	}
	if activated, _ := bsnk.calls(); activated != 0 || len(store.created) != 0 {
		t.Errorf("BSNk activated %d times and %d consents were registered", activated, len(store.created))
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

func TestGiveConsentFailsWhenBSNkFails(t *testing.T) {
	p, bsnk, store := testPortal(t, nil)
	bsnk.err = errors.New("bsnk down")

	if _, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"}); err == nil {
		t.Fatal("want an error when BSNk fails")
	}
	// Nothing may be registered when BSNk made no value for the sources, and
	// nothing is kept from an activation that did not happen.
	if len(store.created) != 0 || len(store.polymorphic) != 0 {
		t.Fatalf("registered %d consents and kept %d values despite BSNk failure", len(store.created), len(store.polymorphic))
	}
}

func TestUseCaseFollowsTheStatedPurpose(t *testing.T) {
	for _, tc := range []struct{ given, want string }{
		{"Goedkeuring installatiegegevens", "Goedkeuring installatiegegevens"},
		{"", DefaultUseCase},
		{"   ", DefaultUseCase},
	} {
		p, _, store := testPortal(t, nil)
		if _, err := p.GiveConsent(context.Background(), BSN("987654321"), GiveInput{
			DienstverlenerOIN: "DV",
			Scopes:            []string{"lvg:vbo:eigendom"},
			UseCase:           tc.given,
		}); err != nil {
			t.Fatalf("give consent: %v", err)
		}
		if got := store.created[0].UseCase; got != tc.want {
			t.Errorf("use case for %q = %q, want %q", tc.given, got, tc.want)
		}
	}
}
