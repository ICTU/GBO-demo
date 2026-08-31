package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestExpandRequiredEnvironmentRejectsUnsetVariable(t *testing.T) {
	t.Setenv("GBO_TEST_REQUIRED_ENV", "")
	_, err := expandRequiredEnvironment(`value = "${GBO_TEST_REQUIRED_ENV}"`)
	if err == nil || !strings.Contains(err.Error(), "GBO_TEST_REQUIRED_ENV") {
		t.Fatalf("error = %v, want missing environment variable", err)
	}
}

func TestAttestationStatusPathUsesFullVCTDigest(t *testing.T) {
	const vct = "https://issuer.example/types/99999999900000000200/example/v1"
	var settings strings.Builder
	appendAttestationSettings(&settings, vct, issuanceCertificateMaterial{})
	digest := sha256.Sum256([]byte(vct))
	want := `/tsl/` + hex.EncodeToString(digest[:])
	if !strings.Contains(settings.String(), want) {
		t.Fatalf("generated settings do not contain full status digest %q", want)
	}
}

func TestFormatOfferParameterSupportsFiniteNumbers(t *testing.T) {
	for _, value := range []any{float64(1.25), int64(2), json.Number("3.5")} {
		if _, err := formatOfferParameter("amount", "number", value); err != nil {
			t.Errorf("formatOfferParameter(%v) = %v", value, err)
		}
	}
}
