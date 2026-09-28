package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

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

func newTypeMetadataPublication(publicBaseURL, sourceID string, definition sourceAttestationDefinition) (*typeMetadataPublication, error) {
	if err := validateTypeMetadataBaseURL(publicBaseURL); err != nil {
		return nil, err
	}
	if !sourceIDPattern.MatchString(sourceID) || definition.TypeID == "" || definition.TypeVersion == "" {
		return nil, fmt.Errorf("valid source ID, type ID and type version are required")
	}

	path := "/types/" + url.PathEscape(sourceID) + "/" + url.PathEscape(definition.TypeID) + "/v" + url.PathEscape(definition.TypeVersion)
	vct := strings.TrimRight(publicBaseURL, "/") + path
	metadata, err := generateTypeMetadata(definition, vct)
	if err != nil {
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

// generateTypeMetadata derives the complete SD-JWT VC Type Metadata from the
// source's claims and display. The source cannot supply any part of it
// directly, so the schema, its required list and the svg_id set always agree
// with the mapping that fills the credential.
func generateTypeMetadata(definition sourceAttestationDefinition, vct string) (map[string]any, error) {
	if len(definition.Claims) == 0 || len(definition.Display) == 0 {
		return nil, fmt.Errorf("type metadata requires claims and display")
	}
	primary := definition.Display[0]
	metadata := map[string]any{"vct": vct, "name": primary.Name}
	if primary.Description != "" {
		metadata["description"] = primary.Description
	}

	displays := make([]any, 0, len(definition.Display))
	for _, display := range definition.Display {
		entry := map[string]any{"lang": display.Lang, "name": display.Name}
		if display.Description != "" {
			entry["description"] = display.Description
		}
		if display.Summary != "" {
			entry["summary"] = display.Summary
		}
		if len(display.Rendering) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(display.Rendering))
			decoder.UseNumber()
			var rendering map[string]any
			if err := decoder.Decode(&rendering); err != nil || rendering == nil {
				return nil, fmt.Errorf("display %q rendering must be a JSON object", display.Lang)
			}
			entry["rendering"] = rendering
		}
		displays = append(displays, entry)
	}
	metadata["display"] = displays

	claims := make([]any, 0, len(definition.Claims))
	properties := make(map[string]any, len(definition.Claims)+2)
	required := make([]any, 0, len(definition.Claims)+2)
	for _, claim := range definition.Claims {
		labels := make([]any, 0, len(claim.Label))
		for _, label := range claim.Label {
			entry := map[string]any{"lang": label.Lang, "label": label.Text}
			for _, description := range claim.Description {
				if description.Lang == label.Lang {
					entry["description"] = description.Text
				}
			}
			labels = append(labels, entry)
		}
		sd := claim.SD
		if sd == "" {
			sd = "always"
		}
		claims = append(claims, map[string]any{
			"path": []any{claim.Name}, "display": labels, "sd": sd, "svg_id": claim.Name,
		})

		property, err := credentialSchemaProperty(claim.Source.Datatype)
		if err != nil {
			return nil, fmt.Errorf("claim %q: %w", claim.Name, err)
		}
		properties[claim.Name] = property
		if !claim.Source.Optional {
			required = append(required, claim.Name)
		}
	}
	metadata["claims"] = claims

	properties["vct"] = map[string]any{"type": "string", "const": vct}
	properties["vct#integrity"] = map[string]any{"type": "string", "pattern": `^sha256-[A-Za-z0-9+/]+={0,2}$`}
	required = append(required, "vct", "vct#integrity")
	metadata["schema"] = map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"title":      primary.Name,
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
	return metadata, nil
}

// credentialSchemaProperty is the JSON Schema of the value a gbo-simple-v1
// rule with this datatype copies into the credential.
func credentialSchemaProperty(datatype string) (map[string]any, error) {
	switch datatype {
	case "string", "boolean", "integer":
		return map[string]any{"type": datatype}, nil
	case "date":
		return map[string]any{"type": "string", "format": "date"}, nil
	case "gYear":
		return map[string]any{"type": "integer"}, nil
	default:
		return nil, fmt.Errorf("datatype %q has no credential representation", datatype)
	}
}

func validateTypeMetadataBaseURL(publicBaseURL string) error {
	base, err := url.Parse(strings.TrimRight(publicBaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("type metadata public base URL must be a root HTTP(S) URL without query or fragment")
	}
	if base.Scheme == "http" {
		hostname := base.Hostname()
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("type metadata public base URL must use HTTPS outside loopback development")
		}
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
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open activation directory: %w", err)
	}
	if err := directoryHandle.Sync(); err != nil {
		_ = directoryHandle.Close()
		return fmt.Errorf("sync activation directory: %w", err)
	}
	if err := directoryHandle.Close(); err != nil {
		return fmt.Errorf("close activation directory: %w", err)
	}
	return nil
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
