package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

type fscContractsResponse struct {
	Contracts  []fscContract `json:"contracts"`
	Pagination struct {
		NextCursor string `json:"next_cursor"`
	} `json:"pagination"`
}

type fscContract struct {
	State   string             `json:"state"`
	Content fscContractContent `json:"content"`
}

type fscContractContent struct {
	CreatedAt int64               `json:"created_at"`
	Validity  fscContractValidity `json:"validity"`
	Grants    []fscContractGrant  `json:"grants"`
}

type fscContractValidity struct {
	NotBefore int64 `json:"not_before"`
	NotAfter  int64 `json:"not_after"`
}

type fscContractGrant struct {
	Type    string             `json:"type"`
	Hash    string             `json:"hash"`
	Outway  fscContractOutway  `json:"outway"`
	Service fscContractService `json:"service"`
}

type fscContractOutway struct {
	PeerID string `json:"peer_id"`
}

type fscContractService struct {
	PeerID string `json:"peer_id"`
	Name   string `json:"name"`
}

type fscConnectionGrant struct {
	ProviderOIN string
	ServiceName string
	GrantHash   string
	CreatedAt   int64
}

type fscContractSnapshot struct {
	byService map[string]fscConnectionGrant
}

func newFSCManagerHTTPClient(caPath, certPath, keyPath string) (*http.Client, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read FSC Manager CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("FSC Manager CA contains no certificate")
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load FSC Manager client certificate: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{ //nolint:gosec // TLS 1.2 is required by the FSC deployment profile.
		MinVersion:   tls.VersionTLS12,
		RootCAs:      pool,
		Certificates: []tls.Certificate{certificate},
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport}, nil
}

func loadFSCContractSnapshot(ctx context.Context, client *http.Client, managerURL, consumerOIN string, now time.Time) (*fscContractSnapshot, error) {
	if client == nil {
		return nil, fmt.Errorf("FSC Manager HTTP client is required")
	}
	parsed, err := url.Parse(managerURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("FSC Manager URL must be an absolute HTTPS URL without credentials, query or fragment")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + "/v1/contracts"
	parsed.RawQuery = ""
	var contracts []fscContract
	cursor := ""
	seenCursors := make(map[string]struct{})
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		payload, err := loadFSCContractsPage(ctx, client, *parsed, cursor)
		if err != nil {
			return nil, err
		}
		contracts = append(contracts, payload.Contracts...)
		cursor = payload.Pagination.NextCursor
		if cursor == "" {
			break
		}
		if _, duplicate := seenCursors[cursor]; duplicate {
			return nil, fmt.Errorf("list FSC contracts returned a repeated pagination cursor")
		}
		seenCursors[cursor] = struct{}{}
		if pageNumber == 99 {
			return nil, fmt.Errorf("list FSC contracts exceeded 100 pages")
		}
	}
	snapshot := &fscContractSnapshot{byService: make(map[string]fscConnectionGrant)}
	unixNow := now.Unix()
	for _, contract := range contracts {
		if contract.State != "CONTRACT_STATE_VALID" || unixNow < contract.Content.Validity.NotBefore || unixNow >= contract.Content.Validity.NotAfter {
			continue
		}
		for _, grant := range contract.Content.Grants {
			if grant.Type != "GRANT_TYPE_SERVICE_CONNECTION" || grant.Outway.PeerID != consumerOIN || !sourceOINPattern.MatchString(grant.Service.PeerID) || !serviceReferencePattern.MatchString(grant.Service.Name) || grant.Hash == "" {
				continue
			}
			candidate := fscConnectionGrant{ProviderOIN: grant.Service.PeerID, ServiceName: grant.Service.Name, GrantHash: grant.Hash, CreatedAt: contract.Content.CreatedAt}
			key := fscServiceKey(candidate.ProviderOIN, candidate.ServiceName)
			current, exists := snapshot.byService[key]
			if !exists || candidate.CreatedAt > current.CreatedAt || (candidate.CreatedAt == current.CreatedAt && candidate.GrantHash > current.GrantHash) {
				snapshot.byService[key] = candidate
			}
		}
	}
	return snapshot, nil
}

