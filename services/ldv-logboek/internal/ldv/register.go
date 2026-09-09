package ldv

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// A Register is the stand-in for the Verantwoordelijke's Register van
// Verwerkingsactiviteiten (RvVA). LDV requires every record to name the
// verwerkingsactiviteit it belongs to, by a reference that a reader can
// actually resolve — which presumes a register this demo does not have.
//
// Rather than leave `dpl.core.processing_activity_id` as an unresolvable
// string, each logbook carries a small versioned document generated from what
// we do have: the scope definitions of the dienstencatalogus for the sources,
// plus the infrastructural processings around them. It is served next to the
// logbook and validated against on every write, so a record can never
// reference an entry that is not there.
//
// This is emphatically not an RvVA. Whether the real thing extends the
// dienstencatalogus or becomes a separate facility is an open question; the
// stand-in exists to make the gap tangible rather than to answer it.
type Register struct {
	// Verantwoordelijke is the organisation this register (and the logbook
	// serving it) belongs to.
	Verantwoordelijke string `json:"verantwoordelijke"`
	// BaseURI is where this register's entries live. Every activity's URI is
	// this plus /<id>/<version>, and that URI is what a record carries:
	// dpl.core.processing_activity_id is a URI (§3.2.2.9), because a bare
	// local reference is not resolvable by whoever reads the record later —
	// possibly at another organisation.
	BaseURI string `json:"base_uri"`
	// Disclaimer is carried into every served entry so nobody mistakes the
	// stand-in for a register of record.
	Disclaimer string     `json:"disclaimer"`
	Activities []Activity `json:"activities"`

	byURI map[string]Activity
}

// Activity is one verwerkingsactiviteit. The fields beyond id/version are
// documentation for a human reader of the demo; the logbook only enforces
// that the reference resolves.
type Activity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Name    string `json:"name"`
	Doel    string `json:"doel"`
	// Grondslag is the AVG basis. In this demo it is either consent
	// (DvTP) or a wettelijke taak; the value is prose, not a code list.
	Grondslag string `json:"grondslag"`
	// ScopeID links the activity back to the dienstencatalogus scope it was
	// generated from, where there is one (`bd:ib:2025`). Infrastructural
	// activities such as the PI→BSN resolution have none.
	ScopeID                    string   `json:"scope_id,omitempty"`
	CategorieenBetrokkenen     []string `json:"categorieen_betrokkenen,omitempty"`
	CategorieenPersoonsgegeven []string `json:"categorieen_persoonsgegevens,omitempty"`
	// Ontvangers names who the data goes to. Empty for processings that do
	// not leave the Verantwoordelijke.
	Ontvangers []string `json:"ontvangers,omitempty"`
}

// URI is what a record carries and what the register serves. Built from the
// register's base rather than stored per entry, so a register that moves does
// not have to be rewritten line by line.
func (a Activity) URI(baseURI string) string {
	return strings.TrimRight(baseURI, "/") + "/" + a.ID + "/" + a.Version
}

// identifierPattern constrains an activity id, and versionPattern its
// version. They double as path validators for the register endpoint, so a
// lookup can never be talked into traversing out of the map.
var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	versionPattern    = regexp.MustCompile(`^v[0-9]+$`)
)

// LoadRegister reads a register document and indexes it. It fails on an empty
// or inconsistent document rather than starting a logbook that would reject
// every write it is sent.
func LoadRegister(path string) (*Register, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read verwerkingsactiviteiten register: %w", err)
	}
	var register Register
	if err := json.Unmarshal(payload, &register); err != nil {
		return nil, fmt.Errorf("parse verwerkingsactiviteiten register: %w", err)
	}
	if err := register.index(); err != nil {
		return nil, err
	}
	return &register, nil
}

func (r *Register) index() error {
	if strings.TrimSpace(r.Verantwoordelijke) == "" {
		return fmt.Errorf("register has no verantwoordelijke")
	}
	base, err := url.Parse(strings.TrimSpace(r.BaseURI))
	if err != nil || !base.IsAbs() || base.Host == "" {
		return fmt.Errorf("register %q needs an absolute base_uri, got %q", r.Verantwoordelijke, r.BaseURI)
	}
	if len(r.Activities) == 0 {
		return fmt.Errorf("register %q has no verwerkingsactiviteiten", r.Verantwoordelijke)
	}
	r.byURI = make(map[string]Activity, len(r.Activities))
	for _, activity := range r.Activities {
		if !identifierPattern.MatchString(activity.ID) || !versionPattern.MatchString(activity.Version) {
			return fmt.Errorf("verwerkingsactiviteit %q/%q: id must be kebab-case and version must look like 'v1'", activity.ID, activity.Version)
		}
		if strings.TrimSpace(activity.Name) == "" || strings.TrimSpace(activity.Doel) == "" {
			return fmt.Errorf("verwerkingsactiviteit %q: name and doel are mandatory", activity.ID)
		}
		uri := activity.URI(r.BaseURI)
		if _, duplicate := r.byURI[uri]; duplicate {
			return fmt.Errorf("verwerkingsactiviteit %q is declared twice", uri)
		}
		r.byURI[uri] = activity
	}
	return nil
}

// Resolve looks up an activity by its URI, which is what a record carries.
func (r *Register) Resolve(uri string) (Activity, bool) {
	activity, ok := r.byURI[uri]
	return activity, ok
}

// ResolveLocal looks up an activity by the id and version in a request path,
// so the URI a record carries actually resolves when dereferenced.
func (r *Register) ResolveLocal(id, version string) (Activity, bool) {
	if !identifierPattern.MatchString(id) || !versionPattern.MatchString(version) {
		return Activity{}, false
	}
	return r.Resolve(Activity{ID: id, Version: version}.URI(r.BaseURI))
}

// URIs lists every entry, sorted, for the register's index endpoint and for
// error messages that would otherwise leave a caller guessing.
func (r *Register) URIs() []string {
	uris := make([]string, 0, len(r.byURI))
	for uri := range r.byURI {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	return uris
}
