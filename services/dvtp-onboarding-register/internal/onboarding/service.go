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

var availableSources = []Source{
	{Key: "belastingdienst", Name: "Belastingdienst"},
	{Key: "brp", Name: "BRP (RvIG)"},
}

// Source is a source holder to which a private party can be admitted.
type Source struct {
	Key  string
	Name string
}

// Participant is a private party's admission state.
type Participant struct {
	OIN            string   `json:"oin"`
	Name           string   `json:"name"`
	Active         bool     `json:"active"`
	AllowedSources []string `json:"allowed_sources"`
	UpdatedAt      string   `json:"-"`
}

// Repository is the persistence port needed by the onboarding use cases.
type Repository interface {
	List(context.Context) ([]Participant, error)
	Save(context.Context, Participant) error
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
	return s.repository.List(ctx)
}

func (s *Service) Save(ctx context.Context, participant Participant) error {
	normalized, err := normalizeParticipant(participant)
	if err != nil {
		return err
	}
	return s.repository.Save(ctx, normalized)
}

func (s *Service) ToggleActive(ctx context.Context, oin string) (bool, error) {
	if !validOIN.MatchString(oin) {
		return false, errors.New("invalid OIN")
	}
	return s.repository.ToggleActive(ctx, oin)
}

func (s *Service) SeedDemo(ctx context.Context) error {
	seed := []Participant{
		{
			OIN:            "99999999900000000300",
			Name:           "Demo Hypotheekverlener BV",
			Active:         true,
			AllowedSources: []string{"belastingdienst"},
		},
		{
			OIN:            "99999999900000000500",
			Name:           "Demo Incassobureau BV",
			Active:         false,
			AllowedSources: []string{"belastingdienst"},
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
	if participant.Name == "" {
		return Participant{}, errors.New("name is required")
	}

	allowed := make(map[string]bool, len(availableSources))
	for _, source := range availableSources {
		allowed[source.Key] = true
	}
	unique := make(map[string]bool, len(participant.AllowedSources))
	for _, source := range participant.AllowedSources {
		if !allowed[source] {
			return Participant{}, fmt.Errorf("unknown source %q", source)
		}
		unique[source] = true
	}
	participant.AllowedSources = participant.AllowedSources[:0]
	for source := range unique {
		participant.AllowedSources = append(participant.AllowedSources, source)
	}
	sort.Strings(participant.AllowedSources)
	if len(participant.AllowedSources) == 0 {
		return Participant{}, errors.New("select at least one source")
	}
	return participant, nil
}
