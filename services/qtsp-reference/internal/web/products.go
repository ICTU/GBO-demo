package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"gbo-demo/qtsp-reference/internal/login"
	"gbo-demo/qtsp-reference/internal/qtsp"
)

// Product is what the citizen can have verified on the request page: one
// attribute of a data service, filled in field by field. A product without an
// attribute is shown but not available yet. Name, given names, birth date and
// nationality are no products: the QTSP has them from the citizen's PID.
type Product struct {
	ID          string
	Label       string
	Description string
	// Identifier is the attribute the product verifies; empty when the
	// product is not available yet.
	Identifier string
	Fields     []Field
}

// Field is one input of a product: Name is its key in the attribute value.
type Field struct {
	Name, Label, Hint, Default string
	Required                   bool
	// Wide spans the whole row of the field grid.
	Wide bool
}

// Available reports whether the product can be requested.
func (p Product) Available() bool { return p.Identifier != "" }

// DemoProducts are the products of the demo's request page. The address
// fields are those of the ASIP's address attribute, prefilled with the
// address of test person Frouke Jansen. All are required: the ASIP compares
// the whole address, so a missing field can never match. No source of market stall permits
// exists yet, so that product is shown as coming soon.
var DemoProducts = []Product{
	{
		ID: "address", Label: "Adres", Description: "We laten bij de bron controleren of dit adres klopt.",
		Identifier: "https://gbo.example/attributes/address/1",
		Fields: []Field{
			{Name: "street", Label: "Straat", Default: "Coolsingel", Required: true},
			{Name: "house_number", Label: "Huisnummer", Hint: "met toevoeging", Default: "12 B", Required: true},
			{Name: "postal_code", Label: "Postcode", Default: "3011 AD", Required: true},
			{Name: "city", Label: "Woonplaats", Default: "Rotterdam", Required: true},
			{Name: "country", Label: "Land", Hint: "landcode", Default: "NL", Required: true, Wide: true},
		},
	},
	{
		ID: "standplaatsvergunning", Label: "Standplaatsvergunning",
		Description: "We laten bij de gemeente controleren of u een geldige standplaatsvergunning heeft.",
	},
}

// The messages of the request page.
const (
	msgNoProduct          = "Kies wat u wilt laten verifiëren."
	msgProductUnavailable = "Dit product is nog niet beschikbaar. Kies een ander product."
	msgUnknownProduct     = "Dit product bestaat niet. Kies een product uit de lijst."
)

// productForm is the request page's state: the chosen product, the values
// typed per field, and the messages to show.
type productForm struct {
	Selected string
	Values   map[string]string
	// Errors are per field, by input id; Summary lists every message.
	Errors  map[string]string
	Summary []formError
}

type formError struct{ Target, Message string }

// fieldID is the id of a product's input on the page.
func fieldID(product, field string) string { return "f-" + product + "-" + field }

// defaultForm is the page before anything is posted: the first available
// product chosen, with its defaults.
func defaultForm(products []Product) productForm {
	f := productForm{Values: map[string]string{}, Errors: map[string]string{}}
	for _, p := range products {
		if f.Selected == "" && p.Available() {
			f.Selected = p.ID
		}
		for _, fl := range p.Fields {
			f.Values[fieldID(p.ID, fl.Name)] = fl.Default
		}
	}
	return f
}

// readProduct reads the posted form: the attribute to verify, or the form
// again with what is wrong. Only the chosen product's fields are checked.
func readProduct(products []Product, form url.Values) (qtsp.Attribute, productForm, bool) {
	f := productForm{Selected: form.Get("product"), Values: map[string]string{}, Errors: map[string]string{}}
	for _, p := range products {
		for _, fl := range p.Fields {
			id := fieldID(p.ID, fl.Name)
			f.Values[id] = strings.TrimSpace(form.Get(id))
		}
	}
	var chosen *Product
	for i := range products {
		if products[i].ID == f.Selected {
			chosen = &products[i]
		}
	}
	switch {
	case f.Selected == "":
		f.Summary = append(f.Summary, formError{Message: msgNoProduct})
		return qtsp.Attribute{}, f, false
	case chosen == nil:
		f.Selected = ""
		f.Summary = append(f.Summary, formError{Message: msgUnknownProduct})
		return qtsp.Attribute{}, f, false
	case !chosen.Available():
		f.Selected = ""
		f.Summary = append(f.Summary, formError{Message: msgProductUnavailable})
		return qtsp.Attribute{}, f, false
	}
	value := map[string]string{}
	for _, fl := range chosen.Fields {
		id := fieldID(chosen.ID, fl.Name)
		v := f.Values[id]
		if v == "" {
			if fl.Required {
				msg := "Vul " + strings.ToLower(fl.Label) + " in."
				f.Errors[id] = msg
				f.Summary = append(f.Summary, formError{Target: id, Message: msg})
			}
			continue
		}
		value[fl.Name] = v
	}
	if len(f.Summary) > 0 {
		return qtsp.Attribute{}, f, false
	}
	raw, _ := json.Marshal(value)
	return qtsp.Attribute{Identifier: chosen.Identifier, Value: raw}, f, true
}

// formView is what the request page template gets.
type formView struct {
	Products []Product
	Form     productForm
	Who      *login.Identity
}

func (h *Handler) renderForm(w http.ResponseWriter, status int, f productForm, who *login.Identity) {
	render(w, status, formPage, formView{Products: h.products, Form: f, Who: who})
}

// outcomeBadge is how the result page shows a verification result: a colour,
// an icon and the result in words, so that no result depends on colour alone.
type outcomeBadge struct{ Class, Icon, Text string }

func badge(result string) outcomeBadge {
	switch result {
	case "Match":
		return outcomeBadge{"match", "✓", "Match"}
	case "MatchWithVariation":
		return outcomeBadge{"var", "≈", "MatchWithVariation"}
	case "NoMatch":
		return outcomeBadge{"no", "✕", "NoMatch"}
	default:
		return outcomeBadge{"unk", "?", result}
	}
}

// shown is an attribute value as the result page shows it: an address on
// one line, a list joined, another object as key: value pairs.
func shown(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	return show(v)
}

func show(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			parts = append(parts, show(x))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		if _, ok := t["street"]; ok {
			line := func(keys ...string) string {
				var out []string
				for _, k := range keys {
					if s := show(t[k]); s != "" && t[k] != nil {
						out = append(out, s)
					}
				}
				return strings.Join(out, " ")
			}
			parts := []string{line("street", "house_number"), line("postal_code", "city"), line("country")}
			var kept []string
			for _, p := range parts {
				if p != "" {
					kept = append(kept, p)
				}
			}
			return strings.Join(kept, ", ")
		}
		if len(t) == 1 {
			for _, x := range t {
				return show(x)
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+show(t[k]))
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
