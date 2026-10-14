package main

import "testing"

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
