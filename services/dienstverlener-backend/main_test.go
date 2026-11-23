package main

import "testing"

func TestTheConsentClientIDMustBeAPeerID(t *testing.T) {
	t.Setenv("CONSENT_REDIRECT_URI", "http://localhost:9001/return")
	t.Setenv("CONSENT_PORTAL_URL", "http://localhost:9002")
	t.Setenv("CONSENT_AUTHORIZATION_URL", "http://hv-outway:8080/consent-authorization")
	for id, ok := range map[string]bool{
		"99999999900000000300":   true,
		"hv peer":                false,
		"9999;x=1":               false,
		"99999999900000000300\n": false,
	} {
		t.Setenv("CONSENT_CLIENT_ID", id)
		if _, err := loadConfig(); (err == nil) != ok {
			t.Errorf("CONSENT_CLIENT_ID %q: err = %v", id, err)
		}
	}
}
