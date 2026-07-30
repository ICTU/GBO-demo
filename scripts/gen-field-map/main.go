// gen-field-map generates the field→parent-type map consumed by the
// OpenFTV GraphQL request-mapper (services/openftv-pdp/mapper). The
// mapper walks a query without the full schema; the only schema
// knowledge it needs is the return type of object-typed fields, so the
// coverage keys stay type-qualified ("InkomensgegevensPerJaar.grondslag").
//
// Usage: go run . <schema-dir> <out.json>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gen-field-map <schema-dir> <out.json>")
		os.Exit(2)
	}
	dir, out := os.Args[1], os.Args[2]

	m := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".graphql" {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		schema, err := gqlparser.LoadSchema(&ast.Source{Name: path, Input: string(src)})
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, t := range schema.Types {
			if t.Kind != ast.Object && t.Kind != ast.Interface {
				continue
			}
			for _, f := range t.Fields {
				named := schema.Types[f.Type.Name()]
				if named != nil && (named.Kind == ast.Object || named.Kind == ast.Interface) {
					m[t.Name+"."+f.Name] = named.Name
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(m))
	for _, k := range keys {
		ordered[k] = m[k]
	}

	b, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d entries)\n", out, len(ordered))
}
