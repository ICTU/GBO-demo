package main

// Helpers around the generated SDK: configuration lookups, resources the
// AppHost keeps track of by name (the C# AppHost walks builder.Resources
// instead), and the host lookups for Node.js.

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"apphost/modules/aspire"
)

type tracked struct {
	handle aspire.Resource
	nest   func(parent aspire.Resource)
	label  func(tags []string, order int)
	kind   kind
}

// kind sorts a resource into one of the dashboard groups (see labelAll).
type kind int

const (
	ownBuild kind = iota
	offTheShelf
	monitoring
	setup
)

type namedContainer struct {
	name   string
	handle aspire.ContainerResource
}

type model struct {
	b        aspire.DistributedApplicationBuilder
	repoRoot string
	runMode  bool

	// TODO(go-apphost/typed-model): the Go SDK has no typed enumeration of the
	// model (builder.Resources in C#); DistributedApplicationModel only
	// returns untyped handles. The AppHost keeps its own index instead.
	resources  map[string]tracked
	containers []namedContainer
	seeds      []string

	directoryManager aspire.ContainerResource
}

func newModel(b aspire.DistributedApplicationBuilder) *model {
	dir, err := b.AppHostDirectory()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	runMode, err := b.ExecutionContext().IsRunMode()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	return &model{
		b:         b,
		repoRoot:  filepath.Clean(filepath.Join(dir, "..")),
		runMode:   runMode,
		resources: map[string]tracked{},
	}
}

// repo returns a path in the repository.
func (m *model) repo(relative string) string { return filepath.Join(m.repoRoot, relative) }

// config reads an AppHost configuration value ("" when unset): launch
// profile environment (Gbo__Profiles), user secrets, or `-- --Key=value`.
func (m *model) config(key string) string {
	v, err := m.b.GetConfiguration().GetConfigValue(key)
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	if v == nil {
		return ""
	}
	return *v
}

func (m *model) configOr(key, fallback string) string {
	if v := m.config(key); v != "" {
		return v
	}
	return fallback
}

// generated is a password made on first run and kept in the user secrets.
func (m *model) generated(name string) aspire.ParameterResource {
	return m.b.AddParameterWithGeneratedValue(name,
		&aspire.GenerateParameterDefault{MinLength: 24, Lower: true, Upper: true, Numeric: true, Special: false},
		aspire.AddParameterWithGeneratedValueSecret(true),
		aspire.AddParameterWithGeneratedValuePersist(true))
}

// secret is a secret parameter with a local demo default.
func (m *model) secret(name, value string) aspire.ParameterResource {
	return m.b.AddParameter(name, aspire.AddParameterValue(value), aspire.AddParameterSecret(true))
}

func (m *model) register(name string, r aspire.Resource, k kind, nest func(aspire.Resource), label func([]string, int)) {
	m.resources[name] = tracked{handle: r, nest: nest, label: label, kind: k}
}

// classify overrides the group a resource got from how it was added.
func (m *model) classify(name string, k kind) {
	if t, ok := m.resources[name]; ok {
		t.kind = k
		m.resources[name] = t
	}
}

func (m *model) trackContainer(name string, c aspire.ContainerResource, k kind) aspire.ContainerResource {
	m.register(name, c, k,
		func(p aspire.Resource) { c.WithParentRelationship(p) },
		func(tags []string, order int) { c.WithTags(tags).WithDisplayOrder(order) })
	m.containers = append(m.containers, namedContainer{name, c})
	return c
}

// container adds a container from a published image.
func (m *model) container(name, image, tag string) aspire.ContainerResource {
	return m.trackContainer(name, m.b.AddContainer(name, image).WithImageTag(tag), offTheShelf)
}

// dockerfile adds a container built from a Dockerfile. An empty dockerfile
// means the Dockerfile in the context directory.
func (m *model) dockerfile(name, context, dockerfile string) aspire.ContainerResource {
	var opts []aspire.AddDockerfileOption
	if dockerfile != "" {
		opts = append(opts, aspire.AddDockerfileDockerfilePath(dockerfile))
	}
	return m.trackContainer(name, m.b.AddDockerfile(name, context, opts...), ownBuild)
}

// containerArgs sets a container's arguments, some of which may be
// parameters or reference expressions.
//
// TODO(go-apphost/string-args): WithArgs takes []string in the Go SDK, where C#
// takes object[] (strings, parameters and expressions). Mixed arguments go
// through WithArgsCallback.
func (m *model) containerArgs(c aspire.ContainerResource, args ...any) aspire.ContainerResource {
	if plain, ok := allStrings(args); ok {
		return c.WithArgs(plain)
	}
	return c.WithArgsCallback(func(ctx aspire.CommandLineArgsCallbackContext) {
		for _, a := range args {
			if err := ctx.Args().Add(argValue(a)); err != nil {
				log.Printf("%v", aspire.FormatError(err))
			}
		}
	})
}

