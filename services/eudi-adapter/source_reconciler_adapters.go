package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

// Reconcile is the package-main composition seam for the onboarding use case.
// Protocol and persistence details stay in adapters below; all source-level
// decisions are made by onboarding.Service.
func (r *sourceReconciler) Reconcile(ctx context.Context, now time.Time) error {
	if r == nil || r.sourceClient == nil || r.store == nil || r.backend == nil {
		return fmt.Errorf("source reconciler is incomplete")
	}
	service, err := onboarding.New(onboarding.Options{OutwayURL: r.outwayURL}, onboarding.Dependencies[certificateArtifacts, *validatedSourceRegistration, *sourceActivation]{
		Sources:      sourceCatalogAdapter{sources: r.sources},
		Contracts:    contractCatalogAdapter{reconciler: r},
		Metadata:     metadataClientAdapter{reconciler: r},
		Compiler:     metadataCompilerAdapter{schemaPath: r.schemaPath, publicBaseURL: r.publicBaseURL},
		Certificates: certificateStoreAdapter{store: r.store},
		Activations:  activationRepositoryAdapter{backend: r.backend},
		Statuses:     statusRepositoryAdapter{writer: r.statuses},
	})
	if err != nil {
		return err
	}
	report, err := service.Reconcile(ctx, now)
	return errors.Join(err, report.Err())
}

type sourceCatalogAdapter struct {
	sources []sourceConfiguration
}

func (a sourceCatalogAdapter) List(context.Context) ([]onboarding.Source, error) {
	sources := make([]onboarding.Source, 0, len(a.sources))
	for _, source := range a.sources {
		sources = append(sources, onboarding.Source{
			ID: source.SourceID, ProviderPeerID: source.MetadataEndpoint.ProviderPeerID,
			MetadataEndpoint: onboarding.MetadataEndpoint{
				Transport: onboarding.Transport(source.MetadataEndpoint.Transport), ServiceReference: source.MetadataEndpoint.ServiceReference,
				Path: sourceMetadataWellKnownPath, Endpoint: source.MetadataEndpoint.Endpoint,
			},
			DataAccessTransport: onboarding.Transport(source.MetadataEndpoint.Transport),
		})
	}
	return sources, nil
}

type contractCatalogAdapter struct {
	reconciler *sourceReconciler
}

func (a contractCatalogAdapter) Snapshot(ctx context.Context, now time.Time) (onboarding.ContractSnapshot, error) {
	snapshot, err := loadFSCContractSnapshot(ctx, a.reconciler.managerClient, a.reconciler.managerURL, a.reconciler.consumerPeerID, now)
	if err != nil {
		return nil, err
	}
	return contractSnapshotAdapter{snapshot: snapshot}, nil
}

type contractSnapshotAdapter struct {
	snapshot *fscContractSnapshot
}

func (a contractSnapshotAdapter) Grant(providerPeerID, serviceReference string) (onboarding.Grant, bool) {
	grant, ok := a.snapshot.service(providerPeerID, serviceReference)
	if !ok {
		return onboarding.Grant{}, false
	}
	return onboarding.Grant{ProviderPeerID: grant.ProviderPeerID, ServiceReference: grant.ServiceName, Hash: grant.GrantHash}, true
}

type metadataClientAdapter struct {
	reconciler *sourceReconciler
}

func (a metadataClientAdapter) Fetch(ctx context.Context, request onboarding.MetadataRequest) (onboarding.MetadataResponse, error) {
	fetched, err := fetchSourceMetadataCandidate(ctx, a.reconciler.sourceClient, request.URL, string(request.Transport), request.GrantHash, request.ETag)
	if err != nil {
		return onboarding.MetadataResponse{}, err
	}
	return onboarding.MetadataResponse{Payload: fetched.Payload, ETag: fetched.ETag, NotModified: fetched.NotModified}, nil
}

type metadataCompilerAdapter struct {
	schemaPath    string
	publicBaseURL string
}

func (a metadataCompilerAdapter) Describe(payload []byte) (onboarding.MetadataDescription, error) {
	if err := validateSourceMetadataSchema(payload, a.schemaPath); err != nil {
		return onboarding.MetadataDescription{}, err
	}
	document, err := decodeSourceMetadataDocument(payload)
	if err != nil {
		return onboarding.MetadataDescription{}, err
	}
	attestations := document.eudiAttestations()
	if len(attestations) == 0 {
		return onboarding.MetadataDescription{}, fmt.Errorf("source metadata has no EUDI attestations")
	}
	dataService := attestations[0].GraphQL.ServiceReference
	for _, definition := range attestations[1:] {
		if definition.GraphQL.ServiceReference != dataService {
			return onboarding.MetadataDescription{}, fmt.Errorf("one metadata document currently must use one FSC data service")
		}
	}
	return onboarding.MetadataDescription{SourceOIN: document.SourceOIN, DataServiceReference: dataService}, nil
}

