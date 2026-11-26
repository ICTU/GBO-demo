package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"consent-register/refresh"

	ldv "gbo-demo/ldv-client"
)

// The refresh endpoint: POST /proofs/refresh, on the register's own listener.
//
// It is internal. The consent portal backend is the OAuth token endpoint over
// FSC: it reads the client's FSC peer, takes the refresh grant, and passes the
// token and the peer on here. This handler only translates; the rules are in
// package refresh. Like POST /consents it is reachable without authentication
// on the backend network (demo shortcut: a real deployment restricts it to the
// portal backend).
//
// Request:  {"refresh_token": "...", "client_peer": "<FSC peer ID>", "scope": "a b"}
//
//	scope is optional and space-delimited, as in RFC 6749.
//
// Answer:   200 {"consent_id", "consent_token", "expires_at", "scopes"}
//
//	400 {"error": "invalid_request" | "invalid_grant" | "invalid_scope"}
//	500 {"error": "server_error"}
//
// The answer has no new refresh token: the token is not rotated, and the
// client keeps the one it got with the first proof.

// refreshRequestLimit bounds the request body.
const refreshRequestLimit = 16 << 10

func handleRefresh(store ConsentStore, issuer *ConsentIssuer, logbook *registerLogbook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now().UTC()
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var body struct {
			RefreshToken string `json:"refresh_token"`
			ClientPeer   string `json:"client_peer"`
			Scope        string `json:"scope"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, refreshRequestLimit)).Decode(&body); err != nil {
			slog.Info("refresh refused", "reason", "unreadable request body")
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		service := refresh.Service{
			Store:   refreshStore{store},
			Signer:  refreshSigner{issuer},
			Logbook: refreshLogbook{logbook: logbook, store: store, request: r, start: start},
		}
		result, err := service.Refresh(r.Context(), refresh.Request{
			RefreshToken: body.RefreshToken,
			ClientPeer:   body.ClientPeer,
			Scopes:       strings.Fields(body.Scope),
		})
		if err != nil {
			code, status := refusalCode(err)
			if status == http.StatusInternalServerError {
				slog.Error("refresh failed", "err", err.Error())
			} else {
				slog.Info("refresh refused", "client_peer", body.ClientPeer, "reason", err.Error())
			}
			writeJSON(w, status, map[string]string{"error": code})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"consent_id":    result.ConsentID,
			"consent_token": result.Proof.Token,
			"expires_at":    result.Proof.ExpiresAt,
			"scopes":        result.Scopes,
		})
	}
}

// refusalCode maps a refresh error onto the RFC 6749 §5.2 error code and the
// status of this register's answer.
func refusalCode(err error) (string, int) {
	for _, refusal := range []error{refresh.ErrInvalidRequest, refresh.ErrInvalidGrant, refresh.ErrInvalidScope} {
		if errors.Is(err, refusal) {
			return refusal.Error(), http.StatusBadRequest
		}
	}
	return "server_error", http.StatusInternalServerError
}

// refreshStore adapts the consent store to the refresh core.
type refreshStore struct{ store ConsentStore }

func (s refreshStore) ByRefreshToken(ctx context.Context, tokenHash []byte) (refresh.Consent, bool, error) {
	consent, found, err := s.store.ByRefreshToken(ctx, tokenHash)
	if err != nil || !found {
		return refresh.Consent{}, found, err
	}
	return refresh.Consent{
		ID:                consent.ConsentID,
		Status:            consent.Status,
		SubjectRef:        consent.SubjectRef,
		DienstverlenerOIN: consent.DienstverlenrOIN,
		ClientPeer:        consent.ClientPeer,
		Scopes:            consent.Scopes,
		ScopeEntries:      consent.ScopeEntries,
		ValidUntil:        consent.ValidUntil,
		EncryptedSubject:  consent.EncryptedSubject,
	}, true, nil
}

func (s refreshStore) Forget(ctx context.Context, consentID string) error {
	return s.store.ForgetRefresh(ctx, consentID)
}

// refreshSigner adapts the consent issuer to the refresh core.
type refreshSigner struct{ issuer *ConsentIssuer }

func (s refreshSigner) Sign(_ context.Context, consent refresh.Consent) (refresh.Proof, error) {
	var subject map[string]EncryptedSubject
	if err := json.Unmarshal(consent.EncryptedSubject, &subject); err != nil {
		return refresh.Proof{}, fmt.Errorf("decode the kept encrypted values: %w", err)
	}
	token, expiresAt, err := s.issuer.Sign(Consent{
		ConsentID:        consent.ID,
		DienstverlenrOIN: consent.DienstverlenerOIN,
		Scopes:           consent.Scopes,
		ScopeEntries:     consent.ScopeEntries,
		ValidUntil:       consent.ValidUntil,
	}, subject)
	if err != nil {
		return refresh.Proof{}, err
	}
	return refresh.Proof{Token: token, ExpiresAt: expiresAt}, nil
}

// refreshLogbook records a refresh as a processing of the register for the
// client peer. A refresh changes nothing in the store, so the record has
// nothing to be in one transaction with and goes in on its own, like a status
// confirmation.
type refreshLogbook struct {
	logbook *registerLogbook
	store   ConsentStore
	request *http.Request
	start   time.Time
}

func (l refreshLogbook) RecordRefresh(ctx context.Context, consent refresh.Consent) error {
	if l.logbook == nil {
		return nil
	}
	const name = "dataverwerking.toestemmingsbewijs-vernieuwen"
	// The portal backend calls this register on its own network, not over
	// FSC, so the processor is the client peer it passes on. The service
	// provider the consent is for is an attribute: today it is the same
	// peer, but an integrator may refresh for a provider.
	record, err := l.logbook.buildRecordFor(l.request, l.logbook.PeerProcessor(consent.ClientPeer),
		consentProofRefreshActivity, name, consent.SubjectRef, l.start, http.StatusOK,
		map[string]any{
			"dpl.gbo.consentId":         consent.ID,
			"dpl.gbo.dienstverlenerOin": consent.DienstverlenerOIN,
		})
	if err != nil || record == nil {
		return err
	}
	if err := l.store.AppendRecord(ctx, record); err != nil {
		ldv.LogFailure(name, err)
		return err
	}
	return nil
}
