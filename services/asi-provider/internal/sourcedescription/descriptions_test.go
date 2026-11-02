package sourcedescription

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gbo-demo/asi-provider/internal/verification"
	"gbo-demo/source-registry/onboarding"
)

var start = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// fakeRegistry is the Source Registry as the reader role sees it: the active
// release, or none, and its sources' lifecycle.
type fakeRegistry struct {
	release    *onboarding.SourceRelease
	staleUntil map[string]time.Time
	err        error
}

func (f *fakeRegistry) ActiveReleaseState(context.Context) (onboarding.ActiveReleaseState, bool, error) {
	if f.err != nil {
		return onboarding.ActiveReleaseState{}, false, f.err
	}
	if f.release == nil {
		return onboarding.ActiveReleaseState{}, false, nil
	}
	state := onboarding.ActiveReleaseState{ReleaseID: f.release.ID}
	for _, source := range f.release.Sources {
		staleUntil := source.StaleUntil
		if extended, ok := f.staleUntil[source.SourceID]; ok {
			staleUntil = extended
		}
		state.Sources = append(state.Sources, onboarding.ReleaseSourceLifecycle{
			SourceID: source.SourceID, CheckedAt: source.CheckedAt, ExpiresAt: source.ExpiresAt.Add(time.Hour),
			FreshUntil: source.FreshUntil, StaleUntil: staleUntil,
		})
	}
	return state, true, nil
}

func (f *fakeRegistry) ActiveRelease(context.Context) (onboarding.SourceRelease, error) {
	if f.err != nil {
		return onboarding.SourceRelease{}, f.err
	}
	if f.release == nil {
		return onboarding.SourceRelease{}, onboarding.ErrNoActiveRelease
	}
	return *f.release, nil
}

