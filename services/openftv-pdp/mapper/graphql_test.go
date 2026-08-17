package mapping

import (
	"encoding/base64"
	"testing"
)

func TestIsEUDIFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		flow string
		want bool
	}{
		{flow: "eudi:attestation", want: true},
		// Bron-specific variants were retired; the policy denies them
		// (engine_test.rego), so the mapper must not treat them as
		// wallet flows either.
		{flow: "eudi:attestation:brp", want: false},
		{flow: "dvtp:query", want: false},
		{flow: "eudi:attestation-unknown", want: false},
		{flow: "", want: false},
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

// fscToken builds an unsigned JWT carrying the given claims payload. The
// mapper reads the payload without verifying — FSC-Inway validated the
// signature before it called us — so the header and signature are filler.
func fscToken(payloadJSON string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payloadJSON)) + ".sig"
}

func TestFlowFromHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "add claim",
			headers: map[string]string{"fsc-authorization": "Bearer " + fscToken(`{"add":{"flow":"dvtp:query"}}`)},
			want:    "dvtp:query",
		},
		{
			name:    "legacy prp claim",
			headers: map[string]string{"fsc-authorization": "Bearer " + fscToken(`{"prp":{"flow":"eudi:attestation"}}`)},
			want:    "eudi:attestation",
		},
		{
			name:    "no token denies rather than defaulting",
			headers: map[string]string{},
			want:    "",
		},
		{
			name:    "claim without a flow",
			headers: map[string]string{"fsc-authorization": "Bearer " + fscToken(`{"add":{"subject_id_type":"pseudonym"}}`)},
			want:    "",
		},
		{
			name:    "undecodable token",
			headers: map[string]string{"fsc-authorization": "Bearer not-a-jwt"},
			want:    "",
		},
		{
			// The X-GBO-Flow header let a caller name the regime it wanted
			// to be judged under. It is no longer sent and no longer read;
			// this guards against it coming back.
			name:    "X-GBO-Flow header is not honoured",
			headers: map[string]string{"x-gbo-flow": "dvtp:query"},
			want:    "",
		},
		{
			name: "header does not override the claim",
			headers: map[string]string{
				"fsc-authorization": "Bearer " + fscToken(`{"add":{"flow":"eudi:attestation"}}`),
				"x-gbo-flow":        "dvtp:query",
			},
			want: "eudi:attestation",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := flowFromHeaders(tt.headers); got != tt.want {
				t.Fatalf("flowFromHeaders() = %q, want %q", got, tt.want)
			}
		})
	}
}
