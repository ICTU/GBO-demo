package onboarding

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCoreHasNoInfrastructureImports makes the ports-and-adapters dependency
// rule executable. Package structure, rather than reviewer memory, protects
// the application core from transport and persistence coupling.
func TestCoreHasNoInfrastructureImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"crypto/tls":    true,
		"flag":          true,
		"net/http":      true,
		"os":            true,
		"path/filepath": true,
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		files := token.NewFileSet()
		file, err := parser.ParseFile(files, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", entry.Name(), err)
			}
			if forbidden[path] {
				position := files.Position(imported.Pos())
				t.Errorf("%s imports infrastructure package %q at %s", entry.Name(), path, position)
			}
		}
	}
}
