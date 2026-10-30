// Package storetest holds the behaviour every store of the issuer must
// have, so the in-memory and PostgreSQL stores are tested alike.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		{"a credential is marked issued once", markIssued},
		{"single-use values", singleUse},
		{"status entries", statusEntries},
		{"revocation by reference", revokeByReference},
	} {
		t.Run(c.name, func(t *testing.T) { c.test(t, newStore(t)) })
	}
}

var ctx = context.Background()

// now is fixed per test binary; stores must keep timestamps to the
// millisecond.
var now = time.Now().UTC().Truncate(time.Millisecond)

func redeemOnce(t *testing.T, s Store) {
	sess := issuance.Session{ID: "s1", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute), Reference: "case-1",
		Documents: []issuance.Document{{CredentialID: "d1", ConfigurationID: "income",
			Claims: map[string]any{"year": "2025", "big": json.Number("9007199254740993")}, NotBefore: now, Expires: now.Add(time.Hour)}}}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// A code of another profile is refused and stays usable.
	if _, err := s.RedeemCode(ctx, "eudi", "c1", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("redemption on another profile: %v", err)
	}
	got, err := s.RedeemCode(ctx, "nl", "c1", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.Profile != "nl" || got.Reference != "case-1" || len(got.Documents) != 1 ||
		got.Documents[0].Claims["year"] != "2025" || fmt.Sprint(got.Documents[0].Claims["big"]) != "9007199254740993" ||
		!got.Documents[0].Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("session not restored: %+v", got)
	}
	if _, err := s.RedeemCode(ctx, "nl", "c1", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("second redemption: %v", err)
	}
	if err := s.CreateSession(ctx, issuance.Session{ID: "s2", Profile: "nl", CodeHash: "c2", ExpiresAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemCode(ctx, "nl", "c2", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("expired code: %v", err)
	}
}

func redeemConcurrently(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.RedeemCode(ctx, "nl", "c1", now); err == nil {
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
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Profile: "eudi", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, issuance.AccessToken{Hash: "t1", SessionID: "s1", JKT: "k", ClientID: "w", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	tok, sess, err := s.Token(ctx, "t1", now)
	if err != nil || tok.JKT != "k" || tok.ClientID != "w" || sess.Profile != "eudi" {
		t.Fatalf("%+v %+v %v", tok, sess, err)
	}
	if _, _, err := s.Token(ctx, "t1", now.Add(time.Minute)); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	if _, _, err := s.Token(ctx, "unknown", now); !errors.Is(err, issuance.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
}

func markIssued(t *testing.T, s Store) {
	if err := s.CreateSession(ctx, issuance.Session{ID: "s1", Profile: "nl", CodeHash: "c1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if first, err := s.MarkIssued(ctx, "s1", "d1"); err != nil || !first {
		t.Fatalf("first: %v %v", first, err)
	}
	if first, err := s.MarkIssued(ctx, "s1", "d1"); err != nil || first {
		t.Fatalf("second: %v %v", first, err)
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
}