func (a metadataCompilerAdapter) Compile(request onboarding.CompileRequest) (*validatedSourceRegistration, error) {
	registration := registrationFromResolvedSource(request.Source)
	if err := registration.validate(); err != nil {
		return nil, err
	}
	validated, err := validateSourcePayload(registration, request.Payload, request.Source.MetadataURL, a.schemaPath, a.publicBaseURL, request.CheckedAt)
	if err != nil {
		return nil, err
	}
	validated.MetadataETag = request.ETag
	return validated, nil
}

type certificateStoreAdapter struct {
	store certificateStore
}

func (a certificateStoreAdapter) Load(_ context.Context, source onboarding.Source) (onboarding.CertificateSet[certificateArtifacts], error) {
	artifacts, err := a.store.Load(sourceRegistration{
		SourceID: source.ID, ProviderPeerID: source.ProviderPeerID, SourceOIN: source.OIN, Name: source.Name,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return onboarding.CertificateSet[certificateArtifacts]{}, fmt.Errorf("%w: %w", onboarding.ErrCertificateNotFound, err)
	}
	if err != nil {
		return onboarding.CertificateSet[certificateArtifacts]{}, err
	}
	return onboarding.CertificateSet[certificateArtifacts]{
		Artifacts: artifacts,
		SourceOIN: artifacts.sourceOIN,
		Name:      artifacts.sourceName,
	}, nil
}

type activationRepositoryAdapter struct {
	backend activationBackend
}

func (a activationRepositoryAdapter) Current(_ context.Context, sourceID string) (*sourceActivation, bool, error) {
	lifecycle, ok := a.backend.(activationLifecycleBackend)
	if !ok {
		return nil, false, nil
	}
	activation, err := lifecycle.CurrentCandidate(sourceID)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return activation, activation != nil, err
}

func (a activationRepositoryAdapter) Activate(_ context.Context, prepared *validatedSourceRegistration, certificates certificateArtifacts) (*sourceActivation, error) {
	return a.backend.Activate(prepared, certificates)
}

func (a activationRepositoryAdapter) Refresh(_ context.Context, existing *sourceActivation, request onboarding.RefreshRequest, certificates certificateArtifacts) (*sourceActivation, error) {
	lifecycle, ok := a.backend.(activationLifecycleBackend)
	if !ok {
		return nil, fmt.Errorf("activation backend cannot refresh a not-modified source")
	}
	registration := registrationFromResolvedSource(request.Source)
	return lifecycle.RefreshCandidate(existing.Source.SourceID, registration, request.Source.MetadataURL, certificates, request.Source.TransportAuthenticated, request.CheckedAt)
}

func (a activationRepositoryAdapter) Info(activation *sourceActivation) (onboarding.CandidateInfo, error) {
	if activation == nil {
		return onboarding.CandidateInfo{}, fmt.Errorf("source activation is required")
	}
	digest, err := activationDeploymentDigest(activation)
	if err != nil {
		return onboarding.CandidateInfo{}, err
	}
	return onboarding.CandidateInfo{
		SourceID: activation.Source.SourceID, MetadataETag: activation.MetadataETag,
		MetadataVersion: activation.MetadataVersion, DataServiceReference: activation.Source.DataAccess.ServiceReference,
		StaleUntil: activation.StaleUntil, DeploymentDigest: digest,
	}, nil
}

func (a activationRepositoryAdapter) RolloutRequired(_ context.Context, activation *sourceActivation) (bool, error) {
	lifecycle, ok := a.backend.(activationLifecycleBackend)
	if !ok {
		return false, nil
	}
	return lifecycle.RolloutRequired(activation)
}

type statusRepositoryAdapter struct {
	writer sourceStatusWriter
}

func (a statusRepositoryAdapter) Put(_ context.Context, status onboarding.Status) error {
	if a.writer == nil {
		return nil
	}
	return a.writer.Write(sourceReconcileStatus{
		SourceID: status.SourceID, State: string(status.State), Reason: string(status.Reason), Message: status.Message,
		MetadataVersion: status.MetadataVersion, DeploymentDigest: status.DeploymentDigest,
		TransportAuthenticated: status.TransportAuthenticated, CheckedAt: status.CheckedAt,
	})
}

func registrationFromResolvedSource(resolved onboarding.ResolvedSource) sourceRegistration {
	source := resolved.Source
	registration := sourceRegistration{
		SourceID: source.ID, ProviderPeerID: source.ProviderPeerID, SourceOIN: source.OIN, Name: source.Name,
		MetadataEndpoint: sourceMetadataEndpoint{Transport: string(source.MetadataEndpoint.Transport)},
		DataAccess:       sourceDataAccess{Transport: string(source.DataAccessTransport)},
	}
	if source.MetadataEndpoint.Transport == onboarding.TransportFSC {
		registration.MetadataEndpoint.ServiceReference = source.MetadataEndpoint.ServiceReference
		registration.MetadataEndpoint.Path = source.MetadataEndpoint.Path
		registration.MetadataEndpoint.GrantHash = resolved.MetadataGrantHash
		registration.DataAccess.ServiceReference = resolved.DataServiceReference
		registration.DataAccess.GrantHash = resolved.DataGrantHash
	} else {
		registration.MetadataEndpoint.Endpoint = source.MetadataEndpoint.Endpoint
	}
	return registration
}
