package issuance

import "time"

// shortLived is the validity of a credential without a revocation
// reference.
const shortLived = 24 * time.Hour

// validity returns nbf and exp of a credential with validDays, issued now
// for an offer with reference. With a reference, the credential is valid
// from the start of the day for validDays, rounded so issuance time does not
// single it out. Without one, nobody can revoke it: it is valid for 24 hours
// from the start of the hour (ARF VCR_01b), however many validDays.
func validity(now time.Time, validDays int, reference string) (time.Time, time.Time) {
	if reference == "" {
		nbf := now.UTC().Truncate(time.Hour)
		return nbf, nbf.Add(shortLived)
	}
	nbf := now.UTC().Truncate(24 * time.Hour)
	return nbf, nbf.AddDate(0, 0, validDays)
}
