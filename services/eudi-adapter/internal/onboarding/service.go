// Package onboarding contains the source-onboarding application use case.
//
// It deliberately knows nothing about HTTP, FSC protocol messages, files,
// certificates or EUDI metadata internals. Those concerns are supplied through
// consumer-owned ports. The type parameters keep adapter-owned values strongly
// typed without leaking their implementation into this package.
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

type Transport string

const (
	TransportFSC       Transport = "fsc"
	TransportUnsecured Transport = "unsecured"
)

type Source struct {
	ID                  string
	OIN                 string
	Name                string
	CertificateSet      string
	MetadataEndpoint    MetadataEndpoint
	DataAccessTransport Transport
}

type MetadataEndpoint struct {
	Transport        Transport
	ServiceReference string
	Path             string
	Endpoint         string
}

func (s Source) TransportAuthenticated() bool {
	return s.MetadataEndpoint.Transport == TransportFSC
}

type Grant struct {
	ProviderOIN      string
	ServiceReference string
	Hash             string
}

type ResolvedSource struct {
	Source                 Source
	MetadataURL            string
	MetadataGrantHash      string
	DataServiceReference   string
	DataGrantHash          string
	TransportAuthenticated bool
}

type MetadataRequest struct {
	URL       string
	Transport Transport
	GrantHash string
	ETag      string
}

type MetadataResponse struct {
	Payload     []byte
	ETag        string
	NotModified bool
}

type MetadataDescription struct {
	SourceOIN            string
	DataServiceReference string
}

type CompileRequest struct {
	Source    ResolvedSource
	Payload   []byte
	ETag      string
	CheckedAt time.Time
}

type RefreshRequest struct {
	Source    ResolvedSource
	CheckedAt time.Time
}

type CandidateInfo struct {
	SourceID             string
	MetadataETag         string
	MetadataVersion      string
	DataServiceReference string
	StaleUntil           time.Time
	DeploymentDigest     string
}

type State string

const (
	StatePending         State = "pending"
	StateActive          State = "active"
	StateStale           State = "stale"
	StateBlocked         State = "blocked"
	StateRolloutRequired State = "rollout_required"
)

type Reason string

const (
	ReasonCertificateSetNotFound  Reason = "CERTIFICATE_SET_NOT_FOUND"
	ReasonCertificateSetInvalid   Reason = "CERTIFICATE_SET_INVALID"
	ReasonMetadataContractMissing Reason = "METADATA_CONTRACT_NOT_FOUND"
	ReasonDataContractMissing     Reason = "DATA_CONTRACT_NOT_FOUND"
	ReasonFSCManagerUnavailable   Reason = "FSC_MANAGER_UNAVAILABLE"
	ReasonMetadataFetchFailed     Reason = "METADATA_FETCH_FAILED"
	ReasonMetadataInvalid         Reason = "METADATA_INVALID"
	ReasonActivationFailed        Reason = "ACTIVATION_FAILED"
)

type Status struct {
	SourceID               string
	State                  State
	Reason                 Reason
	Message                string
	MetadataVersion        string
	DeploymentDigest       string
	TransportAuthenticated bool
	CheckedAt              time.Time
}

type Result struct {
	SourceID string
	Status   Status
	Err      error
}

type Report struct {
	CheckedAt time.Time
	Results   []Result
}

func (r Report) Err() error {
	errs := make([]error, 0, len(r.Results))
	for _, result := range r.Results {
		if result.Err != nil {
			errs = append(errs, result.Err)
		}
	}
	return errors.Join(errs...)
}

// SourceCatalog is the operator-managed desired state as seen by onboarding.
type SourceCatalog interface {
	List(context.Context) ([]Source, error)
}

// ContractCatalog hides FSC Manager transport, pagination and response DTOs.
// Implementations return one immutable snapshot for a reconciliation cycle.
type ContractCatalog interface {
	Snapshot(context.Context, time.Time) (ContractSnapshot, error)
}

type ContractSnapshot interface {
	Grant(providerOIN, serviceReference string) (Grant, bool)
}

