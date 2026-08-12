package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var summaryPlaceholderPattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// typeMetadataPublication is the immutable representation associated with one
// source-owned type version. Integrity is calculated over Body exactly as it
// is served; the source can supply neither the VCT nor its integrity value.
type typeMetadataPublication struct {
	TypeID      string
	TypeVersion string
	VCT         string
	Integrity   string
	body        []byte
	etag        string
	path        string
}

func newTypeMetadataPublication(publicBaseURL, sourceOIN string, definition sourceAttestationDefinition) (*typeMetadataPublication, error) {
	if err := validateTypeMetadataBaseURL(publicBaseURL); err != nil {
		return nil, err
	}
	if sourceOIN == "" || definition.TypeID == "" || definition.TypeVersion == "" {
		return nil, fmt.Errorf("source OIN, type ID and type version are required")
	}

	decoder := json.NewDecoder(bytes.NewReader(definition.TypeMetadata))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return nil, fmt.Errorf("type_metadata must be a JSON object")
	}
	for _, forbidden := range []string{"vct", "vct#integrity"} {
		if _, exists := metadata[forbidden]; exists {
			return nil, fmt.Errorf("type_metadata must not define %q", forbidden)
		}
	}
	if err := validateSummaryPlaceholders(metadata); err != nil {
		return nil, err
	}

	path := "/types/" + url.PathEscape(sourceOIN) + "/" + url.PathEscape(definition.TypeID) + "/v" + url.PathEscape(definition.TypeVersion)
	vct := strings.TrimRight(publicBaseURL, "/") + path
	metadata["vct"] = vct
	if err := addManagedCredentialSchema(metadata, vct); err != nil {
		return nil, err
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal Type Metadata: %w", err)
	}
	digest := sha256.Sum256(body)
	return &typeMetadataPublication{
		TypeID:      definition.TypeID,
		TypeVersion: definition.TypeVersion,
		VCT:         vct,
		Integrity:   "sha256-" + base64.StdEncoding.EncodeToString(digest[:]),
		body:        body,
		etag:        `"` + fmt.Sprintf("%x", digest) + `"`,
		path:        path,
	}, nil
}