func loadFSCContractsPage(ctx context.Context, client *http.Client, endpoint url.URL, cursor string) (fscContractsResponse, error) {
	query := endpoint.Query()
	query.Set("grant_type", "GRANT_TYPE_SERVICE_CONNECTION")
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fscContractsResponse{}, fmt.Errorf("create FSC contract request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fscContractsResponse{}, fmt.Errorf("list FSC contracts: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fscContractsResponse{}, fmt.Errorf("list FSC contracts returned status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload fscContractsResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&payload); err != nil {
		return fscContractsResponse{}, fmt.Errorf("decode FSC contracts: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fscContractsResponse{}, fmt.Errorf("decode FSC contracts: trailing JSON data")
	}
	return payload, nil
}

func fscServiceKey(providerOIN, serviceName string) string {
	return providerOIN + "\x00" + serviceName
}

func (s *fscContractSnapshot) service(providerOIN, serviceName string) (fscConnectionGrant, bool) {
	if s == nil {
		return fscConnectionGrant{}, false
	}
	grant, ok := s.byService[fscServiceKey(providerOIN, serviceName)]
	return grant, ok
}

type sourceReconciler struct {
	managerClient *http.Client
	sourceClient  *http.Client
	managerURL    string
	consumerOIN   string
	outwayURL     string
	schemaPath    string
	publicBaseURL string
	sources       []sourceConfiguration
	store         certificateStore
	backend       activationBackend
	statuses      sourceStatusWriter
}

func (r *sourceReconciler) Reconcile(ctx context.Context, now time.Time) error {
	if r == nil || r.sourceClient == nil || r.store == nil || r.backend == nil {
		return fmt.Errorf("FSC source reconciler is incomplete")
	}
	var reconcileErrors []error
	type readySource struct {
		configuration sourceConfiguration
		certificates  certificateArtifacts
	}
	ready := make([]readySource, 0, len(r.sources))
	hasFSCSource := false
	for _, configuration := range r.sources {
		certificates, err := r.store.Load(configuration.registration())
		if err != nil {
			reason := sourceReasonCertificateSetInvalid
			if errors.Is(err, os.ErrNotExist) {
				reason = sourceReasonCertificateSetNotFound
			}
			message := fmt.Sprintf("certificate set %q is unavailable: %v", configuration.CertificateSet, err)
			reconcileErrors = append(reconcileErrors, r.blocked(configuration, reason, message, now))
			continue
		}
		ready = append(ready, readySource{configuration: configuration, certificates: certificates})
		hasFSCSource = hasFSCSource || configuration.MetadataEndpoint.Transport == sourceTransportFSC
		if err := r.writeStatus(sourceReconcileStatus{
			SourceID: configuration.SourceID, State: sourceStatePending,
			TransportAuthenticated: configuration.MetadataEndpoint.Transport == sourceTransportFSC, CheckedAt: now.UTC(),
		}); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("source %s: record pending status: %w", configuration.SourceID, err))
		}
	}
	if len(ready) == 0 {
		return errors.Join(reconcileErrors...)
	}
	var snapshot *fscContractSnapshot
	if hasFSCSource {
		var err error
		snapshot, err = loadFSCContractSnapshot(ctx, r.managerClient, r.managerURL, r.consumerOIN, now)
		if err != nil {
			for _, source := range ready {
				if source.configuration.MetadataEndpoint.Transport == sourceTransportFSC {
					existing, candidateErr := r.currentCandidate(source.configuration.SourceID)
					if candidateErr != nil {
						reconcileErrors = append(reconcileErrors, r.blocked(source.configuration, sourceReasonActivationFailed, candidateErr.Error(), now))
						continue
					}
					reconcileErrors = append(reconcileErrors, r.metadataUnavailable(source.configuration, existing, sourceReasonFSCManagerUnavailable, err.Error(), now))
				}
			}
			ready = slices.DeleteFunc(ready, func(source readySource) bool {
				return source.configuration.MetadataEndpoint.Transport == sourceTransportFSC
			})
		}
	}
	for _, source := range ready {
		if err := r.reconcileSource(ctx, snapshot, source.configuration, source.certificates, now); err != nil {
			reconcileErrors = append(reconcileErrors, err)
		}
	}
	return errors.Join(reconcileErrors...)
}

