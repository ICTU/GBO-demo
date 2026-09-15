package mapping

import (
	"encoding/json"
	"strings"
	"testing"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

// ── The mapper only maps ──────────────────────────────────────────────────
// #364 and #330: the request-mapper does GraphQL mapping and nothing else.
// It calls neither BSNk nor the consent register and rewrites no
// identifier, so the request reaches the policy as it was sent — keeping a
// plain BSN out of the decision logs is #368. It adds no pip at all: the
// policy resolves the consent from the token itself (policies/dvtp/gbo/
// consent.rego) and reads the PID regime from the request.

const plainBSN = "999991772"

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

func subjectVariable(parc *models.PARC) any {
	resource, _ := parc.Context.GetAttributeValue("resource").(map[string]any)
	variables, _ := resource["variables"].(map[string]any)
	return variables["bsn"]
}

func TestMapperLeavesAPlainBSNUntouched(t *testing.T) {
	out := GraphQLToContext(graphQLRequest(t, map[string]string{}, map[string]any{"bsn": plainBSN}))
	if got := subjectVariable(out); got != plainBSN {
		t.Fatalf("subject variable = %#v, want the plain BSN as sent", got)
	}
	if pip := out.Context.GetAttributeValue("pip"); pip != nil {
		t.Fatalf("pip = %#v, want none without a consent token", pip)
	}
	if body, _ := out.Action.Attributes().GetAttributeValue(models.AttrBody).(string); !strings.Contains(body, plainBSN) {
		t.Fatalf("action body = %q, want the request as sent", body)
	}
}

// Under a consent token the mapper resolves nothing either: the token
// travels on to the policy with the request headers, and the policy
// verifies it.
func TestMapperResolvesNoConsentUnderAConsentToken(t *testing.T) {
	out := GraphQLToContext(graphQLRequest(t,
		map[string]string{"X-GBO-Consent-Token": "ey.some.token"},
		map[string]any{"bsn": "PI-other"},
	))
	if got := subjectVariable(out); got != "PI-other" {
		t.Fatalf("subject variable = %#v, want it as sent", got)
	}
	if pip := out.Context.GetAttributeValue("pip"); pip != nil {
		t.Fatalf("pip = %#v, want none: the policy resolves the consent", pip)
	}
	headers, _ := out.Context.GetAttributeValue(models.AttrHeaders).(map[string]string)
	if headers["X-GBO-Consent-Token"] != "ey.some.token" {
		t.Fatalf("headers = %#v, want the consent token passed on to the policy", headers)
	}
}
