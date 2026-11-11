package issuance

import (
	"context"
	"errors"
	"time"

	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/x5c"
)

// Status list token lifetimes: relying parties may cache a token for
// statusTTL; it expires after statusLifetime.
const (
	statusTTL      = 5 * time.Minute
	statusLifetime = time.Hour
)

// ErrUnknownStatusList is returned for a list no configuration uses.
var ErrUnknownStatusList = errors.New("unknown status list")

// StatusListURI is where a provider's status list is served; it is the uri
// of the status reference in a credential and the sub of the token.
func (s *Service) StatusListURI(provider, list string) string {
	return s.cfg.PublicURL + "/status/" + provider + "/" + list
}

// statusKey names a provider's list in the store: providers have their own
// lists, signed with their own status certificate.
func statusKey(provider, list string) string {
	return provider + "/" + list
}

// StatusList returns the current Status List Token of a provider's list,
// signed with the provider's status certificate.
func (s *Service) StatusList(ctx context.Context, provider, list string) (string, error) {
	prov, ok := s.providers[provider]
	if !ok || !s.knownList(provider, list) {
		return "", ErrUnknownStatusList
	}
	revoked, err := s.status.Revoked(ctx, statusKey(provider, list))
	if err != nil {
		return "", err
	}
	now := s.now()
	return statuslist.Sign(statuslist.Token{
		URI: s.StatusListURI(provider, list), IssuedAt: now, Expires: now.Add(statusLifetime),
		TTL: statusTTL, Size: s.cfg.StatusListSize, Revoked: revoked,
	}, prov.StatusKey, x5c.Header(prov.StatusChain))
}

// Revoke revokes an entry of a provider's status list. A revocation is not
// undone (ARF VCR_04).
func (s *Service) Revoke(ctx context.Context, provider, list string, index int) error {
	if !s.knownList(provider, list) || index < 0 || index >= s.cfg.StatusListSize {
		return ErrUnknownStatusList
	}
	return s.status.Revoke(ctx, statusKey(provider, list), index)
}

// RevokeReference revokes every credential issued for offers with the
// given caller reference, and returns how many. Offers with the reference
// that have not been redeemed, and tokens that have not been used, issue
// nothing anymore.
func (s *Service) RevokeReference(ctx context.Context, reference string) (int, error) {
	if reference == "" {
		return 0, ErrNotFound
	}
	return s.status.RevokeReference(ctx, reference)
}

func (s *Service) knownList(provider, list string) bool {
	for _, c := range s.cfg.Configurations {
		if c.Provider == provider && c.StatusList == list {
			return true
		}
	}
	return false
}
