package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
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
	shadow           sourceMetadataShadow
	sourceVersion    string
	payloadDigest    [sha256.Size]byte
	etag             string
	sourceExpires    time.Time
	freshUntil       time.Time
	staleUntil       time.Time
	publication      *typeMetadataPublication
	definitionDigest [sha256.Size]byte
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
	publications map[string]*typeMetadataPublication
}

type unavailableSourceMetadataRuntime struct {
	usecaseKey string
	err        error
}

func (r *unavailableSourceMetadataRuntime) appliesTo(usecaseKey string, uc Usecase) bool {
	return r != nil && r.usecaseKey == usecaseKey && uc.bron() == bronBD && len(uc.Belastingjaren) == 1
}

func (r *unavailableSourceMetadataRuntime) current(_ time.Time) (*sourceMetadataShadow, error) {
	return nil, r.err
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
	return &sourceMetadataCache{
		client:        client,
		registration:  registration,
		usecaseKey:    usecaseKey,
		publicBaseURL: publicBaseURL,
		storePath:     storePath,
		policy:        policy,
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
	if responseETag == "" {
		return fmt.Errorf("source metadata response has no ETag")
	}
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
	issuedAt, err := time.Parse(time.RFC3339, document.IssuedAt)
	if err != nil {
		return fmt.Errorf("source metadata issued_at is invalid: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, document.ExpiresAt)
	if err != nil {
		return fmt.Errorf("source metadata expires_at is invalid: %w", err)
	}
	if issuedAt.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("source metadata issued_at is in the future")
	}
	if !expiresAt.After(issuedAt) {
		return fmt.Errorf("source metadata expires_at must be after issued_at")
	}
	if expiresAt.Sub(now) < c.policy.MinimumValidity {
		return fmt.Errorf("source metadata validity is shorter than the GBO minimum")
	}
	if _, err := compareNumericVersion(document.Version, "0"); err != nil {
		return fmt.Errorf("source metadata version: %w", err)
	}
	if _, err := compareNumericVersion(shadow.Definition.TypeVersion, "0"); err != nil {
		return fmt.Errorf("Type Metadata version: %w", err)
	}
	publication, err := newTypeMetadataPublication(c.publicBaseURL, document.SourceOIN, shadow.Definition)
	if err != nil {
		return fmt.Errorf("materialise Type Metadata: %w", err)
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

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		if comparison, err := compareNumericVersion(document.Version, c.active.sourceVersion); err != nil {
			return fmt.Errorf("compare source metadata version: %w", err)
		} else if comparison < 0 {
			return fmt.Errorf("source metadata version rollback from %q to %q", c.active.sourceVersion, document.Version)
		} else if comparison == 0 && payloadDigest != c.active.payloadDigest {
			return fmt.Errorf("source metadata version %q has changed bytes", document.Version)
		}
		if comparison, err := compareNumericVersion(shadow.Definition.TypeVersion, c.active.shadow.Definition.TypeVersion); err != nil {
			return fmt.Errorf("compare Type Metadata version: %w", err)
		} else if comparison < 0 {
			return fmt.Errorf("Type Metadata version rollback from %q to %q", c.active.shadow.Definition.TypeVersion, shadow.Definition.TypeVersion)
		} else if comparison == 0 && definitionDigest != c.active.definitionDigest {
			return fmt.Errorf("attestation type version %q has changed definition", shadow.Definition.TypeVersion)
		}
		if existing := c.publications[publication.path]; existing != nil && string(existing.body) != string(publication.body) {
			return fmt.Errorf("Type Metadata URL %q already has different bytes", publication.path)
		}
	}
	if err := persistTypeMetadataPublication(c.storePath, publication); err != nil {
		return err
	}
	c.publications[publication.path] = publication
	c.active = &cachedSourceMetadata{
		shadow:           *shadow,
		sourceVersion:    document.Version,
		payloadDigest:    payloadDigest,
		etag:             responseETag,
		sourceExpires:    expiresAt,
		freshUntil:       freshUntil,
		staleUntil:       staleUntil,
		publication:      publication,
		definitionDigest: definitionDigest,
	}
	return nil
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
	return &shadow, nil
}

func (c *sourceMetadataCache) appliesTo(usecaseKey string, uc Usecase) bool {
	return c != nil && c.usecaseKey == usecaseKey && uc.bron() == bronBD && len(uc.Belastingjaren) == 1
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
