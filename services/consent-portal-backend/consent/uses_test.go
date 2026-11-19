package consent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTheOwnerSeesTheUsesOfAConsent(t *testing.T) {
	p, _, store := testPortal(t, nil)
	ctx := context.Background()
	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	store.uses = map[string]Usage{granted.ConsentID: {Uses: []Use{{UsedAt: at, ServiceProviderOIN: "DV", Source: "Belastingdienst"}}}}

	usage, err := p.ConsentUses(ctx, BSN("111111111"), granted.ConsentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Uses) != 1 || usage.Uses[0].Source != "Belastingdienst" {
		t.Fatalf("usage = %+v", usage)
	}
}

// Another citizen's consent is not found rather than forbidden (#602): the
// caller learns nothing about consent IDs that are not theirs.
func TestTheUsesOfAnotherCitizensConsentAreNotFound(t *testing.T) {
	p, _, store := testPortal(t, nil)
	ctx := context.Background()
	granted, err := p.GiveConsent(ctx, BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	store.usesErr = errors.New("must not be asked")

	_, err = p.ConsentUses(ctx, BSN("222222222"), granted.ConsentID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTheUsesOfAnUnknownConsentAreNotFound(t *testing.T) {
	p, _, _ := testPortal(t, nil)
	if _, err := p.ConsentUses(context.Background(), BSN("111111111"), "c-nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