func (r *sourceReconciler) reconcileSource(ctx context.Context, snapshot *fscContractSnapshot, configuration sourceConfiguration, certificates certificateArtifacts, now time.Time) error {
	if configuration.MetadataEndpoint.Transport == sourceTransportUnsecured {
		return r.reconcileUnsecuredSource(ctx, configuration, certificates, now)
	}
	metadataGrant, ok := snapshot.service(configuration.SourceOIN, configuration.MetadataEndpoint.ServiceReference)
	if !ok {
		return r.blocked(configuration, sourceReasonMetadataContractMissing, fmt.Sprintf("no valid FSC metadata contract for service %q", configuration.MetadataEndpoint.ServiceReference), now)
	}
	metadataURL := strings.TrimRight(r.outwayURL, "/") + configuration.MetadataEndpoint.Path
	existing, err := r.currentCandidate(configuration.SourceID)
	if err != nil {
		return r.blocked(configuration, sourceReasonActivationFailed, err.Error(), now)
	}
	etag := ""
	if existing != nil {
		etag = existing.MetadataETag
	}
	fetched, err := fetchSourceMetadataCandidate(ctx, r.sourceClient, metadataURL, sourceTransportFSC, metadataGrant.GrantHash, etag)
	if err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataFetchFailed, err.Error(), now)
	}
	if fetched.NotModified {
		if existing == nil {
			return r.metadataUnavailable(configuration, nil, sourceReasonMetadataInvalid, "source returned not-modified without an existing candidate", now)
		}
		dataService := existing.Source.DataAccess.ServiceReference
		dataGrant, ok := snapshot.service(configuration.SourceOIN, dataService)
		if !ok {
			return r.metadataUnavailable(configuration, existing, sourceReasonDataContractMissing, fmt.Sprintf("no valid FSC data contract for service %q", dataService), now)
		}
		registration := sourceRegistration{
			SourceID: configuration.SourceID, SourceOIN: configuration.SourceOIN,
			Name: configuration.Name, CertificateSet: configuration.CertificateSet,
			MetadataEndpoint: sourceMetadataEndpoint{
				Transport: sourceTransportFSC, ServiceReference: configuration.MetadataEndpoint.ServiceReference,
				Path: configuration.MetadataEndpoint.Path, GrantHash: metadataGrant.GrantHash,
			},
			DataAccess: sourceDataAccess{
				Transport: sourceTransportFSC, ServiceReference: dataService, GrantHash: dataGrant.GrantHash,
			},
		}
		if err := registration.validate(); err != nil {
			return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
		}
		return r.refreshCandidate(configuration, existing, registration, metadataURL, certificates, true, now)
	}
	payload := fetched.Payload
	if err := validateSourceMetadataSchema(payload, r.schemaPath); err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	document, err := decodeSourceMetadataDocument(payload)
	if err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	if document.SourceOIN != configuration.SourceOIN {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, fmt.Sprintf("source metadata OIN %q does not match configured FSC provider OIN %q", document.SourceOIN, configuration.SourceOIN), now)
	}
	attestations := document.eudiAttestations()
	if len(attestations) == 0 {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, "source metadata has no EUDI attestations", now)
	}
	dataService := attestations[0].GraphQL.ServiceReference
	for _, definition := range attestations[1:] {
		if definition.GraphQL.ServiceReference != dataService {
			return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, "one metadata document currently must use one FSC data service", now)
		}
	}
	dataGrant, ok := snapshot.service(configuration.SourceOIN, dataService)
	if !ok {
		return r.metadataUnavailable(configuration, existing, sourceReasonDataContractMissing, fmt.Sprintf("no valid FSC data contract for service %q", dataService), now)
	}
	registration := sourceRegistration{
		SourceID: configuration.SourceID, SourceOIN: configuration.SourceOIN,
		Name: configuration.Name, CertificateSet: configuration.CertificateSet,
		MetadataEndpoint: sourceMetadataEndpoint{
			Transport: sourceTransportFSC, ServiceReference: configuration.MetadataEndpoint.ServiceReference,
			Path: configuration.MetadataEndpoint.Path, GrantHash: metadataGrant.GrantHash,
		},
		DataAccess: sourceDataAccess{
			Transport: sourceTransportFSC, ServiceReference: dataService, GrantHash: dataGrant.GrantHash,
		},
	}
	if err := registration.validate(); err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	validated, err := validateSourcePayload(registration, payload, metadataURL, r.schemaPath, r.publicBaseURL, now)
	if err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	validated.MetadataETag = fetched.ETag
	return r.activate(configuration, validated, certificates, true, now)
}

