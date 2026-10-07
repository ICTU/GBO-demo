package ftvgraphql

import "strings"

// checkTransport accepts only the transport subset of Section 5.1 and
// returns the raw body. Anything else may reach the source in a form the
// mapper never sees: a query string, GET, another media type, a compressed
// or upgraded connection.
func checkTransport(req Request, graphqlPath string) (string, *Unverifiable) {
	// Byte-equal, no normalisation: /graphql/, //graphql, /GraphQL, a
	// percent-encoded variant or any query string (a bare ? included) is a
	// different request target.
	if graphqlPath == "" || req.Target != graphqlPath {
		return "", unverifiable(SubUnsupportedTransport, "request target is not the GraphQL path")
	}
	if req.Method != "POST" {
		return "", unverifiable(SubUnsupportedTransport, "method %q is not POST", req.Method)
	}
	contentType, present, ok := header(req.Headers, "Content-Type")
	if !ok || !present || !IsJSONMediaType(contentType) {
		return "", unverifiable(SubUnsupportedTransport, "Content-Type is not application/json")
	}
	for _, name := range []string{"Upgrade", "Content-Encoding"} {
		if _, present, ok := header(req.Headers, name); !ok || present {
			return "", unverifiable(SubUnsupportedTransport, "%s header present", name)
		}
	}
	body, ok := req.Body.(string)
	if !ok {
		return "", unverifiable(SubUnsupportedTransport, "body absent or not a string")
	}
	return body, nil
}

// header looks a header up case-insensitively. ok is false when more than
// one name matches or the value is not a string: then the mapper cannot
// tell which value the source reads.
func header(headers map[string]any, name string) (value string, present, ok bool) {
	for k, v := range headers {
		if !strings.EqualFold(k, name) {
			continue
		}
		s, isString := v.(string)
		if present || !isString {
			return "", true, false
		}
		value, present = s, true
	}
	return value, present, true
}

// IsJSONMediaType accepts application/json with at most the parameter
// charset=utf-8, names and values compared case-insensitively (RFC 9110).
// Nothing else: no RFC 2231 forms (charset*), no empty parameter, no space
// around "=". Exported so the source applies this exact rule (guarantee H1):
// a general media-type parser accepts forms this one refuses.
func IsJSONMediaType(v string) bool {
	parts := strings.Split(v, ";")
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "application/json") {
		return false
	}
	for _, p := range parts[1:] {
		name, value, found := strings.Cut(strings.TrimSpace(p), "=")
		if !found || !strings.EqualFold(name, "charset") {
			return false
		}
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		if !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}
