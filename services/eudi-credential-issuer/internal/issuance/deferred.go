package issuance

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// Deferred issuance (OpenID4VCI 1.0 section 9; #614 phase 4, F6, V1-V5). When
// the source is unavailable after a citizen logged in, the wallet gets its
// code anyway; the credential request answers with a transaction_id, and the
// deferred credential request asks the source again with the BSN from the
// login. That BSN is kept sealed, apart from the session, for at most
// DeferralTTL; then it is purged and the transaction is unknown.

// DeferralInterval is how long a wallet waits between deferred requests.
const DeferralInterval = 5 * time.Second

const (
	// pendingSubject holds the sealed BSN of a pending document until its
	// credential request; pendingDeferral a transaction until it ends.
	pendingSubject  = "subject"
	pendingDeferral = "deferral"
	// kindDeferred marks a pending credential as deferred, once.
	kindDeferred = "deferred"
)

// deferral is a deferred transaction: the credential it will issue, the
// keys it binds to, and the sealed BSN to ask the source with.
type deferral struct {
	SessionID       string            `json:"session_id"`
	CredentialID    string            `json:"credential_id"`
	ConfigurationID string            `json:"configuration_id"`
	Offer           string            `json:"offer"`
	Keys            []json.RawMessage `json:"keys"`
	Subject         string            `json:"subject"`
}

// createPendingSession stores a session for offer o whose source has not
// answered: one document without claims per configuration, and the sealed
// BSN apart, once per document, so each credential request can start its own
// deferral. It returns the code.
func (s *Service) createPendingSession(ctx context.Context, t Target, p Profile, o DisclosureOffer, binding *CodeBinding, bsn string) (string, error) {
	prov := s.providers[t.Provider]
	now := s.now()
	sealed, err := sealSubject(prov, bsn)
	if err != nil {
		return "", err
	}
	sess := Session{Provider: t.Provider, Profile: p.Name, ExpiresAt: now.Add(CodeLifetime), Binding: binding, Pending: &PendingSource{Offer: o.Key, Until: now.Add(s.cfg.DeferralTTL)}}
	for _, id := range o.ConfigurationIDs {
		credentialID, err := randomToken()
		if err != nil {
			return "", err
		}
		sess.Documents = append(sess.Documents, Document{CredentialID: credentialID, ConfigurationID: id})
	}
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	if sess.ID, err = randomToken(); err != nil {
		return "", err
	}
	sess.CodeHash = hash(code)
	if err := s.sessions.CreateSession(ctx, sess); err != nil {
		return "", err
	}
	for _, d := range sess.Documents {
		if err := s.putPending(ctx, pendingSubject, subjectID(sess.ID, d.CredentialID), sealed, now.Add(s.cfg.DeferralTTL)); err != nil {
			return "", err
		}
	}
	return code, nil
}

// deferCredential answers the credential request for a document whose source
// has not answered: it starts the deferred transaction, once per credential.
func (s *Service) deferCredential(ctx context.Context, prov *provider, session Session, doc Document, keys []jose.JSONWebKey,
	encrypter jose.Encrypter, now time.Time) (CredentialResult, error) {
	if ok, err := s.onetime.FirstUse(ctx, kindDeferred, session.ID+"/"+doc.CredentialID, now.Add(s.cfg.DeferralTTL), now); err != nil {
		return CredentialResult{}, err
	} else if !ok {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidCredentialRequest, "this credential is deferred already")
	}
	var sealed string
	subject, err := s.sessions.TakePending(ctx, pendingSubject, hash(subjectID(session.ID, doc.CredentialID)), now)
	if errors.Is(err, ErrNotFound) {
		return CredentialResult{}, refuse(oid4vci.ErrCredentialRequestDenied, "the deferral has expired")
	}
	if err != nil {
		return CredentialResult{}, err
	}
	if err := json.Unmarshal(subject.Payload, &sealed); err != nil {
		return CredentialResult{}, err
	}
	d := deferral{SessionID: session.ID, CredentialID: doc.CredentialID, ConfigurationID: doc.ConfigurationID, Offer: session.Pending.Offer, Subject: sealed}
	for _, k := range keys {
		raw, err := k.MarshalJSON()
		if err != nil {
			return CredentialResult{}, err
		}
		d.Keys = append(d.Keys, raw)
	}
	txID, err := randomToken()
	if err != nil {
		return CredentialResult{}, err
	}
	// The BSN is kept no longer than the deferral began with.
	if err := s.putPending(ctx, pendingDeferral, txID, d, subject.ExpiresAt); err != nil {
		return CredentialResult{}, err
	}
	return deferredResult(encrypter, txID)
}

