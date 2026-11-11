package main

import (
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

func TestParseResources(t *testing.T) {
	got, err := parseResources(" http://asip/brp/authsrc-api/ = Basisregistratie Personen (mock) ,http://asip/duo/authsrc-api=DUO")
	if err != nil || len(got) != 2 || got[0].URL != "http://asip/brp/authsrc-api" || got[0].Name != "Basisregistratie Personen (mock)" || got[1].Name != "DUO" {
		t.Fatalf("parseResources = %+v, %v", got, err)
	}
	// The user sees the name when approving: a data service without one,
	// or none at all, stops the server.
	for _, value := range []string{"http://asip/brp/authsrc-api", "=BRP", "", " , "} {
		if _, err := parseResources(value); err == nil {
			t.Errorf("parseResources(%q): err = nil", value)
		}
	}
}

func TestParseIssuanceResources(t *testing.T) {
	got, err := parseIssuanceResources(" https://issuer/belastingdienst/eudi/ = NTRNL-99990001 = Belastingdienst ,https://issuer/rvig/eudi=NTRNL-99990002=RvIG")
	want := []authorization.Resource{
		{URL: "https://issuer/belastingdienst/eudi", Name: "Belastingdienst", Kind: authorization.ResourceIssuance, Organization: "NTRNL-99990001"},
		{URL: "https://issuer/rvig/eudi", Name: "RvIG", Kind: authorization.ResourceIssuance, Organization: "NTRNL-99990002"},
	}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseIssuanceResources = %+v, %v", got, err)
	}
	if got, err := parseIssuanceResources(""); err != nil || len(got) != 0 {
		t.Fatalf("parseIssuanceResources of nothing = %+v, %v; want none", got, err)
	}
	for _, value := range []string{"https://issuer/x=NTRNL-1", "https://issuer/x==name", "=NTRNL-1=name"} {
		if _, err := parseIssuanceResources(value); err == nil {
			t.Errorf("parseIssuanceResources(%q) accepted", value)
		}
	}
}

func TestBoolEnv(t *testing.T) {
	const name = "TEST_AZSP_BOOLEAN"
	t.Setenv(name, "")
	if got, err := boolEnv(name, false); err != nil || got {
		t.Fatalf("boolEnv unset = %t, %v; want false", got, err)
	}
	t.Setenv(name, "true")
	if got, err := boolEnv(name, false); err != nil || !got {
		t.Fatalf("boolEnv true = %t, %v; want true", got, err)
	}
	t.Setenv(name, "sometimes")
	if _, err := boolEnv(name, false); err == nil {
		t.Fatal("boolEnv accepted an invalid boolean")
	}
}
