package issuance

import (
	"context"
	"encoding/json"
	"fmt"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// Preview is the response of the NL credential preview endpoint. It is not
// part of OpenID4VCI: the NL Wallet shows these values to the user before
// it asks for the credentials, and refuses a credential that differs.
type Preview struct {
	CredentialPreviews []CredentialPreview `json:"credential_previews"`
}

// CredentialPreview previews one credential identifier.
type CredentialPreview struct {
	CredentialID string         `json:"credential_id"`
	ConfigID     string         `json:"config_id"`
	Format       string         `json:"format"`
	Payload      map[string]any `json:"credential_payload"`
}

// CredentialPreview returns the previews for a Bearer access token. Only
// KindNL profiles have this endpoint; the NL Wallet calls it without DPoP.
func (s *Service) CredentialPreview(ctx context.Context, t Target, authorization string) (Preview, error) {
	_, p, err := s.resolve(t)
	if err != nil {
		return Preview{}, err
	}
	if p.Kind != KindNL {
		return Preview{}, ErrUnknownProfile
	}
	token, session, err := s.authorize(ctx, t, authorization, "Bearer")
	if err != nil {
		return Preview{}, err
	}
	// The preview accepts the DPoP-bound token without DPoP, because the NL
	// Wallet calls it so; that makes the token a bearer token here (RFC
	// 9449 section 7.2 forbids this). It answers once per token, to limit
	// what a leaked token discloses.
	if first, err := s.onetime.FirstUse(ctx, kindPreview, token.Hash, token.ExpiresAt, s.now()); err != nil {
		return Preview{}, err
	} else if !first {
		return Preview{}, refuse(oid4vci.ErrInvalidRequest, "the preview has been fetched already with this access token")
	}
	out := Preview{CredentialPreviews: []CredentialPreview{}}
	for _, d := range session.Documents {
		c := s.configs[d.ConfigurationID]
		payload := map[string]any{
			"vct": c.VCT,
			"nbf": d.NotBefore.Unix(),
			"exp": d.Expires.Unix(),
		}
		for k, v := range d.Claims {
			tagged, err := tagAttribute(v)
			if err != nil {
				return Preview{}, refuse(oid4vci.ErrServerError, err.Error())
			}
			payload[k] = tagged
		}
		out.CredentialPreviews = append(out.CredentialPreviews, CredentialPreview{
			CredentialID: d.CredentialID, ConfigID: d.ConfigurationID, Format: sdjwtvc.Type, Payload: payload,
		})
	}
	return out, nil
}

// tagAttribute writes a JSON value as the NL Wallet's tagged Attribute
// ({"type": ..., "value": ...}), recursively.
func tagAttribute(v any) (map[string]any, error) {
	switch t := v.(type) {
	case nil:
		return map[string]any{"type": "null"}, nil
	case string:
		return map[string]any{"type": "text", "value": t}, nil
	case bool:
		return map[string]any{"type": "bool", "value": t}, nil
	case float64, int, int64, json.Number:
		return map[string]any{"type": "number", "value": t}, nil
	case []any:
		items := make([]any, len(t))
		for i, e := range t {
			tagged, err := tagAttribute(e)
			if err != nil {
				return nil, err
			}
			items[i] = tagged
		}
		return map[string]any{"type": "array", "value": items}, nil
	case map[string]any:
		obj := map[string]any{}
		for k, e := range t {
			tagged, err := tagAttribute(e)
			if err != nil {
				return nil, err
			}
			obj[k] = tagged
		}
		return map[string]any{"type": "object", "value": obj}, nil
	default:
		return nil, fmt.Errorf("attribute of type %T cannot be previewed", v)
	}
}
