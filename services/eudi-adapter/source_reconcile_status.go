package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	sourceStatePending         = "pending"
	sourceStateActive          = "active"
	sourceStateStale           = "stale"
	sourceStateBlocked         = "blocked"
	sourceStateRolloutRequired = "rollout_required"

	sourceReasonCertificateSetNotFound  = "CERTIFICATE_SET_NOT_FOUND"
	sourceReasonCertificateSetInvalid   = "CERTIFICATE_SET_INVALID"
	sourceReasonMetadataContractMissing = "METADATA_CONTRACT_NOT_FOUND"
	sourceReasonDataContractMissing     = "DATA_CONTRACT_NOT_FOUND"
	sourceReasonFSCManagerUnavailable   = "FSC_MANAGER_UNAVAILABLE"
	sourceReasonMetadataFetchFailed     = "METADATA_FETCH_FAILED"
	sourceReasonMetadataInvalid         = "METADATA_INVALID"
	sourceReasonActivationFailed        = "ACTIVATION_FAILED"
)

type sourceReconcileStatus struct {
	SourceID               string    `json:"source_id"`
	State                  string    `json:"state"`
	Reason                 string    `json:"reason,omitempty"`
	Message                string    `json:"message,omitempty"`
	MetadataVersion        string    `json:"metadata_version,omitempty"`
	DeploymentDigest       string    `json:"deployment_digest,omitempty"`
	TransportAuthenticated bool      `json:"transport_authenticated"`
	CheckedAt              time.Time `json:"checked_at"`
}

type sourceStatusWriter interface {
	Write(sourceReconcileStatus) error
}

type filesystemSourceStatusWriter struct{ directory string }

func newFilesystemSourceStatusWriter(stateDir string) *filesystemSourceStatusWriter {
	return &filesystemSourceStatusWriter{directory: filepath.Join(stateDir, "status")}
}

func (w *filesystemSourceStatusWriter) Write(status sourceReconcileStatus) error {
	if w == nil || w.directory == "" {
		return fmt.Errorf("source status directory is required")
	}
	if !sourceIDPattern.MatchString(status.SourceID) {
		return fmt.Errorf("source status has invalid source_id %q", status.SourceID)
	}
	if err := os.MkdirAll(w.directory, 0o755); err != nil {
		return fmt.Errorf("create source status directory: %w", err)
	}
	body, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal source status: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomically(w.directory, status.SourceID+".json", body, 0o644); err != nil {
		return fmt.Errorf("write source status: %w", err)
	}
	return nil
}
