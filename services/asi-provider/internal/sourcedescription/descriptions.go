// Package sourcedescription keeps the ASIP's view of the active Source
// Registry release: per authentic source, the asi capability the source
// describes, until that description goes stale.
//
// The ASIP reads the release with the registry's reader role, which may read
// releases and the active pointer only. Without a current description of its
// source a data service answers 503: the ASIP cannot say what the source
// holds. A release the ASIP cannot read leaves the previous description in
// place until it goes stale, and an unreachable registry does the same.
package sourcedescription

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/verification"
	"gbo-demo/source-registry/asisnapshot"
	"gbo-demo/source-registry/onboarding"
)

// Registry is what the ASIP reads of the Source Registry.
type Registry interface {
	ActiveReleaseState(context.Context) (onboarding.ActiveReleaseState, bool, error)
	ActiveRelease(context.Context) (onboarding.SourceRelease, error)
}

// Descriptions holds the descriptions of the active release.
type Descriptions struct {
	open     func(context.Context) (Registry, error)
	registry Registry
	now      func() time.Time

	mu        sync.RWMutex
	releaseID string
	sources   map[string]description
}

type description struct {
	asi        *asimapping.Capability
	staleUntil time.Time
}

// New returns Descriptions that open the registry with open on the first
// Refresh, and again after a failed open: the ASIP starts without it.
func New(open func(context.Context) (Registry, error)) *Descriptions {
	return &Descriptions{open: open, now: time.Now}
}

// Run refreshes every interval until ctx ends.
func (d *Descriptions) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.Refresh(ctx); err != nil {
			slog.Warn("Source Registry not read; keeping the descriptions read before until they go stale", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Refresh reads the registry once. For the release read before it only
// takes the sources' lifecycle; a new release replaces all descriptions, or
// none when one of its sources cannot be read.
func (d *Descriptions) Refresh(ctx context.Context) error {
	if d.registry == nil {
		registry, err := d.open(ctx)
		if err != nil {
			return fmt.Errorf("open Source Registry: %w", err)
		}
		d.registry = registry
	}
	state, found, err := d.registry.ActiveReleaseState(ctx)
	if err != nil {
		return err
	}
	if !found {
		return onboarding.ErrNoActiveRelease
	}
	d.mu.Lock()
	if state.ReleaseID == d.releaseID {
		for _, lifecycle := range state.Sources {
			if current, ok := d.sources[lifecycle.SourceID]; ok {
				current.staleUntil = lifecycle.StaleUntil
				d.sources[lifecycle.SourceID] = current
			}
		}
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	release, err := d.registry.ActiveRelease(ctx)
	if err != nil {
		return err
	}
	if release.ID != state.ReleaseID {
		return fmt.Errorf("active release changed while loading: pointer %s, release %s", state.ReleaseID, release.ID)
	}
	if err := release.Validate(); err != nil {
		return fmt.Errorf("release %s: %w", release.ID, err)
	}
	sources := make(map[string]description, len(release.Sources))
	for _, source := range release.Sources {
		asi, err := asisnapshot.Read(source)
		if err != nil {
			return fmt.Errorf("release %s: %w", release.ID, err)
		}
		sources[source.SourceID] = description{asi: asi, staleUntil: source.StaleUntil}
	}
	d.mu.Lock()
	d.releaseID, d.sources = release.ID, sources
	d.mu.Unlock()
	slog.Info("Source Registry release read", "release_id", release.ID)
	return nil
}

// Current returns the asi capability of the source with sourceID, when the
// active release holds one that has not gone stale.
func (d *Descriptions) Current(sourceID string) (*asimapping.Capability, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	source, ok := d.sources[sourceID]
	if !ok || source.asi == nil || d.now().After(source.staleUntil) {
		return nil, false
	}
	return source.asi, true
}

// Require returns a source that asks source only while the source with
// sourceID has a current description; otherwise the source is unavailable.
func (d *Descriptions) Require(sourceID string, source verification.Source) verification.Source {
	return required{descriptions: d, sourceID: sourceID, source: source}
}

type required struct {
	descriptions *Descriptions
	sourceID     string
	source       verification.Source
}

func (r required) Values(ctx context.Context, bsn string, names []string) (map[string]json.RawMessage, error) {
	if _, ok := r.descriptions.Current(r.sourceID); !ok {
		return nil, fmt.Errorf("%w: no current description of source %q in the Source Registry", verification.ErrSourceUnavailable, r.sourceID)
	}
	return r.source.Values(ctx, bsn, names)
}