// subjectID names the sealed BSN of one pending document.
func subjectID(sessionID, credentialID string) string { return sessionID + "/" + credentialID }

func deferredResult(encrypter jose.Encrypter, txID string) (CredentialResult, error) {
	resp := oid4vci.CredentialResponse{TransactionID: txID, Interval: int64(DeferralInterval.Seconds())}
	out := CredentialResult{Response: resp, Deferred: true}
	if encrypter != nil {
		enc, err := encryptResult(encrypter, resp)
		if err != nil {
			return CredentialResult{}, err
		}
		enc.Deferred = true
		return enc, nil
	}
	return out, nil
}

// DeferredCall is a deferred credential request with its authorization.
type DeferredCall struct {
	Target           Target
	Authorization    string
	DPoP             []string
	Request          oid4vci.DeferredRequest
	Encrypted        bool
	EncryptedRequest string
}

// Deferred handles a deferred credential request: it asks the source again.
// Unavailable: 202 with the same transaction_id. No data: the transaction
// ends with credential_request_denied (V5). Data: the credential.
func (s *Service) Deferred(ctx context.Context, call DeferredCall) (CredentialResult, error) {
	prov, p, err := s.resolve(call.Target)
	if err != nil {
		return CredentialResult{}, err
	}
	if !s.offersLogin(call.Target.Provider, p) {
		return CredentialResult{}, ErrUnknownProfile
	}
	now := s.now()
	token, session, err := s.authorize(ctx, call.Target, call.Authorization, "DPoP")
	if err != nil {
		return CredentialResult{}, err
	}
	accessToken := strings.TrimSpace(call.Authorization[len("DPoP"):])
	proof, err := dpop.Verify(call.DPoP, dpop.Expected{Method: "POST", URL: s.Identifier(call.Target) + "/deferred_credential", AccessToken: accessToken}, now)
	if err != nil {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidDPoPProof, err.Error())
	}
	if proof.JKT != token.JKT {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP key is not the key the access token is bound to")
	}
	if ok, err := s.onetime.FirstUse(ctx, kindDPoP, proof.JTI, proof.IAT.Add(dpop.Window), now); err != nil {
		return CredentialResult{}, err
	} else if !ok {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP proof has been used before")
	}
	req, encrypter, err := s.openDeferred(prov, call)
	if err != nil {
		return CredentialResult{}, err
	}
	unknown := refuse(oid4vci.ErrInvalidTransactionID, "the transaction_id is unknown, finished or expired")
	if req.TransactionID == "" {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidCredentialRequest, "transaction_id is required")
	}
	pending, err := s.sessions.TakePending(ctx, pendingDeferral, hash(req.TransactionID), now)
	if errors.Is(err, ErrNotFound) {
		return CredentialResult{}, unknown
	}
	if err != nil {
		return CredentialResult{}, err
	}
	// keep puts the transaction back for a later request.
	keep := func() error { return s.sessions.PutPending(ctx, pending) }
	var d deferral
	if err := json.Unmarshal(pending.Payload, &d); err != nil {
		return CredentialResult{}, err
	}
	if d.SessionID != session.ID {
		return CredentialResult{}, errors.Join(unknown, keep())
	}
	bsn, err := openSubject(prov, d.Subject)
	if err != nil {
		return CredentialResult{}, err
	}
	o, ok := s.loginOffers[call.Target.Provider+"/"+d.Offer]
	c, okc := s.configs[d.ConfigurationID]
	if !ok || !okc || c.Provider != prov.Name {
		return CredentialResult{}, refuse(oid4vci.ErrCredentialRequestDenied, "the offer is no longer configured")
	}
	fetched, err := s.source.FetchLogin(ctx, d.ConfigurationID, bsn, o.Parameters)
	switch {
	case errors.Is(err, ErrSourceUnavailable):
		if err := keep(); err != nil {
			return CredentialResult{}, err
		}
		return deferredResult(encrypter, req.TransactionID)
	case errors.Is(err, ErrNoData) || (err == nil && len(fetched) == 0):
		return CredentialResult{}, refuse(oid4vci.ErrCredentialRequestDenied, "the source has no data for this person")
	case err != nil:
		return CredentialResult{}, errors.Join(err, keep())
	case len(fetched) > 1:
		// A deferred credential is one document: its credential identifier
		// was handed out before the source answered, so further documents
		// have no identifier the wallet knows. Refused, not cut short.
		return CredentialResult{}, refuse(oid4vci.ErrCredentialRequestDenied, "the source answered with more than one document; a deferred credential is one document")
	}
	if err := checkSourceDocument(p, c, fetched[0]); err != nil {
		return CredentialResult{}, err
	}
	notBefore := now.UTC().Truncate(24 * time.Hour)
	doc := Document{CredentialID: d.CredentialID, ConfigurationID: d.ConfigurationID, Claims: fetched[0].Attributes,
		NotBefore: notBefore, Expires: notBefore.AddDate(0, 0, c.ValidDays)}
	keys := make([]jose.JSONWebKey, len(d.Keys))
	for i, raw := range d.Keys {
		if err := keys[i].UnmarshalJSON(raw); err != nil {
			return CredentialResult{}, err
		}
	}
	out, err := s.issueOnce(ctx, prov, p, c, session, doc, keys, encrypter, now)
	if err != nil {
		return CredentialResult{}, err
	}
	// The session now holds the claims, so a refresh re-issues them.
	return out, s.sessions.CompleteDocument(ctx, session.ID, doc)
}