// brpASI is the asi capability the BRP mock publishes.
func brpASI(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Capabilities struct {
			ASI json.RawMessage `json:"asi"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || len(document.Capabilities.ASI) == 0 {
		t.Fatalf("the BRP publishes no asi: %v", err)
	}
	return document.Capabilities.ASI
}

// snapshot is a source's snapshot as the eudi-adapter stores it, with only
// what the ASIP reads.
func snapshot(t *testing.T, sourceID, version string, asi json.RawMessage) json.RawMessage {
	t.Helper()
	body := map[string]any{"schema_version": version, "source": map[string]string{"source_id": sourceID}}
	if asi != nil {
		body["asi"] = asi
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func candidate(sourceID string, snapshot json.RawMessage) onboarding.SourceCandidate {
	metadata := []byte(`{"display":[]}`)
	digest := sha256.Sum256(metadata)
	var certificates []onboarding.PublicCertificate
	for _, role := range []string{"issuer", "reader", "status", "issuer_ca", "reader_ca"} {
		certificates = append(certificates, onboarding.PublicCertificate{
			Role: role, Subject: "CN=" + role, SHA256: strings.Repeat("c", 64), NotAfter: start.Add(24 * time.Hour),
		})
	}
	return onboarding.SourceCandidate{
		SourceID: sourceID, MetadataVersion: "1.0",
		MetadataPayloadDigest: strings.Repeat("a", 64), DeploymentDigest: strings.Repeat("b", 64),
		CheckedAt: start, FreshUntil: start.Add(15 * time.Minute), StaleUntil: start.Add(time.Hour), ExpiresAt: start.Add(2 * time.Hour),
		Snapshot: snapshot, Offers: json.RawMessage(`[]`),
		CertificateSet: onboarding.PublicCertificateSet{ID: sourceID, Certificates: certificates},
		TypeMetadata: []onboarding.TypeMetadata{{
			VCT: "https://adapter.example/types/" + sourceID, Version: "1.0",
			Integrity: "sha256-" + base64.StdEncoding.EncodeToString(digest[:]), Bytes: metadata,
		}},
	}
}

func release(t *testing.T, candidates ...onboarding.SourceCandidate) *onboarding.SourceRelease {
	t.Helper()
	release, err := onboarding.NewSourceRelease(start, candidates)
	if err != nil {
		t.Fatal(err)
	}
	return &release
}

// withBRP is a release in which the BRP, rvig, describes its asi.
func withBRP(t *testing.T) *onboarding.SourceRelease {
	return release(t, candidate("rvig", snapshot(t, "rvig", "4.0", brpASI(t))),
		candidate("belastingdienst", snapshot(t, "belastingdienst", "3.0", nil)))
}

// asking is the source behind the description: it counts the questions.
type asking struct{ asked int }

func (a *asking) Values(context.Context, string, []string) (map[string]json.RawMessage, error) {
	a.asked++
	return map[string]json.RawMessage{"given_name": json.RawMessage(`{"given_name":"Frouke"}`)}, nil
}

type fixture struct {
	registry     *fakeRegistry
	descriptions *Descriptions
	now          time.Time
	source       *asking
}

func newFixture(registry *fakeRegistry) *fixture {
	f := &fixture{registry: registry, now: start, source: &asking{}}
	f.descriptions = New(func(context.Context) (Registry, error) { return f.registry, nil })
	f.descriptions.now = func() time.Time { return f.now }
	return f
}

// available reports whether a request for the BRP data service reaches its
// source, and checks that otherwise it is 503 without asking the source.
func (f *fixture) available(t *testing.T) bool {
	t.Helper()
	asked := f.source.asked
	_, err := f.descriptions.Require("rvig", f.source).Values(context.Background(), "999991772", []string{"given_name"})
	if err == nil {
		return true
	}
	if !errors.Is(err, verification.ErrSourceUnavailable) {
		t.Fatalf("Values() error = %v, want ErrSourceUnavailable", err)
	}
	if f.source.asked != asked {
		t.Fatal("the source was asked without a description")
	}
	return false
}

func (f *fixture) refresh(t *testing.T, wantErr bool) {
	t.Helper()
	if err := f.descriptions.Refresh(context.Background()); (err != nil) != wantErr {
		t.Fatalf("Refresh() error = %v, want error %v", err, wantErr)
	}
}

func TestDescriptions(t *testing.T) {
	t.Run("R1 no active release", func(t *testing.T) {
		f := newFixture(&fakeRegistry{})
		f.refresh(t, true)
		if f.available(t) {
			t.Fatal("available without a release")
		}
	})

	t.Run("R2 the registry is unreachable at start, then reachable", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: withBRP(t)})
		reachable := false
		f.descriptions.open = func(context.Context) (Registry, error) {
			if !reachable {
				return nil, errors.New("connection refused")
			}
			return f.registry, nil
		}
		f.refresh(t, true)
		if f.available(t) {
			t.Fatal("available before the registry was read")
		}
		reachable = true
		f.refresh(t, false)
		if !f.available(t) {
			t.Fatal("not available after the registry was read")
		}
	})

	t.Run("R3 a release in which the source describes asi", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: withBRP(t)})
		f.refresh(t, false)
		asi, ok := f.descriptions.Current("rvig")
		if !ok || asi.AuthenticSource.LegalName != "Basisregistratie Personen (mock)" || len(asi.Attributes) != 5 {
			t.Fatalf("Current(rvig) = %+v, %v", asi, ok)
		}
		if !f.available(t) || f.source.asked != 1 {
			t.Fatal("the source was not asked")
		}
	})

	t.Run("R4 the source without asi", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: release(t, candidate("rvig", snapshot(t, "rvig", "3.0", nil)))})
		f.refresh(t, false)
		if f.available(t) {
			t.Fatal("available without asi")
		}
	})

	t.Run("R5 a release without the source", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: release(t, candidate("belastingdienst", snapshot(t, "belastingdienst", "3.0", nil)))})
		f.refresh(t, false)
		if f.available(t) {
			t.Fatal("available without the source in the release")
		}
	})

	t.Run("R6 the description is stale", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: withBRP(t)})
		f.refresh(t, false)
		f.now = start.Add(time.Hour + time.Second)
		if f.available(t) {
			t.Fatal("available after stale_until")
		}
	})

	t.Run("R7 the registry goes away after a read", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: withBRP(t)})
		f.refresh(t, false)
		f.registry.err = errors.New("connection refused")
		f.now = start.Add(30 * time.Minute)
		f.refresh(t, true)
		if !f.available(t) {
			t.Fatal("not available before stale_until")
		}
		f.now = start.Add(time.Hour + time.Second)
		if f.available(t) {
			t.Fatal("available after stale_until")
		}
	})

	t.Run("R8 a new release that cannot be read keeps the previous one", func(t *testing.T) {
		unreadable := map[string]func(t *testing.T) *onboarding.SourceRelease{
			"an asi of another shape": func(t *testing.T) *onboarding.SourceRelease {
				asi := strings.Replace(string(brpASI(t)), `"version"`, `"versie"`, 1)
				return release(t, candidate("rvig", snapshot(t, "rvig", "4.0", json.RawMessage(asi))))
			},
			"an unknown snapshot version": func(t *testing.T) *onboarding.SourceRelease {
				return release(t, candidate("rvig", snapshot(t, "rvig", "9.0", brpASI(t))))
			},
			"a digest that does not match": func(t *testing.T) *onboarding.SourceRelease {
				tampered := release(t, candidate("rvig", snapshot(t, "rvig", "4.0", brpASI(t))))
				tampered.Sources[0].Snapshot = snapshot(t, "rvig", "3.0", nil)
				return tampered
			},
		}
		for name, next := range unreadable {
			t.Run(name, func(t *testing.T) {
				f := newFixture(&fakeRegistry{release: withBRP(t)})
				f.refresh(t, false)
				f.registry.release = next(t)
				f.refresh(t, true)
				if !f.available(t) {
					t.Fatal("the previous description was dropped")
				}
				f.now = start.Add(time.Hour + time.Second)
				if f.available(t) {
					t.Fatal("the previous description outlived its stale_until")
				}
			})
		}
	})

	t.Run("R9 the same release with a later stale_until", func(t *testing.T) {
		f := newFixture(&fakeRegistry{release: withBRP(t)})
		f.refresh(t, false)
		f.registry.staleUntil = map[string]time.Time{"rvig": start.Add(3 * time.Hour)}
		f.refresh(t, false)
		f.now = start.Add(2 * time.Hour)
		if !f.available(t) {
			t.Fatal("not available within the extended stale_until")
		}
	})
}
