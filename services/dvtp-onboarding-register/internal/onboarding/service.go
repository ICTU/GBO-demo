// Package onboarding contains the DvTP admission use cases and business rules.
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var validPeerID = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// Source is a source holder to which a private party can be admitted.
type Source struct {
	PeerID string `json:"peer_id"`
	Name   string `json:"name"`
}

// Participant is a private or technical party's admission state.
type Participant struct {
	PeerID               string   `json:"peer_id"`
	Name                 string   `json:"name"`
	Active               bool     `json:"active"`
	AllowedSourcePeerIDs []string `json:"allowed_source_peer_ids"`
	UpdatedAt            string   `json:"-"`
}

// Configuration contains deployment-specific FSC identities.
type Configuration struct {
	SourceHolders      []Source      `json:"source_holders"`
	SystemParticipants []Participant `json:"system_participants,omitempty"`
	SeedParticipants   []Participant `json:"seed_participants,omitempty"`
}

type Repository interface {
	List(context.Context) ([]Participant, error)
	Save(context.Context, Participant) error
	UpdateDetails(context.Context, Participant) (bool, error)
	InsertIfAbsent(context.Context, Participant) error
	ToggleActive(context.Context, string) (bool, error)
}

type Service struct {
	repository         Repository
	sources            []Source
	systemParticipants []Participant
	seedParticipants   []Participant
	reservedPeerIDs    map[string]bool
	allowedSources     map[string]bool
}

func NewService(repository Repository, configuration Configuration) (*Service, error) {
	if repository == nil {
		return nil, errors.New("repository is required")
	}
	service := &Service{repository: repository, reservedPeerIDs: make(map[string]bool), allowedSources: make(map[string]bool)}
	for _, source := range configuration.SourceHolders {
		source.PeerID = strings.TrimSpace(source.PeerID)
		source.Name = strings.TrimSpace(source.Name)
		if !validPeerID.MatchString(source.PeerID) {
			return nil, fmt.Errorf("source holder peer_id %q must contain exactly 20 alphanumeric characters", source.PeerID)
		}
		if source.Name == "" {
			return nil, fmt.Errorf("source holder %q requires a name", source.PeerID)
		}
		if service.allowedSources[source.PeerID] {
			return nil, fmt.Errorf("duplicate source holder peer_id %q", source.PeerID)
		}
		service.allowedSources[source.PeerID] = true
		service.sources = append(service.sources, source)
	}
	if len(service.sources) == 0 {
		return nil, errors.New("at least one source holder is required")
	}
	for _, participant := range configuration.SystemParticipants {
		normalized, err := service.normalizeParticipant(participant, false)
		if err != nil {
			return nil, fmt.Errorf("system participant: %w", err)
		}
		if service.reservedPeerIDs[normalized.PeerID] {
			return nil, fmt.Errorf("duplicate system participant peer_id %q", normalized.PeerID)
		}
		service.reservedPeerIDs[normalized.PeerID] = true
		service.systemParticipants = append(service.systemParticipants, normalized)
	}
	seedPeerIDs := make(map[string]bool, len(configuration.SeedParticipants))
	for _, participant := range configuration.SeedParticipants {
		normalized, err := service.normalizeParticipant(participant, true)
		if err != nil {
			return nil, fmt.Errorf("seed participant: %w", err)
		}
		if seedPeerIDs[normalized.PeerID] {
			return nil, fmt.Errorf("duplicate seed participant peer_id %q", normalized.PeerID)
		}
		seedPeerIDs[normalized.PeerID] = true
		service.seedParticipants = append(service.seedParticipants, normalized)
	}
	return service, nil
}

func (s *Service) Sources() []Source { return append([]Source(nil), s.sources...) }

// List returns only operator-managed private participants for the UI. Filtering
// configured technical peers also protects upgrades where such a peer existed
// in the database before it became deployment-managed configuration.
func (s *Service) List(ctx context.Context) ([]Participant, error) {
	participants, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	privateParticipants := make([]Participant, 0, len(participants))
	for _, participant := range participants {
		if !s.reservedPeerIDs[participant.PeerID] {
			privateParticipants = append(privateParticipants, participant)
		}
	}
	return privateParticipants, nil
}

// ListForPolicy includes configured technical participants in the OpenFTV
// feed without persisting them or exposing them as mutable UI records.
func (s *Service) ListForPolicy(ctx context.Context) ([]Participant, error) {
	participants, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	result := append([]Participant(nil), participants...)
	result = append(result, s.systemParticipants...)
	return result, nil
}

func (s *Service) Save(ctx context.Context, participant Participant) error {
	normalized, err := s.normalizeParticipant(participant, true)
	if err != nil {
		return err
	}
	return s.repository.Save(ctx, normalized)
}

func (s *Service) UpdateDetails(ctx context.Context, peerID, name string, allowedSourcePeerIDs []string) (bool, error) {
	normalized, err := s.normalizeParticipant(Participant{PeerID: peerID, Name: name, AllowedSourcePeerIDs: allowedSourcePeerIDs}, true)
	if err != nil {
		return false, err
	}
	return s.repository.UpdateDetails(ctx, normalized)
}

func (s *Service) ToggleActive(ctx context.Context, peerID string) (bool, error) {
	if !validPeerID.MatchString(peerID) {
		return false, errors.New("invalid Peer ID")
	}
	if s.reservedPeerIDs[peerID] {
		return false, errors.New("peer ID is reserved for a technical system party")
	}
	return s.repository.ToggleActive(ctx, peerID)
}

func (s *Service) SeedDemo(ctx context.Context) error {
	for _, participant := range s.seedParticipants {
		if err := s.repository.InsertIfAbsent(ctx, participant); err != nil {
			return fmt.Errorf("seed participant %s: %w", participant.PeerID, err)
		}
	}
	return nil
}

func (s *Service) normalizeParticipant(participant Participant, rejectReserved bool) (Participant, error) {
	participant.PeerID = strings.TrimSpace(participant.PeerID)
	participant.Name = strings.TrimSpace(participant.Name)
	if !validPeerID.MatchString(participant.PeerID) {
		return Participant{}, errors.New("peer ID must contain exactly 20 alphanumeric characters")
	}
	if rejectReserved && s.reservedPeerIDs[participant.PeerID] {
		return Participant{}, errors.New("peer ID is reserved for a technical system party")
	}
	if participant.Name == "" {
		return Participant{}, errors.New("name is required")
	}
	unique := make(map[string]bool, len(participant.AllowedSourcePeerIDs))
	for _, sourcePeerID := range participant.AllowedSourcePeerIDs {
		if !s.allowedSources[sourcePeerID] {
			return Participant{}, fmt.Errorf("unknown source holder Peer ID %q", sourcePeerID)
		}
		unique[sourcePeerID] = true
	}
	participant.AllowedSourcePeerIDs = participant.AllowedSourcePeerIDs[:0]
	for sourcePeerID := range unique {
		participant.AllowedSourcePeerIDs = append(participant.AllowedSourcePeerIDs, sourcePeerID)
	}
	sort.Strings(participant.AllowedSourcePeerIDs)
	if len(participant.AllowedSourcePeerIDs) == 0 {
		return Participant{}, errors.New("select at least one source")
	}
	return participant, nil
}
