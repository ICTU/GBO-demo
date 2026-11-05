// Package storetest holds the behaviour every store of the issuer must
// have, so the in-memory and PostgreSQL stores are tested alike.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// Store is what an issuer store implements.
type Store interface {
	issuance.Sessions
	issuance.OneTime
	issuance.StatusLists
}

// Run runs the contract against a fresh store from newStore per subtest.
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	for _, c := range []struct {
		name string
		test func(t *testing.T, s Store)
	}{
		{"a code is redeemed once and only before it expires", redeemOnce},
		{"concurrent redemptions: exactly one wins", redeemConcurrently},
		{"tokens resolve to their session until they expire", tokens},
		{"a credential is reserved and issued once", reserveIssue},
		{"concurrent reservations: exactly one wins", reserveConcurrently},
		{"a taken-over reservation is lost to its first owner", takeOver},
		{"single-use values", singleUse},
		{"status entries", statusEntries},
		{"revocation by reference", revokeByReference},
		{"revocation and concurrent allocation", revokeWhileAllocating},
		{"a disclosure session is taken once and only before it expires", takeDisclosureOnce},
		{"concurrent takes of a disclosure session: exactly one wins", takeDisclosureConcurrently},
		{"a pending authorization is taken once and only before it expires", takePendingOnce},
		{"an authorization code keeps its binding", codeBinding},
		{"a transaction code: right, wrong, used up, unexpected", txCode},
		{"a session is read back and kept longer", sessionExtend},
	} {
		t.Run(c.name, func(t *testing.T) { c.test(t, newStore(t)) })
	}
}

var ctx = context.Background()

// now is fixed per test binary; stores must keep timestamps to the
// millisecond.
var now = time.Now().UTC().Truncate(time.Millisecond)

func redeemOnce(t *testing.T, s Store) {
	sess := issuance.Session{ID: "s1", Provider: "tax", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute), Reference: "case-1",
		Documents: []issuance.Document{{CredentialID: "d1", ConfigurationID: "income",
			Claims: map[string]any{"year": "2025", "big": json.Number("9007199254740993")}, NotBefore: now, Expires: now.Add(time.Hour)}}}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// A code of another profile is refused and stays usable.
	if _, err := s.RedeemCode(ctx, "tax", "eudi", "c1", "", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("redemption on another profile: %v", err)
	}
	if _, err := s.RedeemCode(ctx, "civil", "nl", "c1", "", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("redemption at another provider: %v", err)
	}
	got, err := s.RedeemCode(ctx, "tax", "nl", "c1", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.Provider != "tax" || got.Profile != "nl" || got.Reference != "case-1" || len(got.Documents) != 1 ||
		got.Documents[0].Claims["year"] != "2025" || fmt.Sprint(got.Documents[0].Claims["big"]) != "9007199254740993" ||
		!got.Documents[0].Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("session not restored: %+v", got)
	}
	if _, err := s.RedeemCode(ctx, "tax", "nl", "c1", "", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("second redemption: %v", err)
	}
	if err := s.CreateSession(ctx, issuance.Session{ID: "s2", Provider: "tax", Profile: "nl", CodeHash: "c2", ExpiresAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemCode(ctx, "tax", "nl", "c2", "", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("expired code: %v", err)
	}
}

func redeemConcurrently(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Provider: "tax", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.RedeemCode(ctx, "tax", "nl", "c1", "", now); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d redemptions succeeded", wins)
	}
}

