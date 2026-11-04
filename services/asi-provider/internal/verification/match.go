package verification

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	asimapping "gbo-demo/asi-mapping"
)

// Verification results, ETSI TS 119 478 REQ-ASIP-6.1.1.2-04.
const (
	ResultMatch     = "http://uri.etsi.org/19478/VerificationResult/Match"
	ResultNoMatch   = "http://uri.etsi.org/19478/VerificationResult/NoMatch"
	ResultVariation = "http://uri.etsi.org/19478/VerificationResult/MatchWithVariation"
	ResultUnknown   = "http://uri.etsi.org/19478/VerificationResult/Unknown"
)

// compareValues compares the value a QTSP claims with the value the source
// holds, both decoded JSON, of the shape schema describes.
//
//   - Match: equal; arrays are compared as sets, so order does not matter.
//   - MatchWithVariation: equal after the documented normalisation of every
//     string without a format (see normaliseString). A string with a format,
//     such as a date, has one way of writing and matches exactly or not.
//   - NoMatch: otherwise.
func compareValues(claimed, held any, schema *asimapping.ValueSchema) string {
	if equalValues(claimed, held, schema, identity) {
		return ResultMatch
	}
	if equalValues(claimed, held, schema, normaliseString) {
		return ResultVariation
	}
	return ResultNoMatch
}

func identity(s string) string { return s }

// normaliser is the normalisation of a string of schema: none for a string
// with a format.
func normaliser(schema *asimapping.ValueSchema, norm func(string) string) func(string) string {
	if schema != nil && schema.Format != "" {
		return identity
	}
	return norm
}

func property(schema *asimapping.ValueSchema, name string) *asimapping.ValueSchema {
	if schema == nil {
		return nil
	}
	return schema.Properties[name]
}

func items(schema *asimapping.ValueSchema) *asimapping.ValueSchema {
	if schema == nil {
		return nil
	}
	return schema.Items
}

func equalValues(a, b any, schema *asimapping.ValueSchema, norm func(string) string) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		n := normaliser(schema, norm)
		return ok && n(av) == n(bv)
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !equalValues(x, y, property(schema, k), norm) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		return equalAsSets(av, bv, items(schema), norm)
	default:
		return reflect.DeepEqual(a, b)
	}
}

// equalAsSets compares two arrays regardless of order.
func equalAsSets(a, b []any, schema *asimapping.ValueSchema, norm func(string) string) bool {
	key := func(v any) string {
		out, _ := json.Marshal(normaliseTree(v, schema, norm))
		return string(out)
	}
	ka := make([]string, len(a))
	kb := make([]string, len(b))
	for i := range a {
		ka[i] = key(a[i])
		kb[i] = key(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	return reflect.DeepEqual(ka, kb)
}

func normaliseTree(v any, schema *asimapping.ValueSchema, norm func(string) string) any {
	switch t := v.(type) {
	case string:
		return normaliser(schema, norm)(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normaliseTree(x, property(schema, k), norm)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normaliseTree(x, items(schema), norm)
		}
		return out
	default:
		return v
	}
}

// normaliseString applies the admissible orthographic variations of
// REQ-ASIP-6.1.1.1-10 that this mock supports, in this order:
//
//  1. Latin letters that do not decompose are transliterated as in
//     ICAO Doc 9303 (ß → ss, æ → ae, ø → oe, ĳ → ij, and so on);
//  2. diacritics are removed (é → e, ë → e);
//  3. hyphens become spaces;
//  4. runs of white space collapse to one space, and leading and trailing
//     white space is removed;
//  5. case is folded.
//
// Transliteration between scripts (Cyrillic, Greek, ...) is not supported.
func normaliseString(s string) string {
	s = latinTransliteration.Replace(s)
	stripped, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err == nil {
		s = stripped
	}
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.ToLower(s)
}

// latinTransliteration maps the Latin letters that removing diacritics does
// not cover to their ICAO Doc 9303 transliteration.
var latinTransliteration = strings.NewReplacer(
	"ß", "ss", "ẞ", "SS",
	"Æ", "AE", "æ", "ae",
	"Ø", "OE", "ø", "oe",
	"Œ", "OE", "œ", "oe",
	"Ĳ", "IJ", "ĳ", "ij",
	"Þ", "TH", "þ", "th",
	"Ð", "D", "ð", "d",
	"Đ", "D", "đ", "d",
	"Ł", "L", "ł", "l",
	"Ħ", "H", "ħ", "h",
	"ı", "i",
)

// path is a location in the subset of RFC 9535 JSONPath this mock supports:
// a root "$" followed by member names (".name" or "['name']") and array
// indexes ("[n]").
type path []step

type step struct {
	member string
	index  bool
	n      int
}

func parsePath(location string) (path, error) {
	if !strings.HasPrefix(location, "$") {
		return nil, fmt.Errorf("JSONPath must start with $")
	}
	rest := location[1:]
	var p path
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "['"):
			end := strings.Index(rest, "']")
			if end < 0 {
				return nil, fmt.Errorf("unterminated member name in %q", location)
			}
			p, rest = append(p, step{member: rest[2:end]}), rest[end+2:]
		case strings.HasPrefix(rest, "["):
			end := strings.Index(rest, "]")
			if end < 0 {
				return nil, fmt.Errorf("unterminated index in %q", location)
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil {
				return nil, fmt.Errorf("unsupported selector in %q", location)
			}
			p, rest = append(p, step{index: true, n: n}), rest[end+1:]
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			n := strings.IndexAny(rest, ".[")
			if n < 0 {
				n = len(rest)
			}
			name := rest[:n]
			if name == "" || name == "*" {
				return nil, fmt.Errorf("unsupported selector in %q", location)
			}
			p, rest = append(p, step{member: name}), rest[n:]
		default:
			return nil, fmt.Errorf("unsupported selector in %q", location)
		}
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("a fragment location must select a member, not the whole attribute")
	}
	return p, nil
}

// selectIn returns the value at the path in doc.
func (p path) selectIn(doc any) (any, error) {
	cur := doc
	for _, s := range p {
		if s.index {
			arr, ok := cur.([]any)
			if !ok || s.n < 0 || s.n >= len(arr) {
				return nil, fmt.Errorf("index %d selects nothing", s.n)
			}
			cur = arr[s.n]
		} else {
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%q is not an object member", s.member)
			}
			cur = obj[s.member]
		}
		if cur == nil {
			return nil, errPathEmpty
		}
	}
	return cur, nil
}

// schemaIn returns the schema of the value at the path.
func (p path) schemaIn(schema *asimapping.ValueSchema) *asimapping.ValueSchema {
	for _, s := range p {
		if s.index {
			schema = items(schema)
		} else {
			schema = property(schema, s.member)
		}
	}
	return schema
}

// lastMember is the last member name on the path, the key of a fragment
// value.
func (p path) lastMember() string {
	for i := len(p) - 1; i >= 0; i-- {
		if !p[i].index {
			return p[i].member
		}
	}
	return ""
}

// errPathEmpty means the path is well formed but selects no value in the
// stored attribute: there is no source data for that fragment.
var errPathEmpty = fmt.Errorf("path selects no value")
