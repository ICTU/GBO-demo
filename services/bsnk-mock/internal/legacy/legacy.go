// Package legacy is the core of the mock's first interface, which the
// consent portal and the source sidecars still call: a BSN becomes an opaque
// PI that the mock remembers, and that PI becomes the BSN again.
//
// It has nothing of BSNk's model in it. Package polymorphic follows that
// model and replaces this one once the callers have moved.
package legacy

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

const piSalt = "pi-salt"

// demoBSN is known to a fresh store, so that a PI made for it before a
// restart still resolves.
const demoBSN = "123456789"

// Store remembers which BSN a PI was made for.
type Store struct {
	mu      sync.RWMutex
	piToBSN map[string]string
}

// NewStore returns a store that knows the demo BSN.
func NewStore() *Store {
	s := &Store{piToBSN: make(map[string]string)}
	s.piToBSN[piFor(demoBSN)] = demoBSN
	return s
}

func hashHex16(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:16]
}

func piFor(bsn string) string {
	return "PI-" + hashHex16(bsn+piSalt)
}

// Pseudonymize returns the PI for a BSN and the pseudonym of that BSN at the
// recipient, and remembers the PI.
func (s *Store) Pseudonymize(bsn, recipientOIN string) (pi, pseudonym string) {
	pi = piFor(bsn)
	s.mu.Lock()
	s.piToBSN[pi] = bsn
	s.mu.Unlock()
	return pi, "EP-" + hashHex16(bsn+recipientOIN)
}

// Resolve returns the BSN a PI was made for, if the store has seen it.
func (s *Store) Resolve(pi string) (bsn string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bsn, ok = s.piToBSN[pi]
	return bsn, ok
}
