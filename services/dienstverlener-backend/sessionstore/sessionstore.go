// Package sessionstore keeps the provider's sessions in memory, a driven
// adapter for consentrequest.Sessions.
//
// Demo shortcuts: a restart drops every session, consent tokens included, and
// the backend runs as one replica. Anyone can create sessions (every start
// that the portal accepts makes one) and nothing caps how many there are;
// expired ones are swept at most once a minute. A real deployment keeps
// sessions in a shared store with a limit per client.
package sessionstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
)

// sweepInterval is how often New removes expired sessions: often enough to
// bound the memory, rarely enough that a burst of new sessions does not scan
// the whole store each time.
const sweepInterval = time.Minute

// Memory holds each session for Lifetime from its creation; using it does
// not extend it.
type Memory struct {
	Lifetime time.Duration
	// Now is time.Now when nil; injected by tests.
	Now func() time.Time

	mu        sync.Mutex
	sessions  map[string]entry
	lastSweep time.Time
}

type entry struct {
	session consentrequest.Session
	expires time.Time
}

func (m *Memory) New(_ context.Context, s consentrequest.Session) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b[:])

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.sessions == nil {
		m.sessions = map[string]entry{}
	}
	if now.Sub(m.lastSweep) >= sweepInterval {
		for key, e := range m.sessions {
			if !now.Before(e.expires) {
				delete(m.sessions, key)
			}
		}
		m.lastSweep = now
	}
	m.sessions[id] = entry{session: s, expires: now.Add(m.Lifetime)}
	return id, nil
}

func (m *Memory) Get(_ context.Context, id string) (consentrequest.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(id)
	if !ok {
		return consentrequest.Session{}, consentrequest.ErrUnknownSession
	}
	return e.session, nil
}

func (m *Memory) Update(_ context.Context, id string, change func(*consentrequest.Session) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(id)
	if !ok {
		return consentrequest.ErrUnknownSession
	}
	s := e.session
	if err := change(&s); err != nil {
		return err
	}
	e.session = s
	m.sessions[id] = e
	return nil
}

func (m *Memory) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

// Len is the number of sessions held, expired ones not yet swept included.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Memory) live(id string) (entry, bool) {
	e, ok := m.sessions[id]
	if !ok {
		return entry{}, false
	}
	if !m.now().Before(e.expires) {
		delete(m.sessions, id)
		return entry{}, false
	}
	return e, true
}

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
