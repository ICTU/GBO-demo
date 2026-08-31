package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/eudi-adapter/internal/postgresregistry"
)

type sourceReleaseReader interface {
	ActiveReleaseID(context.Context) (string, bool, error)
	ActiveRelease(context.Context) (onboarding.SourceRelease, error)
}

type releaseRuntimeSnapshot struct {
	releaseID    string
	bindings     map[string]sourceRuntimeBinding
	typeMetadata map[string]*typeMetadataPublication
	offers       []byte
	staleUntil   time.Time
}

type sourceReleaseRuntime struct {
	registry        sourceReleaseReader
	baseConfig      config
	refreshInterval time.Duration

	mu         sync.Mutex
	snapshot   *releaseRuntimeSnapshot
	checkAfter time.Time
}

type releaseSourceRuntime struct {
	config     config
	metadata   activeSourceMetadata
	freshUntil time.Time
	staleUntil time.Time
}

func openRuntimeSourceRegistry(ctx context.Context, cfg config) (*postgresregistry.Store, error) {
	return postgresregistry.Open(ctx, postgresregistry.Options{
		DatabaseURL: cfg.SourceRegistryDatabaseURL,
		Schema:      cfg.SourceRegistrySchema,
	})
}

func newSourceReleaseRuntimeMux(cfg config, client *http.Client, registry sourceReleaseReader) *http.ServeMux {
	runtime := &sourceReleaseRuntime{registry: registry, baseConfig: cfg, refreshInterval: cfg.SourceRegistryRefresh}
	if runtime.refreshInterval <= 0 {
		runtime.refreshInterval = 5 * time.Second
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /attestations/{sourceID}/{typeID}", func(w http.ResponseWriter, request *http.Request) {
		snapshot, err := runtime.current(request.Context(), time.Now())
		if err != nil {
			http.Error(w, "source activations unavailable", http.StatusServiceUnavailable)
			return
		}
		binding := snapshot.bindings[request.PathValue("sourceID")+"\x00"+request.PathValue("typeID")]
		if binding.runtime == nil {
			http.NotFound(w, request)
			return
		}
		handleSourceAttestation(binding.config, client, binding.runtime).ServeHTTP(w, request)
	})
	mux.HandleFunc("GET /types/", func(w http.ResponseWriter, request *http.Request) {
		snapshot, err := runtime.current(request.Context(), time.Now())
		if err != nil {
			http.Error(w, "source release unavailable", http.StatusServiceUnavailable)
			return
		}
		publication := snapshot.typeMetadata[request.URL.Path]
		if publication == nil {
			http.NotFound(w, request)
			return
		}
		publication.ServeHTTP(w, request)
	})
	mux.HandleFunc("GET /eudi-offers.json", func(w http.ResponseWriter, request *http.Request) {
		snapshot, err := runtime.current(request.Context(), time.Now())
		if err != nil || time.Now().After(snapshot.staleUntil) {
			http.Error(w, "source release unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(snapshot.offers)
	})
	return mux
}

func (r *sourceReleaseRuntime) current(ctx context.Context, now time.Time) (*releaseRuntimeSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot != nil && now.Before(r.checkAfter) {
		return r.snapshot, nil
	}
	releaseID, found, err := r.registry.ActiveReleaseID(ctx)
	if err != nil {
		r.checkAfter = now.Add(activationReloadRetryInterval)
		if r.snapshot != nil {
			slog.Warn("Source Registry active release check failed; retaining complete previous snapshot", "err", err.Error())
			return r.snapshot, nil
		}
		return nil, err
	}
	if !found {
		r.checkAfter = now.Add(activationReloadRetryInterval)
		if r.snapshot != nil {
			return r.snapshot, nil
		}
		return nil, onboarding.ErrNoActiveRelease
	}
	if r.snapshot != nil && r.snapshot.releaseID == releaseID {
		r.checkAfter = now.Add(r.refreshInterval)
		return r.snapshot, nil
	}
	release, err := r.registry.ActiveRelease(ctx)
	if err != nil {
		r.checkAfter = now.Add(activationReloadRetryInterval)
		if r.snapshot != nil {
			slog.Warn("Source Registry release refresh failed; retaining complete previous snapshot", "release_id", releaseID, "err", err.Error())
			return r.snapshot, nil
		}
		return nil, err
	}
	if release.ID != releaseID {
		return nil, fmt.Errorf("active release changed while loading: pointer=%s release=%s", releaseID, release.ID)
	}
	next, err := buildReleaseRuntimeSnapshot(r.baseConfig, release)
	if err != nil {
		r.checkAfter = now.Add(activationReloadRetryInterval)
		if r.snapshot != nil {
			slog.Error("active Source Registry release is invalid; retaining complete previous snapshot", "release_id", releaseID, "err", err.Error())
			return r.snapshot, nil
		}
		return nil, err
	}
	r.snapshot = next
	r.checkAfter = now.Add(r.refreshInterval)
	slog.Info("Source Registry release activated", "release_id", release.ID, "materialization_digest", release.MaterializationDigest)
	return next, nil
}

func buildReleaseRuntimeSnapshot(base config, release onboarding.SourceRelease) (*releaseRuntimeSnapshot, error) {
	if release.ID == "" || len(release.Sources) == 0 {
		return nil, fmt.Errorf("active source release is incomplete")
	}
	snapshot := &releaseRuntimeSnapshot{
		releaseID: release.ID, bindings: make(map[string]sourceRuntimeBinding),
		typeMetadata: make(map[string]*typeMetadataPublication), offers: append([]byte(nil), release.Offers...),
	}
	for _, source := range release.Sources {
		candidate := onboarding.SourceCandidate{
			SourceID: source.SourceID, MetadataVersion: source.MetadataVersion,
			MetadataPayloadDigest: source.MetadataPayloadDigest, MetadataETag: source.MetadataETag,
			DeploymentDigest: source.DeploymentDigest, CheckedAt: source.CheckedAt,
			ExpiresAt: source.ExpiresAt, FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
			TransportAuthenticated: source.TransportAuthenticated, Snapshot: source.Snapshot,
			CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
		}
		activation, err := activationFromRegistryCandidate(candidate)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", source.SourceID, err)
		}
		if snapshot.staleUntil.IsZero() || source.StaleUntil.Before(snapshot.staleUntil) {
			snapshot.staleUntil = source.StaleUntil
		}
		for _, activatedType := range activation.Types {
			cfg := base
			cfg.SourceMetadataSourceID = activation.Source.SourceID
			cfg.SourceMetadataOIN = activation.Source.SourceOIN
			cfg.SourceMetadataTypeID = activatedType.TypeID
			cfg.SourceMetadataVersion = activation.MetadataVersion
			cfg.SourceMetadataDefinition = activatedType.Definition
			cfg.SourceMetadataVCT = activatedType.VCT
			cfg.SourceMetadataVCTIntegrity = activatedType.VCTIntegrity
			cfg.SourceMetadataFreshUntil = activation.FreshUntil
			cfg.SourceMetadataStaleUntil = activation.StaleUntil
			cfg.SourceDataTransport = activation.Source.DataAccess.Transport
			cfg.SourceDataFSCServiceReference = activation.Source.DataAccess.ServiceReference
			cfg.SourceDataFSCGrantHash = activation.Source.DataAccess.GrantHash
			runtime := &releaseSourceRuntime{
				config: cfg,
				metadata: activeSourceMetadata{
					Version: cfg.SourceMetadataVersion, SourceID: cfg.SourceMetadataSourceID,
					SourceOIN: cfg.SourceMetadataOIN, TypeID: cfg.SourceMetadataTypeID,
					Definition: cfg.SourceMetadataDefinition, VCT: cfg.SourceMetadataVCT,
					VCTIntegrity: cfg.SourceMetadataVCTIntegrity,
				},
				freshUntil: activation.FreshUntil, staleUntil: activation.StaleUntil,
			}
			key := source.SourceID + "\x00" + activatedType.TypeID
			if _, duplicate := snapshot.bindings[key]; duplicate {
				return nil, fmt.Errorf("duplicate active source/type %s/%s", source.SourceID, activatedType.TypeID)
			}
			snapshot.bindings[key] = sourceRuntimeBinding{config: cfg, runtime: runtime}
		}
		for _, metadata := range source.TypeMetadata {
			publication, err := publicationFromRelease(metadata)
			if err != nil {
				return nil, fmt.Errorf("source %q Type Metadata: %w", source.SourceID, err)
			}
			if existing := snapshot.typeMetadata[publication.path]; existing != nil {
				return nil, fmt.Errorf("duplicate Type Metadata path %q", publication.path)
			}
			snapshot.typeMetadata[publication.path] = publication
		}
	}
	return snapshot, nil
}

