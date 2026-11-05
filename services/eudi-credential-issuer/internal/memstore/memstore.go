// Package memstore keeps the issuer's state in memory. It is for tests and
// for a local run without a database; state is lost on restart and not
// shared between replicas.
package memstore

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sort"
	"sync"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// Store implements issuance.Sessions, issuance.OneTime and
// issuance.StatusLists.
type Store struct {
	mu       sync.Mutex
	sessions map[string]issuance.Session
	byCode   map[string]string
	used     map[string]bool
	tokens   map[string]issuance.AccessToken
	issued   map[string]bool
	reserved map[string]reservation
	values   map[string]time.Time
	status   map[string]map[int]*entry
	revoked  map[string]bool
	disclose map[string]issuance.DisclosureSession
	pending  map[string]issuance.PendingAuthorization
	attempts map[string]int
}

type entry struct {
	revoked   bool
	reference string
}

// New returns an empty store.
func New() *Store {
	return &Store{
		sessions: map[string]issuance.Session{}, byCode: map[string]string{}, used: map[string]bool{},
		tokens: map[string]issuance.AccessToken{}, issued: map[string]bool{}, reserved: map[string]reservation{}, values: map[string]time.Time{},
		status: map[string]map[int]*entry{}, revoked: map[string]bool{}, disclose: map[string]issuance.DisclosureSession{},
		pending: map[string]issuance.PendingAuthorization{}, attempts: map[string]int{},
	}
}

func (s *Store) CreateSession(_ context.Context, sess issuance.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byCode[sess.CodeHash]; dup {
		return errors.New("memstore: duplicate code")
	}
	s.sessions[sess.ID] = sess
	s.byCode[sess.CodeHash] = sess.ID
	return nil
}

func (s *Store) RedeemCode(_ context.Context, provider, profile, codeHash, txCodeHash string, now time.Time) (issuance.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byCode[codeHash]
	if !ok || s.used[codeHash] {
		return issuance.Session{}, issuance.ErrNotFound
	}
	sess := s.sessions[id]
	if sess.Provider != provider || sess.Profile != profile || !now.Before(sess.ExpiresAt) {
		return issuance.Session{}, issuance.ErrNotFound
	}
	switch {
	case sess.TxCodeHash == "" && txCodeHash != "":
		return issuance.Session{}, issuance.ErrTxCodeUnexpected
	case sess.TxCodeHash != "" && txCodeHash == "":
		return issuance.Session{}, issuance.ErrTxCodeMissing
	case sess.TxCodeHash != "" && sess.TxCodeHash != txCodeHash:
		s.attempts[codeHash]++
		if s.attempts[codeHash] >= issuance.MaxTxCodeAttempts {
			s.used[codeHash] = true
		}
		return issuance.Session{}, issuance.ErrTxCodeMismatch
	}
	s.used[codeHash] = true
	return sess, nil
}

func (s *Store) SaveToken(_ context.Context, t issuance.AccessToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t.Hash] = t
	return nil
}

func (s *Store) Token(_ context.Context, tokenHash string, now time.Time) (issuance.AccessToken, issuance.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tokenHash]
	if !ok || !now.Before(t.ExpiresAt) {
		return issuance.AccessToken{}, issuance.Session{}, issuance.ErrNotFound
	}
	return t, s.sessions[t.SessionID], nil
}

// reservation is who reserved a credential, until when.
type reservation struct {
	owner string
	until time.Time
}

func (s *Store) ReserveIssue(_ context.Context, sessionID, credentialID, owner string, now, until time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + "/" + credentialID
	if s.issued[key] {
		return false, nil
	}
	if r, ok := s.reserved[key]; ok && now.Before(r.until) {
		return false, nil
	}
	s.reserved[key] = reservation{owner: owner, until: until}
	return true, nil
}

func (s *Store) CompleteIssue(_ context.Context, sessionID, credentialID, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + "/" + credentialID
	if r, ok := s.reserved[key]; !ok || r.owner != owner || s.issued[key] {
		return false, nil
	}
	delete(s.reserved, key)
	s.issued[key] = true
	return true, nil
}

func (s *Store) ReleaseIssue(_ context.Context, sessionID, credentialID, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + "/" + credentialID
	if r, ok := s.reserved[key]; ok && r.owner == owner {
		delete(s.reserved, key)
	}
	return nil
}

