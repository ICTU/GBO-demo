package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// memorySource is the in-memory authentic source of slice 1. It holds, per
// BSN, the attribute values as the JSON objects the catalogue describes.
// Slice 4 replaces it with a query through the existing chain.
type memorySource struct {
	persons map[string]map[string]json.RawMessage
}

func loadMemorySource(path string) (*memorySource, error) {
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
	s := &memorySource{persons: map[string]map[string]json.RawMessage{}}
	for _, p := range doc.Persons {
		if p.BSN == "" {
			return nil, fmt.Errorf("parse source: person without bsn")
		}
		s.persons[p.BSN] = p.Attributes
	}
	return s, nil
}

// value returns the stored value of an attribute for a person, and false when
// the source holds none.
func (s *memorySource) value(bsn, name string) (json.RawMessage, bool) {
	attrs, ok := s.persons[bsn]
	if !ok {
		return nil, false
	}
	v, ok := attrs[name]
	return v, ok
}