func (r *releaseSourceRuntime) current(now time.Time) (*activeSourceMetadata, error) {
	metadata, _, err := r.currentSource(now)
	return metadata, err
}

func (r *releaseSourceRuntime) currentSource(now time.Time) (*activeSourceMetadata, config, error) {
	if now.After(r.staleUntil) {
		return nil, config{}, fmt.Errorf("activated source snapshot expired outside stale grace")
	}
	metadata := r.metadata
	metadata.CacheState = "fresh"
	if now.After(r.freshUntil) {
		metadata.CacheState = "stale"
	}
	return &metadata, r.config, nil
}

func publicationFromRelease(metadata onboarding.TypeMetadata) (*typeMetadataPublication, error) {
	parsed, err := url.Parse(metadata.VCT)
	if err != nil || parsed.Path == "" {
		return nil, fmt.Errorf("invalid VCT %q", metadata.VCT)
	}
	digest := sha256.Sum256(metadata.Bytes)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	if integrity != metadata.Integrity {
		return nil, fmt.Errorf("integrity mismatch for %q", metadata.VCT)
	}
	return &typeMetadataPublication{
		TypeVersion: metadata.Version, VCT: metadata.VCT, Integrity: metadata.Integrity,
		body: append([]byte(nil), metadata.Bytes...), etag: `"` + fmt.Sprintf("%x", digest) + `"`, path: parsed.Path,
	}, nil
}
