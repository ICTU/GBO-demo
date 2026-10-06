package ftvgraphql

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// Schema is the SDL from the policy bundle the rules were written against
// (Section 7.1), loaded once and shared by every evaluation.
type Schema struct {
	schema *ast.Schema
	// Digest is "sha256:" and the hex SHA-256 of the SDL text (Section 7.3).
	Digest string
}

var executableLocations = map[ast.DirectiveLocation]bool{
	ast.LocationQuery:              true,
	ast.LocationMutation:           true,
	ast.LocationSubscription:       true,
	ast.LocationField:              true,
	ast.LocationFragmentDefinition: true,
	ast.LocationFragmentSpread:     true,
	ast.LocationInlineFragment:     true,
	ast.LocationVariableDefinition: true,
}

// LoadSchema parses and checks the bundled SDL. The SDL must not define an
// executable directive: a source-defined directive could change what
// executes, and validation must reject every directive but @skip and
// @include.
func LoadSchema(sdl string) (*Schema, error) {
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sdl})
	if err != nil {
		return nil, fmt.Errorf("load schema: %w", err)
	}
	if s.Query == nil {
		return nil, fmt.Errorf("schema has no query root type")
	}
	var executable []string
	for name, d := range s.Directives {
		if d.Position != nil && d.Position.Src != nil && d.Position.Src.BuiltIn {
			continue
		}
		for _, loc := range d.Locations {
			if executableLocations[loc] {
				executable = append(executable, "@"+name)
				break
			}
		}
	}
	if len(executable) > 0 {
		sort.Strings(executable)
		return nil, fmt.Errorf("schema defines executable directives %v", executable)
	}
	sum := sha256.Sum256([]byte(sdl))
	return &Schema{schema: s, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}
