package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The EC public key of RFC 7517, appendix A.1. The expected thumbprint was
// computed independently with `step crypto jwk thumbprint`.
func TestJWKThumbprintKnownAnswer(t *testing.T) {
	coordinate := func(s string) *big.Int {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return new(big.Int).SetBytes(b)
	}
	pub := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     coordinate("MKBCTNIcKUSDii11ySs3526iDZ8AiTo7Tu6KPAqv7D4"),
		Y:     coordinate("4Etl6SRW2YiLUrN5vfvVHuhp7x8PxltmWWlbbM4IFyM"),
	}
	if got, want := jwkThumbprint(pub), "cn-I_WNMClehiVp51i_0VpOENW1upEerA8sEam5hn-s"; got != want {
		t.Fatalf("thumbprint = %q, want %q", got, want)
	}
}

func TestADerivedKidChangesWithTheKey(t *testing.T) {
	cfg := config{TokenIssuer: "issuer", TokenAudience: "audience"}
	first, err := NewConsentIssuer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewConsentIssuer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.kid != jwkThumbprint(&first.key.PublicKey) {
		t.Fatalf("kid %q is not the key's thumbprint", first.kid)
	}
	if first.kid == second.kid {
		t.Fatalf("two generated keys share the kid %q", first.kid)
	}
	assertKidInJWKSAndToken(t, first, first.kid)
}

func TestAnExplicitKidIsKept(t *testing.T) {
	issuer, err := NewConsentIssuer(config{SigningKeyID: "explicit-key", TokenIssuer: "issuer", TokenAudience: "audience"})
	if err != nil {
		t.Fatal(err)
	}
	assertKidInJWKSAndToken(t, issuer, "explicit-key")
}

func assertKidInJWKSAndToken(t *testing.T, issuer *ConsentIssuer, want string) {
	t.Helper()
	keys := issuer.JWKS()["keys"].([]map[string]string)
	if len(keys) != 1 || keys[0]["kid"] != want {
		t.Fatalf("JWKS kid = %v, want %q", keys, want)
	}
	signed, err := issuer.Sign(Consent{ConsentID: "c1", ValidUntil: time.Now().Add(time.Hour)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := jwt.NewParser().ParseUnverified(signed, &ConsentClaims{})
	if err != nil {
		t.Fatal(err)
	}
	if token.Header["kid"] != want {
		t.Fatalf("token kid = %v, want %q", token.Header["kid"], want)
	}
}

func TestConsentStoreSetting(t *testing.T) {
	tests := []struct {
		name, store, databaseURL, keyPath string
		want                              string
		wantErr                           bool
	}{
		{name: "default without database", want: storeMemory},
		{name: "default with database", databaseURL: "postgres://example", keyPath: "/key.pem", want: storePostgres},
		{name: "explicit memory", store: "memory", want: storeMemory},
		{name: "explicit postgres", store: "postgres", databaseURL: "postgres://example", keyPath: "/key.pem", want: storePostgres},
		{name: "postgres without database", store: "postgres", keyPath: "/key.pem", wantErr: true},
		{name: "memory with database", store: "memory", databaseURL: "postgres://example", wantErr: true},
		{name: "unknown store", store: "sqlite", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CONSENT_STORE", tt.store)
			t.Setenv("DATABASE_URL", tt.databaseURL)
			t.Setenv("CONSENT_SIGNING_KEY_PATH", tt.keyPath)
			cfg, err := loadConfig()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("loadConfig accepted CONSENT_STORE=%q DATABASE_URL=%q", tt.store, tt.databaseURL)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Store != tt.want {
				t.Fatalf("store = %q, want %q", cfg.Store, tt.want)
			}
		})
	}
}
