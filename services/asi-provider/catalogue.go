package main

// The provisional catalogue of attributes. There is no Dutch catalogue of
// attributes yet, so the identifiers are placeholders of the form
// https://gbo.example/attributes/<name>/<version>.
//
// A catalogue entry is "ours" when this mock acts as the authentic source for
// it. An entry that is in the catalogue but not ours (the diploma) exists so a
// request for it can be answered with Unknown instead of 404.

const attributeBase = "https://gbo.example/attributes/"

type catalogueEntry struct {
	// Name is the key under which the value is stored in the source.
	Name string
	// Ours is true when this source is the authentic source for the attribute.
	Ours bool
}

type catalogue map[string]catalogueEntry

func defaultCatalogue() catalogue {
	c := catalogue{}
	for _, name := range []string{"family_name", "given_name", "birth_date", "nationality", "address"} {
		c[attributeBase+name+"/1"] = catalogueEntry{Name: name, Ours: true}
	}
	c[attributeBase+"education_qualification/1"] = catalogueEntry{Name: "education_qualification", Ours: false}
	return c
}

func (c catalogue) lookup(id string) (catalogueEntry, bool) {
	e, ok := c[id]
	return e, ok
}
