// Package dataservice reads the data services of the ASIP from their
// configuration file. The ASIP serves one data service per authentic source,
// under /<id>/authsrc-api: ETSI TS 119 478 allows one authenticSource per
// response (REQ-ASIP-6.1.1.2-08), and the Data Service Directory lists a data
// service per attribute (REQ-DIP-5.3.3-03).
//
// The file names per data service its source in the Source Registry. What
// the source holds, how the ASIP asks it, the source's legal name and its
// country come from the source's description in the active release.
package dataservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// DataService is one data service: an authentic source behind the ASIP.
type DataService struct {
	// ID is the path segment: /<ID>/authsrc-api.
	ID string `json:"id"`
	// Provider is the legal name of the ASIP in the responses.
	Provider string `json:"provider"`
	// Source is the authentic source's id in the Source Registry, whose
	// active release describes it.
	Source string `json:"source"`
	// GrantPath is the grant-link of the ASIP's FSC contract with the source,
	// on its Outway.
	GrantPath string `json:"grant_path"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads the data services from path. A file without a source, or with
// a data service twice, stops the ASIP at start, before any request.
func Load(path string) ([]DataService, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read data services: %w", err)
	}
	// An unknown field is refused: a misspelt field would otherwise read as
	// empty, and a field that now comes from the source's description
	// (authentic_source, country, attributes) would be silently ignored.
	var doc struct {
		Comment      string        `json:"_comment"`
		DataServices []DataService `json:"data_services"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse data services: %w", err)
	}
	if len(doc.DataServices) == 0 {
		return nil, fmt.Errorf("data services: %s names none", path)
	}
	seen := map[string]bool{}
	for _, ds := range doc.DataServices {
		if !idPattern.MatchString(ds.ID) {
			return nil, fmt.Errorf("data service %q: id must be a lowercase path segment", ds.ID)
		}
		if seen[ds.ID] {
			return nil, fmt.Errorf("data service %q: named twice", ds.ID)
		}
		seen[ds.ID] = true
		if ds.Provider == "" {
			return nil, fmt.Errorf("data service %q: provider is required", ds.ID)
		}
		if ds.Source == "" {
			return nil, fmt.Errorf("data service %q: source is required", ds.ID)
		}
		if len(ds.GrantPath) < 2 || ds.GrantPath[0] != '/' {
			return nil, fmt.Errorf("data service %q: grant_path must be a path on the Outway", ds.ID)
		}
	}
	return doc.DataServices, nil
}
