package onboarding

import (
	"context"
	"reflect"
	"testing"
)

const (
	testBDPeerID      = "0000009958MINBZK0000"
	testRvIGPeerID    = "0000009958RVIG000000"
	testEUDIPeerID    = "0000009961MINEZK0000"
	testPrivatePeerID = "0000009950HYPBV00000"
)

func testConfiguration() Configuration {
	return Configuration{
		SourceHolders:      []Source{{PeerID: testBDPeerID, Name: "Belastingdienst"}, {PeerID: testRvIGPeerID, Name: "RvIG"}},
		SystemParticipants: []Participant{{PeerID: testEUDIPeerID, Name: "EUDI issuer", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID, testRvIGPeerID}}},
		SeedParticipants:   []Participant{{PeerID: testPrivatePeerID, Name: "Hypotheek BV", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID}}},
	}
}

func mustTestService(t *testing.T, repository Repository) *Service {
	t.Helper()
	service, err := NewService(repository, testConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	return service
}

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
	r.participants[participant.PeerID] = participant
	return nil
}

func (r *memoryRepository) InsertIfAbsent(_ context.Context, participant Participant) error {
	if _, exists := r.participants[participant.PeerID]; !exists {
		r.participants[participant.PeerID] = participant
	}
	return nil
}

func (r *memoryRepository) ToggleActive(_ context.Context, peerID string) (bool, error) {
	participant, exists := r.participants[peerID]
	if !exists {
		return false, nil
	}
	participant.Active = !participant.Active
	r.participants[peerID] = participant
	return true, nil
}

func (r *memoryRepository) UpdateDetails(_ context.Context, participant Participant) (bool, error) {
	current, exists := r.participants[participant.PeerID]
	if !exists {
		return false, nil
	}
	participant.Active = current.Active
	r.participants[participant.PeerID] = participant
	return true, nil
}

func TestSaveNormalizesParticipant(t *testing.T) {
	repository := newMemoryRepository()
	service := mustTestService(t, repository)
	participant := Participant{
		PeerID: testPrivatePeerID, Name: " Hypotheekadvies BV ", Active: true,
		AllowedSourcePeerIDs: []string{testRvIGPeerID, testBDPeerID, testRvIGPeerID},
	}
	if err := service.Save(t.Context(), participant); err != nil {
		t.Fatal(err)
	}
	got := repository.participants[participant.PeerID]
	if got.Name != "Hypotheekadvies BV" {
		t.Fatalf("Name = %q", got.Name)
	}
	if want := []string{testBDPeerID, testRvIGPeerID}; !reflect.DeepEqual(got.AllowedSourcePeerIDs, want) {
		t.Fatalf("AllowedSourcePeerIDs = %v, want %v", got.AllowedSourcePeerIDs, want)
	}
}

func TestSaveRejectsInvalidParticipant(t *testing.T) {
	service := mustTestService(t, newMemoryRepository())
	tests := []Participant{
		{PeerID: "123", Name: "Too short", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID}},
		{PeerID: testEUDIPeerID, Name: "Technical EUDI issuer", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID}},
		{PeerID: testPrivatePeerID, Active: true, AllowedSourcePeerIDs: []string{testBDPeerID}},
		{PeerID: testPrivatePeerID, Name: "No source", Active: true},
		{PeerID: testPrivatePeerID, Name: "Unknown source", Active: true, AllowedSourcePeerIDs: []string{"00000000000000000001"}},
	}
	for _, participant := range tests {
		if err := service.Save(t.Context(), participant); err == nil {
			t.Errorf("Save(%+v) succeeded, want validation error", participant)
		}
	}
}

