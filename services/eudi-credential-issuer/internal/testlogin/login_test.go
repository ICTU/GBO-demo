package testlogin_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/testlogin"
	"net/url"
	"strings"
	"testing"
	"time"
)

type people struct{}

func (people) Load(context.Context) ([]testlogin.Person, error) {
	return []testlogin.Person{{BSN: "999991772", Name: "Frouke"}}, nil
}
func TestSelectionCodes(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	resource := "https://issuer.test/test-pid/eudi"
	s := testlogin.New("test-pid", resource, people{}, memstore.New(), func() time.Time { return now })
	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	start := func() string {
		t.Helper()
		location, err := s.AuthorizationURL(ctx, "test-pid", resource, nil, "state", challenge)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(location)
		return u.Query().Get("ticket")
	}
	choose := func() string {
		t.Helper()
		ticket := start()
		location, err := s.Choose(ctx, ticket, "999991772")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Choose(ctx, ticket, "999991772"); !errors.Is(err, issuance.ErrInvalidAuthorization) {
			t.Fatal("replayed choice")
		}
		u, _ := url.Parse(location)
		if u.Query().Get("state") != "state" {
			t.Fatal("state changed")
		}
		return u.Query().Get("code")
	}
	code := choose()
	if bsn, err := s.Exchange(ctx, "test-pid", resource, code, verifier); err != nil || bsn != "999991772" {
		t.Fatal(bsn, err)
	}
	if _, err := s.Exchange(ctx, "test-pid", resource, code, verifier); !errors.Is(err, issuance.ErrInvalidAuthorization) {
		t.Fatal("replayed login code")
	}
	code = choose()
	if _, err := s.Exchange(ctx, "test-pid", resource, code, strings.Repeat("b", 43)); !errors.Is(err, issuance.ErrInvalidAuthorization) {
		t.Fatal("wrong PKCE")
	}
	code = choose()
	now = now.Add(61 * time.Second)
	if _, err := s.Exchange(ctx, "test-pid", resource, code, verifier); !errors.Is(err, issuance.ErrInvalidAuthorization) {
		t.Fatal("expired local login code")
	}
	ticket := start()
	now = now.Add(issuance.LoginLifetime)
	if _, err := s.Choices(ctx, ticket); !errors.Is(err, issuance.ErrInvalidAuthorization) {
		t.Fatal("expired selection")
	}
	if _, err := s.AuthorizationURL(ctx, "other", resource, nil, "state", challenge); !errors.Is(err, issuance.ErrInvalidAuthorization) {
		t.Fatal("cross-provider login")
	}
}
