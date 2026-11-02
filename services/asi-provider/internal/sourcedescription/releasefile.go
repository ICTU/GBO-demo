package sourcedescription

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/source-registry/onboarding"
)

// A release file stands in for the Source Registry where there is none, as
// in the QTSP's end-to-end test: it holds a release as the registry does
// (onboarding.SourceRelease in JSON), and the ASIP reads it as the active
// release. The ASIP reads either the registry or a release file.

// ReadReleaseFile returns the registry whose active release is the one in
// the file at path.
func ReadReleaseFile(path string) (Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release file: %w", err)
	}
	var release onboarding.SourceRelease
	if err := json.Unmarshal(raw, &release); err != nil {
		return nil, fmt.Errorf("parse release file: %w", err)
	}
	return releaseFile{release: release}, nil
}

type releaseFile struct{ release onboarding.SourceRelease }

func (f releaseFile) ActiveReleaseState(context.Context) (onboarding.ActiveReleaseState, bool, error) {
	state := onboarding.ActiveReleaseState{ReleaseID: f.release.ID}
	for _, source := range f.release.Sources {
		state.Sources = append(state.Sources, onboarding.ReleaseSourceLifecycle{
			SourceID: source.SourceID, CheckedAt: source.CheckedAt, ExpiresAt: source.ExpiresAt,
			FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
		})
	}
	return state, true, nil
}

func (f releaseFile) ActiveRelease(context.Context) (onboarding.SourceRelease, error) {
	return f.release, nil
}

// ReleaseFromDocument builds a release with one source, sourceID, whose
// description is the asi capability of its published source metadata
// document, valid from now for valid. It checks the capability against the
// catalogue, as onboarding does. It is not a release the eudi-adapter
// onboarded: its certificates and Type Metadata are placeholders, and its
// snapshot holds only what the ASIP reads.
func ReleaseFromDocument(sourceID string, document []byte, catalogue asimapping.Catalogue, now time.Time, valid time.Duration) (onboarding.SourceRelease, error) {
	var published struct {
		Version      string `json:"version"`
		Capabilities struct {
			ASI *asimapping.Capability `json:"asi"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(document, &published); err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("parse source metadata: %w", err)
	}
	asi := published.Capabilities.ASI
	if asi == nil {
		return onboarding.SourceRelease{}, errors.New("the source metadata describes no asi")
	}
	if err := asi.Validate(catalogue); err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("asi capability: %w", err)
	}
	snapshot, err := json.Marshal(map[string]any{
		"schema_version": "4.0",
		"source":         map[string]string{"source_id": sourceID},
		"asi":            asi,
	})
	if err != nil {
		return onboarding.SourceRelease{}, err
	}
	placeholder := []byte(`{}`)
	digest := sha256.Sum256(placeholder)
	var certificates []onboarding.PublicCertificate
	for _, role := range []string{"issuer", "reader", "status", "issuer_ca", "reader_ca"} {
		certificates = append(certificates, onboarding.PublicCertificate{
			Role: role, Subject: "CN=placeholder " + role, SHA256: strings.Repeat("0", 64), NotAfter: now.Add(valid),
		})
	}
	payload := sha256.Sum256(document)
	return onboarding.NewSourceRelease(now, []onboarding.SourceCandidate{{
		SourceID: sourceID, MetadataVersion: published.Version,
		MetadataPayloadDigest: fmt.Sprintf("%x", payload), DeploymentDigest: strings.Repeat("0", 64),
		CheckedAt: now, FreshUntil: now.Add(valid), StaleUntil: now.Add(valid), ExpiresAt: now.Add(valid),
		Snapshot: snapshot, Offers: json.RawMessage(`[]`),
		CertificateSet: onboarding.PublicCertificateSet{ID: sourceID, Certificates: certificates},
		TypeMetadata: []onboarding.TypeMetadata{{
			VCT: "urn:placeholder:" + sourceID, Version: "0",
			Integrity: "sha256-" + base64.StdEncoding.EncodeToString(digest[:]), Bytes: placeholder,
		}},
	}})
}

// WriteReleaseFile is the command write-release-file: it writes the release
// of ReleaseFromDocument to a file.
func WriteReleaseFile(args []string) error {
	fs := flag.NewFlagSet("write-release-file", flag.ContinueOnError)
	sourceID := fs.String("source", "", "the source's id in the Source Registry")
	metadataPath := fs.String("metadata", "", "the source's published source metadata document")
	cataloguePath := fs.String("catalogue", "", "GBO's attribute catalogue (schemas/asi-attributes.json)")
	valid := fs.Duration("valid", 24*time.Hour, "how long the description stays current")
	out := fs.String("out", "", "the release file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sourceID == "" || *metadataPath == "" || *cataloguePath == "" || *out == "" {
		return errors.New("--source, --metadata, --catalogue and --out are required")
	}
	document, err := os.ReadFile(*metadataPath)
	if err != nil {
		return err
	}
	catalogue, err := asimapping.LoadCatalogue(*cataloguePath)
	if err != nil {
		return err
	}
	release, err := ReleaseFromDocument(*sourceID, document, catalogue, time.Now().UTC(), *valid)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(release, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, raw, 0o600)
}
