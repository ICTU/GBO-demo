package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/eudi-adapter/internal/postgresregistry"
)

type sourceReleaseOperations interface {
	ActiveRelease(context.Context) (onboarding.SourceRelease, error)
	Release(context.Context, string) (onboarding.SourceRelease, error)
	ActivateRelease(context.Context, string) error
}

type sourceReleaseSummary struct {
	ID                    string                 `json:"id"`
	Digest                string                 `json:"digest"`
	MaterializationDigest string                 `json:"materialization_digest"`
	CreatedAt             time.Time              `json:"created_at"`
	Sources               []releaseSourceSummary `json:"sources"`
}

type releaseSourceSummary struct {
	SourceID               string    `json:"source_id"`
	MetadataVersion        string    `json:"metadata_version"`
	DeploymentDigest       string    `json:"deployment_digest"`
	TransportAuthenticated bool      `json:"transport_authenticated"`
	FreshUntil             time.Time `json:"fresh_until"`
	StaleUntil             time.Time `json:"stale_until"`
}

func runSourceRegistryMigrateCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "migrate-source-registry" {
		return false, nil
	}
	set := flag.NewFlagSet("migrate-source-registry", flag.ContinueOnError)
	set.SetOutput(stderr)
	var databaseURL, schema, readerRole string
	set.StringVar(&databaseURL, "database-url", os.Getenv("SOURCE_REGISTRY_DATABASE_URL"), "PostgreSQL Source Registry connection URL")
	set.StringVar(&schema, "database-schema", getEnv("SOURCE_REGISTRY_SCHEMA", "source_registry"), "PostgreSQL Source Registry schema")
	set.StringVar(&readerRole, "reader-role", os.Getenv("SOURCE_REGISTRY_READER_ROLE"), "optional PostgreSQL role granted read-only release access")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	store, err := postgresregistry.Open(ctx, postgresregistry.Options{DatabaseURL: databaseURL, Schema: schema})
	if err != nil {
		return true, err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return true, err
	}
	if readerRole != "" {
		if err := store.GrantReadOnly(ctx, readerRole); err != nil {
			return true, err
		}
	}
	_, _ = fmt.Fprintln(stdout, "Source Registry migrations applied")
	return true, nil
}

func runSourceRegistryOperationsCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || (arguments[0] != "inspect-source-registry" && arguments[0] != "activate-source-release") {
		return false, nil
	}
	command := arguments[0]
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(stderr)
	var databaseURL, schema, releaseID string
	set.StringVar(&databaseURL, "database-url", os.Getenv("SOURCE_REGISTRY_DATABASE_URL"), "PostgreSQL Source Registry connection URL")
	set.StringVar(&schema, "database-schema", getEnv("SOURCE_REGISTRY_SCHEMA", "source_registry"), "PostgreSQL Source Registry schema")
	set.StringVar(&releaseID, "release-id", "", "immutable Source Release ID (defaults to the active release for inspection)")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if command == "activate-source-release" && strings.TrimSpace(releaseID) == "" {
		return true, fmt.Errorf("--release-id is required")
	}
	store, err := postgresregistry.Open(ctx, postgresregistry.Options{DatabaseURL: databaseURL, Schema: schema})
	if err != nil {
		return true, err
	}
	defer store.Close()

	var release onboarding.SourceRelease
	if command == "activate-source-release" {
		release, err = activateSourceRelease(ctx, store, releaseID)
	} else if releaseID == "" {
		release, err = store.ActiveRelease(ctx)
	} else {
		release, err = store.Release(ctx, releaseID)
	}
	if err != nil {
		return true, err
	}
	if err := writeSourceReleaseSummary(stdout, release); err != nil {
		return true, err
	}
	return true, nil
}

func activateSourceRelease(ctx context.Context, registry sourceReleaseOperations, releaseID string) (onboarding.SourceRelease, error) {
	// Load before changing the pointer so a typo or incomplete target cannot
	// disturb the currently active release.
	target, err := registry.Release(ctx, releaseID)
	if err != nil {
		return onboarding.SourceRelease{}, err
	}
	if err := registry.ActivateRelease(ctx, target.ID); err != nil {
		return onboarding.SourceRelease{}, err
	}
	active, err := registry.ActiveRelease(ctx)
	if err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("verify activated Source Release: %w", err)
	}
	if active.ID != target.ID {
		return onboarding.SourceRelease{}, fmt.Errorf("activated Source Release mismatch: got %s, want %s", active.ID, target.ID)
	}
	return active, nil
}

func writeSourceReleaseSummary(writer io.Writer, release onboarding.SourceRelease) error {
	summary := sourceReleaseSummary{
		ID: release.ID, Digest: release.Digest, MaterializationDigest: release.MaterializationDigest,
		CreatedAt: release.CreatedAt, Sources: make([]releaseSourceSummary, 0, len(release.Sources)),
	}
	for _, source := range release.Sources {
		summary.Sources = append(summary.Sources, releaseSourceSummary{
			SourceID: source.SourceID, MetadataVersion: source.MetadataVersion,
			DeploymentDigest: source.DeploymentDigest, TransportAuthenticated: source.TransportAuthenticated,
			FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
		})
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return fmt.Errorf("write Source Release summary: %w", err)
	}
	return nil
}
