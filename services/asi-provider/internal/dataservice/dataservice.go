// Package dataservice reads the data services of the ASIP from their
// configuration file. The ASIP serves one data service per authentic source,
// under /<id>/authsrc-api: ETSI TS 119 478 allows one authenticSource per
// response (REQ-ASIP-6.1.1.2-08), and the Data Service Directory lists a data
// service per attribute (REQ-DIP-5.3.3-03).
//
// The adapter that asks a source stays code; the file names it per data
// service, with the attributes it provides.
package dataservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
)

// DataService is one data service: an authentic source behind the ASIP.
type DataService struct {
	// ID is the path segment: /<ID>/authsrc-api.
	ID string `json:"id"`
	// Provider is the legal name of the ASIP in the responses.
	Provider string `json:"provider"`
	// AuthenticSource is the legal name of the authentic source, empty when
	// the ASIP is the authentic source: then the responses name none.
	AuthenticSource string `json:"authentic_source"`
	// Country is the Member State of the data service (ISO 3166-1 alpha-2),
	// in its discovery records.
	Country string `json:"country"`
	// Source names the adapter that asks the authentic source.
	Source string `json:"source"`
	// GrantPath is the grant-link of the ASIP's FSC contract with the source,
	// on its Outway.
	GrantPath string `json:"grant_path"`
	// Attributes maps each attribute identifier to the name the adapter
	// knows it by.
	Attributes map[string]string `json:"attributes"`
}

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Load reads the data services from path. adapters lists per adapter the
// attribute names it can ask for. A file that names an unknown adapter, an
// attribute the adapter cannot ask for, or a data service twice stops the
// ASIP at start, before any request.
func Load(path string, adapters map[string][]string) ([]DataService, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read data services: %w", err)
	}
	// An unknown field is refused: a misspelt authentic_source would
	// otherwise read as empty, and the responses would name no source.
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
		if !countryPattern.MatchString(ds.Country) {
			return nil, fmt.Errorf("data service %q: country must be an ISO 3166-1 alpha-2 code", ds.ID)
		}
		names, ok := adapters[ds.Source]
		if !ok {
			return nil, fmt.Errorf("data service %q: no adapter %q", ds.ID, ds.Source)
		}
		if len(ds.GrantPath) < 2 || ds.GrantPath[0] != '/' {
			return nil, fmt.Errorf("data service %q: grant_path must be a path on the Outway", ds.ID)
		}
		if len(ds.Attributes) == 0 {
			return nil, fmt.Errorf("data service %q: names no attributes", ds.ID)
		}
		for id, name := range ds.Attributes {
			if u, err := url.Parse(id); err != nil || !u.IsAbs() {
				return nil, fmt.Errorf("data service %q: attribute identifier %q is not an absolute URI", ds.ID, id)
			}
			if !slices.Contains(names, name) {
				return nil, fmt.Errorf("data service %q: adapter %q cannot ask for attribute %q", ds.ID, ds.Source, name)
			}
		}
	}
	return doc.DataServices, nil
}