// executable adds a host process with plain string arguments.
func (m *model) executable(name, command, workingDirectory string, args ...string) aspire.ExecutableResource {
	if args == nil {
		// TODO(go-apphost/nil-args): AddExecutable leaves a nil args slice out
		// of the request, and the AppHost server then rejects the call with
		// "Missing required argument 'args'". An empty slice works.
		args = []string{}
	}
	e := m.b.AddExecutable(name, command, workingDirectory, args)
	m.register(name, e, setup,
		func(p aspire.Resource) { e.WithParentRelationship(p) },
		func(tags []string, order int) { e.WithTags(tags).WithDisplayOrder(order) })
	return e
}

// executableArgs adds a host process whose arguments may include parameters
// or reference expressions (see containerArgs).
func (m *model) executableArgs(name, command, workingDirectory string, args ...any) aspire.ExecutableResource {
	if plain, ok := allStrings(args); ok {
		return m.executable(name, command, workingDirectory, plain...)
	}
	e := m.executable(name, command, workingDirectory)
	return e.WithArgsCallback(func(ctx aspire.CommandLineArgsCallbackContext) {
		for _, a := range args {
			if err := ctx.Args().Add(argValue(a)); err != nil {
				log.Printf("%v", aspire.FormatError(err))
			}
		}
	})
}

// nestUnder lists child under parent in the dashboard, when both exist.
func (m *model) nestUnder(child, parent string) {
	c, ok := m.resources[child]
	p, ok2 := m.resources[parent]
	if ok && ok2 {
		c.nest(p.handle)
	}
}

// argValue wraps a bare parameter in a reference expression.
//
// TODO(go-apphost/parameter-arg): a ParameterResource added to the arguments as
// is runs fine but breaks `aspire publish` ("Command line args must be
// strings" in publish-compose); wrapped in an expression it publishes as
// ${NAME}, as C# does.
func argValue(a any) any {
	if p, ok := a.(aspire.ParameterResource); ok {
		return aspire.RefExpr("%v", p)
	}
	return a
}

// labelAll tags every resource with its group and sorts the groups in the
// dashboard: the services built from this repository first, then the
// components taken off the shelf, then monitoring, then the run-once setup
// steps. A child nested under a parent stays under it; the order applies
// among the top-level resources and among siblings.
func (m *model) labelAll() {
	groups := map[kind]struct {
		tag   string
		order int
	}{
		ownBuild:    {"own build", 300},
		offTheShelf: {"off the shelf", 200},
		monitoring:  {"monitoring", 100},
		setup:       {"setup", 0},
	}
	for _, t := range m.resources {
		g := groups[t.kind]
		t.label([]string{g.tag}, g.order)
	}
}

func allStrings(args []any) ([]string, bool) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		s, ok := a.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mkdirAll(path string) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		log.Fatal(err)
	}
}

// findNodeDir returns the first bin directory with a node of at least
// minimumMajor and an npm next to it: Gbo:NodeDir, PATH, Homebrew, nvm,
// Volta, fnm.
func findNodeDir(configured string, minimumMajor int) string {
	home, _ := os.UserHomeDir()
	versions := func(root, suffix string) []string {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil
		}
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(root, e.Name(), suffix))
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		return dirs
	}
	candidates := []string{configured}
	candidates = append(candidates, filepath.SplitList(os.Getenv("PATH"))...)
	candidates = append(candidates, "/opt/homebrew/opt/node/bin", "/opt/homebrew/bin", "/usr/local/opt/node/bin", "/usr/local/bin")
	if matches, _ := filepath.Glob("/opt/homebrew/opt/node@*"); matches != nil {
		for _, d := range matches {
			candidates = append(candidates, filepath.Join(d, "bin"))
		}
	}
	candidates = append(candidates, versions(filepath.Join(home, ".nvm/versions/node"), "bin")...)
	candidates = append(candidates, versions(filepath.Join(home, ".volta/tools/image/node"), "bin")...)
	candidates = append(candidates, versions(filepath.Join(home, ".local/share/fnm/node-versions"), "installation/bin")...)

	seen := map[string]bool{}
	for _, dir := range candidates {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		node := filepath.Join(dir, "node")
		if !fileExists(node) || !fileExists(filepath.Join(dir, "npm")) {
			continue
		}
		out, err := exec.Command(node, "--version").Output()
		if err != nil {
			continue
		}
		version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
		if major, err := strconv.Atoi(strings.Split(version, ".")[0]); err == nil && major >= minimumMajor {
			return dir
		}
	}
	return ""
}