func TestUpdateDetailsNormalizesParticipantAndPreservesActiveState(t *testing.T) {
	repository := newMemoryRepository()
	peerID := testPrivatePeerID
	repository.participants[peerID] = Participant{
		PeerID: peerID, Name: "Original", Active: false, AllowedSourcePeerIDs: []string{testBDPeerID},
	}
	service := mustTestService(t, repository)
	found, err := service.UpdateDetails(t.Context(), peerID, " Gewijzigde naam ", []string{
		testRvIGPeerID, testBDPeerID, testRvIGPeerID,
	})
	if err != nil || !found {
		t.Fatalf("UpdateDetails() = %t, %v", found, err)
	}
	got := repository.participants[peerID]
	if got.Name != "Gewijzigde naam" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.Active {
		t.Fatal("UpdateDetails changed the participant's active state")
	}
	if want := []string{testBDPeerID, testRvIGPeerID}; !reflect.DeepEqual(got.AllowedSourcePeerIDs, want) {
		t.Fatalf("AllowedSourcePeerIDs = %v, want %v", got.AllowedSourcePeerIDs, want)
	}
}

func TestListForPolicyAddsConfiguredTechnicalParties(t *testing.T) {
	repository := newMemoryRepository()
	service := mustTestService(t, repository)
	participants, err := service.ListForPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(participants) != 1 || participants[0].PeerID != testEUDIPeerID {
		t.Fatalf("configured technical participant missing from policy feed: %+v", participants)
	}
}

func TestConfiguredTechnicalPartyShadowsLegacyDatabaseRecord(t *testing.T) {
	repository := newMemoryRepository()
	repository.participants[testEUDIPeerID] = Participant{
		PeerID: testEUDIPeerID, Name: "stale editable record", Active: false,
		AllowedSourcePeerIDs: []string{testBDPeerID},
	}
	service := mustTestService(t, repository)
	privateParticipants, err := service.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(privateParticipants) != 0 {
		t.Fatalf("technical participant leaked into UI list: %+v", privateParticipants)
	}
	policyParticipants, err := service.ListForPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(policyParticipants) != 1 || policyParticipants[0].Name != "EUDI issuer" || !policyParticipants[0].Active {
		t.Fatalf("policy feed did not use deployment-managed participant: %+v", policyParticipants)
	}
}

func TestNewServiceRejectsInvalidDeploymentConfiguration(t *testing.T) {
	tests := map[string]Configuration{
		"no source holders":   {},
		"invalid source peer": {SourceHolders: []Source{{PeerID: "123", Name: "Source"}}},
		"duplicate source": {SourceHolders: []Source{
			{PeerID: testBDPeerID, Name: "One"},
			{PeerID: testBDPeerID, Name: "Two"},
		}},
		"system participant with unknown source": {
			SourceHolders: []Source{{PeerID: testBDPeerID, Name: "Source"}},
			SystemParticipants: []Participant{{
				PeerID: testEUDIPeerID, Name: "EUDI issuer", Active: true,
				AllowedSourcePeerIDs: []string{testRvIGPeerID},
			}},
		},
		"duplicate seed participant": {
			SourceHolders: []Source{{PeerID: testBDPeerID, Name: "Source"}},
			SeedParticipants: []Participant{
				{PeerID: testPrivatePeerID, Name: "One", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID}},
				{PeerID: testPrivatePeerID, Name: "Two", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID}},
			},
		},
	}
	for name, configuration := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewService(newMemoryRepository(), configuration); err == nil {
				t.Fatal("NewService succeeded, want validation error")
			}
		})
	}
}

func TestToggleRejectsReservedTechnicalParty(t *testing.T) {
	service := mustTestService(t, newMemoryRepository())
	if _, err := service.ToggleActive(t.Context(), testEUDIPeerID); err == nil {
		t.Fatal("ToggleActive accepted a reserved technical party")
	}
}

func TestToggleValidatesPeerID(t *testing.T) {
	service := mustTestService(t, newMemoryRepository())
	if _, err := service.ToggleActive(t.Context(), "123"); err == nil {
		t.Fatal("ToggleActive succeeded for an invalid Peer ID")
	}
}

func TestSeedDemoIsIdempotentAndPreservesChanges(t *testing.T) {
	repository := newMemoryRepository()
	service := mustTestService(t, repository)
	ctx := context.Background()
	if err := service.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	hypotheek := repository.participants[testPrivatePeerID]
	hypotheek.Active = false
	repository.participants[hypotheek.PeerID] = hypotheek
	if err := service.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repository.participants) != 1 {
		t.Fatalf("participants = %d, want 1", len(repository.participants))
	}
	if repository.participants[hypotheek.PeerID].Active {
		t.Fatal("second seed overwrote operator change")
	}
}

