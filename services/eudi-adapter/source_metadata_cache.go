package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var defaultSourceMetadataCachePolicy = sourceMetadataCachePolicy{
	MinimumValidity:  time.Hour,
	MaximumFreshness: 15 * time.Minute,
	StaleGrace:       time.Hour,
}

const (
	activationReloadInterval      = 5 * time.Minute
	activationReloadRetryInterval = 5 * time.Second
)

type sourceMetadataCachePolicy struct {
	MinimumValidity  time.Duration
	MaximumFreshness time.Duration
	StaleGrace       time.Duration
}

type unavailableSourceMetadataRuntime struct {
	sourceID     string
	typeID       string
	err          error
	publications map[string]*typeMetadataPublication
}

type activatedSourceRuntime struct {
	mu             sync.Mutex
	config         config
	metadata       activeSourceMetadata
	freshUntil     time.Time
	staleUntil     time.Time
	reloadAfter    time.Time
	activationPath string
	typeID         string
	storePath      string
	publications   map[string]*typeMetadataPublication
}

func newActivatedSourceRuntime(cfg config) (*activatedSourceRuntime, error) {
	if !sourceIDPattern.MatchString(cfg.SourceMetadataSourceID) || !typeIDPattern.MatchString(cfg.SourceMetadataTypeID) || cfg.SourceMetadataVCT == "" {
		return nil, fmt.Errorf("activated source snapshot is incomplete")
	}
	publications, err := loadTypeMetadataPublications(cfg.TypeMetadataStorePath)
	if err != nil {
		return nil, err
	}
	runtime := &activatedSourceRuntime{
		config: cfg,
		metadata: activeSourceMetadata{
			Version: cfg.SourceMetadataVersion, SourceID: cfg.SourceMetadataSourceID, SourceOIN: cfg.SourceMetadataOIN,
			TypeID: cfg.SourceMetadataTypeID, Definition: cfg.SourceMetadataDefinition,
			VCT: cfg.SourceMetadataVCT, VCTIntegrity: cfg.SourceMetadataVCTIntegrity,
		},
		freshUntil: cfg.SourceMetadataFreshUntil, staleUntil: cfg.SourceMetadataStaleUntil,
		activationPath: cfg.SourceActivationPath, typeID: cfg.SourceMetadataTypeID, storePath: cfg.TypeMetadataStorePath,
		publications: publications,
	}
	if runtime.activationPath != "" {
		if err := runtime.reload(time.Now()); err != nil {
			return nil, err
		}
	}
	return runtime, nil
}

func (r *activatedSourceRuntime) current(now time.Time) (*activeSourceMetadata, error) {
	metadata, _, err := r.currentSource(now)
	return metadata, err
}

func (r *activatedSourceRuntime) currentSource(now time.Time) (*activeSourceMetadata, config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activationPath != "" && !now.Before(r.reloadAfter) {
		if err := r.reload(now); err != nil {
			r.reloadAfter = now.Add(activationReloadRetryInterval)
			return nil, config{}, err
		}
	}
	if !r.staleUntil.IsZero() && now.After(r.staleUntil) {
		return nil, config{}, fmt.Errorf("activated source snapshot expired outside stale grace")
	}
	metadata := r.metadata
	metadata.CacheState = "fresh"
	if !r.freshUntil.IsZero() && now.After(r.freshUntil) {
		metadata.CacheState = "stale"
	}
	return &metadata, r.config, nil
}

