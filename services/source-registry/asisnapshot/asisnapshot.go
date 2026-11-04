// Package asisnapshot reads, from a source in a Source Registry release, the
// asi capability the source describes: what the ASIP needs of a release.
//
// The eudi-adapter writes the snapshot when it onboards a source; its tests
// hold what it writes against Read, so the format has one reader for the
// ASIP rather than a copy in each consumer.
package asisnapshot

import (
	"bytes"
	"encoding/json"
	"fmt"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/source-registry/onboarding"
)

// The snapshot versions Read accepts. A snapshot without asi is 3.0; one
// with asi is 4.0, so an adapter that knows only 3.0 does not rewrite it.
const (
	withoutASI = "3.0"
	withASI    = "4.0"
)

// Read returns the asi capability of source, nil when the source describes
// none. An error means the snapshot cannot be read: an unknown version, a
// snapshot of another source, or an asi capability of another shape.
func Read(source onboarding.ReleaseSource) (*asimapping.Capability, error) {
	var snapshot struct {
		SchemaVersion string `json:"schema_version"`
		Source        struct {
			SourceID string `json:"source_id"`
		} `json:"source"`
		ASI json.RawMessage `json:"asi"`
	}
	if err := json.Unmarshal(source.Snapshot, &snapshot); err != nil {
		return nil, fmt.Errorf("source %q: decode snapshot: %w", source.SourceID, err)
	}
	if snapshot.Source.SourceID != source.SourceID {
		return nil, fmt.Errorf("source %q: the snapshot is of source %q", source.SourceID, snapshot.Source.SourceID)
	}
	switch snapshot.SchemaVersion {
	case withoutASI:
		return nil, nil
	case withASI:
	default:
		return nil, fmt.Errorf("source %q: snapshot version %q is not one the ASIP reads", source.SourceID, snapshot.SchemaVersion)
	}
	if len(snapshot.ASI) == 0 || string(snapshot.ASI) == "null" {
		return nil, fmt.Errorf("source %q: a %s snapshot without asi", source.SourceID, withASI)
	}
	var capability asimapping.Capability
	decoder := json.NewDecoder(bytes.NewReader(snapshot.ASI))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capability); err != nil {
		return nil, fmt.Errorf("source %q: decode asi: %w", source.SourceID, err)
	}
	return &capability, nil
}
