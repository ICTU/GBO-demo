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

// sourceConfiguration is operator-managed desired state. FSC grant hashes and
// the data service are deliberately absent: the reconciler resolves them from
// current contracts and the validated source metadata on every run.
type sourceConfiguration struct {
	SourceID         string                 `yaml:"source_id"`
	SourceOIN        string                 `yaml:"source_oin"`
	Name             string                 `yaml:"name"`
	CertificateSet   string                 `yaml:"certificate_set"`
	MetadataEndpoint sourceMetadataEndpoint `yaml:"metadata_endpoint"`
	DataAccess       sourceDataAccess       `yaml:"data_access"`
}

func loadSourceConfigurations(directory string) ([]sourceConfiguration, error) {
	entries, err := filepath.Glob(filepath.Join(directory, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list source configurations: %w", err)
	}
	sort.Strings(entries)
	configurations := make([]sourceConfiguration, 0, len(entries))
	byID := make(map[string]string, len(entries))
	byCertificateSet := make(map[string]string, len(entries))
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
		if err := configuration.validate(); err != nil {
			return nil, fmt.Errorf("source configuration %q: %w", path, err)
		}
		if previous, exists := byID[configuration.SourceID]; exists {
			return nil, fmt.Errorf("source_id %q is configured in both %q and %q", configuration.SourceID, previous, path)
		}
		if previous, exists := byCertificateSet[configuration.CertificateSet]; exists {
			return nil, fmt.Errorf("certificate_set %q is configured in both %q and %q", configuration.CertificateSet, previous, path)
		}
		binding := configuration.MetadataEndpoint.Transport + "\x00" + configuration.SourceOIN + "\x00" + configuration.MetadataEndpoint.ServiceReference + "\x00" + configuration.MetadataEndpoint.Endpoint
		if previous, exists := byTransportBinding[binding]; exists {
			return nil, fmt.Errorf("metadata endpoint for source OIN %q is configured in both %q and %q", configuration.SourceOIN, previous, path)
		}
		byID[configuration.SourceID] = path
		byCertificateSet[configuration.CertificateSet] = path
		byTransportBinding[binding] = path
		configurations = append(configurations, configuration)
	}
	return configurations, nil
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
	if !sourceOINPattern.MatchString(c.SourceOIN) {
		return fmt.Errorf("source_oin must contain exactly 20 digits")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !sourceIDPattern.MatchString(c.CertificateSet) {
		return fmt.Errorf("certificate_set is invalid")
	}
	switch c.MetadataEndpoint.Transport {
	case sourceTransportFSC:
		if !serviceReferencePattern.MatchString(c.MetadataEndpoint.ServiceReference) {
			return fmt.Errorf("metadata_endpoint service_reference is invalid")
		}
		if err := validateAbsoluteURLPath(c.MetadataEndpoint.Path); err != nil {
			return fmt.Errorf("metadata_endpoint path: %w", err)
		}
		if c.MetadataEndpoint.Endpoint != "" {
			return fmt.Errorf("metadata_endpoint endpoint is not allowed for FSC transport")
		}
	case sourceTransportUnsecured:
		if c.MetadataEndpoint.ServiceReference != "" || c.MetadataEndpoint.Path != "" {
			return fmt.Errorf("metadata_endpoint service_reference and path are not allowed for unsecured transport")
		}
		if err := validateAbsoluteUnsecuredEndpoint(c.MetadataEndpoint.Endpoint); err != nil {
			return fmt.Errorf("metadata_endpoint endpoint: %w", err)
		}
	default:
		return fmt.Errorf("metadata_endpoint transport must be %q or %q", sourceTransportFSC, sourceTransportUnsecured)
	}
	if c.MetadataEndpoint.GrantHash != "" {
		return fmt.Errorf("metadata_endpoint grant_hash is resolved during reconciliation and must not be configured")
	}
	if c.DataAccess.Transport != c.MetadataEndpoint.Transport {
		return fmt.Errorf("metadata_endpoint and data_access must use the same transport")
	}
	if c.DataAccess.ServiceReference != "" || c.DataAccess.GrantHash != "" {
		return fmt.Errorf("data_access service_reference and grant_hash are resolved during reconciliation")
	}
	return nil
}
