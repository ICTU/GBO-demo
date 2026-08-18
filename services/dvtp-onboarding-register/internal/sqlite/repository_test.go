package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"

	"dvtp-onboarding-register/internal/onboarding"
)

func testRepository(t *testing.T) *Repository {
	t.Helper()
	repository, err := Open(filepath.Join(t.TempDir(), "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	return repository
}

func TestRepositoryPersistsAndTogglesParticipant(t *testing.T) {
	repository := testRepository(t)
	participant := onboarding.Participant{
		OIN:            "00000001234567890000",
		Name:           "Hypotheekadvies BV",
		Active:         true,
		AllowedSources: []string{"belastingdienst", "brp"},
	}
	if err := repository.Save(t.Context(), participant); err != nil {
		t.Fatal(err)
	}
	participants, err := repository.List(t.Context())
	if err != nil || len(participants) != 1 {
		t.Fatalf("participants = %+v, err = %v", participants, err)
	}
	if !reflect.DeepEqual(participants[0].AllowedSources, participant.AllowedSources) {
		t.Fatalf("AllowedSources = %v", participants[0].AllowedSources)
	}
	found, err := repository.ToggleActive(t.Context(), participant.OIN)
	if err != nil || !found {
		t.Fatalf("ToggleActive = %v, %v", found, err)
	}
	participants, _ = repository.List(t.Context())
	if participants[0].Active {
		t.Fatal("participant remained active")
	}
}

func TestInsertIfAbsentDoesNotOverwriteParticipant(t *testing.T) {
	repository := testRepository(t)
	participant := onboarding.Participant{
		OIN:            "00000001234567890000",
		Name:           "Original",
		Active:         true,
		AllowedSources: []string{"belastingdienst"},
	}
	if err := repository.InsertIfAbsent(t.Context(), participant); err != nil {
		t.Fatal(err)
	}
	participant.Name = "Replacement"
	if err := repository.InsertIfAbsent(t.Context(), participant); err != nil {
		t.Fatal(err)
	}
	participants, _ := repository.List(t.Context())
	if participants[0].Name != "Original" {
		t.Fatalf("Name = %q", participants[0].Name)
	}
}