func (s *Store) Put(_ context.Context, kind, value string, expiry time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[kind+"/"+value] = expiry
	return nil
}

func (s *Store) Take(_ context.Context, kind, value string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := kind + "/" + value
	exp, ok := s.values[key]
	delete(s.values, key)
	return ok && now.Before(exp), nil
}

func (s *Store) FirstUse(_ context.Context, kind, id string, expiry, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "seen/" + kind + "/" + id
	if exp, ok := s.values[key]; ok && now.Before(exp) {
		return false, nil
	}
	s.values[key] = expiry
	return true, nil
}

func (s *Store) Allocate(_ context.Context, list string, size int, reference string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.status[list]
	if entries == nil {
		entries = map[int]*entry{}
		s.status[list] = entries
	}
	if len(entries) >= size {
		return 0, errors.New("memstore: status list is full")
	}
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
		if err != nil {
			return 0, err
		}
		idx := int(n.Int64())
		if _, taken := entries[idx]; !taken {
			entries[idx] = &entry{reference: reference, revoked: reference != "" && s.revoked[reference]}
			return idx, nil
		}
	}
}

func (s *Store) Revoke(_ context.Context, list string, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.status[list][index]
	if !ok {
		return issuance.ErrNotFound
	}
	e.revoked = true
	return nil
}

func (s *Store) RevokeReference(_ context.Context, reference string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reference == "" {
		return 0, nil
	}
	s.revoked[reference] = true
	n := 0
	for _, entries := range s.status {
		for _, e := range entries {
			if reference != "" && e.reference == reference && !e.revoked {
				e.revoked = true
				n++
			}
		}
	}
	return n, nil
}

func (s *Store) ReferenceRevoked(_ context.Context, reference string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[reference], nil
}

func (s *Store) Revoked(_ context.Context, list string) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int
	for idx, e := range s.status[list] {
		if e.revoked {
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out, nil
}

func (s *Store) CreateDisclosure(_ context.Context, d issuance.DisclosureSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.disclose[d.ID]; dup {
		return errors.New("memstore: duplicate disclosure session")
	}
	s.disclose[d.ID] = d
	return nil
}

func (s *Store) TakeDisclosure(_ context.Context, id string, now time.Time) (issuance.DisclosureSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.disclose[id]
	if !ok {
		return issuance.DisclosureSession{}, issuance.ErrNotFound
	}
	delete(s.disclose, id)
	if !now.Before(d.ExpiresAt) {
		return issuance.DisclosureSession{}, issuance.ErrNotFound
	}
	return d, nil
}

func (s *Store) PutPending(_ context.Context, p issuance.PendingAuthorization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := p.Kind + " " + p.ID
	if _, dup := s.pending[key]; dup {
		return errors.New("memstore: duplicate pending authorization")
	}
	p.Payload = append([]byte(nil), p.Payload...)
	s.pending[key] = p
	return nil
}

func (s *Store) TakePending(_ context.Context, kind, id string, now time.Time) (issuance.PendingAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := kind + " " + id
	p, ok := s.pending[key]
	delete(s.pending, key)
	if !ok || !now.Before(p.ExpiresAt) {
		return issuance.PendingAuthorization{}, issuance.ErrNotFound
	}
	return p, nil
}

func (s *Store) Pending(_ context.Context, kind, id string, now time.Time) (issuance.PendingAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[kind+" "+id]
	if !ok || !now.Before(p.ExpiresAt) {
		return issuance.PendingAuthorization{}, issuance.ErrNotFound
	}
	return p, nil
}

func (s *Store) Session(_ context.Context, id string) (issuance.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return issuance.Session{}, issuance.ErrNotFound
	}
	return sess, nil
}

func (s *Store) ExtendSession(_ context.Context, id string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return issuance.ErrNotFound
	}
	if until.After(sess.ExpiresAt) {
		sess.ExpiresAt = until
		s.sessions[id] = sess
	}
	return nil
}

func (s *Store) CompleteDocument(_ context.Context, id string, doc issuance.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return issuance.ErrNotFound
	}
	docs, err := issuance.CompleteDocuments(sess.Documents, doc)
	if err != nil {
		return err
	}
	sess.Documents = docs
	if issuance.AllComplete(docs) {
		sess.Pending = nil
	}
	s.sessions[id] = sess
	return nil
}
