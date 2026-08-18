package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dvtp-onboarding-register/internal/onboarding"
)

type memoryRepository struct {
	participants map[string]onboarding.Participant
}

func newTestService(t *testing.T) (*onboarding.Service, *memoryRepository) {
	t.Helper()
	repository := &memoryRepository{participants: make(map[string]onboarding.Participant)}
	return onboarding.NewService(repository), repository
}

func (r *memoryRepository) List(context.Context) ([]onboarding.Participant, error) {
	participants := make([]onboarding.Participant, 0, len(r.participants))
	for _, participant := range r.participants {
		participants = append(participants, participant)
	}
	return participants, nil
}

func (r *memoryRepository) Save(_ context.Context, participant onboarding.Participant) error {
	r.participants[participant.OIN] = participant
	return nil
}

func (r *memoryRepository) InsertIfAbsent(_ context.Context, participant onboarding.Participant) error {
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

func TestOpenFTVParticipantsEndpoint(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/openftv/participants", nil)
	NewHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	var participants []onboarding.Participant
	if err := json.NewDecoder(recorder.Body).Decode(&participants); err != nil {
		t.Fatal(err)
	}
	if len(participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(participants))
	}
}

func TestParticipantFormCreatesParticipant(t *testing.T) {
	service, repository := newTestService(t)
	form := url.Values{
		"oin":         {"00000001234567890000"},
		"name":        {"Hypotheekadvies BV"},
		"active":      {"on"},
		"source_oins": {"99999999900000000200"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/participants", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	NewHandler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/?saved=1" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if len(repository.participants) != 1 {
		t.Fatalf("participants = %d", len(repository.participants))
	}
}

func TestParticipantFormRejectsMissingSource(t *testing.T) {
	service, _ := newTestService(t)
	form := url.Values{
		"oin":    {"00000001234567890000"},
		"name":   {"Hypotheekadvies BV"},
		"active": {"on"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/participants", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	NewHandler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "error=") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestRegisterPageAndSecurityHeaders(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	NewHandler(service).ServeHTTP(recorder, request)
	body, _ := io.ReadAll(recorder.Body)
	if !strings.Contains(string(body), "DvTP toelatingsregister") || !strings.Contains(string(body), "Demo Hypotheekverlener BV") {
		t.Fatalf("page did not render register: %s", body)
	}
	if header := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(header, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q", header)
	}
}
