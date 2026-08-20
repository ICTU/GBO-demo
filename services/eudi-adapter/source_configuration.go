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
		binding := configuration.MetadataEndpoint.Transport + "\x00" + configuration.MetadataEndpoint.ProviderPeerID + "\x00" + configuration.MetadataEndpoint.ServiceReference + "\x00" + configuration.MetadataEndpoint.Endpoint
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
	case sourceTransportUnsecured:
		if c.MetadataEndpoint.ProviderPeerID != "" {
			return fmt.Errorf("metadata_endpoint provider_peer_id is not allowed for unsecured transport")
		}
		if c.MetadataEndpoint.ServiceReference != "" {
			return fmt.Errorf("metadata_endpoint service_reference is not allowed for unsecured transport")
		}
		if err := validateAbsoluteUnsecuredEndpoint(c.MetadataEndpoint.Endpoint); err != nil {
			return fmt.Errorf("metadata_endpoint endpoint: %w", err)
		}
	default:
		return fmt.Errorf("metadata_endpoint transport must be %q or %q", sourceTransportFSC, sourceTransportUnsecured)
	}
	return nil
}