func (r *activatedSourceRuntime) reload(now time.Time) error {
	configs, err := configsFromSourceActivation(r.config)
	if err != nil {
		return fmt.Errorf("reload deployed source snapshot: %w", err)
	}
	for _, candidate := range configs {
		if candidate.SourceMetadataTypeID != r.typeID {
			continue
		}
		if candidate.SourceMetadataSourceID != r.metadata.SourceID || candidate.SourceMetadataOIN != r.metadata.SourceOIN {
			return fmt.Errorf("reloaded deployed source snapshot does not match the active source")
		}
		r.config = candidate
		r.metadata = activeSourceMetadata{
			Version: candidate.SourceMetadataVersion, SourceID: candidate.SourceMetadataSourceID, SourceOIN: candidate.SourceMetadataOIN,
			TypeID: candidate.SourceMetadataTypeID, Definition: candidate.SourceMetadataDefinition,
			VCT: candidate.SourceMetadataVCT, VCTIntegrity: candidate.SourceMetadataVCTIntegrity,
		}
		r.freshUntil, r.staleUntil = candidate.SourceMetadataFreshUntil, candidate.SourceMetadataStaleUntil
		r.reloadAfter = now.Add(activationReloadInterval)
		return nil
	}
	return fmt.Errorf("reloaded deployed source snapshot no longer contains type %q", r.typeID)
}

func (r *activatedSourceRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storePath != "" {
		if publications, err := loadTypeMetadataPublications(r.storePath); err == nil {
			r.publications = publications
		}
	}
	publication := r.publications[request.URL.Path]
	if publication == nil {
		http.NotFound(w, request)
		return
	}
	publication.ServeHTTP(w, request)
}

func (r *unavailableSourceMetadataRuntime) current(_ time.Time) (*activeSourceMetadata, error) {
	return nil, r.err
}

func newUnavailableSourceMetadataRuntime(sourceID, typeID, storePath string, cause error) *unavailableSourceMetadataRuntime {
	publications, err := loadTypeMetadataPublications(storePath)
	if err != nil {
		cause = fmt.Errorf("%w; restore existing Type Metadata: %v", cause, err)
		publications = make(map[string]*typeMetadataPublication)
	}
	return &unavailableSourceMetadataRuntime{sourceID: sourceID, typeID: typeID, err: cause, publications: publications}
}

func (r *unavailableSourceMetadataRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	publication := r.publications[request.URL.Path]
	if publication == nil {
		http.NotFound(w, request)
		return
	}
	publication.ServeHTTP(w, request)
}

func configsFromSourceActivation(cfg config) ([]config, error) {
	if cfg.SourceActivationPath == "" {
		return nil, fmt.Errorf("SOURCE_ACTIVATION_PATH is required when source metadata is enabled")
	}
	raw, err := os.ReadFile(cfg.SourceActivationPath)
	if err != nil {
		return nil, fmt.Errorf("read active source registration: %w", err)
	}
	var activation sourceActivation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&activation); err != nil {
		return nil, fmt.Errorf("parse active source registration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse active source registration: trailing JSON data")
	}
	if activation.SchemaVersion != "1.0" {
		return nil, fmt.Errorf("unsupported active source schema_version %q", activation.SchemaVersion)
	}
	if !sourceIDPattern.MatchString(activation.Source.SourceID) {
		return nil, fmt.Errorf("active source registration has invalid source_id %q", activation.Source.SourceID)
	}
	if err := activation.Source.validate(); err != nil {
		return nil, fmt.Errorf("validate active source registration: %w", err)
	}
	if len(activation.Types) == 0 {
		return nil, fmt.Errorf("active source registration contains no attestation types")
	}
	resolved := make([]config, 0, len(activation.Types))
	seenTypes := make(map[string]struct{}, len(activation.Types))
	for _, activatedType := range activation.Types {
		if err := activatedType.validate(); err != nil {
			return nil, fmt.Errorf("validate active attestation type: %w", err)
		}
		if _, duplicate := seenTypes[activatedType.TypeID]; duplicate {
			return nil, fmt.Errorf("duplicate active attestation type %q", activatedType.TypeID)
		}
		seenTypes[activatedType.TypeID] = struct{}{}
		candidate := cfg
		candidate.SourceMetadataSourceID = activation.Source.SourceID
		candidate.SourceMetadataOIN = activation.Source.SourceOIN
		candidate.SourceMetadataTypeID = activatedType.TypeID
		candidate.SourceMetadataVersion = activation.MetadataVersion
		candidate.SourceMetadataDefinition = activatedType.Definition
		candidate.SourceMetadataVCT = activatedType.VCT
		candidate.SourceMetadataVCTIntegrity = activatedType.VCTIntegrity
		candidate.SourceMetadataFreshUntil = activation.FreshUntil
		candidate.SourceMetadataStaleUntil = activation.StaleUntil
		candidate.SourceDataTransport = activation.Source.DataAccess.Transport
		candidate.SourceDataFSCServiceReference = activation.Source.DataAccess.ServiceReference
		candidate.SourceDataFSCGrantHash = activation.Source.DataAccess.GrantHash
		resolved = append(resolved, candidate)
	}
	return resolved, nil
}

