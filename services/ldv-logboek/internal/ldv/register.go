package ldv

import (
	"encoding/json"
	"fmt"
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
	// Disclaimer is carried into every served entry so nobody mistakes the
	// stand-in for a register of record.
	Disclaimer string     `json:"disclaimer"`
	Activities []Activity `json:"activities"`

	byReference map[string]Activity
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

// Reference is the `<id>@<version>` form used in
// `dpl.core.processing_activity_id` and in the register's own URIs.
func (a Activity) Reference() string { return a.ID + "@" + a.Version }

// referencePattern constrains both halves of a reference. It doubles as the
// path validator for the register endpoint, so a lookup can never be talked
// into traversing out of the map.
var referencePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*@v[0-9]+$`)

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
	if len(r.Activities) == 0 {
		return fmt.Errorf("register %q has no verwerkingsactiviteiten", r.Verantwoordelijke)
	}
	r.byReference = make(map[string]Activity, len(r.Activities))
	for _, activity := range r.Activities {
		reference := activity.Reference()
		if !referencePattern.MatchString(reference) {
			return fmt.Errorf("verwerkingsactiviteit %q: reference must look like 'bd-ib-2025@v1'", reference)
		}
		if strings.TrimSpace(activity.Name) == "" || strings.TrimSpace(activity.Doel) == "" {
			return fmt.Errorf("verwerkingsactiviteit %q: name and doel are mandatory", reference)
		}
		if _, duplicate := r.byReference[reference]; duplicate {
			return fmt.Errorf("verwerkingsactiviteit %q is declared twice", reference)
		}
		r.byReference[reference] = activity
	}
	return nil
}

// Resolve looks up a `<id>@<version>` reference.
func (r *Register) Resolve(reference string) (Activity, bool) {
	activity, ok := r.byReference[reference]
	return activity, ok
}

// References lists every entry, sorted, for the register's index endpoint and
// for error messages that would otherwise leave a caller guessing.
func (r *Register) References() []string {
	references := make([]string, 0, len(r.byReference))
	for reference := range r.byReference {
		references = append(references, reference)
	}
	sort.Strings(references)
	return references
}
