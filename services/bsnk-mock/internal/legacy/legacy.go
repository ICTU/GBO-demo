// Package legacy is the core of the mock's first interface, of which one call
// is left: the consent portal derives its own reference to a citizen with it.
//
// It has nothing of BSNk's model in it. Package polymorphic follows that
// model and replaces this one once the portal has moved.
package legacy

import (
	"crypto/sha256"
	"encoding/hex"
)

// Pseudonym returns the pseudonym of a BSN at a recipient: the same for one
// citizen at one recipient, and different at every other recipient.
func Pseudonym(bsn, recipientOIN string) string {
	h := sha256.Sum256([]byte(bsn + recipientOIN))
	return "EP-" + hex.EncodeToString(h[:])[:16]
}
