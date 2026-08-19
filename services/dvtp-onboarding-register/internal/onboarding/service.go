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

var validOIN = regexp.MustCompile(`^[0-9]{20}$`)

const eudiIssuerOIN = "99999999900000000100"

var reservedParticipantOINs = map[string]bool{
	eudiIssuerOIN: true,
}

var availableSources = []Source{
	{OIN: "99999999900000000200", Name: "Belastingdienst"},
	{OIN: "99999999900000000400", Name: "BRP (RvIG)"},
}

// Source is a source holder to which a private party can be admitted.
type Source struct {
	OIN  string
	Name string
}

// Participant is a private party's admission state.
type Participant struct {
	OIN               string   `json:"oin"`
	Name              string   `json:"name"`
	Active            bool     `json:"active"`
	AllowedSourceOINs []string `json:"allowed_source_oins"`
	UpdatedAt         string   `json:"-"`
}

// Repository is the persistence port needed by the onboarding use cases.
type Repository interface {
	List(context.Context) ([]Participant, error)
	Save(context.Context, Participant) error
	UpdateDetails(context.Context, Participant) (bool, error)
	InsertIfAbsent(context.Context, Participant) error
	ToggleActive(context.Context, string) (bool, error)
}

// Service is the single entry point for DvTP admission use cases.
type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Sources() []Source {
	return append([]Source(nil), availableSources...)
}

func (s *Service) List(ctx context.Context) ([]Participant, error) {
	participants, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	privateParticipants := make([]Participant, 0, len(participants))
	for _, participant := range participants {
		if !reservedParticipantOINs[participant.OIN] {
			privateParticipants = append(privateParticipants, participant)
		}
	}
	return privateParticipants, nil
}

func (s *Service) Save(ctx context.Context, participant Participant) error {
	normalized, err := normalizeParticipant(participant)
	if err != nil {
		return err
	}
	return s.repository.Save(ctx, normalized)
}

// UpdateDetails changes an existing participant's display name and source
// admissions without changing whether the participant is active.
func (s *Service) UpdateDetails(ctx context.Context, oin, name string, allowedSourceOINs []string) (bool, error) {
	normalized, err := normalizeParticipant(Participant{
		OIN:               oin,
		Name:              name,
		AllowedSourceOINs: allowedSourceOINs,
	})
	if err != nil {
		return false, err
	}
	return s.repository.UpdateDetails(ctx, normalized)
}

func (s *Service) ToggleActive(ctx context.Context, oin string) (bool, error) {
	if !validOIN.MatchString(oin) {
		return false, errors.New("invalid OIN")
	}
	if reservedParticipantOINs[oin] {
		return false, errors.New("OIN is reserved for a technical system party")
	}
	return s.repository.ToggleActive(ctx, oin)
}

func (s *Service) SeedDemo(ctx context.Context) error {
	seed := []Participant{
		{
			OIN:               "99999999900000000300",
			Name:              "Demo Hypotheekverlener BV",
			Active:            true,
			AllowedSourceOINs: []string{"99999999900000000200"},
		},
		{
			OIN:               "99999999900000000500",
			Name:              "Demo Incassobureau BV",
			Active:            false,
			AllowedSourceOINs: []string{"99999999900000000200"},
		},
	}
	for _, participant := range seed {
		normalized, err := normalizeParticipant(participant)
		if err != nil {
			return err
		}
		if err := s.repository.InsertIfAbsent(ctx, normalized); err != nil {
			return fmt.Errorf("seed participant %s: %w", normalized.OIN, err)
		}
	}
	return nil
}

func normalizeParticipant(participant Participant) (Participant, error) {
	participant.OIN = strings.TrimSpace(participant.OIN)
	participant.Name = strings.TrimSpace(participant.Name)
	if !validOIN.MatchString(participant.OIN) {
		return Participant{}, errors.New("OIN must contain exactly 20 digits")
	}
	if reservedParticipantOINs[participant.OIN] {
		return Participant{}, errors.New("OIN is reserved for a technical system party")
	}
	if participant.Name == "" {
		return Participant{}, errors.New("name is required")
	}

	allowed := make(map[string]bool, len(availableSources))
	for _, source := range availableSources {
		allowed[source.OIN] = true
	}
	unique := make(map[string]bool, len(participant.AllowedSourceOINs))
	for _, sourceOIN := range participant.AllowedSourceOINs {
		if !allowed[sourceOIN] {
			return Participant{}, fmt.Errorf("unknown source OIN %q", sourceOIN)
		}
		unique[sourceOIN] = true
	}
	participant.AllowedSourceOINs = participant.AllowedSourceOINs[:0]
	for sourceOIN := range unique {
		participant.AllowedSourceOINs = append(participant.AllowedSourceOINs, sourceOIN)
	}
	sort.Strings(participant.AllowedSourceOINs)
	if len(participant.AllowedSourceOINs) == 0 {
		return Participant{}, errors.New("select at least one source")
	}
	return participant, nil
}
