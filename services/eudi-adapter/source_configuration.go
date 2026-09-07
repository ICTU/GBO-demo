package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const sourceMetadataWellKnownPath = "/.well-known/gbo"

// sourceConfiguration is the minimal operator-managed desired state. Legal
// identity and display name come from the provisioned certificate set whose
// directory is the source_id. The data service, data transport and grant
// hashes are resolved from the selected transport, current contracts and the
// validated source metadata.
type sourceConfiguration struct {
	SourceID         string                           `yaml:"-"`
	MetadataEndpoint configuredSourceMetadataEndpoint `yaml:"metadata_endpoint"`
	// DataAccess is optional. Omitting it keeps the historical shape in which
	// one transport served both legs; stating it lets a source take its data
	// over FSC while its description arrives another way.
	DataAccess *configuredSourceDataAccess `yaml:"data_access,omitempty"`
}

// configuredSourceMetadataEndpoint deliberately does not reuse the runtime
// sourceMetadataEndpoint type. Keeping a separate type makes yaml.KnownFields
// reject resolved fields such as path and grant_hash instead of accidentally
// turning them into operator inputs.
type configuredSourceMetadataEndpoint struct {
	Transport        string `yaml:"transport"`
	ProviderPeerID   string `yaml:"provider_peer_id,omitempty"`
	ServiceReference string `yaml:"service_reference,omitempty"`
	Endpoint         string `yaml:"endpoint,omitempty"`
	// Path locates the document inside the operator-managed metadata
	// directory. It applies to file transport only; the FSC well-known path is
	// a fixed convention and never an operator input.
	Path string `yaml:"path,omitempty"`
}

// configuredSourceDataAccess carries only what an operator legitimately owns.
// The data service reference and grant hash stay out: the source names its own
// service in its document, and the grant is resolved from current contracts.
type configuredSourceDataAccess struct {
	Transport      string `yaml:"transport"`
	ProviderPeerID string `yaml:"provider_peer_id,omitempty"`
}

// dataTransport defaults to the metadata transport so existing single-leg
// configurations keep their meaning without being rewritten.
func (c sourceConfiguration) dataTransport() string {
	if c.DataAccess != nil {
		return c.DataAccess.Transport
	}
	return c.MetadataEndpoint.Transport
}

// providerPeerID is the one FSC peer behind this source. It is declared on
// whichever leg speaks FSC; validation guarantees it is declared exactly once.
func (c sourceConfiguration) providerPeerID() string {
	if c.DataAccess != nil && c.DataAccess.ProviderPeerID != "" {
		return c.DataAccess.ProviderPeerID
	}
	return c.MetadataEndpoint.ProviderPeerID
}

func loadSourceConfigurations(directory string) ([]sourceConfiguration, error) {
	patterns := []string{
		filepath.Join(directory, "*.yaml"),
		filepath.Join(directory, "*", "*.yaml"),
	}
	var entries []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("list source configurations: %w", err)
		}
		for _, match := range matches {
			relative, err := filepath.Rel(directory, match)
			if err != nil {
				return nil, fmt.Errorf("resolve source configuration path %q: %w", match, err)
			}
			if pathContainsHiddenComponent(relative) {
				continue
			}
			entries = append(entries, match)
		}
	}
	sort.Strings(entries)
	configurations := make([]sourceConfiguration, 0, len(entries))
	byID := make(map[string]string, len(entries))
	byTransportBinding := make(map[string]string, len(entries))
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read source configuration %q: %w", path, err)
		}
		configuration, err := parseSourceConfiguration(raw)
		if err != nil {
			return nil, fmt.Errorf("source configuration %q: %w", path, err)
		}
		configuration.SourceID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if err := configuration.validate(); err != nil {
			return nil, fmt.Errorf("source configuration %q: %w", path, err)
		}
		if previous, exists := byID[configuration.SourceID]; exists {
			return nil, fmt.Errorf("source_id %q is configured in both %q and %q", configuration.SourceID, previous, path)
		}
		binding := strings.Join([]string{
			configuration.MetadataEndpoint.Transport, configuration.MetadataEndpoint.ProviderPeerID,
			configuration.MetadataEndpoint.ServiceReference, configuration.MetadataEndpoint.Endpoint,
			configuration.MetadataEndpoint.Path,
		}, "\x00")
		if previous, exists := byTransportBinding[binding]; exists {
			return nil, fmt.Errorf("metadata endpoint for provider Peer ID %q is configured in both %q and %q", configuration.MetadataEndpoint.ProviderPeerID, previous, path)
		}
		byID[configuration.SourceID] = path
		byTransportBinding[binding] = path
		configurations = append(configurations, configuration)
	}
	return configurations, nil
}

func pathContainsHiddenComponent(path string) bool {
	for _, component := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if strings.HasPrefix(component, ".") {
			return true
		}
	}
	return false
}

