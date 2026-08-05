package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
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

type sourceMetadataCachePolicy struct {
	MinimumValidity  time.Duration
	MaximumFreshness time.Duration
	StaleGrace       time.Duration
}

type cachedSourceMetadata struct {
	shadow        sourceMetadataShadow
	etag          string
	sourceExpires time.Time
	freshUntil    time.Time
	staleUntil    time.Time
}

type sourceMetadataActivationState struct {
	SourceOIN        string `json:"source_oin"`
	TypeID           string `json:"type_id"`
	SourceVersion    string `json:"source_version"`
	TypeVersion      string `json:"type_version"`
	PayloadDigest    string `json:"payload_digest"`
	DefinitionDigest string `json:"definition_digest"`
	Checksum         string `json:"checksum"`
}

// sourceMetadataCache owns activation of verified source declarations. A
// refresh first verifies and materialises all public bytes; only then is the
// active snapshot swapped atomically.
type sourceMetadataCache struct {
	client        *http.Client
	registration  sourceMetadataConfig
	usecaseKey    string
	publicBaseURL string
	storePath     string
	policy        sourceMetadataCachePolicy

	refreshMu    sync.Mutex
	mu           sync.RWMutex
	active       *cachedSourceMetadata
	baseline     *sourceMetadataActivationState
	publications map[string]*typeMetadataPublication
}

type unavailableSourceMetadataRuntime struct {
	usecaseKey   string
	err          error
	publications map[string]*typeMetadataPublication
}

func (r *unavailableSourceMetadataRuntime) appliesTo(usecaseKey string, uc Usecase) bool {
	return r != nil && uc.acceptsSourceMetadata(usecaseKey, r.usecaseKey)
}

func (r *unavailableSourceMetadataRuntime) current(_ time.Time) (*sourceMetadataShadow, error) {
	return nil, r.err
}

func newUnavailableSourceMetadataRuntime(usecaseKey, storePath string, cause error) *unavailableSourceMetadataRuntime {
	publications, err := loadTypeMetadataPublications(storePath)
	if err != nil {
		cause = fmt.Errorf("%w; restore existing Type Metadata: %v", cause, err)
		publications = make(map[string]*typeMetadataPublication)
	}
	return &unavailableSourceMetadataRuntime{usecaseKey: usecaseKey, err: cause, publications: publications}
}

func (r *unavailableSourceMetadataRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	publication := r.publications[request.URL.Path]
	if publication == nil {
		http.NotFound(w, request)
		return
	}
	publication.ServeHTTP(w, request)
}

func loadConfiguredSourceMetadataCache(client *http.Client, cfg config) (*sourceMetadataCache, error) {
	if cfg.SourceMetadataPublicJWKPath == "" {
		return nil, fmt.Errorf("SOURCE_METADATA_PUBLIC_JWK_PATH is required when cache mode is enabled")
	}
	if !strings.HasPrefix(cfg.SourceMetadataOutwayPath, "/") || strings.HasPrefix(cfg.SourceMetadataOutwayPath, "//") {
		return nil, fmt.Errorf("SOURCE_METADATA_OUTWAY_PATH must be an absolute path on the configured FSC Outway")
	}
	publicJWK, err := os.ReadFile(cfg.SourceMetadataPublicJWKPath)
	if err != nil {
		return nil, fmt.Errorf("read source metadata public JWK: %w", err)
	}
	return newSourceMetadataCache(client, sourceMetadataConfig{
		URL:         strings.TrimRight(cfg.OutwayURL, "/") + cfg.SourceMetadataOutwayPath,
		ExpectedOIN: cfg.SourceMetadataOIN,
		PublicJWK:   publicJWK,
		TypeID:      cfg.SourceMetadataTypeID,
	}, cfg.SourceMetadataUsecaseKey, cfg.TypeMetadataPublicBaseURL, cfg.TypeMetadataStorePath, defaultSourceMetadataCachePolicy)
}

func startSourceMetadataRefresh(ctx context.Context, cache *sourceMetadataCache, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := cache.Refresh(ctx, now); err != nil {
					slog.Error("source metadata cache refresh failed", "err", err.Error())
				}
			}
		}
	}()
}

