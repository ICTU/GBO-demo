package mapping

import "testing"

func TestIsEUDIFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		flow string
		want bool
	}{
		{flow: "eudi:attestation", want: true},
		{flow: "eudi:attestation:brp", want: true},
		{flow: "dvtp:query", want: false},
		{flow: "eudi:attestation-unknown", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.flow, func(t *testing.T) {
			t.Parallel()
			if got := isEUDIFlow(tt.flow); got != tt.want {
				t.Fatalf("isEUDIFlow(%q) = %v, want %v", tt.flow, got, tt.want)
			}
		})
	}
}
