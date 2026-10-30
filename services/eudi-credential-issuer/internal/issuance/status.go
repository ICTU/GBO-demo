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

// StatusListURI is where a status list is served; it is the uri of the
// status reference in a credential and the sub of the token.
func (s *Service) StatusListURI(list string) string {
	return s.cfg.PublicURL + "/status/" + list
}

// StatusList returns the current Status List Token of list.
func (s *Service) StatusList(ctx context.Context, list string) (string, error) {
	if !s.knownList(list) {
		return "", ErrUnknownStatusList
	}
	revoked, err := s.status.Revoked(ctx, list)
	if err != nil {
		return "", err
	}
	now := s.now()
	return statuslist.Sign(statuslist.Token{
		URI: s.StatusListURI(list), IssuedAt: now, Expires: now.Add(statusLifetime),
		TTL: statusTTL, Size: s.cfg.StatusListSize, Revoked: revoked,
	}, s.cfg.StatusKey, x5c.Header(s.cfg.StatusChain))
}

// Revoke revokes an entry of a status list. A revocation is not undone
// (ARF VCR_04).
func (s *Service) Revoke(ctx context.Context, list string, index int) error {
	if !s.knownList(list) || index < 0 || index >= s.cfg.StatusListSize {
		return ErrUnknownStatusList
	}
	return s.status.Revoke(ctx, list, index)
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

func (s *Service) knownList(list string) bool {
	for _, c := range s.cfg.Configurations {
		if c.StatusList == list {
			return true
		}
	}
	return false
}
