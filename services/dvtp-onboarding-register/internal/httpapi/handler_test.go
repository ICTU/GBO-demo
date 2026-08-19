package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dvtp-onboarding-register/internal/onboarding"
)

const testCSRFToken = "test-csrf-token"

type memoryRepository struct {
	participants map[string]onboarding.Participant
	toggleErr    error
	updateErr    error
}

func newTestService(t *testing.T) (*onboarding.Service, *memoryRepository) {
	t.Helper()
	repository := &memoryRepository{participants: make(map[string]onboarding.Participant)}
	service, err := onboarding.NewService(repository, onboarding.Configuration{
		SourceHolders: []onboarding.Source{
			{PeerID: "99999999900000000200", Name: "Belastingdienst"},
			{PeerID: "99999999900000000400", Name: "BRP (RvIG)"},
		},
		SystemParticipants: []onboarding.Participant{{
			PeerID: "0000009961MINEZK0000", Name: "EUDI issuer", Active: true,
			AllowedSourcePeerIDs: []string{"99999999900000000200", "99999999900000000400"},
		}},
		SeedParticipants: []onboarding.Participant{
			{PeerID: "99999999900000000300", Name: "Demo Hypotheekverlener BV", Active: true, AllowedSourcePeerIDs: []string{"99999999900000000200"}},
			{PeerID: "99999999900000000500", Name: "Demo Incassobureau BV", Active: false, AllowedSourcePeerIDs: []string{"99999999900000000200"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, repository
}

func (r *memoryRepository) List(context.Context) ([]onboarding.Participant, error) {
	participants := make([]onboarding.Participant, 0, len(r.participants))
	for _, participant := range r.participants {
		participants = append(participants, participant)
	}
	return participants, nil
}

func (r *memoryRepository) Save(_ context.Context, participant onboarding.Participant) error {
	r.participants[participant.PeerID] = participant
	return nil
}

func (r *memoryRepository) InsertIfAbsent(_ context.Context, participant onboarding.Participant) error {
	if _, exists := r.participants[participant.PeerID]; !exists {
		r.participants[participant.PeerID] = participant
	}
	return nil
}

func (r *memoryRepository) ToggleActive(_ context.Context, peerID string) (bool, error) {
	if r.toggleErr != nil {
		return false, r.toggleErr
	}
	participant, exists := r.participants[peerID]
	if !exists {
		return false, nil
	}
	participant.Active = !participant.Active
	r.participants[peerID] = participant
	return true, nil
}

func (r *memoryRepository) UpdateDetails(_ context.Context, participant onboarding.Participant) (bool, error) {
	if r.updateErr != nil {
		return false, r.updateErr
	}
	current, exists := r.participants[participant.PeerID]
	if !exists {
		return false, nil
	}
	participant.Active = current.Active
	r.participants[participant.PeerID] = participant
	return true, nil
}

func testHandler(service *onboarding.Service) http.Handler {
	return NewHandler(service, testCSRFToken)
}

func formRequest(method, path string, form url.Values) *http.Request {
	form.Set("csrf_token", testCSRFToken)
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://example.com")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func TestOpenFTVParticipantsEndpoint(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/openftv/participants", nil)
	testHandler(service).ServeHTTP(recorder, request)

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
	if len(participants) != 3 {
		t.Fatalf("participants = %d, want 3", len(participants))
	}
}

func TestParticipantFormCreatesParticipant(t *testing.T) {
	service, repository := newTestService(t)
	form := url.Values{
		"peer_id":         {"00000001234567890000"},
		"name":            {"Hypotheekadvies BV"},
		"active":          {"on"},
		"source_peer_ids": {"99999999900000000200"},
	}
	recorder := httptest.NewRecorder()
	request := formRequest(http.MethodPost, "/participants", form)
	testHandler(service).ServeHTTP(recorder, request)
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
		"peer_id": {"00000001234567890000"},
		"name":    {"Hypotheekadvies BV"},
		"active":  {"on"},
	}
	recorder := httptest.NewRecorder()
	request := formRequest(http.MethodPost, "/participants", form)
	testHandler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "error=") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestParticipantEditPagePrefillsCurrentDetails(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	peerID := "99999999900000000300"
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?edit="+peerID, nil)
	testHandler(service).ServeHTTP(recorder, request)
	body := recorder.Body.String()
	for _, want := range []string{
		`id="edit-participant"`,
		`action="/participants/` + peerID + `"`,
		`value="Demo Hypotheekverlener BV"`,
		`value="99999999900000000200" checked`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("edit page does not contain %q: %s", want, body)
		}
	}
}

func TestParticipantEditUpdatesNameAndSourcesWithoutChangingActiveState(t *testing.T) {
	service, repository := newTestService(t)
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	peerID := "99999999900000000500"
	form := url.Values{
		"name":            {"Gewijzigd Incassobureau BV"},
		"source_peer_ids": {"99999999900000000200", "99999999900000000400"},
	}
	recorder := httptest.NewRecorder()
	request := formRequest(http.MethodPost, "/participants/"+peerID, form)
	testHandler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/?saved=1" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	got := repository.participants[peerID]
	if got.Name != "Gewijzigd Incassobureau BV" || len(got.AllowedSourcePeerIDs) != 2 {
		t.Fatalf("updated participant = %+v", got)
	}
	if got.Active {
		t.Fatal("edit route activated a paused participant")
	}
}

func TestRegisterPageAndSecurityHeaders(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	testHandler(service).ServeHTTP(recorder, request)
	body, _ := io.ReadAll(recorder.Body)
	if !strings.Contains(string(body), "DvTP toelatingsregister") || !strings.Contains(string(body), "Demo Hypotheekverlener BV") {
		t.Fatalf("page did not render register: %s", body)
	}
	if strings.Count(string(body), `name="csrf_token" value="`+testCSRFToken+`"`) != 3 {
		t.Fatalf("page did not render CSRF tokens in every form: %s", body)
	}
	if header := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(header, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q", header)
	}
	if header := recorder.Header().Get("Referrer-Policy"); header != "same-origin" {
		t.Fatalf("Referrer-Policy = %q, want %q", header, "same-origin")
	}
}

func TestParticipantFormRejectsCrossSiteAndMissingToken(t *testing.T) {
	for _, test := range []struct {
		name      string
		origin    string
		fetchSite string
		token     string
	}{
		{name: "cross-site origin", origin: "https://attacker.example", fetchSite: "cross-site", token: testCSRFToken},
		{name: "missing token", origin: "http://example.com", fetchSite: "same-origin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, repository := newTestService(t)
			form := url.Values{
				"peer_id":         {"00000001234567890000"},
				"name":            {"Hypotheekadvies BV"},
				"source_peer_ids": {"99999999900000000200"},
				"csrf_token":      {test.token},
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/participants", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			testHandler(service).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
			}
			if len(repository.participants) != 0 {
				t.Fatal("cross-site request mutated the register")
			}
		})
	}
}

func TestToggleStorageFailureReturnsUnavailable(t *testing.T) {
	service, repository := newTestService(t)
	repository.toggleErr = errors.New("database locked")
	recorder := httptest.NewRecorder()
	request := formRequest(http.MethodPost, "/participants/00000001234567890000/toggle", url.Values{})
	testHandler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), "register unavailable") {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}
