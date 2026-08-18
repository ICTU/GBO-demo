package onboarding

import (
	"context"
	"reflect"
	"testing"
)

type memoryRepository struct {
	participants map[string]Participant
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{participants: make(map[string]Participant)}
}

func (r *memoryRepository) List(context.Context) ([]Participant, error) {
	participants := make([]Participant, 0, len(r.participants))
	for _, participant := range r.participants {
		participants = append(participants, participant)
	}
	return participants, nil
}

func (r *memoryRepository) Save(_ context.Context, participant Participant) error {
	r.participants[participant.OIN] = participant
	return nil
}

func (r *memoryRepository) InsertIfAbsent(_ context.Context, participant Participant) error {
	if _, exists := r.participants[participant.OIN]; !exists {
		r.participants[participant.OIN] = participant
	}
	return nil
}

func (r *memoryRepository) ToggleActive(_ context.Context, oin string) (bool, error) {
	participant, exists := r.participants[oin]
	if !exists {
		return false, nil
	}
	participant.Active = !participant.Active
	r.participants[oin] = participant
	return true, nil
}

func TestSaveNormalizesParticipant(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	participant := Participant{
		OIN:            "00000001234567890000",
		Name:           " Hypotheekadvies BV ",
		Active:         true,
		AllowedSources: []string{"brp", "belastingdienst", "brp"},
	}
	if err := service.Save(t.Context(), participant); err != nil {
		t.Fatal(err)
	}
	got := repository.participants[participant.OIN]
	if got.Name != "Hypotheekadvies BV" {
		t.Fatalf("Name = %q", got.Name)
	}
	if want := []string{"belastingdienst", "brp"}; !reflect.DeepEqual(got.AllowedSources, want) {
		t.Fatalf("AllowedSources = %v, want %v", got.AllowedSources, want)
	}
}

func TestSaveRejectsInvalidParticipant(t *testing.T) {
	service := NewService(newMemoryRepository())
	tests := []Participant{
		{OIN: "123", Name: "Too short", Active: true, AllowedSources: []string{"belastingdienst"}},
		{OIN: "00000001234567890000", Active: true, AllowedSources: []string{"belastingdienst"}},
		{OIN: "00000001234567890000", Name: "No source", Active: true},
		{OIN: "00000001234567890000", Name: "Unknown source", Active: true, AllowedSources: []string{"kadaster"}},
	}
	for _, participant := range tests {
		if err := service.Save(t.Context(), participant); err == nil {
			t.Errorf("Save(%+v) succeeded, want validation error", participant)
		}
	}
}

func TestToggleValidatesOIN(t *testing.T) {
	service := NewService(newMemoryRepository())
	if _, err := service.ToggleActive(t.Context(), "123"); err == nil {
		t.Fatal("ToggleActive succeeded for an invalid OIN")
	}
}

func TestSeedDemoIsIdempotentAndPreservesChanges(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	ctx := context.Background()
	if err := service.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	hypotheek := repository.participants["99999999900000000300"]
	hypotheek.Active = false
	repository.participants[hypotheek.OIN] = hypotheek
	if err := service.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repository.participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(repository.participants))
	}
	if repository.participants[hypotheek.OIN].Active {
		t.Fatal("second seed overwrote operator change")
	}
}
