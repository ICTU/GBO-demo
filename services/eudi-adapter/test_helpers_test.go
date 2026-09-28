package main

import (
	"cmp"
	"net/http"
	"slices"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// testClaims labels every rule with its own claim name, in name order.
func testClaims(rules map[string]mappingRule) sourceClaims {
	claims := make(sourceClaims, 0, len(rules))
	for name, rule := range rules {
		claims = append(claims, sourceClaim{Name: name, Source: rule, Label: localizedText{{Lang: "nl-NL", Text: name}}})
	}
	slices.SortFunc(claims, func(a, b sourceClaim) int { return cmp.Compare(a.Name, b.Name) })
	return claims
}

func testDisplay(name string) sourceDisplays {
	return sourceDisplays{{Lang: "nl-NL", Name: name}}
}