// MetadataClient hides both FSC Outway and unsecured HTTP request mechanics.
type MetadataClient interface {
	Fetch(context.Context, MetadataRequest) (MetadataResponse, error)
}

// MetadataCompiler owns capability-specific schema and semantic validation.
// P is an adapter-owned, strongly typed compiled metadata value.
type MetadataCompiler[P any] interface {
	Describe(payload []byte) (MetadataDescription, error)
	Compile(CompileRequest) (P, error)
}

type CertificateStore[C any] interface {
	Load(context.Context, Source) (C, error)
}

// ActivationRepository owns persistence and artifact materialisation.
// A is an adapter-owned, strongly typed activation snapshot.
type ActivationRepository[C, P, A any] interface {
	Current(context.Context, string) (A, bool, error)
	Activate(context.Context, P, C) (A, error)
	Refresh(context.Context, A, RefreshRequest, C) (A, error)
	Info(A) (CandidateInfo, error)
	RolloutRequired(context.Context, A) (bool, error)
}

type StatusRepository interface {
	Put(context.Context, Status) error
}

type Dependencies[C, P, A any] struct {
	Sources      SourceCatalog
	Contracts    ContractCatalog
	Metadata     MetadataClient
	Compiler     MetadataCompiler[P]
	Certificates CertificateStore[C]
	Activations  ActivationRepository[C, P, A]
	Statuses     StatusRepository
}

type Options struct {
	OutwayURL string
}

type Service[C, P, A any] struct {
	options Options
	ports   Dependencies[C, P, A]
}

func New[C, P, A any](options Options, ports Dependencies[C, P, A]) (*Service[C, P, A], error) {
	if ports.Sources == nil || ports.Metadata == nil || ports.Compiler == nil || ports.Certificates == nil || ports.Activations == nil {
		return nil, fmt.Errorf("onboarding dependencies are incomplete")
	}
	return &Service[C, P, A]{options: options, ports: ports}, nil
}

