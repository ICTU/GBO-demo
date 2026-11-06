package authorization

import (
	"context"
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

func parseOptionalAuthorizationDetails(raw, audience, scope string, present, required bool) (*AuthorizationDetail, error) {
	if !present {
		if required {
			return nil, errors.New("authorization_details must be a JSON array with one ASI authorization detail")
		}
		return nil, nil
	}
	detail, err := parseAuthorizationDetails(raw, audience, scope)
	if err != nil {
		return nil, err
	}
	return &detail, nil
}

func parseAuthorizationDetails(raw, audience, scope string) (AuthorizationDetail, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var details []AuthorizationDetail
	if dec.Decode(&details) != nil {
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

// CredentialDetailsType is the authorization details type of OpenID4VCI 1.0
// section 5.1.1: a credential configuration of the issuer.
const CredentialDetailsType = "openid_credential"

// credentialDetails reads the credential configurations an issuance request
// names in authorization_details (RFC 9396) and what the issuer's metadata
// says of them. It returns an error code and reason for details that do not
// conform or name a configuration the issuer does not have. When the
// metadata cannot be read the consent names no credential.
func (s *Service) credentialDetails(raw, issuer string) ([]CredentialDisplay, string, string) {
	var details []struct {
		Type                      string `json:"type"`
		CredentialConfigurationID string `json:"credential_configuration_id"`
	}
	if err := json.Unmarshal([]byte(raw), &details); err != nil || len(details) == 0 {
		return nil, "invalid_authorization_details", "authorization_details must be a JSON array of openid_credential details"
	}
	var out []CredentialDisplay
	for _, d := range details {
		switch {
		case d.Type == AuthorizationDetailsType:
			return nil, "invalid_request", "ASI authorization_details are not used for issuance"
		case d.Type != CredentialDetailsType || d.CredentialConfigurationID == "":
			return nil, "invalid_authorization_details", "authorization_details for issuance must be openid_credential details with a credential_configuration_id"
		}
		if s.cfg.CredentialMetadata == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), credentialMetadataTimeout)
		display, err := s.cfg.CredentialMetadata(ctx, issuer, d.CredentialConfigurationID)
		cancel()
		switch {
		case errors.Is(err, ErrUnknownCredentialConfiguration):
			return nil, "invalid_authorization_details", "the issuer has no credential configuration " + d.CredentialConfigurationID
		case err != nil:
			return nil, "", ""
		}
		out = append(out, display)
	}
	return out, "", ""
}
