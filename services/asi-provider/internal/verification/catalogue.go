package verification

// The provisional catalogue of attributes. There is no Dutch catalogue of
// attributes yet, so the identifiers are placeholders of the form
// https://gbo.example/attributes/<name>/<version>.
//
// A catalogue entry is "ours" when this mock acts as the authentic source for
// it. An entry that is in the catalogue but not ours (the diploma) exists so a
// request for it can be answered with Unknown instead of 404.

const attributeBase = "https://gbo.example/attributes/"

// Entry is an attribute in the catalogue.
type Entry struct {
	// Name is the key under which the value is stored in the source.
	Name string
	// Ours is true when this source is the authentic source for the attribute.
	Ours bool
}

// Catalogue maps attribute identifiers to entries.
type Catalogue map[string]Entry

// DefaultCatalogue is the provisional catalogue of the mock.
func DefaultCatalogue() Catalogue {
	c := Catalogue{}
	for _, name := range []string{"family_name", "given_name", "birth_date", "nationality", "address"} {
		c[attributeBase+name+"/1"] = Entry{Name: name, Ours: true}
	}
	c[attributeBase+"education_qualification/1"] = Entry{Name: "education_qualification", Ours: false}
	return c
}

func (c Catalogue) lookup(id string) (Entry, bool) {
	e, ok := c[id]
	return e, ok
}