func tokens(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Provider: "tax", Profile: "eudi", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, issuance.AccessToken{Hash: "t1", SessionID: "s1", JKT: "k", ClientID: "w", WalletProvider: "nl", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	tok, sess, err := s.Token(ctx, "t1", now)
	if err != nil || tok.JKT != "k" || tok.ClientID != "w" || tok.WalletProvider != "nl" || sess.Provider != "tax" || sess.Profile != "eudi" {
		t.Fatalf("%+v %+v %v", tok, sess, err)
	}
	if _, _, err := s.Token(ctx, "t1", now.Add(time.Minute)); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	if _, _, err := s.Token(ctx, "unknown", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
}

func reserveIssue(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Provider: "tax", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	reserve := func(owner string, at time.Time) bool {
		t.Helper()
		ok, err := s.ReserveIssue(ctx, "s1", "d1", owner, at, at.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	complete := func(owner string) bool {
		t.Helper()
		ok, err := s.CompleteIssue(ctx, "s1", "d1", owner)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !reserve("a", now) {
		t.Fatal("the first reservation succeeds")
	}
	if reserve("b", now) {
		t.Fatal("a live reservation blocks another")
	}
	if err := s.ReleaseIssue(ctx, "s1", "d1", "a"); err != nil {
		t.Fatal(err)
	}
	if !reserve("b", now) {
		t.Fatal("a released reservation can be made again")
	}
	if !complete("b") {
		t.Fatal("the owner completes its reservation")
	}
	if reserve("c", now.Add(time.Hour)) || complete("b") {
		t.Fatal("an issued credential is reserved and completed once")
	}
	// Releasing an issued credential leaves it issued.
	if err := s.ReleaseIssue(ctx, "s1", "d1", "b"); err != nil {
		t.Fatal(err)
	}
	if reserve("c", now.Add(time.Hour)) {
		t.Fatal("release must not undo issuance")
	}
}

// takeOver: request A takes longer than its reservation, B takes it over.
// A may then neither complete nor release B's reservation.
func takeOver(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Provider: "tax", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ReserveIssue(ctx, "s1", "d1", "a", now, now.Add(time.Minute)); !ok {
		t.Fatal("A reserves")
	}
	later := now.Add(2 * time.Minute)
	if ok, _ := s.ReserveIssue(ctx, "s1", "d1", "b", later, later.Add(time.Minute)); !ok {
		t.Fatal("B takes over the expired reservation")
	}
	// A fails afterwards: its release must leave B's reservation alone.
	if err := s.ReleaseIssue(ctx, "s1", "d1", "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ReserveIssue(ctx, "s1", "d1", "c", later, later.Add(time.Minute)); ok {
		t.Fatal("A released B's reservation")
	}
	// A completes afterwards: refused, it no longer holds the reservation.
	if ok, err := s.CompleteIssue(ctx, "s1", "d1", "a"); err != nil || ok {
		t.Fatalf("A completed B's reservation: %v %v", ok, err)
	}
	if ok, err := s.CompleteIssue(ctx, "s1", "d1", "b"); err != nil || !ok {
		t.Fatalf("B completes: %v %v", ok, err)
	}
	// And after B completed, A still cannot.
	if ok, _ := s.CompleteIssue(ctx, "s1", "d1", "a"); ok {
		t.Fatal("A completed an issued credential")
	}
}

func reserveConcurrently(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Provider: "tax", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := s.ReserveIssue(ctx, "s1", "d1", fmt.Sprint(i), now, now.Add(time.Minute)); err == nil && ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d reservations succeeded", wins)
	}
}

func singleUse(t *testing.T, s Store) {
	if err := s.Put(ctx, "nonce", "n1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Take(ctx, "nonce", "n1", now); err != nil || !ok {
		t.Fatalf("take: %v %v", ok, err)
	}
	if ok, _ := s.Take(ctx, "nonce", "n1", now); ok {
		t.Fatal("a value is taken once")
	}
	_ = s.Put(ctx, "nonce", "n2", now)
	if ok, _ := s.Take(ctx, "nonce", "n2", now); ok {
		t.Fatal("an expired value is not taken")
	}
	if ok, _ := s.Take(ctx, "challenge", "n1", now); ok {
		t.Fatal("kinds are separate")
	}
	if ok, err := s.FirstUse(ctx, "jti", "j1", now.Add(time.Minute), now); err != nil || !ok {
		t.Fatalf("first use: %v %v", ok, err)
	}
	if ok, _ := s.FirstUse(ctx, "jti", "j1", now.Add(time.Minute), now); ok {
		t.Fatal("a replay is detected")
	}
	if ok, _ := s.FirstUse(ctx, "jti", "j1", now.Add(2*time.Minute), now.Add(time.Minute)); !ok {
		t.Fatal("after expiry an identifier may be used again")
	}
}

func statusEntries(t *testing.T, s Store) {
	seen := map[int]bool{}
	for range 16 {
		idx, err := s.Allocate(ctx, "income", 16, "")
		if err != nil {
			t.Fatal(err)
		}
		if idx < 0 || idx >= 16 || seen[idx] {
			t.Fatalf("index %d out of range or handed out twice", idx)
		}
		seen[idx] = true
	}
	if _, err := s.Allocate(ctx, "income", 16, ""); err == nil {
		t.Fatal("a full list must refuse allocation")
	}
	if err := s.Revoke(ctx, "income", 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, "other", 3); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("revoking an unallocated entry: %v", err)
	}
	revoked, err := s.Revoked(ctx, "income")
	if err != nil || len(revoked) != 1 || revoked[0] != 3 {
		t.Fatalf("revoked %v %v", revoked, err)
	}
}

func revokeByReference(t *testing.T, s Store) {
	a1, _ := s.Allocate(ctx, "income", 1024, "case-1")
	a2, _ := s.Allocate(ctx, "tax", 1024, "case-1")
	_, _ = s.Allocate(ctx, "income", 1024, "case-2")
	_, _ = s.Allocate(ctx, "income", 1024, "")
	n, err := s.RevokeReference(ctx, "case-1")
	if err != nil || n != 2 {
		t.Fatalf("revoked %d, %v; want 2", n, err)
	}
	income, _ := s.Revoked(ctx, "income")
	tax, _ := s.Revoked(ctx, "tax")
	if len(income) != 1 || income[0] != a1 || len(tax) != 1 || tax[0] != a2 {
		t.Fatalf("income %v tax %v", income, tax)
	}
	if n, _ := s.RevokeReference(ctx, ""); n != 0 {
		t.Fatal("the empty reference revokes nothing")
	}
	if revoked, err := s.ReferenceRevoked(ctx, "case-1"); err != nil || !revoked {
		t.Fatalf("case-1 is revoked: %v %v", revoked, err)
	}
	if revoked, _ := s.ReferenceRevoked(ctx, "case-3"); revoked {
		t.Fatal("case-3 is not revoked")
	}
	// An entry allocated after the revocation is born revoked.
	late, _ := s.Allocate(ctx, "income", 1024, "case-1")
	if income, _ := s.Revoked(ctx, "income"); !slices.Contains(income, late) {
		t.Fatal("an entry allocated for a revoked reference must be revoked")
	}
	// A reference revoked before anything was issued is still recorded.
	if n, err := s.RevokeReference(ctx, "case-4"); err != nil || n != 0 {
		t.Fatalf("revoke unused reference: %d %v", n, err)
	}
	if revoked, _ := s.ReferenceRevoked(ctx, "case-4"); !revoked {
		t.Fatal("an unused reference is recorded as revoked")
	}
}

// revokeWhileAllocating revokes a reference while entries for it are being
// allocated: afterwards no entry for the reference may be valid.
func revokeWhileAllocating(t *testing.T, s Store) {
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Allocate(ctx, "income", 4096, "case-r"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := s.RevokeReference(ctx, "case-r"); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	// Allocate once more for the reference: born revoked.
	if _, err := s.Allocate(ctx, "income", 4096, "case-r"); err != nil {
		t.Fatal(err)
	}
	revoked, err := s.Revoked(ctx, "income")
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 17 {
		t.Fatalf("%d of 17 entries for the revoked reference are revoked", len(revoked))
	}
}

func takeDisclosureOnce(t *testing.T, s Store) {
	d := issuance.DisclosureSession{ID: "d1", Provider: "tax", Profile: "nl", Offer: "income-2025", Nonce: "n", State: "st",
		ResponseKeyID: "k", ResponseKey: []byte{1, 2, 3}, ExpiresAt: now.Add(time.Minute)}
	if err := s.CreateDisclosure(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDisclosure(ctx, d); err == nil {
		t.Fatal("a duplicate session id must be refused")
	}
	got, err := s.TakeDisclosure(ctx, "d1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(d.ExpiresAt) || string(got.ResponseKey) != string(d.ResponseKey) {
		t.Fatalf("got %+v, want %+v", got, d)
	}
	got.ExpiresAt = d.ExpiresAt
	if fmt.Sprint(got) != fmt.Sprint(d) {
		t.Fatalf("got %+v, want %+v", got, d)
	}
	if _, err := s.TakeDisclosure(ctx, "d1", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("a used session: %v", err)
	}
	if _, err := s.TakeDisclosure(ctx, "unknown", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("an unknown session: %v", err)
	}
	expired := d
	expired.ID = "d2"
	if err := s.CreateDisclosure(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeDisclosure(ctx, "d2", d.ExpiresAt); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("an expired session: %v", err)
	}
	if _, err := s.TakeDisclosure(ctx, "d2", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("an expired session is gone after its take: %v", err)
	}
}

func takeDisclosureConcurrently(t *testing.T, s Store) {
	if err := s.CreateDisclosure(ctx, issuance.DisclosureSession{ID: "d1", Provider: "tax", Profile: "nl", Offer: "o",
		Nonce: "n", State: "st", ResponseKeyID: "k", ResponseKey: []byte{1}, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.TakeDisclosure(ctx, "d1", now); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d takes won, want 1", wins)
	}
}

func takePendingOnce(t *testing.T, s Store) {
	for _, p := range []issuance.PendingAuthorization{
		{Kind: "par", ID: "p1", Payload: []byte(`{"a":1}`), ExpiresAt: now.Add(time.Minute)},
		{Kind: "login", ID: "p1", Payload: []byte(`{"b":2}`), ExpiresAt: now.Add(time.Minute)},
		{Kind: "par", ID: "p2", Payload: []byte(`{}`), ExpiresAt: now.Add(-time.Second)},
	} {
		if err := s.PutPending(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutPending(ctx, issuance.PendingAuthorization{Kind: "par", ID: "p1", Payload: []byte(`{}`), ExpiresAt: now.Add(time.Minute)}); err == nil {
		t.Fatal("a second pending authorization with the same kind and id was stored")
	}
	got, err := s.TakePending(ctx, "par", "p1", now)
	if err != nil || string(got.Payload) != `{"a":1}` || !got.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("take = %+v, %v", got, err)
	}
	if _, err := s.TakePending(ctx, "par", "p1", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("second take: %v, want ErrNotFound", err)
	}
	if got, err := s.TakePending(ctx, "login", "p1", now); err != nil || string(got.Payload) != `{"b":2}` {
		t.Fatalf("take of the other kind = %+v, %v", got, err)
	}
	if _, err := s.TakePending(ctx, "par", "p2", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("take of an expired one: %v, want ErrNotFound", err)
	}
}

func codeBinding(t *testing.T, s Store) {
	binding := &issuance.CodeBinding{ClientID: "w", RedirectURI: "eu.europa.ec.euidi://authorization", CodeChallenge: "ch", DPoPJKT: "jkt"}
	for _, sess := range []issuance.Session{
		{ID: "acf", Provider: "tax", Profile: "eudi", CodeHash: "c-acf", ExpiresAt: now.Add(time.Minute), Binding: binding},
		{ID: "pre", Provider: "tax", Profile: "eudi", CodeHash: "c-pre", ExpiresAt: now.Add(time.Minute)},
	} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RedeemCode(ctx, "tax", "eudi", "c-acf", "", now)
	if err != nil || got.Binding == nil || *got.Binding != *binding {
		t.Fatalf("binding = %+v, %v; want %+v", got.Binding, err, binding)
	}
	if got, err := s.RedeemCode(ctx, "tax", "eudi", "c-pre", "", now); err != nil || got.Binding != nil {
		t.Fatalf("pre-authorized session binding = %+v, %v; want none", got.Binding, err)
	}
}

func txCode(t *testing.T, s Store) {
	for _, sess := range []issuance.Session{
		{ID: "tx", Provider: "tax", Profile: "eudi", CodeHash: "c-tx", ExpiresAt: now.Add(time.Minute), TxCodeHash: "right"},
		{ID: "tx2", Provider: "tax", Profile: "eudi", CodeHash: "c-tx2", ExpiresAt: now.Add(time.Minute), TxCodeHash: "right"},
		{ID: "plain", Provider: "tax", Profile: "eudi", CodeHash: "c-plain", ExpiresAt: now.Add(time.Minute)},
	} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	for _, tx := range []string{"", "wrong"} {
		if _, err := s.RedeemCode(ctx, "tax", "eudi", "c-tx", tx, now); !errors.Is(err, issuance.ErrTxCodeMismatch) {
			t.Fatalf("redeem with %q: %v, want ErrTxCodeMismatch", tx, err)
		}
	}
	if got, err := s.RedeemCode(ctx, "tax", "eudi", "c-tx", "right", now); err != nil || got.ID != "tx" || got.TxCodeHash != "right" {
		t.Fatalf("redeem with the right code after two failures: %+v, %v", got, err)
	}
	for i := 0; i < issuance.MaxTxCodeAttempts; i++ {
		if _, err := s.RedeemCode(ctx, "tax", "eudi", "c-tx2", "wrong", now); !errors.Is(err, issuance.ErrTxCodeMismatch) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	if _, err := s.RedeemCode(ctx, "tax", "eudi", "c-tx2", "right", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("redeem after the last failure: %v, want ErrNotFound", err)
	}
	if _, err := s.RedeemCode(ctx, "tax", "eudi", "c-plain", "any", now); !errors.Is(err, issuance.ErrTxCodeUnexpected) {
		t.Fatalf("a code for an offer without one: %v, want ErrTxCodeUnexpected", err)
	}
	if _, err := s.RedeemCode(ctx, "tax", "eudi", "c-plain", "", now); err != nil {
		t.Fatalf("the offer without a code stays usable: %v", err)
	}
}

func sessionExtend(t *testing.T, s Store) {
	sess := issuance.Session{ID: "ext", Provider: "tax", Profile: "eudi", CodeHash: "c-ext", ExpiresAt: now.Add(time.Minute), Reference: "r",
		Documents: []issuance.Document{{CredentialID: "d1", ConfigurationID: "income", Claims: map[string]any{"year": "2025"}, NotBefore: now, Expires: now.Add(time.Hour)}}}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := s.ExtendSession(ctx, "ext", now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A shorter extension does not shorten it.
	if err := s.ExtendSession(ctx, "ext", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Session(ctx, "ext")
	if err != nil || !got.ExpiresAt.Equal(now.Add(30*24*time.Hour)) || got.Reference != "r" || len(got.Documents) != 1 || got.Documents[0].Claims["year"] != "2025" {
		t.Fatalf("session = %+v, %v", got, err)
	}
	if _, err := s.Session(ctx, "nope"); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	if err := s.ExtendSession(ctx, "nope", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("extend an unknown session: %v", err)
	}
}
