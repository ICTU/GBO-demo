package main

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
)

// Verification results, ETSI TS 119 478 REQ-ASIP-6.1.1.2-04.
const (
	resultURIMatch     = "http://uri.etsi.org/19478/VerificationResult/Match"
	resultURINoMatch   = "http://uri.etsi.org/19478/VerificationResult/NoMatch"
	resultURIVariation = "http://uri.etsi.org/19478/VerificationResult/MatchWithVariation"
	resultURIUnknown   = "http://uri.etsi.org/19478/VerificationResult/Unknown"
)

// compareValues compares the value a QTSP claims with the value the source
// holds, both decoded JSON.
//
//   - Match: equal; arrays are compared as sets, so order does not matter.
//   - MatchWithVariation: equal after the documented normalisation of every
//     string (see normaliseString).
//   - NoMatch: otherwise.
func compareValues(claimed, held any) string {
	if equalValues(claimed, held, identity) {
		return resultURIMatch
	}
	if equalValues(claimed, held, normaliseString) {
		return resultURIVariation
	}
	return resultURINoMatch
}

func identity(s string) string { return s }

func equalValues(a, b any, norm func(string) string) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && norm(av) == norm(bv)
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !equalValues(x, y, norm) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		return equalAsSets(av, bv, norm)
	default:
		return reflect.DeepEqual(a, b)
	}
}

// equalAsSets compares two arrays regardless of order.
func equalAsSets(a, b []any, norm func(string) string) bool {
	key := func(v any) string {
		out, _ := json.Marshal(normaliseTree(v, norm))
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

func normaliseTree(v any, norm func(string) string) any {
	switch t := v.(type) {
	case string:
		return norm(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normaliseTree(x, norm)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normaliseTree(x, norm)
		}
		return out
	default:
		return v
	}
}

// normaliseString applies the admissible orthographic variations of
// REQ-ASIP-6.1.1.1-10 that this mock supports, in this order:
//
//  1. diacritics are removed (é → e, ë → e);
//  2. hyphens become spaces;
//  3. runs of white space collapse to one space, and leading and trailing
//     white space is removed;
//  4. case is folded.
//
// Transliteration between scripts is not supported.
func normaliseString(s string) string {
	stripped, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err == nil {
		s = stripped
	}
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.ToLower(s)
}

// jsonPath resolves the subset of RFC 9535 JSONPath this mock supports:
// a root "$" followed by member names (".name" or "['name']") and array
// indexes ("[n]"). It returns the selected value and the last member name,
// which is used as the key of a fragment value.
func jsonPath(doc any, path string) (any, string, error) {
	if !strings.HasPrefix(path, "$") {
		return nil, "", fmt.Errorf("JSONPath must start with $")
	}
	rest := path[1:]
	cur := doc
	last := ""
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "['"):
			end := strings.Index(rest, "']")
			if end < 0 {
				return nil, "", fmt.Errorf("unterminated member name in %q", path)
			}
			name := rest[2:end]
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, "", fmt.Errorf("%q does not select an object member", path)
			}
			cur, last, rest = obj[name], name, rest[end+2:]
		case strings.HasPrefix(rest, "["):
			end := strings.Index(rest, "]")
			if end < 0 {
				return nil, "", fmt.Errorf("unterminated index in %q", path)
			}
			idx, err := strconv.Atoi(rest[1:end])
			if err != nil {
				return nil, "", fmt.Errorf("unsupported selector in %q", path)
			}
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, "", fmt.Errorf("%q selects nothing", path)
			}
			cur, rest = arr[idx], rest[end+1:]
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			n := strings.IndexAny(rest, ".[")
			if n < 0 {
				n = len(rest)
			}
			name := rest[:n]
			if name == "" || name == "*" {
				return nil, "", fmt.Errorf("unsupported selector in %q", path)
			}
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, "", fmt.Errorf("%q does not select an object member", path)
			}
			cur, last, rest = obj[name], name, rest[n:]
		default:
			return nil, "", fmt.Errorf("unsupported selector in %q", path)
		}
		if cur == nil {
			return nil, last, errPathEmpty
		}
	}
	return cur, last, nil
}

// errPathEmpty means the path is well formed but selects no value in the
// stored attribute: there is no source data for that fragment.
var errPathEmpty = fmt.Errorf("path selects no value")
