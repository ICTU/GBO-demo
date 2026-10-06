package ftvgraphql

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// graphqlRequest is a GraphQL-over-HTTP request body (Section 6.1).
type graphqlRequest struct {
	Query         string
	OperationName *string
	Variables     map[string]any
}

var errNestingDepth = errors.New("JSON nesting too deep")

// decodeBody reads the body strictly. Where JSON parsers disagree — a
// duplicate member name, a lone surrogate, invalid UTF-8 — the mapper and
// the source could read different requests, so the body is refused.
// Numbers stay json.Number: the text the source sees, never a rounded
// float.
func decodeBody(body string, maxDepth int, allowedExtensions []string) (graphqlRequest, *Unverifiable) {
	var req graphqlRequest
	if !utf8.ValidString(body) {
		return req, unverifiable(SubInvalidBody, "body is not valid UTF-8")
	}
	if hasLoneSurrogate(body) {
		return req, unverifiable(SubInvalidBody, "body has a lone surrogate escape")
	}
	v, err := decodeStrictJSON(body, maxDepth)
	if errors.Is(err, errNestingDepth) {
		return req, unverifiable(SubLimitExceeded, "JSON nesting depth over %d", maxDepth)
	}
	if err != nil {
		return req, unverifiable(SubInvalidBody, "%s", err.Error())
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return req, unverifiable(SubInvalidBody, "body is not a JSON object")
	}
	return requestFromObject(obj, allowedExtensions)
}

func requestFromObject(obj map[string]any, allowedExtensions []string) (graphqlRequest, *Unverifiable) {
	var req graphqlRequest
	for name := range obj {
		switch name {
		case "query", "operationName", "variables", "extensions":
		default:
			return req, unverifiable(SubInvalidBody, "body member %q not allowed", name)
		}
	}
	query, ok := obj["query"].(string)
	if !ok {
		return req, unverifiable(SubInvalidBody, "query absent or not a string")
	}
	req.Query = query
	switch op := obj["operationName"].(type) {
	case nil:
	case string:
		// GetOperation compares the string: "" is present and names nothing.
		if op == "" {
			return req, unverifiable(SubInvalidBody, "operationName is the empty string")
		}
		req.OperationName = &op
	default:
		return req, unverifiable(SubInvalidBody, "operationName is not a string")
	}
	switch vars := obj["variables"].(type) {
	case nil:
		req.Variables = map[string]any{}
	case map[string]any:
		req.Variables = vars
	default:
		return req, unverifiable(SubInvalidBody, "variables is not an object")
	}
	if ext, present := obj["extensions"]; present {
		extObj, ok := ext.(map[string]any)
		if !ok {
			return req, unverifiable(SubInvalidBody, "extensions is not an object")
		}
		for name := range extObj {
			if name == "persistedQuery" || !slices.Contains(allowedExtensions, name) {
				return req, unverifiable(SubInvalidBody, "extensions member %q not allowed", name)
			}
		}
	}
	return req, nil
}

// decodeStrictJSON decodes one JSON value and rejects duplicate member names
// at any depth, nesting beyond maxDepth, and trailing data.
func decodeStrictJSON(s string, maxDepth int) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := decodeValue(dec, 0, maxDepth)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("data after the JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder, depth, maxDepth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("body is not valid JSON")
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	if depth+1 > maxDepth {
		return nil, errNestingDepth
	}
	if delim == '[' {
		list := []any{}
		for dec.More() {
			item, err := decodeValue(dec, depth+1, maxDepth)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
		_, err := dec.Token()
		return list, err
	}
	obj := map[string]any{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, errors.New("body is not valid JSON")
		}
		key := keyTok.(string)
		if _, dup := obj[key]; dup {
			return nil, errors.New("duplicate JSON member name")
		}
		val, err := decodeValue(dec, depth+1, maxDepth)
		if err != nil {
			return nil, err
		}
		obj[key] = val
	}
	_, err = dec.Token()
	return obj, err
}

// hasLoneSurrogate reports a \uD800–\uDFFF escape that is not half of a
// pair. Go's decoder turns one into U+FFFD; another parser keeps it.
// Outside a string a backslash is invalid JSON anyway, so the scan does not
// track string boundaries.
func hasLoneSurrogate(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			continue
		}
		r, ok := unicodeEscape(s, i)
		if !ok {
			i++ // skip the escaped character, so \\u is not read as \u
			continue
		}
		i += 5
		switch {
		case utf16.IsSurrogate(r) && r < 0xDC00:
			low, ok := unicodeEscape(s, i+1)
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return true
			}
			i += 6
		case utf16.IsSurrogate(r):
			return true
		}
	}
	return false
}

// unicodeEscape reads a \uXXXX escape starting at s[i].
func unicodeEscape(s string, i int) (rune, bool) {
	if i+6 > len(s) || s[i] != '\\' || s[i+1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(s[i+2:i+6], 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(n), true
}
