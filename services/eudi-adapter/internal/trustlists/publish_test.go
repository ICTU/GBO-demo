package trustlists

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

type memorySource struct {
	snapshot Snapshot
	err      error
}

func (s *memorySource) Load(context.Context) (Snapshot, error) { return s.snapshot, s.err }

type memorySigner struct {
	calls  int
	failAt int
}

func (*memorySigner) Identity() string { return "test-scheme-signer" }
func (s *memorySigner) Sign(_ context.Context, body []byte, _ time.Time) ([]byte, error) {
	s.calls++
	if s.calls == s.failAt {
		return nil, errors.New("signer failed")
	}
	return bytes.Clone(body), nil
}

type memoryPublications struct{ current *Publication }

func (s *memoryPublications) Update(_ context.Context, update func(*Publication) (*Publication, error)) error {
	next, err := update(s.current)
	if err == nil && next != nil {
		s.current = next
	}
	return err
}

func fixtureRoot(t *testing.T, name string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func fixtureSource(t *testing.T) *memorySource {
	t.Helper()
	gbo := map[Role][]byte{PID: fixtureRoot(t, "GBO test-PID root"), PubEAA: fixtureRoot(t, "ca.gbo-issuer"), WRPAC: fixtureRoot(t, "ca.gbo-reader"), WRPRC: fixtureRoot(t, "GBO development registrar root")}
	return &memorySource{snapshot: Snapshot{GBO: gbo, NLVersion: 49, NLSource: "https://nl.example/config", NLAuthentication: NLJWTVerified, NL: map[Role][]Anchor{
		PID:    {{DER: fixtureRoot(t, "NL PID CA"), Origins: []string{"https://nl.example/config#version=49"}}},
		PubEAA: {{DER: gbo[PubEAA], Origins: []string{"https://nl.example/config#version=49"}}},
	}}}
}

func TestPublishRolePolicies(t *testing.T) {
	source := fixtureSource(t)
	signer, store := &memorySigner{}, &memoryPublications{}
	options := Options{PublicURL: "https://trust.example", Validity: 30 * 24 * time.Hour, Now: time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)}
	result, err := Publish(context.Background(), options, source, signer, store)
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts[PID] != 2 || result.Counts[PubEAA] != 1 || result.Counts[WRPAC] != 1 || result.Counts[WRPRC] != 1 {
		t.Fatalf("role separation and deduplication: %v", result.Counts)
	}
	previous := store.current
	t.Run("issuer cannot become PID", func(t *testing.T) {
		original := source.snapshot.GBO[PID]
		source.snapshot.GBO[PID] = source.snapshot.GBO[PubEAA]
		defer func() { source.snapshot.GBO[PID] = original }()
		if _, err := Publish(context.Background(), options, source, signer, store); err == nil {
			t.Fatal("role substitution accepted")
		}
		if store.current != previous {
			t.Fatal("refusal replaced publication")
		}
	})
	t.Run("missing root", func(t *testing.T) {
		original := source.snapshot.GBO[PID]
		delete(source.snapshot.GBO, PID)
		defer func() { source.snapshot.GBO[PID] = original }()
		if _, err := Publish(context.Background(), options, source, signer, store); err == nil {
			t.Fatal("missing root accepted")
		}
		if store.current != previous {
			t.Fatal("refusal replaced publication")
		}
	})
	t.Run("source unavailable", func(t *testing.T) {
		source.err = errors.New("unavailable")
		defer func() { source.err = nil }()
		if _, err := Publish(context.Background(), options, source, signer, store); err == nil {
			t.Fatal("unavailable source accepted")
		}
		if store.current != previous {
			t.Fatal("refusal replaced publication")
		}
	})
}

func TestPublishSequenceAndRenewal(t *testing.T) {
	source := fixtureSource(t)
	signer, store := &memorySigner{}, &memoryPublications{}
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	options := Options{PublicURL: "https://trust.example", Validity: 30 * 24 * time.Hour, Now: now}
	if _, err := Publish(context.Background(), options, source, signer, store); err != nil {
		t.Fatal(err)
	}
	first := store.current
	if first.Sequence != 1 || !first.Issue.Equal(now) || !first.NextUpdate.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("first publication: %+v", first)
	}
	options.Now = now.Add(time.Hour)
	result, err := Publish(context.Background(), options, source, signer, store)
	if err != nil || result.Changed || store.current != first || signer.calls != 4 {
		t.Fatal("no-op changed files, times or signer calls")
	}
	options.Renew = true
	if _, err := Publish(context.Background(), options, source, signer, store); err != nil {
		t.Fatal(err)
	}
	if store.current.Sequence != 2 || !store.current.Issue.Equal(options.Now) {
		t.Fatal("renewal must advance the sequence and issue time")
	}
	options.Renew = false
	source.snapshot.GBO[PID] = fixtureRoot(t, "GBO test-PID root")
	if _, err := Publish(context.Background(), options, source, signer, store); err != nil {
		t.Fatal(err)
	}
	if store.current.Sequence != 3 {
		t.Fatal("root change must advance the sequence")
	}
	previous := store.current
	options.Renew = true
	signer.failAt = signer.calls + 3
	if _, err := Publish(context.Background(), options, source, signer, store); err == nil {
		t.Fatal("partial signing failure expected")
	}
	if store.current != previous {
		t.Fatal("partial signing failure replaced publication")
	}
	signer.failAt = 0
	source.snapshot.NLVersion--
	if _, err := Publish(context.Background(), options, source, signer, store); err == nil {
		t.Fatal("NL rollback accepted")
	}
	if store.current != previous {
		t.Fatal("NL rollback replaced publication")
	}
}

func TestPublishAuthenticationChange(t *testing.T) {
	source := fixtureSource(t)
	store, signer := &memoryPublications{}, &memorySigner{}
	options := Options{PublicURL: "https://trust.example", Validity: 30 * 24 * time.Hour, Now: time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)}
	if _, err := Publish(context.Background(), options, source, signer, store); err != nil {
		t.Fatal(err)
	}
	source.snapshot.NLAuthentication = NLHTTPSDemo
	result, err := Publish(context.Background(), options, source, signer, store)
	if err != nil || !result.Changed || result.Sequence != 2 || store.current.NLAuthentication != NLHTTPSDemo {
		t.Fatalf("authentication change must republish: %+v %v", result, err)
	}
}