func validateSummaryPlaceholders(metadata map[string]any) error {
	requiredIDs := make(map[string]struct{})
	if displays, ok := metadata["display"].([]any); ok {
		for _, rawDisplay := range displays {
			display, ok := rawDisplay.(map[string]any)
			if !ok {
				continue
			}
			summary, _ := display["summary"].(string)
			for _, match := range summaryPlaceholderPattern.FindAllStringSubmatch(summary, -1) {
				if id := strings.TrimSpace(match[1]); id != "" {
					requiredIDs[id] = struct{}{}
				}
			}
		}
	}
	if len(requiredIDs) == 0 {
		return nil
	}
	availableIDs := make(map[string]struct{})
	if claims, ok := metadata["claims"].([]any); ok {
		for _, rawClaim := range claims {
			claim, ok := rawClaim.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := claim["svg_id"].(string); ok && id != "" {
				availableIDs[id] = struct{}{}
			}
		}
	}
	missing := make([]string, 0)
	for id := range requiredIDs {
		if _, available := availableIDs[id]; !available {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("type_metadata summary placeholders require matching claim svg_id values: %s", strings.Join(missing, ", "))
	}
	return nil
}

func validateTypeMetadataBaseURL(publicBaseURL string) error {
	base, err := url.Parse(strings.TrimRight(publicBaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("type metadata public base URL must be a root HTTP(S) URL without query or fragment")
	}
	return nil
}

func addManagedCredentialSchema(metadata map[string]any, vct string) error {
	schema, ok := metadata["schema"].(map[string]any)
	if !ok {
		return fmt.Errorf("type_metadata.schema must be a JSON object")
	}
	propertiesValue, hasProperties := schema["properties"]
	properties, ok := propertiesValue.(map[string]any)
	if hasProperties && !ok {
		return fmt.Errorf("type_metadata.schema.properties must be a JSON object")
	}
	if !hasProperties {
		properties = make(map[string]any)
		schema["properties"] = properties
	}
	properties["vct"] = map[string]any{"type": "string", "const": vct}
	properties["vct#integrity"] = map[string]any{"type": "string", "pattern": `^sha256-[A-Za-z0-9+/]+={0,2}$`}
	requiredValue, hasRequired := schema["required"]
	required, ok := requiredValue.([]any)
	if hasRequired && !ok {
		return fmt.Errorf("type_metadata.schema.required must be an array")
	}
	seen := make(map[string]bool, len(required))
	for _, value := range required {
		name, ok := value.(string)
		if !ok {
			return fmt.Errorf("type_metadata.schema.required must contain only strings")
		}
		seen[name] = true
	}
	for _, name := range []string{"vct", "vct#integrity"} {
		if !seen[name] {
			required = append(required, name)
		}
	}
	schema["required"] = required
	return nil
}

func restoreTypeMetadataPublication(body []byte) (*typeMetadataPublication, error) {
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("parse stored Type Metadata: %w", err)
	}
	vct, ok := metadata["vct"].(string)
	if !ok || vct == "" {
		return nil, fmt.Errorf("stored Type Metadata has no vct")
	}
	parsed, err := url.Parse(vct)
	if err != nil || !strings.HasPrefix(parsed.Path, "/types/") {
		return nil, fmt.Errorf("stored Type Metadata has an invalid vct")
	}
	digest := sha256.Sum256(body)
	return &typeMetadataPublication{
		VCT:       vct,
		Integrity: "sha256-" + base64.StdEncoding.EncodeToString(digest[:]),
		body:      body,
		etag:      `"` + fmt.Sprintf("%x", digest) + `"`,
		path:      parsed.Path,
	}, nil
}

func loadTypeMetadataPublications(directory string) (map[string]*typeMetadataPublication, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create Type Metadata store: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read Type Metadata store: %w", err)
	}
	publications := make(map[string]*typeMetadataPublication)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read stored Type Metadata %q: %w", entry.Name(), err)
		}
		publication, err := restoreTypeMetadataPublication(body)
		if err != nil {
			return nil, fmt.Errorf("restore Type Metadata %q: %w", entry.Name(), err)
		}
		if entry.Name() != typeMetadataFilename(publication) {
			return nil, fmt.Errorf("stored Type Metadata %q failed its filename integrity check", entry.Name())
		}
		if existing := publications[publication.path]; existing != nil && !bytes.Equal(existing.body, publication.body) {
			return nil, fmt.Errorf("type metadata store contains conflicting bytes for %q", publication.path)
		}
		publications[publication.path] = publication
	}
	return publications, nil
}

func persistTypeMetadataPublication(directory string, publication *typeMetadataPublication) error {
	path := filepath.Join(directory, typeMetadataFilename(publication))
	pathDigest := sha256.Sum256([]byte(publication.path))
	prefix := hex.EncodeToString(pathDigest[:]) + "-"
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read Type Metadata store: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && entry.Name() != filepath.Base(path) {
			return fmt.Errorf("type metadata URL %q already has different stored bytes", publication.path)
		}
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, publication.body) {
			return fmt.Errorf("type metadata URL %q already has different stored bytes", publication.path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read immutable Type Metadata: %w", err)
	}
	if err := writeFileAtomically(directory, filepath.Base(path), publication.body, 0o644); err != nil {
		return fmt.Errorf("persist immutable Type Metadata: %w", err)
	}
	return nil
}

func writeFileAtomically(directory, filename string, body []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(directory, ".atomic-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set file permissions: %w", err)
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, filename)); err != nil {
		return fmt.Errorf("activate file: %w", err)
	}
	return nil
}

func typeMetadataFilename(publication *typeMetadataPublication) string {
	pathDigest := sha256.Sum256([]byte(publication.path))
	bodyDigest := sha256.Sum256(publication.body)
	return hex.EncodeToString(pathDigest[:]) + "-" + hex.EncodeToString(bodyDigest[:]) + ".json"
}

func (p *typeMetadataPublication) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != p.path {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", p.etag)
	if r.Header.Get("If-None-Match") == p.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(p.body)
}