const testIntegratorPeerID = "0000009950INTEGR0000"

func TestListForPolicyAttachesMandatesToTheIntegrator(t *testing.T) {
	configuration := testConfiguration()
	configuration.SeedParticipants = append(configuration.SeedParticipants, Participant{
		PeerID: testIntegratorPeerID, Name: "Integrator BV", Active: true, AllowedSourcePeerIDs: []string{testBDPeerID},
	})
	configuration.Integrations = []Integration{{
		IntegratorPeerID: testIntegratorPeerID, ServiceProviderPeerID: testPrivatePeerID, Rules: []string{"DVT0001", " DVT0001 "},
	}}
	repository := newMemoryRepository()
	service, err := NewService(repository, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	participants, err := service.ListForPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actsFor := map[string][]Mandate{}
	for _, participant := range participants {
		if participant.ActsFor == nil {
			t.Fatalf("acts_for must be an empty list, not absent: %+v", participant)
		}
		actsFor[participant.PeerID] = participant.ActsFor
	}
	want := []Mandate{{PeerID: testPrivatePeerID, Rules: []string{"DVT0001"}}}
	if !reflect.DeepEqual(actsFor[testIntegratorPeerID], want) {
		t.Fatalf("integrator acts_for = %+v, want %+v", actsFor[testIntegratorPeerID], want)
	}
	if len(actsFor[testPrivatePeerID]) != 0 {
		t.Fatalf("service provider acts for nobody, got %+v", actsFor[testPrivatePeerID])
	}
}

func TestNewServiceRejectsInvalidIntegrations(t *testing.T) {
	tests := map[string][]Integration{
		"invalid integrator": {{IntegratorPeerID: "123", ServiceProviderPeerID: testPrivatePeerID, Rules: []string{"DVT0001"}}},
		"acts for itself":    {{IntegratorPeerID: testIntegratorPeerID, ServiceProviderPeerID: testIntegratorPeerID, Rules: []string{"DVT0001"}}},
		"no rules":           {{IntegratorPeerID: testIntegratorPeerID, ServiceProviderPeerID: testPrivatePeerID, Rules: []string{" "}}},
		"duplicate": {
			{IntegratorPeerID: testIntegratorPeerID, ServiceProviderPeerID: testPrivatePeerID, Rules: []string{"DVT0001"}},
			{IntegratorPeerID: testIntegratorPeerID, ServiceProviderPeerID: testPrivatePeerID, Rules: []string{"LVG0001"}},
		},
	}
	for name, integrations := range tests {
		t.Run(name, func(t *testing.T) {
			configuration := testConfiguration()
			configuration.Integrations = integrations
			if _, err := NewService(newMemoryRepository(), configuration); err == nil {
				t.Fatal("NewService accepted an invalid integration")
			}
		})
	}
}

const testUnseededPeerID = "0000009950ADMINUI000"

func serviceProviderConfiguration() Configuration {
	configuration := testConfiguration()
	configuration.ServiceProviders = []ServiceProvider{
		{
			PeerID:          testPrivatePeerID,
			ReturnLocations: []string{"https://hypotheek.example/return"},
			Services:        []ProviderService{{Service: " aangiftegegevens ", Purpose: " Hypotheek-aanvraag "}},
		},
		{
			PeerID:          testUnseededPeerID,
			ReturnLocations: []string{"https://admin-added.example/return"},
			Services:        []ProviderService{{Service: "installatieregister", Purpose: "Goedkeuring installatiegegevens"}},
		},
	}
	return configuration
}

func TestConsentParticipantCombinesAdmissionAndRegistration(t *testing.T) {
	service, err := NewService(newMemoryRepository(), serviceProviderConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	participant, found, err := service.ConsentParticipant(t.Context(), testPrivatePeerID)
	if err != nil || !found {
		t.Fatalf("ConsentParticipant = %+v, %v, %v", participant, found, err)
	}
	want := ConsentParticipant{
		PeerID:          testPrivatePeerID,
		Name:            "Hypotheek BV",
		Active:          true,
		Services:        []ProviderService{{Service: "aangiftegegevens", Purpose: "Hypotheek-aanvraag"}},
		ReturnLocations: []string{"https://hypotheek.example/return"},
	}
	if !reflect.DeepEqual(participant, want) {
		t.Fatalf("ConsentParticipant = %+v, want %+v", participant, want)
	}
}

func TestConsentParticipantForUnseededPeer(t *testing.T) {
	service, err := NewService(newMemoryRepository(), serviceProviderConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.ConsentParticipant(t.Context(), testUnseededPeerID); err != nil || found {
		t.Fatalf("registration without admission: found = %v, err = %v", found, err)
	}
	if err := service.Save(t.Context(), Participant{PeerID: testUnseededPeerID, Name: "Added via UI", AllowedSourcePeerIDs: []string{testBDPeerID}}); err != nil {
		t.Fatal(err)
	}
	participant, found, err := service.ConsentParticipant(t.Context(), testUnseededPeerID)
	if err != nil || !found {
		t.Fatalf("ConsentParticipant = %+v, %v, %v", participant, found, err)
	}
	if participant.Name != "Added via UI" || participant.Active || len(participant.Services) != 1 || participant.ReturnLocations[0] != "https://admin-added.example/return" {
		t.Fatalf("ConsentParticipant = %+v", participant)
	}
}

func TestConsentParticipantWithoutRegistrationHasNoServices(t *testing.T) {
	service := mustTestService(t, newMemoryRepository())
	participant, found, err := service.ConsentParticipant(t.Context(), testEUDIPeerID)
	if err != nil || !found {
		t.Fatalf("ConsentParticipant = %+v, %v, %v", participant, found, err)
	}
	if participant.Services == nil || len(participant.Services) != 0 || participant.ReturnLocations == nil || len(participant.ReturnLocations) != 0 {
		t.Fatalf("ConsentParticipant = %+v, want empty services and return locations", participant)
	}
	for _, peerID := range []string{"99999999999999999999", "not-a-peer-id"} {
		if _, found, err := service.ConsentParticipant(t.Context(), peerID); err != nil || found {
			t.Fatalf("ConsentParticipant(%q): found = %v, err = %v", peerID, found, err)
		}
	}
}

func TestNewServiceRejectsInvalidServiceProviders(t *testing.T) {
	valid := ProviderService{Service: "aangiftegegevens", Purpose: "Hypotheek-aanvraag"}
	tests := map[string][]ServiceProvider{
		"missing peer_id":       {{Services: []ProviderService{valid}}},
		"invalid peer_id":       {{PeerID: "123", Services: []ProviderService{valid}}},
		"no services":           {{PeerID: testPrivatePeerID}},
		"missing service":       {{PeerID: testPrivatePeerID, Services: []ProviderService{{Purpose: "Hypotheek-aanvraag"}}}},
		"missing purpose":       {{PeerID: testPrivatePeerID, Services: []ProviderService{{Service: "aangiftegegevens", Purpose: " "}}}},
		"duplicate service":     {{PeerID: testPrivatePeerID, Services: []ProviderService{valid, valid}}},
		"empty return location": {{PeerID: testPrivatePeerID, ReturnLocations: []string{"https://hypotheek.example/return", " "}, Services: []ProviderService{valid}}},
		"duplicate peer_id": {
			{PeerID: testPrivatePeerID, Services: []ProviderService{valid}},
			{PeerID: testPrivatePeerID, Services: []ProviderService{valid}},
		},
	}
	for name, providers := range tests {
		t.Run(name, func(t *testing.T) {
			configuration := testConfiguration()
			configuration.ServiceProviders = providers
			if _, err := NewService(newMemoryRepository(), configuration); err == nil {
				t.Fatal("NewService accepted an invalid service provider")
			}
		})
	}
}
