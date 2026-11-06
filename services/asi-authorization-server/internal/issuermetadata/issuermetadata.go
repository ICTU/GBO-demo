// Package issuermetadata reads from a credential issuer's metadata what the
// consent for issuance shows of a credential configuration (OpenID4VCI 1.0
// sections 12.2.2 and 12.2.4).
package issuermetadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// maxMetadata bounds the metadata document the server reads.
const maxMetadata = 1 << 20

// locale is the language the consent page is in.
const locale = "nl"

type display struct {
	Name   string `json:"name"`
	Locale string `json:"locale"`
}

// Fetcher returns a reader of issuer metadata over client: the unsigned
// metadata at the well-known URL of the issuer identifier.
func Fetcher(client *http.Client) func(ctx context.Context, issuer, configuration string) (authorization.CredentialDisplay, error) {
	return func(ctx context.Context, issuer, configuration string) (authorization.CredentialDisplay, error) {
		u, err := url.Parse(issuer)
		if err != nil {
			return authorization.CredentialDisplay{}, err
		}
		u.Path = "/.well-known/openid-credential-issuer" + strings.TrimSuffix(u.Path, "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return authorization.CredentialDisplay{}, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return authorization.CredentialDisplay{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return authorization.CredentialDisplay{}, fmt.Errorf("issuer metadata: status %d", resp.StatusCode)
		}
		var md struct {
			Configurations map[string]struct {
				CredentialMetadata struct {
					Display []display `json:"display"`
					Claims  []struct {
						Path    []any     `json:"path"`
						Display []display `json:"display"`
					} `json:"claims"`
				} `json:"credential_metadata"`
			} `json:"credential_configurations_supported"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadata)).Decode(&md); err != nil {
			return authorization.CredentialDisplay{}, fmt.Errorf("issuer metadata: %w", err)
		}
		c, ok := md.Configurations[configuration]
		if !ok {
			return authorization.CredentialDisplay{}, authorization.ErrUnknownCredentialConfiguration
		}
		out := authorization.CredentialDisplay{Name: pick(c.CredentialMetadata.Display, configuration)}
		for _, cl := range c.CredentialMetadata.Claims {
			parts := make([]string, 0, len(cl.Path))
			for _, p := range cl.Path {
				parts = append(parts, fmt.Sprint(p))
			}
			out.Claims = append(out.Claims, pick(cl.Display, strings.Join(parts, ".")))
		}
		return out, nil
	}
}

// pick returns the Dutch name, else the first, else fallback.
func pick(ds []display, fallback string) string {
	for _, d := range ds {
		if d.Locale == locale && d.Name != "" {
			return d.Name
		}
	}
	if len(ds) > 0 && ds[0].Name != "" {
		return ds[0].Name
	}
	return fallback
}
