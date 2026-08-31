package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// memoryActivationBackend exercises the reconciler lifecycle contract without
// preserving the removed filesystem persistence implementation in tests.
type memoryActivationBackend struct {
	candidates map[string]*sourceActivation
	active     map[string]*sourceActivation
}

func newMemoryActivationBackend() *memoryActivationBackend {
	return &memoryActivationBackend{candidates: make(map[string]*sourceActivation), active: make(map[string]*sourceActivation)}
}

func (b *memoryActivationBackend) CurrentCandidate(sourceID string) (*sourceActivation, error) {
	candidate := b.candidates[sourceID]
	if candidate == nil {
		return nil, os.ErrNotExist
	}
	return cloneTestActivation(candidate), nil
}

func (b *memoryActivationBackend) RefreshCandidate(sourceID string, source sourceRegistration, metadataURL string, certificates certificateArtifacts, transportAuthenticated bool, now time.Time) (*sourceActivation, error) {
	activation, err := b.CurrentCandidate(sourceID)
	if err != nil {
		return nil, err
	}
	if source.SourceID != sourceID {
		return nil, fmt.Errorf("refreshed source registration belongs to %q", source.SourceID)
	}
	if err := source.validate(); err != nil {
		return nil, err
	}
	if activation.ExpiresAt.Sub(now) < defaultSourceMetadataCachePolicy.MinimumValidity {
		return nil, fmt.Errorf("source metadata validity is shorter than the GBO minimum")
	}
	activation.Source = source
	activation.MetadataURL = metadataURL
	activation.Certificates = certificates
	activation.TransportAuthenticated = transportAuthenticated
	activation.CheckedAt = now.UTC()
	activation.FreshUntil = minTime(now.Add(defaultSourceMetadataCachePolicy.MaximumFreshness), activation.ExpiresAt)
	activation.StaleUntil = minTime(activation.FreshUntil.Add(defaultSourceMetadataCachePolicy.StaleGrace), activation.ExpiresAt)
	b.candidates[sourceID] = cloneTestActivation(activation)
	if deployed := b.active[sourceID]; deployed != nil {
		candidateDigest, candidateErr := activationDeploymentDigest(activation)
		deployedDigest, deployedErr := activationDeploymentDigest(deployed)
		if candidateErr != nil || deployedErr != nil {
			return nil, fmt.Errorf("compare deployment digests: %v %v", candidateErr, deployedErr)
		}
		if candidateDigest == deployedDigest {
			b.active[sourceID] = cloneTestActivation(activation)
		}
	}
	return cloneTestActivation(activation), nil
}

func (b *memoryActivationBackend) RolloutRequired(candidate *sourceActivation) (bool, error) {
	deployed := b.active[candidate.Source.SourceID]
	if deployed == nil {
		return true, nil
	}
	candidateDigest, err := activationDeploymentDigest(candidate)
	if err != nil {
		return false, err
	}
	deployedDigest, err := activationDeploymentDigest(deployed)
	if err != nil {
		return false, err
	}
	return candidateDigest != deployedDigest, nil
}

func (b *memoryActivationBackend) Activate(validated *validatedSourceRegistration, certificates certificateArtifacts) (*sourceActivation, error) {
	if validated == nil || !sourceIDPattern.MatchString(validated.Registration.SourceID) {
		return nil, fmt.Errorf("activation requires a valid source_id")
	}
	activation := activationFromValidatedSource(validated)
	activation.Certificates = certificates
	for index := range activation.Types {
		activation.Types[index].TypeMetadataReference = activation.Types[index].VCT
	}
	b.candidates[activation.Source.SourceID] = cloneTestActivation(activation)
	if deployed := b.active[activation.Source.SourceID]; deployed != nil {
		candidateDigest, _ := activationDeploymentDigest(activation)
		deployedDigest, _ := activationDeploymentDigest(deployed)
		if candidateDigest == deployedDigest {
			b.active[activation.Source.SourceID] = cloneTestActivation(activation)
		}
	}
	return cloneTestActivation(activation), nil
}

func (b *memoryActivationBackend) setActive(activation *sourceActivation) {
	b.active[activation.Source.SourceID] = cloneTestActivation(activation)
}

func (b *memoryActivationBackend) activeCandidate(sourceID string) (*sourceActivation, error) {
	activation := b.active[sourceID]
	if activation == nil {
		return nil, os.ErrNotExist
	}
	return cloneTestActivation(activation), nil
}

func cloneTestActivation(activation *sourceActivation) *sourceActivation {
	body, err := json.Marshal(activation)
	if err != nil {
		panic(err)
	}
	var clone sourceActivation
	if err := json.Unmarshal(body, &clone); err != nil {
		panic(err)
	}
	clone.RegistryDeploymentDigest = activation.RegistryDeploymentDigest
	clone.PublicCertificates = activation.PublicCertificates
	return &clone
}