func newSourceMetadataCache(client *http.Client, registration sourceMetadataConfig, usecaseKey, publicBaseURL, storePath string, policy sourceMetadataCachePolicy) (*sourceMetadataCache, error) {
	if client == nil || registration.URL == "" || registration.ExpectedOIN == "" || len(registration.PublicJWK) == 0 || registration.TypeID == "" {
		return nil, fmt.Errorf("source metadata cache registration is incomplete")
	}
	if usecaseKey == "" || publicBaseURL == "" || storePath == "" {
		return nil, fmt.Errorf("source metadata usecase, public base URL and store path are required")
	}
	if err := validateTypeMetadataBaseURL(publicBaseURL); err != nil {
		return nil, err
	}
	if policy.MinimumValidity <= 0 || policy.MaximumFreshness <= 0 || policy.StaleGrace < 0 {
		return nil, fmt.Errorf("source metadata cache policy is invalid")
	}
	publications, err := loadTypeMetadataPublications(storePath)
	if err != nil {
		return nil, err
	}
	baseline, err := loadSourceMetadataActivationState(storePath, registration)
	if err != nil {
		return nil, err
	}
	return &sourceMetadataCache{
		client:        client,
		registration:  registration,
		usecaseKey:    usecaseKey,
		publicBaseURL: publicBaseURL,
		storePath:     storePath,
		policy:        policy,
		baseline:      baseline,
		publications:  publications,
	}, nil
}

func (c *sourceMetadataCache) Refresh(ctx context.Context, now time.Time) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.RLock()
	etag := ""
	if c.active != nil {
		etag = c.active.etag
	}
	c.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.registration.URL, nil)
	if err != nil {
		return fmt.Errorf("create source metadata refresh request: %w", err)
	}
	txID, err := newFscTransactionID()
	if err != nil {
		return fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	req.Header.Set("Fsc-Transaction-Id", txID)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh source metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if etag == "" {
			return fmt.Errorf("source returned not modified for an unconditional metadata request")
		}
		return c.extendActiveLifetime(now)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("source metadata refresh status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != sourceMetadataMediaType {
		return fmt.Errorf("source metadata content type must be %s", sourceMetadataMediaType)
	}
	responseETag := resp.Header.Get("ETag")
	compactJWS, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read source metadata: %w", err)
	}
	payload, err := verifySourceMetadataJWS(strings.TrimSpace(string(compactJWS)), c.registration.PublicJWK)
	if err != nil {
		return err
	}
	shadow, document, err := parseSourceMetadataPayload(payload, c.registration)
	if err != nil {
		return err
	}
	expiresAt, err := validateSourceMetadataEnvelope(document, shadow.Definition, now, c.policy)
	if err != nil {
		return err
	}
	publication, err := newTypeMetadataPublication(c.publicBaseURL, document.SourceOIN, shadow.Definition)
	if err != nil {
		return fmt.Errorf("materialise type metadata: %w", err)
	}
	payloadDigest := sha256.Sum256(payload)
	definitionBytes, err := json.Marshal(shadow.Definition)
	if err != nil {
		return fmt.Errorf("marshal attestation definition: %w", err)
	}
	definitionDigest := sha256.Sum256(definitionBytes)
	freshUntil, staleUntil := c.lifetimes(now, expiresAt)
	shadow.UsecaseKey = c.usecaseKey
	shadow.VCT = publication.VCT
	shadow.VCTIntegrity = publication.Integrity

	if c.baseline != nil {
		if comparison, err := compareNumericVersion(document.Version, c.baseline.SourceVersion); err != nil {
			return fmt.Errorf("compare source metadata version: %w", err)
		} else if comparison < 0 {
			return fmt.Errorf("source metadata version rollback from %q to %q", c.baseline.SourceVersion, document.Version)
		} else if comparison == 0 && hex.EncodeToString(payloadDigest[:]) != c.baseline.PayloadDigest {
			return fmt.Errorf("source metadata version %q has changed bytes", document.Version)
		}
		if comparison, err := compareNumericVersion(shadow.Definition.TypeVersion, c.baseline.TypeVersion); err != nil {
			return fmt.Errorf("compare type metadata version: %w", err)
		} else if comparison < 0 {
			return fmt.Errorf("type metadata version rollback from %q to %q", c.baseline.TypeVersion, shadow.Definition.TypeVersion)
		} else if comparison == 0 && hex.EncodeToString(definitionDigest[:]) != c.baseline.DefinitionDigest {
			return fmt.Errorf("attestation type version %q has changed definition", shadow.Definition.TypeVersion)
		}
	}
	c.mu.RLock()
	existing := c.publications[publication.path]
	c.mu.RUnlock()
	if existing != nil && !bytes.Equal(existing.body, publication.body) {
		return fmt.Errorf("type metadata URL %q already has different bytes", publication.path)
	}
	if err := persistTypeMetadataPublication(c.storePath, publication); err != nil {
		return err
	}
	activation := &sourceMetadataActivationState{
		SourceOIN:        document.SourceOIN,
		TypeID:           shadow.Definition.TypeID,
		SourceVersion:    document.Version,
		TypeVersion:      shadow.Definition.TypeVersion,
		PayloadDigest:    hex.EncodeToString(payloadDigest[:]),
		DefinitionDigest: hex.EncodeToString(definitionDigest[:]),
	}
	activation.Checksum = sourceMetadataActivationChecksum(*activation)
	if err := persistSourceMetadataActivationState(c.storePath, c.registration, *activation); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publications[publication.path] = publication
	c.baseline = activation
	c.active = &cachedSourceMetadata{
		shadow:        *shadow,
		etag:          responseETag,
		sourceExpires: expiresAt,
		freshUntil:    freshUntil,
		staleUntil:    staleUntil,
	}
	return nil
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