// CompleteDocuments returns docs with the document of doc's credential id
// replaced by doc, or ErrNotFound. For the stores.
func CompleteDocuments(docs []Document, doc Document) ([]Document, error) {
	out := make([]Document, len(docs))
	found := false
	for i, d := range docs {
		if d.CredentialID == doc.CredentialID {
			d, found = doc, true
		}
		out[i] = d
	}
	if !found {
		return nil, ErrNotFound
	}
	return out, nil
}

// AllComplete tells whether every document has its claims.
func AllComplete(docs []Document) bool {
	for _, d := range docs {
		if d.Claims == nil {
			return false
		}
	}
	return true
}

// openDeferred returns the deferred request, decrypted when the wallet
// encrypted it, and the encrypter for an encrypted response (section 9.1).
func (s *Service) openDeferred(prov *provider, call DeferredCall) (oid4vci.DeferredRequest, jose.Encrypter, error) {
	req := call.Request
	if call.Encrypted {
		body, err := oid4vci.DecryptRequest(call.EncryptedRequest, []oid4vci.RequestKey{prov.requestKey})
		if err != nil {
			return req, nil, refuse(oid4vci.ErrInvalidCredentialRequest, err.Error())
		}
		req = oid4vci.DeferredRequest{}
		if err := json.Unmarshal(body, &req); err != nil {
			return req, nil, refuse(oid4vci.ErrInvalidCredentialRequest, "the decrypted request is not a JSON deferred request")
		}
	}
	if req.ResponseEncryption == nil {
		return req, nil, nil
	}
	if !call.Encrypted {
		return req, nil, refuse(oid4vci.ErrInvalidEncryptionParameters, "an encrypted response must be asked for in an encrypted request")
	}
	e, err := oid4vci.NewResponseEncrypter(*req.ResponseEncryption)
	if err != nil {
		return req, nil, refuse(oid4vci.ErrInvalidEncryptionParameters, err.Error())
	}
	return req, e, nil
}

// subjectKey is the provider's key to seal a BSN with, derived from its
// request encryption key, which every replica has.
func subjectKey(prov *provider) ([]byte, error) {
	if prov.RequestEncryptionKey == nil {
		return nil, errors.New("the provider has no request encryption key to derive a sealing key from")
	}
	return hkdf.Key(sha256.New, prov.RequestEncryptionKey.D.Bytes(), nil, "gbo deferred subject v1", 32)
}

// sealSubject encrypts bsn with AES-GCM under the provider's sealing key.
func sealSubject(prov *provider, bsn string) (string, error) {
	key, err := subjectKey(prov)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(bsn), []byte(prov.Name))), nil
}

func openSubject(prov *provider, sealed string) (string, error) {
	key, err := subjectKey(prov)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("the sealed subject is too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(prov.Name))
	if err != nil {
		return "", errors.New("the sealed subject does not open")
	}
	return string(plain), nil
}
