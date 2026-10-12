// Package memorysource is the in-memory authentic source: per BSN, the
// attribute values from a JSON file. It implements verification.Source.
// Slice 4 of #336 puts a query through the existing chain next to it.
package memorysource

import (
	"encoding/json"
	"fmt"
	"os"
)

// Source holds, per BSN, the attribute values as the JSON objects the
// catalogue describes.
type Source struct {
	persons map[string]map[string]json.RawMessage
}

// Load reads the persons file.
func Load(path string) (*Source, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	var doc struct {
		Persons []struct {
			BSN        string                     `json:"bsn"`
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"persons"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse source: %w", err)
	}
	s := &Source{persons: map[string]map[string]json.RawMessage{}}
	for _, p := range doc.Persons {
		if p.BSN == "" {
			return nil, fmt.Errorf("parse source: person without bsn")
		}
		s.persons[p.BSN] = p.Attributes
	}
	return s, nil
}

// Value returns the stored value of an attribute for a person, and false when
// the source holds none.
func (s *Source) Value(bsn, name string) (json.RawMessage, bool) {
	attrs, ok := s.persons[bsn]
	if !ok {
		return nil, false
	}
	v, ok := attrs[name]
	return v, ok
}
