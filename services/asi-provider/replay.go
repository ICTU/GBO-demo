package main

import (
	"sync"
	"time"
)

// replayCache remembers identifiers (jti) until they expire, so a DPoP proof
// is accepted only once.
type replayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newReplayCache() *replayCache {
	return &replayCache{seen: map[string]time.Time{}}
}

// firstUse records id until expiry and reports whether it was not seen
// before.
func (c *replayCache) firstUse(id string, expiry, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, exp := range c.seen {
		if now.After(exp) {
			delete(c.seen, k)
		}
	}
	if _, seen := c.seen[id]; seen {
		return false
	}
	c.seen[id] = expiry
	return true
}
