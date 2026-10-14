package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Matrix is the conformance matrix: an entry per requirement ID, and groups
// that cover every ID with a prefix (for example a clause that does not
// apply). An entry overrides a group.
type Matrix struct {
	Spec         string            `yaml:"spec"`
	IDs          string            `yaml:"ids"`
	Groups       []Group           `yaml:"groups"`
	Requirements map[string]Status `yaml:"requirements"`

	// ids is the list of requirement IDs, read from IDs.
	ids []string
}

type Group struct {
	Prefix string `yaml:"prefix"`
	Status `yaml:",inline"`
}

// Status is how the ASI route meets a requirement.
type Status struct {
	Status string   `yaml:"status"`
	Tests  []string `yaml:"tests,omitempty"`
	Reason string   `yaml:"reason,omitempty"`
	// Slice is where a failing requirement will be met.
	Slice string `yaml:"slice,omitempty"`
}

const (
	statusPassed        = "passed"
	statusStub          = "stub"
	statusNotApplicable = "not-applicable"
	statusFailing       = "failing"
)

var statuses = []string{statusPassed, statusStub, statusNotApplicable, statusFailing}

// Load reads the matrix and the ID list it names, relative to the matrix.
// Unknown fields and duplicate IDs are errors.
func Load(path string) (*Matrix, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m Matrix
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.IDs == "" {
		return nil, fmt.Errorf("%s: no ids file", path)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(path), m.IDs))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if line := strings.TrimSpace(s.Text()); line != "" && !strings.HasPrefix(line, "#") {
			m.ids = append(m.ids, line)
		}
	}
	return &m, s.Err()
}

// roleOf is the role of a requirement ID: REQ-<role>-<clause>-<n>.
func roleOf(id string) string {
	parts := strings.SplitN(id, "-", 3)
	if len(parts) < 3 {
		return "?"
	}
	return parts[1]
}

// Check returns a summary per role, or the problems with the matrix.
func Check(m *Matrix, root string) (string, []string) {
	var problems []string
	known := map[string]bool{}
	for _, id := range m.ids {
		if known[id] {
			problems = append(problems, fmt.Sprintf("%s: twice in the ID list", id))
		}
		known[id] = true
	}
	tests := testIndex{root: root, funcs: map[string]map[string]bool{}, literals: map[string]map[string]bool{}}

	for _, id := range sortedKeys(m.Requirements) {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("%s: not a requirement ID of the list", id))
		}
		problems = append(problems, m.Requirements[id].problems(id, &tests)...)
	}
	for _, g := range m.Groups {
		name := "group " + g.Prefix
		matched := slices.ContainsFunc(m.ids, func(id string) bool { return strings.HasPrefix(id, g.Prefix) })
		if g.Prefix == "" || !matched {
			problems = append(problems, fmt.Sprintf("%s: covers no requirement ID of the list", name))
		}
		problems = append(problems, g.problems(name, &tests)...)
	}

	count := map[string]map[string]int{}
	for _, id := range m.ids {
		st, ok := m.statusOf(id)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no entry and no group", id))
			continue
		}
		role := roleOf(id)
		if count[role] == nil {
			count[role] = map[string]int{}
		}
		count[role][st.Status]++
	}
	if len(problems) > 0 {
		return "", problems
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d requirements\n", m.Spec, len(m.ids))
	fmt.Fprintf(&b, "%-6s %7s %5s %15s %8s\n", "role", "passed", "stub", "not-applicable", "failing")
	for _, role := range sortedKeys(count) {
		c := count[role]
		fmt.Fprintf(&b, "%-6s %7d %5d %15d %8d\n", role, c[statusPassed], c[statusStub], c[statusNotApplicable], c[statusFailing])
	}
	return b.String(), nil
}

// statusOf is the entry of id, or else the group with the longest prefix.
func (m *Matrix) statusOf(id string) (Status, bool) {
	if st, ok := m.Requirements[id]; ok {
		return st, true
	}
	var best *Group
	for i, g := range m.Groups {
		if g.Prefix != "" && strings.HasPrefix(id, g.Prefix) && (best == nil || len(g.Prefix) > len(best.Prefix)) {
			best = &m.Groups[i]
		}
	}
	if best == nil {
		return Status{}, false
	}
	return best.Status, true
}

func (s Status) problems(name string, tests *testIndex) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, name+": "+fmt.Sprintf(format, args...)) }
	if !slices.Contains(statuses, s.Status) {
		add("status %q is not one of %s", s.Status, strings.Join(statuses, ", "))
		return out
	}
	switch s.Status {
	case statusPassed, statusStub:
		if len(s.Tests) == 0 {
			add("%s without tests", s.Status)
		}
	case statusNotApplicable, statusFailing:
		if len(s.Tests) > 0 {
			add("%s with tests", s.Status)
		}
	}
	if s.Status != statusPassed && strings.TrimSpace(s.Reason) == "" {
		add("%s without a reason", s.Status)
	}
	if s.Status == statusFailing && strings.TrimSpace(s.Slice) == "" {
		add("failing without the slice that will meet it")
	}
	if s.Status != statusFailing && s.Slice != "" {
		add("a slice is only for failing")
	}
	for _, t := range s.Tests {
		if err := tests.exists(t); err != nil {
			add("%v", err)
		}
	}
	return out
}

// testIndex finds Go test functions: a reference is <dir>:<TestName>, or
// <dir>:<TestName>/<subtest>, with dir relative to the repository root. A
// subtest cannot be listed without running the tests, so its name, and each
// part of a nested one, must at least be a string literal in a test file of
// that directory: a removed or misspelt subtest is then caught.
type testIndex struct {
	root     string
	funcs    map[string]map[string]bool
	literals map[string]map[string]bool
}

var (
	testFunc      = regexp.MustCompile(`(?m)^func (Test\w*)\(`)
	stringLiteral = regexp.MustCompile("\"((?:[^\"\\\\\n]|\\\\.)*)\"|`([^`]*)`")
)

func (x *testIndex) exists(ref string) error {
	dir, name, ok := strings.Cut(ref, ":")
	if !ok || dir == "" || name == "" {
		return fmt.Errorf("test %q is not <dir>:<TestName>", ref)
	}
	name, subtest, _ := strings.Cut(name, "/")
	if x.funcs[dir] == nil {
		x.funcs[dir], x.literals[dir] = map[string]bool{}, map[string]bool{}
		files, err := filepath.Glob(filepath.Join(x.root, dir, "*_test.go"))
		if err != nil {
			return err
		}
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			for _, m := range testFunc.FindAllSubmatch(src, -1) {
				x.funcs[dir][string(m[1])] = true
			}
			for _, m := range stringLiteral.FindAllSubmatch(src, -1) {
				if lit, err := strconv.Unquote(string(m[0])); err == nil {
					x.literals[dir][lit] = true
				}
			}
		}
	}
	if !x.funcs[dir][name] {
		return fmt.Errorf("test %s does not exist", ref)
	}
	if subtest != "" {
		for _, part := range strings.Split(subtest, "/") {
			if !x.literals[dir][part] {
				return fmt.Errorf("subtest %s does not exist", ref)
			}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
