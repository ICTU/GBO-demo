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
// apply) or a role (for example the wallet's requirements). An entry
// overrides a group. A matrix that names profiles may give a requirement
// another status per profile.
type Matrix struct {
	Spec         string            `yaml:"spec"`
	IDs          string            `yaml:"ids"`
	Profiles     []string          `yaml:"profiles,omitempty"`
	Groups       []Group           `yaml:"groups"`
	Requirements map[string]Status `yaml:"requirements"`

	// ids is the list of requirement IDs, read from IDs, and roles the role
	// the list gives an ID.
	ids   []string
	roles map[string]string
}

type Group struct {
	Prefix string `yaml:"prefix,omitempty"`
	Role   string `yaml:"role,omitempty"`
	Status `yaml:",inline"`
}

// Status is how a requirement is met, for every profile unless Profiles
// says otherwise for one.
type Status struct {
	ProfileStatus `yaml:",inline"`
	Profiles      map[string]ProfileStatus `yaml:"profiles,omitempty"`
}

// ProfileStatus is how a requirement is met.
type ProfileStatus struct {
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
	// statusDeviation is a requirement not met on purpose; the reason says
	// why and for which wallet.
	statusDeviation = "deviation"
)

var statuses = []string{statusPassed, statusStub, statusNotApplicable, statusFailing, statusDeviation}

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
	m.roles = map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch fields := strings.Fields(line); len(fields) {
		case 1:
			m.ids = append(m.ids, fields[0])
		case 2:
			m.ids = append(m.ids, fields[0])
			m.roles[fields[0]] = fields[1]
		default:
			return nil, fmt.Errorf("%s: %s: more than an ID and a role", m.IDs, line)
		}
	}
	return &m, s.Err()
}

// roleOf is the role the ID list gives id, or else the role in the ID:
// REQ-<role>-<clause>-<n>.
func (m *Matrix) roleOf(id string) string {
	if role, ok := m.roles[id]; ok {
		return role
	}
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
		problems = append(problems, m.problems(id, m.Requirements[id], &tests)...)
	}
	for _, g := range m.Groups {
		name := g.name()
		if g.Prefix == "" && g.Role == "" {
			problems = append(problems, name+": a group needs a prefix or a role")
		} else if !slices.ContainsFunc(m.ids, func(id string) bool { return m.covers(g, id) }) {
			problems = append(problems, fmt.Sprintf("%s: covers no requirement ID of the list", name))
		}
		problems = append(problems, m.problems(name, g.Status, &tests)...)
	}

	// count is per profile ("" without profiles), role and status.
	profiles := m.Profiles
	if len(profiles) == 0 {
		profiles = []string{""}
	}
	count := map[string]map[string]map[string]int{}
	for _, id := range m.ids {
		st, ok := m.statusOf(id)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no entry and no group", id))
			continue
		}
		role := m.roleOf(id)
		for _, p := range profiles {
			ps := st.ProfileStatus
			if o, ok := st.Profiles[p]; ok {
				ps = o
			}
			if count[p] == nil {
				count[p] = map[string]map[string]int{}
			}
			if count[p][role] == nil {
				count[p][role] = map[string]int{}
			}
			count[p][role][ps.Status]++
		}
	}
	if len(problems) > 0 {
		return "", problems
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d requirements\n", m.Spec, len(m.ids))
	column := ""
	if len(m.Profiles) > 0 {
		column = fmt.Sprintf("%-8s ", "profile")
	}
	// The role column is at least six wide, as it always was.
	width := 6
	for _, roles := range count {
		for role := range roles {
			width = max(width, len(role))
		}
	}
	fmt.Fprintf(&b, "%s%-*s %7s %5s %15s %8s %10s\n", column, width, "role", "passed", "stub", "not-applicable", "failing", "deviation")
	for _, p := range profiles {
		if p != "" {
			column = fmt.Sprintf("%-8s ", p)
		}
		for _, role := range sortedKeys(count[p]) {
			c := count[p][role]
			fmt.Fprintf(&b, "%s%-*s %7d %5d %15d %8d %10d\n", column, width, role, c[statusPassed], c[statusStub], c[statusNotApplicable], c[statusFailing], c[statusDeviation])
		}
	}
	return b.String(), nil
}

func (g Group) name() string {
	switch {
	case g.Prefix != "" && g.Role != "":
		return "group " + g.Prefix + " role " + g.Role
	case g.Role != "":
		return "group role " + g.Role
	}
	return "group " + g.Prefix
}

// covers tells whether group g covers id: its prefix, its role, or both.
func (m *Matrix) covers(g Group, id string) bool {
	if g.Prefix == "" && g.Role == "" {
		return false
	}
	return (g.Prefix == "" || strings.HasPrefix(id, g.Prefix)) && (g.Role == "" || m.roleOf(id) == g.Role)
}

// statusOf is the entry of id, or else the most specific group that covers
// it: the longest prefix, and of equal prefixes the one with a role.
func (m *Matrix) statusOf(id string) (Status, bool) {
	if st, ok := m.Requirements[id]; ok {
		return st, true
	}
	specificity := func(g Group) int {
		n := 2 * len(g.Prefix)
		if g.Role != "" {
			n++
		}
		return n
	}
	var best *Group
	for i, g := range m.Groups {
		if m.covers(g, id) && (best == nil || specificity(g) > specificity(*best)) {
			best = &m.Groups[i]
		}
	}
	if best == nil {
		return Status{}, false
	}
	return best.Status, true
}

// problems checks a status and its status per profile.
func (m *Matrix) problems(name string, s Status, tests *testIndex) []string {
	out := s.problems(name, tests)
	if len(s.Profiles) > 0 && len(m.Profiles) == 0 {
		return append(out, name+": a status per profile, but the matrix names no profiles")
	}
	for _, p := range sortedKeys(s.Profiles) {
		pname := name + " profile " + p
		if !slices.Contains(m.Profiles, p) {
			out = append(out, pname+": not a profile of the matrix")
			continue
		}
		out = append(out, s.Profiles[p].problems(pname, tests)...)
	}
	return out
}

func (s ProfileStatus) problems(name string, tests *testIndex) []string {
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