func (r *sourceReconciler) reconcileUnsecuredSource(ctx context.Context, configuration sourceConfiguration, certificates certificateArtifacts, now time.Time) error {
	existing, err := r.currentCandidate(configuration.SourceID)
	if err != nil {
		return r.blocked(configuration, sourceReasonActivationFailed, err.Error(), now)
	}
	etag := ""
	if existing != nil {
		etag = existing.MetadataETag
	}
	fetched, err := fetchSourceMetadataCandidate(ctx, r.sourceClient, configuration.MetadataEndpoint.Endpoint, sourceTransportUnsecured, "", etag)
	if err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataFetchFailed, err.Error(), now)
	}
	if fetched.NotModified {
		if existing == nil {
			return r.metadataUnavailable(configuration, nil, sourceReasonMetadataInvalid, "source returned not-modified without an existing candidate", now)
		}
		registration := configuration.registration()
		registration.MetadataEndpoint = configuration.MetadataEndpoint
		registration.DataAccess = configuration.DataAccess
		return r.refreshCandidate(configuration, existing, registration, configuration.MetadataEndpoint.Endpoint, certificates, false, now)
	}
	payload := fetched.Payload
	registration := configuration.registration()
	registration.MetadataEndpoint = configuration.MetadataEndpoint
	registration.DataAccess = configuration.DataAccess
	if err := registration.validate(); err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	validated, err := validateSourcePayload(registration, payload, configuration.MetadataEndpoint.Endpoint, r.schemaPath, r.publicBaseURL, now)
	if err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	validated.MetadataETag = fetched.ETag
	return r.activate(configuration, validated, certificates, false, now)
}

func (r *sourceReconciler) activate(configuration sourceConfiguration, validated *validatedSourceRegistration, certificates certificateArtifacts, transportAuthenticated bool, now time.Time) error {
	activation, err := r.backend.Activate(validated, certificates)
	if err != nil {
		return r.blocked(configuration, sourceReasonActivationFailed, fmt.Sprintf("activate source: %v", err), now)
	}
	state := sourceStateActive
	digest, digestErr := activationDeploymentDigest(activation)
	if digestErr != nil {
		return r.blocked(configuration, sourceReasonActivationFailed, digestErr.Error(), now)
	}
	if lifecycle, ok := r.backend.(activationLifecycleBackend); ok {
		rolloutRequired, err := lifecycle.RolloutRequired(activation)
		if err != nil {
			return r.blocked(configuration, sourceReasonActivationFailed, err.Error(), now)
		}
		if rolloutRequired {
			state = sourceStateRolloutRequired
		}
	}
	if err := r.writeStatus(sourceReconcileStatus{
		SourceID: configuration.SourceID, State: state, DeploymentDigest: digest,
		MetadataVersion: activation.MetadataVersion, TransportAuthenticated: transportAuthenticated, CheckedAt: now.UTC(),
	}); err != nil {
		return fmt.Errorf("source %s: record %s status: %w", configuration.SourceID, state, err)
	}
	return nil
}

