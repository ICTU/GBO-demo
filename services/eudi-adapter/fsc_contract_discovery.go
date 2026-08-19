package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	ProviderPeerID string
	ServiceName    string
	GrantHash      string
	CreatedAt      int64
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

func loadFSCContractSnapshot(ctx context.Context, client *http.Client, managerURL, consumerPeerID string, now time.Time) (*fscContractSnapshot, error) {
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
			if grant.Type != "GRANT_TYPE_SERVICE_CONNECTION" || grant.Outway.PeerID != consumerPeerID || !peerIDPattern.MatchString(grant.Service.PeerID) || !serviceReferencePattern.MatchString(grant.Service.Name) || grant.Hash == "" {
				continue
			}
			candidate := fscConnectionGrant{ProviderPeerID: grant.Service.PeerID, ServiceName: grant.Service.Name, GrantHash: grant.Hash, CreatedAt: contract.Content.CreatedAt}
			key := fscServiceKey(candidate.ProviderPeerID, candidate.ServiceName)
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

func fscServiceKey(providerPeerID, serviceName string) string {
	return providerPeerID + "\x00" + serviceName
}

func (s *fscContractSnapshot) service(providerPeerID, serviceName string) (fscConnectionGrant, bool) {
	if s == nil {
		return fscConnectionGrant{}, false
	}
	grant, ok := s.byService[fscServiceKey(providerPeerID, serviceName)]
	return grant, ok
}

type sourceReconciler struct {
	managerClient  *http.Client
	sourceClient   *http.Client
	managerURL     string
	consumerPeerID string
	outwayURL      string
	schemaPath     string
	publicBaseURL  string
	sources        []sourceConfiguration
	store          certificateStore
	backend        activationBackend
	statuses       sourceStatusWriter
}
