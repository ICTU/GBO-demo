package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"bsnk-mock/internal/polymorphic"
)

// Reading a value is not something BSNk does: a party decrypts by itself,
// with its own keys, in a decryption component that it runs. The two
// handlers here take the requests and give the answers of that component, at
// the same paths, so that a caller written against them needs no code change
// to use the real one. They are on this service only so that the demo needs
// one mock, not two.
//
// Like that component, a failure answers 500 with the reason as plain text.

type identityRequest struct {
	SignedEncryptedIdentity string            `json:"signedEncryptedIdentity"`
	ServiceProviderKeys     []string          `json:"serviceProviderKeys"`
	SchemeKeys              map[string]string `json:"schemeKeys"`
}

type identityResponse struct {
	DecodedInput     polymorphic.SignedEncryptedIdentity `json:"decodedInput"`
	BSN              string                              `json:"bsn"`
	DecryptionResult decryptionResult                    `json:"decryption_result"`
	IssuanceDate     string                              `json:"issuanceDate"`
	ExtraElements    []polymorphic.ExtraElement          `json:"extraElements,omitempty"`
}

// decryptionResult describes the identity as the decrypted value encodes it.
type decryptionResult struct {
	Bytes      string `json:"bytes"`
	Version    string `json:"version"`
	Type       string `json:"type"`
	Length     int    `json:"length"`
	Identifier string `json:"identifier"`
}

func decryptIdentity(r *http.Request) (any, error) {
	var req identityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("request body: %w", err)
	}
	res, err := polymorphic.DecryptIdentity(req.SignedEncryptedIdentity, req.ServiceProviderKeys, req.SchemeKeys)
	if err != nil {
		return nil, err
	}
	return identityResponse{
		DecodedInput: res.DecodedInput,
		BSN:          res.BSN,
		DecryptionResult: decryptionResult{
			Bytes:      base64.StdEncoding.EncodeToString(res.Encoded.Bytes),
			Version:    res.Encoded.Version,
			Type:       res.Encoded.Type,
			Length:     len(res.Encoded.Identifier),
			Identifier: res.Encoded.Identifier,
		},
		IssuanceDate:  res.IssuanceDate,
		ExtraElements: res.ExtraElements,
	}, nil
}

type pseudonymRequest struct {
	SignedEncryptedPseudonym            string            `json:"signedEncryptedPseudonym"`
	ServiceProviderKeys                 []string          `json:"serviceProviderKeys"`
	SchemeKeys                          map[string]string `json:"schemeKeys"`
	TargetClosingKey                    *json.RawMessage  `json:"targetClosingKey"`
	TargetClosingKeySchemeKeySetVersion *json.RawMessage  `json:"targetClosingKeySchemeKeySetVersion"`
}

type pseudonymResponse struct {
	DecodedInput     polymorphic.SignedEncryptedPseudonym `json:"decodedInput"`
	Pseudonym        string                               `json:"pseudonym"`
	DecodedPseudonym polymorphic.DecryptedPseudonym       `json:"decodedPseudonym"`
	IssuanceDate     string                               `json:"issuanceDate"`
	ExtraElements    []polymorphic.ExtraElement           `json:"extraElements,omitempty"`
}

func decryptPseudonym(r *http.Request) (any, error) {
	var req pseudonymRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("request body: %w", err)
	}
	if req.TargetClosingKey != nil || req.TargetClosingKeySchemeKeySetVersion != nil {
		return nil, errors.New("conversion to another closing key is not supported by the mock")
	}
	res, err := polymorphic.DecryptPseudonym(req.SignedEncryptedPseudonym, req.ServiceProviderKeys, req.SchemeKeys)
	if err != nil {
		return nil, err
	}
	return pseudonymResponse{res.DecodedInput, res.Pseudonym, res.DecodedPseudonym, res.IssuanceDate, res.ExtraElements}, nil
}

// decryption wraps a use case as a POST-only handler that answers JSON on
// success and plain text with status 500 on any failure.
func decryption(handle func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, http.MethodPost) {
			return
		}
		answer, err := handle(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	}
}
