package main

import "time"

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
