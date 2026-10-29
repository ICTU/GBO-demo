// Package asimapping reads the asi capability of a source's metadata: the
// attributes a source offers for verification and how its own fields make up
// each attribute's value.
//
// The source metadata schema (schemas/gbo-source-metadata-v1.schema.json)
// checks the form of the capability. Validate adds what a JSON Schema cannot
// check: the GraphQL document, and the mapping against the value_schema GBO
// keeps per attribute identifier (schemas/asi-attributes.json).
package asimapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ValueSchema is the shape of an attribute's value, in the subset of JSON
// Schema GBO uses until the EU catalogue of attributes provides the schemas.
type ValueSchema struct {
	Type       string                  `json:"type"`
	Properties map[string]*ValueSchema `json:"properties,omitempty"`
	Required   []string                `json:"required,omitempty"`
	Items      *ValueSchema            `json:"items,omitempty"`
	Format     string                  `json:"format,omitempty"`
}

// Catalogue maps attribute identifiers to the shape of their value.
type Catalogue map[string]*ValueSchema

type catalogueFile struct {
	Attributes []struct {
		Identifier  string       `json:"identifier"`
		ValueSchema *ValueSchema `json:"value_schema"`
	} `json:"attributes"`
}

// LoadCatalogue reads the catalogue at path.
func LoadCatalogue(path string) (Catalogue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read attribute catalogue: %w", err)
	}
	return ParseCatalogue(data)
}

// ParseCatalogue reads a catalogue. A keyword outside the subset, or an
// identifier listed twice, rejects the whole catalogue.
func ParseCatalogue(data []byte) (Catalogue, error) {
	var file catalogueFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse attribute catalogue: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse attribute catalogue: trailing JSON data")
	}
	catalogue := make(Catalogue, len(file.Attributes))
	for _, attribute := range file.Attributes {
		if attribute.Identifier == "" || attribute.ValueSchema == nil {
			return nil, fmt.Errorf("attribute catalogue: every attribute needs an identifier and a value_schema")
		}
		if _, duplicate := catalogue[attribute.Identifier]; duplicate {
			return nil, fmt.Errorf("attribute catalogue: duplicate identifier %q", attribute.Identifier)
		}
		catalogue[attribute.Identifier] = attribute.ValueSchema
	}
	return catalogue, nil
}
