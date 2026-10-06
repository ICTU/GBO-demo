package ftvgraphql

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

// ManifestFile is the file in a catalog directory that names, per FSC
// service, the path it serves GraphQL on and its bundled SDL.
//
//	{ "services": { "bri": { "path": "/graphql", "schema": "bd.graphql" } } }
const ManifestFile = "services.json"

// Service is the bundled configuration of one FSC service.
type Service struct {
	Path   string  // the one path the service serves GraphQL on
	Schema *Schema // nil when the SDL could not be loaded; Err says why
	Err    error
}

// Catalog holds the Service of every FSC service the PDP maps GraphQL
// requests for. One PDP serves several sources, each with its own schema;
// the service a request is for selects it.
type Catalog struct {
	services map[string]Service
}

type manifest struct {
	Services map[string]struct {
		Path   string `json:"path"`
		Schema string `json:"schema"`
	} `json:"services"`
}

// LoadCatalog reads the manifest and every SDL it names from fsys. A
// manifest that cannot be read fails the whole catalog. An SDL that cannot
// be loaded fails only its own service, whose requests then fail closed.
func LoadCatalog(fsys fs.FS) (*Catalog, error) {
	raw, err := fs.ReadFile(fsys, ManifestFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ManifestFile, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ManifestFile, err)
	}
	if len(m.Services) == 0 {
		return nil, fmt.Errorf("%s names no services", ManifestFile)
	}
	c := &Catalog{services: map[string]Service{}}
	for name, entry := range m.Services {
		s := Service{Path: entry.Path}
		switch {
		case entry.Path == "" || entry.Schema == "":
			s.Err = errors.New("path and schema are required")
		default:
			s.Schema, s.Err = loadSchemaFile(fsys, entry.Schema)
		}
		c.services[name] = s
	}
	return c, nil
}

func loadSchemaFile(fsys fs.FS, name string) (*Schema, error) {
	sdl, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	return LoadSchema(string(sdl))
}

// Service returns the configuration of the named service.
func (c *Catalog) Service(name string) (Service, bool) {
	if c == nil {
		return Service{}, false
	}
	s, ok := c.services[name]
	return s, ok
}

// Names returns the service names in sorted order.
func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.services))
	for name := range c.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Map maps a request for the named service. A service the catalog does not
// know, or a catalog that could not be loaded (nil), has no bundled schema:
// CONFIG_ERROR/SCHEMA_UNAVAILABLE.
func (c *Catalog) Map(service string, req Request) Output {
	s, ok := c.Service(service)
	if !ok {
		return Output{
			Profile: Profile,
			Fields:  []Field{},
			Unverifiable: &Unverifiable{
				Code:    CodeConfigError,
				Subcode: SubSchemaUnavailable,
				Message: fmt.Sprintf("no GraphQL schema for service %q", service),
			},
		}
	}
	return Map(req, s.Schema, Settings{GraphQLPath: s.Path})
}