// Reconcile evaluates every configured source independently. A source failure
// is recorded in the report and never prevents another source from progressing.
func (s *Service[C, P, A]) Reconcile(ctx context.Context, at time.Time) (Report, error) {
	sources, err := s.ports.Sources.List(ctx)
	if err != nil {
		return Report{CheckedAt: at.UTC()}, fmt.Errorf("load source catalog: %w", err)
	}
	report := Report{CheckedAt: at.UTC(), Results: make([]Result, 0, len(sources))}
	type readySource struct {
		source       Source
		certificates C
	}
	ready := make([]readySource, 0, len(sources))
	hasFSC := false
	for _, source := range sources {
		certificates, loadErr := s.ports.Certificates.Load(ctx, source)
		if loadErr != nil {
			reason := ReasonCertificateSetInvalid
			if errors.Is(loadErr, fs.ErrNotExist) {
				reason = ReasonCertificateSetNotFound
			}
			message := fmt.Sprintf("certificate set %q is unavailable: %v", source.CertificateSet, loadErr)
			report.Results = append(report.Results, s.blocked(ctx, source, reason, message, at))
			continue
		}
		ready = append(ready, readySource{source: source, certificates: certificates})
		hasFSC = hasFSC || source.MetadataEndpoint.Transport == TransportFSC
		status := Status{SourceID: source.ID, State: StatePending, TransportAuthenticated: source.TransportAuthenticated(), CheckedAt: at.UTC()}
		if statusErr := s.putStatus(ctx, status); statusErr != nil {
			report.Results = append(report.Results, Result{SourceID: source.ID, Status: status, Err: fmt.Errorf("source %s: record pending status: %w", source.ID, statusErr)})
		}
	}

	var snapshot ContractSnapshot
	if hasFSC {
		if s.ports.Contracts == nil {
			err = fmt.Errorf("FSC contract catalog is required for configured FSC sources")
		} else {
			snapshot, err = s.ports.Contracts.Snapshot(ctx, at)
		}
		if err != nil {
			remaining := ready[:0]
			for _, item := range ready {
				if item.source.MetadataEndpoint.Transport != TransportFSC {
					remaining = append(remaining, item)
					continue
				}
				report.Results = append(report.Results, s.unavailable(ctx, item.source, ReasonFSCManagerUnavailable, err.Error(), at))
			}
			ready = remaining
		}
	}

	for _, item := range ready {
		result := s.reconcileSource(ctx, snapshot, item.source, item.certificates, at)
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func (s *Service[C, P, A]) reconcileSource(ctx context.Context, snapshot ContractSnapshot, source Source, certificates C, at time.Time) Result {
	metadataURL := source.MetadataEndpoint.Endpoint
	metadataGrant := Grant{}
	if source.MetadataEndpoint.Transport == TransportFSC {
		var ok bool
		metadataGrant, ok = snapshot.Grant(source.OIN, source.MetadataEndpoint.ServiceReference)
		if !ok {
			return s.blocked(ctx, source, ReasonMetadataContractMissing, fmt.Sprintf("no valid FSC metadata contract for service %q", source.MetadataEndpoint.ServiceReference), at)
		}
		metadataURL = strings.TrimRight(s.options.OutwayURL, "/") + source.MetadataEndpoint.Path
	}

	existing, exists, err := s.ports.Activations.Current(ctx, source.ID)
	if err != nil {
		return s.blocked(ctx, source, ReasonActivationFailed, err.Error(), at)
	}
	info := CandidateInfo{}
	if exists {
		info, err = s.ports.Activations.Info(existing)
		if err != nil {
			return s.blocked(ctx, source, ReasonActivationFailed, err.Error(), at)
		}
	}
	fetched, err := s.ports.Metadata.Fetch(ctx, MetadataRequest{
		URL: metadataURL, Transport: source.MetadataEndpoint.Transport,
		GrantHash: metadataGrant.Hash, ETag: info.MetadataETag,
	})
	if err != nil {
		return s.unavailableWithCandidate(ctx, source, existing, exists, info, ReasonMetadataFetchFailed, err.Error(), at)
	}

	resolved := ResolvedSource{Source: source, MetadataURL: metadataURL, MetadataGrantHash: metadataGrant.Hash, TransportAuthenticated: source.TransportAuthenticated()}
	if fetched.NotModified {
		if !exists {
			return s.unavailable(ctx, source, ReasonMetadataInvalid, "source returned not-modified without an existing candidate", at)
		}
		resolved.DataServiceReference = info.DataServiceReference
		if source.MetadataEndpoint.Transport == TransportFSC {
			dataGrant, ok := snapshot.Grant(source.OIN, info.DataServiceReference)
			if !ok {
				return s.unavailableWithCandidate(ctx, source, existing, true, info, ReasonDataContractMissing, fmt.Sprintf("no valid FSC data contract for service %q", info.DataServiceReference), at)
			}
			resolved.DataGrantHash = dataGrant.Hash
		}
		refreshed, refreshErr := s.ports.Activations.Refresh(ctx, existing, RefreshRequest{Source: resolved, CheckedAt: at}, certificates)
		if refreshErr != nil {
			return s.unavailableWithCandidate(ctx, source, existing, true, info, ReasonMetadataInvalid, refreshErr.Error(), at)
		}
		return s.activated(ctx, source, refreshed, at)
	}

	description, err := s.ports.Compiler.Describe(fetched.Payload)
	if err != nil {
		return s.unavailableWithCandidate(ctx, source, existing, exists, info, ReasonMetadataInvalid, err.Error(), at)
	}
	if description.SourceOIN != source.OIN {
		return s.unavailableWithCandidate(ctx, source, existing, exists, info, ReasonMetadataInvalid, fmt.Sprintf("source metadata OIN %q does not match configured provider OIN %q", description.SourceOIN, source.OIN), at)
	}
	resolved.DataServiceReference = description.DataServiceReference
	if source.MetadataEndpoint.Transport == TransportFSC {
		dataGrant, ok := snapshot.Grant(source.OIN, description.DataServiceReference)
		if !ok {
			return s.unavailableWithCandidate(ctx, source, existing, exists, info, ReasonDataContractMissing, fmt.Sprintf("no valid FSC data contract for service %q", description.DataServiceReference), at)
		}
		resolved.DataGrantHash = dataGrant.Hash
	}
	prepared, err := s.ports.Compiler.Compile(CompileRequest{Source: resolved, Payload: fetched.Payload, ETag: fetched.ETag, CheckedAt: at})
	if err != nil {
		return s.unavailableWithCandidate(ctx, source, existing, exists, info, ReasonMetadataInvalid, err.Error(), at)
	}
	activated, err := s.ports.Activations.Activate(ctx, prepared, certificates)
	if err != nil {
		return s.blocked(ctx, source, ReasonActivationFailed, fmt.Sprintf("activate source: %v", err), at)
	}
	return s.activated(ctx, source, activated, at)
}

func (s *Service[C, P, A]) activated(ctx context.Context, source Source, activation A, at time.Time) Result {
	info, err := s.ports.Activations.Info(activation)
	if err != nil {
		return s.blocked(ctx, source, ReasonActivationFailed, err.Error(), at)
	}
	rolloutRequired, err := s.ports.Activations.RolloutRequired(ctx, activation)
	if err != nil {
		return s.blocked(ctx, source, ReasonActivationFailed, err.Error(), at)
	}
	state := StateActive
	if rolloutRequired {
		state = StateRolloutRequired
	}
	status := Status{
		SourceID: source.ID, State: state, MetadataVersion: info.MetadataVersion,
		DeploymentDigest: info.DeploymentDigest, TransportAuthenticated: source.TransportAuthenticated(), CheckedAt: at.UTC(),
	}
	statusErr := s.putStatus(ctx, status)
	return Result{SourceID: source.ID, Status: status, Err: statusErr}
}

func (s *Service[C, P, A]) unavailable(ctx context.Context, source Source, reason Reason, message string, at time.Time) Result {
	existing, exists, err := s.ports.Activations.Current(ctx, source.ID)
	if err != nil {
		return s.blocked(ctx, source, ReasonActivationFailed, err.Error(), at)
	}
	info := CandidateInfo{}
	if exists {
		info, err = s.ports.Activations.Info(existing)
		if err != nil {
			return s.blocked(ctx, source, ReasonActivationFailed, err.Error(), at)
		}
	}
	return s.unavailableWithCandidate(ctx, source, existing, exists, info, reason, message, at)
}

func (s *Service[C, P, A]) unavailableWithCandidate(ctx context.Context, source Source, _ A, exists bool, info CandidateInfo, reason Reason, message string, at time.Time) Result {
	if exists && !info.StaleUntil.IsZero() && !at.After(info.StaleUntil) {
		status := Status{
			SourceID: source.ID, State: StateStale, Reason: reason, Message: message,
			MetadataVersion: info.MetadataVersion, DeploymentDigest: info.DeploymentDigest,
			TransportAuthenticated: source.TransportAuthenticated(), CheckedAt: at.UTC(),
		}
		statusErr := s.putStatus(ctx, status)
		staleErr := fmt.Errorf("source %s stale (%s): %s", source.ID, reason, message)
		return Result{SourceID: source.ID, Status: status, Err: errors.Join(staleErr, statusErr)}
	}
	return s.blocked(ctx, source, reason, message, at)
}

func (s *Service[C, P, A]) blocked(ctx context.Context, source Source, reason Reason, message string, at time.Time) Result {
	status := Status{
		SourceID: source.ID, State: StateBlocked, Reason: reason, Message: message,
		TransportAuthenticated: source.TransportAuthenticated(), CheckedAt: at.UTC(),
	}
	statusErr := s.putStatus(ctx, status)
	blockedErr := fmt.Errorf("source %s blocked (%s): %s", source.ID, reason, message)
	if statusErr != nil {
		blockedErr = errors.Join(blockedErr, fmt.Errorf("record blocked status: %w", statusErr))
	}
	return Result{SourceID: source.ID, Status: status, Err: blockedErr}
}

func (s *Service[C, P, A]) putStatus(ctx context.Context, status Status) error {
	if s.ports.Statuses == nil {
		return nil
	}
	return s.ports.Statuses.Put(ctx, status)
}
