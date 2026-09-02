package ldvclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// Subject-id types. `pi` is the polymorphic identity the DvTP chain carries
// end to end; `logboek-pseudoniem` is a key-derived, logbook-local stand-in
// for where a component holds only a BSN (the EUDI flow) and has nothing else
// to name the Betrokkene by. Never the BSN itself (REQ-60/72).
//
// Types a single component owns are not declared here: the consent register's
// portal-scoped reference and RvIG's own person id are meaningful only in
// those components, and putting them in the shared client would suggest they
// are interchangeable with these.
const (
	SubjectTypePI        = "pi"
	SubjectTypePseudonym = "logboek-pseudoniem"
)

// LocalPseudonym derives a stable, logbook-local reference to a Betrokkene
// from a BSN, for the flows where a component holds nothing else. Keyed HMAC,
// truncated: stable within one Verantwoordelijke, meaningless outside it, and
// not reversible without the key.
//
// A demo stand-in, not a pseudonymisation scheme. The real chain would name
// the Betrokkene by a pseudonym it was given, and the fact that the EUDI flow
// has none to give is the gap this makes visible.
//
// The key is per Verantwoordelijke on purpose: one shared key would make the
// same citizen recognisable across organisations' logbooks, which is exactly
// what a logbook-local pseudonym must not do.
func (c *Client) LocalPseudonym(bsn string) string {
	mac := hmac.New(sha256.New, c.pseudonymKey)
	_, _ = mac.Write([]byte(bsn))
	return "LP-" + hex.EncodeToString(mac.Sum(nil))[:24]
}

// Subject decides how a record names the Betrokkene the request was about.
//
// When an upstream component of the same Verantwoordelijke passed a pseudonym
// on, that one is used: a Betrokkene that changes name halfway down the chain
// cannot be followed through the logboek. Otherwise this component holds only
// a BSN and derives a logbook-local pseudonym from it.
//
// An unrecognised type on the wire is treated as no type at all. The logbook
// would refuse it anyway, and refusing here means the component still names
// the Betrokkene by something it can vouch for.
func (c *Client) Subject(header http.Header, bsn string) (string, string) {
	passed := strings.TrimSpace(header.Get(HeaderSubjectID))
	switch strings.TrimSpace(header.Get(HeaderSubjectIDType)) {
	case SubjectTypePI:
		if passed != "" {
			return passed, SubjectTypePI
		}
	case SubjectTypePseudonym:
		if passed != "" {
			return passed, SubjectTypePseudonym
		}
	}
	return c.LocalPseudonym(bsn), SubjectTypePseudonym
}
