package verification

// The catalogue of a data service: the attributes its authentic source
// provides. There is no Dutch catalogue of attributes yet, so the identifiers
// are placeholders of the form https://gbo.example/attributes/<name>/<version>,
// set in the configuration of the data services.
//
// An attribute of another authentic source is not in the catalogue: a QTSP
// finds the data service of each attribute in the Data Service Directory
// (ETSI TS 119 478 clause 5.3), and asking the wrong one is 404.

// Entry is an attribute in the catalogue.
type Entry struct {
	// Name is the name under which the source adapter knows the attribute.
	Name string
}

// Catalogue maps attribute identifiers to entries.
type Catalogue map[string]Entry

func (c Catalogue) lookup(id string) (Entry, bool) {
	e, ok := c[id]
	return e, ok
}
