package mapping

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

func TestHasConsentEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{name: "consent token present", headers: map[string]string{"x-gbo-consent-token": "ey.some.token"}, want: true},
		{name: "no headers falls through to the PID regime", headers: map[string]string{}, want: false},
		{name: "empty value is not evidence", headers: map[string]string{"x-gbo-consent-token": ""}, want: false},
		{
			// The regime is no longer named by the caller. X-GBO-Flow was
			// removed for letting a caller pick what it was judged under,
			// and the FSC grant property that replaced it is gone too
			// (#334); neither may come back through this door.
			name: "a named flow does not select the regime",
			headers: map[string]string{
				"x-gbo-flow":        "dvtp:query",
				"fsc-authorization": "Bearer ey.prp.flow",
			},
			want: false,
		},
		{
			// A subject variable cannot discriminate — both regimes carry
			// one — so nothing about the query changes this answer.
			name:    "scope header alone is not consent evidence",
			headers: map[string]string{"x-gbo-scope": "bd:ib:2025"},
			want:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := hasConsentEvidence(tt.headers); got != tt.want {
				t.Fatalf("hasConsentEvidence(%v) = %v, want %v", tt.headers, got, tt.want)
			}
		})
	}
}

// ── Identifier scrubbing ──────────────────────────────────────────────────
// No raw BSN may reach OPA's input, and with it the decision log, whichever
// regime a request claims. The consent header decides HOW the subject
// identifier is made safe — kept only when it is a pseudonym, pseudonymised
// in the PID regime — never WHETHER.

const (
	demoBSN = "123456789"
	demoPI  = "PI-2f1a7c9b40e6d853"
)

// graphQLRequest builds the PARC the PEP hands the mapper: the GraphQL body
// on the action, the request headers on the context.
func graphQLRequest(t *testing.T, headers map[string]string, variables map[string]any) *models.PARC {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"query":     `query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { heeftBelastingjaarAangifte(belastingjaren: [2025]) { belastingjaar } } }`,
		"variables": variables,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &models.PARC{
		Principal: models.NewEntity("org", "99999999900000000300", models.NewAttributeSet()),
		Action:    models.NewEntity("name", "POST", models.NewAttributeSet(models.NewAttribute(models.AttrBody, string(body)))),
		Resource:  models.NewEntity("service", "bri", models.NewAttributeSet()),
		Context:   models.NewAttributeSet(models.NewAttribute(models.AttrHeaders, headers)),
	}
}

// decisionInput serialises everything the mapper hands on to OPA, so a test
// can assert what never appears in it.
func decisionInput(t *testing.T, parc *models.PARC) string {
	t.Helper()
	out, err := json.Marshal(parc)
	if err != nil {
		t.Fatal(err)
	}
	// Guard against a vacuous negative check: the serialisation must carry
	// the request itself, or "BSN absent" would prove nothing.
	if !strings.Contains(string(out), "ingeschrevenPersoon") {
		t.Fatalf("decision input does not carry the request: %s", out)
	}
	return string(out)
}

func subjectVariable(parc *models.PARC) any {
	resource, _ := parc.Context.GetAttributeValue("resource").(map[string]any)
	variables, _ := resource["variables"].(map[string]any)
	return variables["bsn"]
}

func pidPI(parc *models.PARC) any {
	pip, _ := parc.Context.GetAttributeValue("pip").(map[string]any)
	pid, _ := pip["pid"].(map[string]any)
	return pid["pi"]
}

func bsnkTestServer(t *testing.T, status int, pi string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pseudonymize" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"pi": pi})
	}))
	t.Cleanup(server.Close)
	t.Setenv("GBO_BSNK_URL", server.URL)
}

// bsnkMustNotBeCalled fails the test on any BSNk request: under a consent
// token the mapper resolves nothing, and pseudonymising there would turn a
// BSN into a PI that satisfies the consent's constraint binding.
func bsnkMustNotBeCalled(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected BSNk call under a consent token: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv("GBO_BSNK_URL", server.URL)
}

func consentRequest(t *testing.T, subject string) *models.PARC {
	t.Helper()
	return graphQLRequest(t,
		map[string]string{"X-GBO-Consent-Token": "ey.some.token"},
		map[string]any{"bsn": subject},
	)
}

// Review of #363: an unverifiable consent header switched scrubbing off, so
// an EUDI request carrying one kept its raw BSN in the action body, the
// resource variables and the resolved arguments.
func TestConsentHeaderDoesNotBypassBSNScrubbing(t *testing.T) {
	bsnkMustNotBeCalled(t)
	out := GraphQLToContext(graphQLRequest(t,
		map[string]string{"X-GBO-Consent-Token": "invalid"},
		map[string]any{"bsn": demoBSN},
	))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
}

// A pseudonym is kept under a consent token: it is what DVT0001's
// constraint binding compares against the verified token, in the policy.
func TestConsentRegimeKeepsAPseudonymousSubject(t *testing.T) {
	bsnkMustNotBeCalled(t)
	out := GraphQLToContext(consentRequest(t, demoPI))
	if got := subjectVariable(out); got != demoPI {
		t.Fatalf("subject variable = %#v, want the pseudonym kept", got)
	}
}

// Anything that is not a pseudonym is blanked, not pseudonymised:
// pseudonymising a BSN yields that citizen's PI, which would then satisfy
// DVT0001's constraint binding for a consumer that was never meant to hold
// the BSN. Blanked, the binding fails as it does on main.
func TestConsentRegimeBlanksAnyOtherSubject(t *testing.T) {
	for _, subject := range []string{demoBSN, "PI-" + demoBSN, "pi-2f1a7c9b40e6d853", demoPI + " "} {
		t.Run(subject, func(t *testing.T) {
			bsnkMustNotBeCalled(t)
			out := GraphQLToContext(consentRequest(t, subject))
			if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
				t.Fatalf("raw BSN reached the decision input: %s", in)
			}
			if got := subjectVariable(out); got != "" {
				t.Fatalf("subject variable = %#v, want it blanked", got)
			}
		})
	}
}

// The consent is the policy's to resolve (#330). The mapper sets no pip at
// all under a consent token, so nothing it fills in can pass for a verified
// attribute.
func TestConsentRegimeResolvesNoConsent(t *testing.T) {
	bsnkMustNotBeCalled(t)
	out := GraphQLToContext(consentRequest(t, demoPI))
	if attr := out.Context.GetAttribute("pip"); attr != nil {
		t.Fatalf("mapper set pip under a consent token: %#v", attr.Value())
	}
}

func TestPIDRegimePseudonymisesTheBSN(t *testing.T) {
	bsnkTestServer(t, http.StatusOK, demoPI)
	out := GraphQLToContext(graphQLRequest(t, map[string]string{}, map[string]any{"bsn": demoBSN}))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
	if got := pidPI(out); got != demoPI {
		t.Fatalf("pip.pid.pi = %#v", got)
	}
	if got := subjectVariable(out); got != demoPI {
		t.Fatalf("subject variable = %#v, want the PI substituted", got)
	}
}

// A BSNk failure scrubs the identifier rather than passing it through. The
// policy still sees that PID enrichment was attempted (pip.pid is present),
// which is what keeps the deny reason PID_NOT_PRESENT.
func TestFailedPseudonymisationScrubsRatherThanPassesThrough(t *testing.T) {
	bsnkTestServer(t, http.StatusInternalServerError, "")
	out := GraphQLToContext(graphQLRequest(t, map[string]string{}, map[string]any{"bsn": demoBSN}))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
	if got := pidPI(out); got != "" {
		t.Fatalf("pip.pid.pi = %#v, want empty", got)
	}
}