func (r *sourceReconciler) currentCandidate(sourceID string) (*sourceActivation, error) {
	lifecycle, ok := r.backend.(activationLifecycleBackend)
	if !ok {
		return nil, nil
	}
	activation, err := lifecycle.CurrentCandidate(sourceID)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return activation, err
}

func (r *sourceReconciler) refreshCandidate(configuration sourceConfiguration, existing *sourceActivation, source sourceRegistration, metadataURL string, certificates certificateArtifacts, transportAuthenticated bool, now time.Time) error {
	lifecycle, ok := r.backend.(activationLifecycleBackend)
	if !ok {
		return r.blocked(configuration, sourceReasonActivationFailed, "activation backend cannot refresh a not-modified source", now)
	}
	activation, err := lifecycle.RefreshCandidate(configuration.SourceID, source, metadataURL, certificates, transportAuthenticated, now)
	if err != nil {
		return r.metadataUnavailable(configuration, existing, sourceReasonMetadataInvalid, err.Error(), now)
	}
	state := sourceStateActive
	rolloutRequired, err := lifecycle.RolloutRequired(activation)
	if err != nil {
		return r.blocked(configuration, sourceReasonActivationFailed, err.Error(), now)
	}
	if rolloutRequired {
		state = sourceStateRolloutRequired
	}
	digest, err := activationDeploymentDigest(activation)
	if err != nil {
		return r.blocked(configuration, sourceReasonActivationFailed, err.Error(), now)
	}
	return r.writeStatus(sourceReconcileStatus{
		SourceID: configuration.SourceID, State: state, DeploymentDigest: digest,
		MetadataVersion:        activation.MetadataVersion,
		TransportAuthenticated: configuration.MetadataEndpoint.Transport == sourceTransportFSC, CheckedAt: now.UTC(),
	})
}

func (r *sourceReconciler) metadataUnavailable(configuration sourceConfiguration, existing *sourceActivation, reason, message string, now time.Time) error {
	if existing != nil && !existing.StaleUntil.IsZero() && !now.After(existing.StaleUntil) {
		digest, _ := activationDeploymentDigest(existing)
		statusErr := r.writeStatus(sourceReconcileStatus{
			SourceID: configuration.SourceID, State: sourceStateStale, Reason: reason, Message: message,
			MetadataVersion: existing.MetadataVersion, DeploymentDigest: digest,
			TransportAuthenticated: configuration.MetadataEndpoint.Transport == sourceTransportFSC, CheckedAt: now.UTC(),
		})
		staleErr := fmt.Errorf("source %s stale (%s): %s", configuration.SourceID, reason, message)
		if statusErr != nil {
			return errors.Join(staleErr, statusErr)
		}
		return staleErr
	}
	return r.blocked(configuration, reason, message, now)
}

func (r *sourceReconciler) blocked(configuration sourceConfiguration, reason, message string, now time.Time) error {
	statusErr := r.writeStatus(sourceReconcileStatus{
		SourceID: configuration.SourceID, State: sourceStateBlocked, Reason: reason,
		Message: message, TransportAuthenticated: configuration.MetadataEndpoint.Transport == sourceTransportFSC, CheckedAt: now.UTC(),
	})
	blockedErr := fmt.Errorf("source %s blocked (%s): %s", configuration.SourceID, reason, message)
	if statusErr != nil {
		return errors.Join(blockedErr, fmt.Errorf("record blocked status: %w", statusErr))
	}
	return blockedErr
}

func (r *sourceReconciler) writeStatus(status sourceReconcileStatus) error {
	if r.statuses == nil {
		return nil
	}
	return r.statuses.Write(status)
}