func (c *sourceMetadataCache) extendActiveLifetime(now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return fmt.Errorf("source returned not modified without an active cache entry")
	}
	if c.active.sourceExpires.Sub(now) < c.policy.MinimumValidity {
		return fmt.Errorf("source metadata validity is shorter than the GBO minimum")
	}
	c.active.freshUntil, c.active.staleUntil = c.lifetimes(now, c.active.sourceExpires)
	return nil
}

func (c *sourceMetadataCache) lifetimes(now, sourceExpires time.Time) (time.Time, time.Time) {
	freshUntil := minTime(now.Add(c.policy.MaximumFreshness), sourceExpires)
	return freshUntil, minTime(freshUntil.Add(c.policy.StaleGrace), sourceExpires)
}

func (c *sourceMetadataCache) Current(now time.Time) (*sourceMetadataShadow, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.active == nil {
		return nil, fmt.Errorf("source metadata cache has no active version")
	}
	if now.After(c.active.staleUntil) {
		return nil, fmt.Errorf("source metadata cache expired outside stale grace")
	}
	shadow := c.active.shadow
	shadow.CacheState = "fresh"
	if now.After(c.active.freshUntil) {
		shadow.CacheState = "stale"
	}
	return &shadow, nil
}

func (c *sourceMetadataCache) appliesTo(usecaseKey string, uc Usecase) bool {
	return c != nil && uc.acceptsSourceMetadata(usecaseKey, c.usecaseKey)
}

func (c *sourceMetadataCache) current(now time.Time) (*sourceMetadataShadow, error) {
	return c.Current(now)
}

func (c *sourceMetadataCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	publication := c.publications[r.URL.Path]
	c.mu.RUnlock()
	if publication == nil {
		http.NotFound(w, r)
		return
	}
	publication.ServeHTTP(w, r)
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

func sourceMetadataActivationFilename(registration sourceMetadataConfig) string {
	digest := sha256.Sum256([]byte(registration.ExpectedOIN + "\x00" + registration.TypeID))
	return "activation-" + hex.EncodeToString(digest[:]) + ".state"
}

func sourceMetadataActivationChecksum(state sourceMetadataActivationState) string {
	canonical := strings.Join([]string{
		state.SourceOIN,
		state.TypeID,
		state.SourceVersion,
		state.TypeVersion,
		state.PayloadDigest,
		state.DefinitionDigest,
	}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func loadSourceMetadataActivationState(directory string, registration sourceMetadataConfig) (*sourceMetadataActivationState, error) {
	path := filepath.Join(directory, sourceMetadataActivationFilename(registration))
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source metadata activation state: %w", err)
	}
	var state sourceMetadataActivationState
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("parse source metadata activation state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse source metadata activation state: trailing JSON data")
	}
	if state.SourceOIN != registration.ExpectedOIN || state.TypeID != registration.TypeID {
		return nil, fmt.Errorf("source metadata activation state does not match the configured source and type")
	}
	if state.Checksum != sourceMetadataActivationChecksum(state) {
		return nil, fmt.Errorf("source metadata activation state failed its integrity check")
	}
	for name, encoded := range map[string]string{
		"payload_digest":    state.PayloadDigest,
		"definition_digest": state.DefinitionDigest,
	} {
		decoded, err := hex.DecodeString(encoded)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("source metadata activation state has invalid %s", name)
		}
	}
	if _, err := compareNumericVersion(state.SourceVersion, "0"); err != nil {
		return nil, fmt.Errorf("source metadata activation state source version: %w", err)
	}
	if _, err := compareNumericVersion(state.TypeVersion, "0"); err != nil {
		return nil, fmt.Errorf("source metadata activation state type version: %w", err)
	}
	return &state, nil
}

func persistSourceMetadataActivationState(directory string, registration sourceMetadataConfig, state sourceMetadataActivationState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal source metadata activation state: %w", err)
	}
	filename := sourceMetadataActivationFilename(registration)
	if existing, err := os.ReadFile(filepath.Join(directory, filename)); err == nil && bytes.Equal(existing, body) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read source metadata activation state: %w", err)
	}
	if err := writeFileAtomically(directory, filename, body, 0o600); err != nil {
		return fmt.Errorf("persist source metadata activation state: %w", err)
	}
	return nil
}
