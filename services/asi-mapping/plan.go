package asimapping

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"
)

// Request is what the ASIP needs of one attribute: the whole attribute, or
// only the members its fragments name.
type Request struct {
	Identifier string
	Members    []string
}

// Plan returns the source's document with ...Selected replaced by the fields
// the requests need, so one query asks only those. It returns "" when no
// field is needed: a fragment on a const member is not source data.
func (c Capability) Plan(requests []Request) (string, error) {
	root := newSelection()
	for _, request := range requests {
		attribute, ok := c.lookupAttribute(request.Identifier)
		if !ok {
			return "", fmt.Errorf("attribute %q is not offered by this source", request.Identifier)
		}
		members, err := attribute.requested(request.Members)
		if err != nil {
			return "", err
		}
		at := root
		if attribute.On != "" {
			at = root.on(attribute.On)
		}
		for _, member := range members {
			for _, pointer := range attribute.Fields[member].pointers() {
				at.add(pointer)
			}
		}
	}
	if root.empty() {
		return "", nil
	}
	return c.GraphQL.replaceSelected(root.render())
}

func (c Capability) lookupAttribute(identifier string) (Attribute, bool) {
	for _, attribute := range c.Attributes {
		if attribute.Identifier == identifier {
			return attribute, true
		}
	}
	return Attribute{}, false
}

// requested returns the members to map: those named, or all of them.
func (a Attribute) requested(members []string) ([]string, error) {
	if len(members) == 0 {
		all := make([]string, 0, len(a.Fields))
		for member := range a.Fields {
			all = append(all, member)
		}
		sort.Strings(all)
		return all, nil
	}
	for _, member := range members {
		if _, ok := a.Fields[member]; !ok {
			return nil, fmt.Errorf("attribute %q has no member %q", a.Identifier, member)
		}
	}
	return members, nil
}

// pointers returns the source fields the member is made of; a const has
// none.
func (f Field) pointers() []string {
	switch {
	case f.Each != "":
		return []string{f.Each + f.Pointer}
	case len(f.Join) > 0:
		return f.Join
	case f.Pointer != "":
		return []string{f.Pointer}
	}
	return nil
}

// selection is a GraphQL selection set under construction.
type selection struct {
	fields    map[string]*selection
	fragments map[string]*selection
}

func newSelection() *selection {
	return &selection{fields: map[string]*selection{}, fragments: map[string]*selection{}}
}

func (s *selection) on(typeName string) *selection {
	if s.fragments[typeName] == nil {
		s.fragments[typeName] = newSelection()
	}
	return s.fragments[typeName]
}

func (s *selection) add(pointer string) {
	at := s
	for _, name := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if at.fields[name] == nil {
			at.fields[name] = newSelection()
		}
		at = at.fields[name]
	}
}

func (s *selection) empty() bool {
	if len(s.fields) > 0 {
		return false
	}
	for _, fragment := range s.fragments {
		if !fragment.empty() {
			return false
		}
	}
	return true
}

func (s *selection) render() string {
	var parts []string
	for _, name := range sortedKeys(s.fields) {
		child := s.fields[name]
		if child.empty() {
			parts = append(parts, name)
		} else {
			parts = append(parts, name+" { "+child.render()+" }")
		}
	}
	for _, typeName := range sortedKeys(s.fragments) {
		if fragment := s.fragments[typeName]; !fragment.empty() {
			parts = append(parts, "... on "+typeName+" { "+fragment.render()+" }")
		}
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]*selection) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// replaceSelected puts selection in the place of ...Selected. Validate has
// checked that the document is one path of fields ending in it.
func (g GraphQL) replaceSelected(selection string) (string, error) {
	document, err := parser.ParseQuery(&ast.Source{Name: "asi", Input: g.Document})
	if err != nil {
		return "", fmt.Errorf("graphql.document does not parse: %w", err)
	}
	replacement, err := parser.ParseQuery(&ast.Source{Name: "selection", Input: "{ " + selection + " }"})
	if err != nil {
		return "", fmt.Errorf("derived selection does not parse: %w", err)
	}
	set := &document.Operations[0].SelectionSet
	for {
		if len(*set) != 1 {
			return "", fmt.Errorf("graphql.document is not one path ending in ...%s", selectedFragment)
		}
		switch s := (*set)[0].(type) {
		case *ast.Field:
			set = &s.SelectionSet
			continue
		case *ast.FragmentSpread:
			if s.Name == selectedFragment {
				*set = replacement.Operations[0].SelectionSet
				var out bytes.Buffer
				formatter.NewFormatter(&out).FormatQueryDocument(document)
				return out.String(), nil
			}
		}
		return "", fmt.Errorf("graphql.document is not one path ending in ...%s", selectedFragment)
	}
}
