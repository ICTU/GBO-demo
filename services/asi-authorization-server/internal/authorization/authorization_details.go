package authorization

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
)

const (
	// AuthorizationDetailsType identifies the ASI-specific authorization
	// detail carried according to RFC 9396.
	AuthorizationDetailsType = "https://gbo.example/authorization-details/authentic-source-attributes"
	// AuthorizationDetailsClaim is the request parameter and access-token
	// claim that binds an authorization to the selected attributes.
	AuthorizationDetailsClaim = "authorization_details"
)

// AuthorizationDetail is the exact ASI request the user authorizes: the data
// service, operation and attribute identifiers. It is sent as the single
// element of the RFC 9396 authorization_details array.
type AuthorizationDetail struct {
	Type                 string   `json:"type"`
	Locations            []string `json:"locations"`
	Actions              []string `json:"actions"`
	AttributeIdentifiers []string `json:"attribute_identifiers"`
}

func parseAuthorizationDetails(raw, audience, scope string) (AuthorizationDetail, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var details []AuthorizationDetail
	if raw == "" || dec.Decode(&details) != nil {
		return AuthorizationDetail{}, errors.New("authorization_details must be a JSON array with one ASI authorization detail")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AuthorizationDetail{}, errors.New("authorization_details contains data after the JSON array")
	}
	if len(details) != 1 {
		return AuthorizationDetail{}, errors.New("authorization_details must contain exactly one ASI authorization detail")
	}
	detail := details[0]
	if detail.Type != AuthorizationDetailsType {
		return AuthorizationDetail{}, errors.New("authorization_details has an unsupported type")
	}
	if len(detail.Locations) != 1 || detail.Locations[0] != audience {
		return AuthorizationDetail{}, errors.New("authorization_details locations must name the requested resource")
	}
	if !slices.Equal(detail.Actions, strings.Fields(scope)) {
		return AuthorizationDetail{}, errors.New("authorization_details actions must equal the requested scope")
	}
	if len(detail.AttributeIdentifiers) == 0 || len(detail.AttributeIdentifiers) > 50 {
		return AuthorizationDetail{}, errors.New("authorization_details must name between 1 and 50 attributes")
	}
	seen := map[string]bool{}
	for _, id := range detail.AttributeIdentifiers {
		u, err := url.Parse(id)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || seen[id] {
			return AuthorizationDetail{}, errors.New("authorization_details attribute identifiers must be unique absolute HTTPS URIs")
		}
		seen[id] = true
	}
	return detail, nil
}
