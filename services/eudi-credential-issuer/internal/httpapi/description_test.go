package httpapi

import "testing"

func TestDescriptionKeepsAllowedCharacters(t *testing.T) {
	in := "typ \"dc+sd-jwt\" \\ é\ttab~"
	if got, want := description(in), "typ dc+sd-jwt  tab~"; got != want {
		t.Fatalf("description(%q) = %q, want %q", in, got, want)
	}
}