func parseSourceConfiguration(raw []byte) (sourceConfiguration, error) {
	var configuration sourceConfiguration
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&configuration); err != nil {
		return sourceConfiguration{}, fmt.Errorf("parse YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return sourceConfiguration{}, fmt.Errorf("multiple YAML documents are not supported")
	}
	return configuration, nil
}

// sourcesNeedFSCContracts reports whether this configured set requires an FSC
// Manager client. Either leg counts: a source may take only its data over FSC.
func sourcesNeedFSCContracts(sources []sourceConfiguration) bool {
	for _, source := range sources {
		if source.MetadataEndpoint.Transport == sourceTransportFSC || source.dataTransport() == sourceTransportFSC {
			return true
		}
	}
	return false
}

func (c sourceConfiguration) validate() error {
	if !sourceIDPattern.MatchString(c.SourceID) {
		return fmt.Errorf("source_id is invalid")
	}
	switch c.MetadataEndpoint.Transport {
	case sourceTransportFSC:
		if !peerIDPattern.MatchString(c.MetadataEndpoint.ProviderPeerID) {
			return fmt.Errorf("metadata_endpoint provider_peer_id must contain exactly 20 alphanumeric characters for FSC transport")
		}
		if !serviceReferencePattern.MatchString(c.MetadataEndpoint.ServiceReference) {
			return fmt.Errorf("metadata_endpoint service_reference is invalid")
		}
		if c.MetadataEndpoint.Endpoint != "" {
			return fmt.Errorf("metadata_endpoint endpoint is not allowed for FSC transport")
		}
		if c.MetadataEndpoint.Path != "" {
			return fmt.Errorf("metadata_endpoint path is not allowed for FSC transport")
		}
	case sourceTransportUnsecured:
		if c.MetadataEndpoint.ProviderPeerID != "" {
			return fmt.Errorf("metadata_endpoint provider_peer_id is not allowed for unsecured transport")
		}
		if c.MetadataEndpoint.ServiceReference != "" {
			return fmt.Errorf("metadata_endpoint service_reference is not allowed for unsecured transport")
		}
		if c.MetadataEndpoint.Path != "" {
			return fmt.Errorf("metadata_endpoint path is not allowed for unsecured transport")
		}
		if err := validateAbsoluteUnsecuredEndpoint(c.MetadataEndpoint.Endpoint); err != nil {
			return fmt.Errorf("metadata_endpoint endpoint: %w", err)
		}
	case sourceTransportFile:
		if c.MetadataEndpoint.ProviderPeerID != "" {
			return fmt.Errorf("metadata_endpoint provider_peer_id is not allowed for file transport; declare it on data_access")
		}
		if c.MetadataEndpoint.ServiceReference != "" {
			return fmt.Errorf("metadata_endpoint service_reference is not allowed for file transport")
		}
		if c.MetadataEndpoint.Endpoint != "" {
			return fmt.Errorf("metadata_endpoint endpoint is not allowed for file transport")
		}
		if err := validateStorageRelativePath(c.MetadataEndpoint.Path); err != nil {
			return fmt.Errorf("metadata_endpoint path: %w", err)
		}
	default:
		return fmt.Errorf("metadata_endpoint transport must be %q, %q or %q", sourceTransportFSC, sourceTransportUnsecured, sourceTransportFile)
	}
	return c.validateDataAccess()
}

func (c sourceConfiguration) validateDataAccess() error {
	if c.DataAccess == nil {
		// A document read from disk says nothing about how to reach the source
		// itself, so the data leg cannot be inherited and must be stated.
		if c.MetadataEndpoint.Transport == sourceTransportFile {
			return fmt.Errorf("data_access is required for file metadata transport")
		}
		return nil
	}
	switch c.DataAccess.Transport {
	case sourceTransportFSC:
		if c.MetadataEndpoint.Transport == sourceTransportFSC {
			if c.DataAccess.ProviderPeerID != "" {
				return fmt.Errorf("data_access provider_peer_id is already declared on metadata_endpoint")
			}
		} else if !peerIDPattern.MatchString(c.DataAccess.ProviderPeerID) {
			return fmt.Errorf("data_access provider_peer_id must contain exactly 20 alphanumeric characters for FSC transport")
		}
	case sourceTransportUnsecured:
		if c.DataAccess.ProviderPeerID != "" {
			return fmt.Errorf("data_access provider_peer_id is not allowed for unsecured transport")
		}
		if c.MetadataEndpoint.Transport == sourceTransportFSC {
			return fmt.Errorf("FSC metadata must not be combined with unsecured data_access")
		}
	default:
		return fmt.Errorf("data_access transport must be %q or %q", sourceTransportFSC, sourceTransportUnsecured)
	}
	return nil
}
