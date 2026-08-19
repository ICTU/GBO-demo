package sqlite

import (
	"database/sql"
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
		OIN:               "00000001234567890000",
		Name:              "Hypotheekadvies BV",
		Active:            true,
		AllowedSourceOINs: []string{"99999999900000000200", "99999999900000000400"},
	}
	if err := repository.Save(t.Context(), participant); err != nil {
		t.Fatal(err)
	}
	participants, err := repository.List(t.Context())
	if err != nil || len(participants) != 1 {
		t.Fatalf("participants = %+v, err = %v", participants, err)
	}
	if !reflect.DeepEqual(participants[0].AllowedSourceOINs, participant.AllowedSourceOINs) {
		t.Fatalf("AllowedSourceOINs = %v", participants[0].AllowedSourceOINs)
	}
	found, err := repository.ToggleActive(t.Context(), participant.OIN)
	if err != nil || !found {
		t.Fatalf("ToggleActive = %v, %v", found, err)
	}
	participants, _ = repository.List(t.Context())
	if participants[0].Active {
		t.Fatal("participant remained active")
	}

	updated := participant
	updated.Name = "Gewijzigde naam"
	updated.AllowedSourceOINs = []string{"99999999900000000400"}
	found, err = repository.UpdateDetails(t.Context(), updated)
	if err != nil || !found {
		t.Fatalf("UpdateDetails = %v, %v", found, err)
	}
	participants, _ = repository.List(t.Context())
	if participants[0].Name != updated.Name || !reflect.DeepEqual(participants[0].AllowedSourceOINs, updated.AllowedSourceOINs) {
		t.Fatalf("participant details = %+v, want %+v", participants[0], updated)
	}
	if participants[0].Active {
		t.Fatal("UpdateDetails overwrote the independently managed active state")
	}
}

func TestInsertIfAbsentDoesNotOverwriteParticipant(t *testing.T) {
	repository := testRepository(t)
	participant := onboarding.Participant{
		OIN:               "00000001234567890000",
		Name:              "Original",
		Active:            true,
		AllowedSourceOINs: []string{"99999999900000000200"},
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

func TestOpenMigratesLegacySourceKeysToOINs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
        INSERT INTO participants (oin, name, active, allowed_sources, updated_at)
        VALUES ('00000001234567890000', 'Legacy', 1, '["belastingdienst","brp"]', '2026-01-01T00:00:00Z')
    `); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	participants, err := repository.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"99999999900000000200", "99999999900000000400"}
	if !reflect.DeepEqual(participants[0].AllowedSourceOINs, want) {
		t.Fatalf("AllowedSourceOINs = %v, want %v", participants[0].AllowedSourceOINs, want)
	}
}