func configsFromSourceActivations(cfg config) ([]config, error) {
	if cfg.SourceActivationPath != "" {
		return configsFromSourceActivation(cfg)
	}
	if cfg.SourceActivationsPath == "" {
		return nil, fmt.Errorf("SOURCE_ACTIVATIONS_PATH is required")
	}
	entries, err := os.ReadDir(cfg.SourceActivationsPath)
	if err != nil {
		return nil, fmt.Errorf("read active source registrations: %w", err)
	}
	resolved := make([]config, 0, len(entries))
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		candidate := cfg
		candidate.SourceActivationPath = filepath.Join(cfg.SourceActivationsPath, entry.Name())
		activationConfigs, err := configsFromSourceActivation(candidate)
		if err != nil {
			return nil, fmt.Errorf("activation %s: %w", entry.Name(), err)
		}
		if len(activationConfigs) > 0 && entry.Name() != activationConfigs[0].SourceMetadataSourceID+".json" {
			return nil, fmt.Errorf("activation %s does not match source_id %q", entry.Name(), activationConfigs[0].SourceMetadataSourceID)
		}
		for _, activationConfig := range activationConfigs {
			key := activationConfig.SourceMetadataSourceID + "\x00" + activationConfig.SourceMetadataTypeID
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("duplicate active source/type %s/%s", activationConfig.SourceMetadataSourceID, activationConfig.SourceMetadataTypeID)
			}
			seen[key] = struct{}{}
			resolved = append(resolved, activationConfig)
		}
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("SOURCE_ACTIVATIONS_PATH contains no active source registrations")
	}
	return resolved, nil
}

func validateSourceMetadataEnvelope(document sourceMetadataDocument, definition sourceAttestationDefinition, now time.Time, policy sourceMetadataCachePolicy) (time.Time, error) {
	issuedAt, err := time.Parse(time.RFC3339, document.IssuedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("source metadata issued_at is invalid: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, document.ExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("source metadata expires_at is invalid: %w", err)
	}
	if issuedAt.After(now.Add(5 * time.Minute)) {
		return time.Time{}, fmt.Errorf("source metadata issued_at is in the future")
	}
	if !expiresAt.After(issuedAt) {
		return time.Time{}, fmt.Errorf("source metadata expires_at must be after issued_at")
	}
	if expiresAt.Sub(now) < policy.MinimumValidity {
		return time.Time{}, fmt.Errorf("source metadata validity is shorter than the GBO minimum")
	}
	if _, err := compareNumericVersion(document.Version, "0"); err != nil {
		return time.Time{}, fmt.Errorf("source metadata version: %w", err)
	}
	if _, err := compareNumericVersion(definition.TypeVersion, "0"); err != nil {
		return time.Time{}, fmt.Errorf("type metadata version: %w", err)
	}
	return expiresAt, nil
}

func compareNumericVersion(left, right string) (int, error) {
	parse := func(value string) ([]int, error) {
		value = strings.TrimPrefix(value, "v")
		parts := strings.Split(value, ".")
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty version")
		}
		result := make([]int, len(parts))
		for i, part := range parts {
			parsed, err := strconv.Atoi(part)
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("version %q is not numeric dotted notation", value)
			}
			result[i] = parsed
		}
		return result, nil
	}
	a, err := parse(left)
	if err != nil {
		return 0, err
	}
	b, err := parse(right)
	if err != nil {
		return 0, err
	}
	length := max(len(a), len(b))
	for i := 0; i < length; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1, nil
		}
		if av > bv {
			return 1, nil
		}
	}
	return 0, nil
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
