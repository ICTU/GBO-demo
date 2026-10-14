// Package brpsource is the authentic source through the GBO chain: the BRP,
// asked over FSC through the ASIP's own Outway and grant-link. The Inway's
// PDP judges each query (rule ASI0001). It implements verification.Source.
//
// Per request the ASIP writes one query of its own, with only the fields the
// requested attributes need, and the BSN from the access token as a
// variable: one FSC transaction and one PDP decision per request.
package brpsource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"gbo-demo/asi-provider/internal/tracing"
	"gbo-demo/asi-provider/internal/verification"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseBody = 1 << 20
)

// selections holds per catalogue attribute the fields of IngeschrevenPersoon
// it is made of. The address is a resident's: a non-resident's address has
// free lines, not the attribute's members, and yields no value.
var selections = map[string]string{
	"family_name": "voorvoegsel geslachtsnaam",
	"given_name":  "voornamen",
	"birth_date":  "geboortedatum",
	"nationality": "heeftNationaliteit { nationaliteit }",
	"address":     "... on Ingezetene { woontOp { straatnaam huisnummer huisletter huisnummertoevoeging postcode woonplaatsnaam } }",
}

// Attributes returns the names of the attributes this adapter can ask the
// BRP for.
func Attributes() []string {
	names := make([]string, 0, len(selections))
	for name := range selections {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Source asks the BRP through the Outway.
type Source struct {
	endpoint string
	client   *http.Client
}

// New returns a source that posts its queries to outwayURL plus path, the
// grant-link of the ASIP's contract with the BRP service.
func New(outwayURL, path string) *Source {
	return &Source{
		endpoint: strings.TrimRight(outwayURL, "/") + "/" + strings.TrimLeft(path, "/"),
		client:   &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

// Values asks the BRP once for the attributes with names of the person with
// bsn. A person the BRP does not know, or a field it holds no value for, is
// no value: the name is absent.
func (s *Source) Values(ctx context.Context, bsn string, names []string) (map[string]json.RawMessage, error) {
	var parts []string
	for _, name := range names {
		selection, ok := selections[name]
		if !ok {
			return nil, fmt.Errorf("no BRP query for attribute %q", name)
		}
		if !slices.Contains(parts, selection) {
			parts = append(parts, selection)
		}
	}
	p, err := s.query(ctx, bsn, names, strings.Join(parts, " "))
	if err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{}
	if p == nil {
		return values, nil
	}
	for _, name := range names {
		value, ok := p.attribute(name)
		if !ok {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		values[name] = raw
	}
	return values, nil
}

// person is the part of IngeschrevenPersoon the selections ask for.
type person struct {
	Geslachtsnaam      string `json:"geslachtsnaam"`
	Voorvoegsel        string `json:"voorvoegsel"`
	Voornamen          string `json:"voornamen"`
	Geboortedatum      string `json:"geboortedatum"`
	HeeftNationaliteit []struct {
		Nationaliteit string `json:"nationaliteit"`
	} `json:"heeftNationaliteit"`
	WoontOp *struct {
		Straatnaam           string `json:"straatnaam"`
		Huisnummer           int    `json:"huisnummer"`
		Huisletter           string `json:"huisletter"`
		Huisnummertoevoeging string `json:"huisnummertoevoeging"`
		Postcode             string `json:"postcode"`
		Woonplaatsnaam       string `json:"woonplaatsnaam"`
	} `json:"woontOp"`
}

// attribute maps the BRP fields to the attribute's JSON object.
func (p *person) attribute(name string) (any, bool) {
	switch name {
	case "family_name":
		if p.Geslachtsnaam == "" {
			return nil, false
		}
		return map[string]string{"family_name": joinNonEmpty(p.Voorvoegsel, p.Geslachtsnaam)}, true
	case "given_name":
		return map[string]string{"given_name": p.Voornamen}, p.Voornamen != ""
	case "birth_date":
		return map[string]string{"birth_date": p.Geboortedatum}, p.Geboortedatum != ""
	case "nationality":
		codes := []string{}
		for _, n := range p.HeeftNationaliteit {
			codes = append(codes, n.Nationaliteit)
		}
		return map[string][]string{"nationalities": codes}, len(codes) > 0
	case "address":
		// Every member of the attribute is required: an address the BRP
		// holds in part (postcode is nullable) is no value, not a partial
		// one. Letter and addition are optional parts of the number.
		a := p.WoontOp
		if a == nil || a.Straatnaam == "" || a.Huisnummer == 0 || a.Postcode == "" || a.Woonplaatsnaam == "" {
			return nil, false
		}
		return map[string]string{
			"street":       a.Straatnaam,
			"house_number": joinNonEmpty(strconv.Itoa(a.Huisnummer), a.Huisletter, a.Huisnummertoevoeging),
			"postal_code":  a.Postcode,
			"city":         a.Woonplaatsnaam,
			"country":      "NL",
		}, true
	}
	return nil, false
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// query posts one GraphQL query through the Outway and returns the person,
// nil when the BRP does not know the BSN. Errors name the status, never the
// BSN, so they can be logged. Its span names the FSC transaction and the
// attributes asked for, never the BSN or a value.
func (s *Source) query(ctx context.Context, bsn string, names []string, selection string) (_ *person, err error) {
	txID, err := tracing.TransactionID(ctx)
	if err != nil {
		return nil, err
	}
	ctx, span := otel.Tracer("asi-provider/brpsource").Start(ctx, "BRP query", trace.WithAttributes(
		attribute.String(tracing.TransactionIDAttribute, txID),
		attribute.StringSlice("gbo.asi.attributes", names),
	))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	body, err := json.Marshal(map[string]any{
		"query":     "query AsiAttributes($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { " + selection + " } }",
		"variables": map[string]string{"bsn": bsn},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Fsc-Transaction-Id", txID)
	// The instrumented client adds the traceparent of its span, in this trace.

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", verification.ErrSourceUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", verification.ErrSourceUnavailable, err)
	}
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, fmt.Errorf("%w: status %d (Fsc-Transaction-Id %s)", verification.ErrSourceUnavailable, resp.StatusCode, txID)
	case resp.StatusCode != http.StatusOK:
		// The Inway answers a PDP denial with 401; any refusal is a fault
		// in the chain's configuration, not a missing value.
		return nil, fmt.Errorf("refused by the chain: status %d, code %q (Fsc-Transaction-Id %s)", resp.StatusCode, fscCode(raw), txID)
	}
	var out struct {
		Data struct {
			IngeschrevenPersoon *person `json:"ingeschrevenPersoon"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode BRP response (Fsc-Transaction-Id %s): %w", txID, err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("BRP answered %d GraphQL errors (Fsc-Transaction-Id %s)", len(out.Errors), txID)
	}
	return out.Data.IngeschrevenPersoon, nil
}

// fscCode is the code of an FSC error body, empty for any other body.
func fscCode(raw []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Code
}

var _ verification.Source = (*Source)(nil)
