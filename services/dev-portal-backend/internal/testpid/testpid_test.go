package testpid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

type fakeIssuer struct {
	calls []OfferRequest
	links map[string]string
	err   error
}

func (f *fakeIssuer) Offer(_ context.Context, req OfferRequest) (map[string]string, error) {
	f.calls = append(f.calls, req)
	return f.links, f.err
}

var frouke = Person{BSN: "999991772", Name: "Frouke Jansen"}

// K1: the persons are the BRP mock persons, named as on a PID.
func TestPersonsAreTheBRPMockPersons(t *testing.T) {
	persons, err := ParsePersons([]byte(`[
		{"bsn": "999991772", "voornamen": "Frouke", "geslachtsnaam": "Jansen", "geboortedatum": "2000-03-24"},
		{"bsn": "111222333", "voornamen": "Jan", "voorvoegsel": "de", "geslachtsnaam": "Vries"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Person{frouke, {BSN: "111222333", Name: "Jan de Vries"}}
	if !reflect.DeepEqual(persons, want) {
		t.Errorf("persons = %v, want %v", persons, want)
	}
}

// K1: the mock the issuer's test-pid-source reads parses as it is.
func TestTheDemoMockParses(t *testing.T) {
	data, err := os.ReadFile("../../../brp-graphql-server/mockdata/personen.json")
	if err != nil {
		t.Fatal(err)
	}
	persons, err := ParsePersons(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(persons) == 0 || persons[0] != frouke {
		t.Errorf("persons = %v, want Frouke Jansen first", persons)
	}
}

// K6: a mock in which a person cannot be told apart is no list to offer.
func TestAPersonWithoutOrWithADuplicateBSNIsRefused(t *testing.T) {
	for name, data := range map[string]string{
		"no bsn":    `[{"voornamen": "Frouke", "geslachtsnaam": "Jansen"}]`,
		"duplicate": `[{"bsn": "999991772"}, {"bsn": "999991772"}]`,
		"not json":  `{`,
	} {
		if _, err := ParsePersons([]byte(data)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// K2: a test person gets the offer link of the test PID on the eudi profile.
func TestATestPersonGetsTheOfferLink(t *testing.T) {
	issuer := &fakeIssuer{links: map[string]string{"openid-credential-offer": "openid-credential-offer://?x", "haip-vci": "haip-vci://?x"}}
	link, err := New([]Person{frouke}, issuer).Offer(context.Background(), frouke.BSN)
	if err != nil {
		t.Fatal(err)
	}
	if link != "openid-credential-offer://?x" {
		t.Errorf("link = %q", link)
	}
	want := []OfferRequest{{Profile: "eudi", ConfigurationIDs: []string{"test-pid-pid"}, BSN: frouke.BSN}}
	if !reflect.DeepEqual(issuer.calls, want) {
		t.Errorf("issuer asked %v, want %v", issuer.calls, want)
	}
}

// K3: any other BSN is refused before the issuer is asked.
func TestAnUnknownBSNIsRefusedWithoutAskingTheIssuer(t *testing.T) {
	issuer := &fakeIssuer{links: map[string]string{"openid-credential-offer": "openid-credential-offer://?x"}}
	_, err := New([]Person{frouke}, issuer).Offer(context.Background(), "123456782")
	if !errors.Is(err, ErrUnknownPerson) {
		t.Errorf("err = %v, want unknown_person", err)
	}
	if len(issuer.calls) != 0 {
		t.Errorf("issuer asked %v, want no call", issuer.calls)
	}
}

// K4: each failure at the issuer has its own reason.
func TestIssuerFailuresHaveTheirReason(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want Error
	}{
		{&Refusal{Status: 400, Code: "unknown_configuration"}, ErrNotConfigured},
		{&Refusal{Status: 400, Code: "unknown_profile"}, ErrNotConfigured},
		{&Refusal{Status: 404, Code: "no_data"}, ErrNoData},
		{&Refusal{Status: 503, Code: "source_unavailable"}, ErrSourceUnavailable},
		{&Refusal{Status: 502, Code: "bad_source_data"}, ErrBadSourceData},
		{&Refusal{Status: 500, Code: "server_error"}, ErrIssuer},
		{&Refusal{Status: 502}, ErrIssuer},
		{fmt.Errorf("dial: %w", ErrUnreachable), ErrIssuerUnavailable},
		{errors.New("unreadable answer"), ErrIssuer},
	} {
		_, err := New([]Person{frouke}, &fakeIssuer{err: tc.err}).Offer(context.Background(), frouke.BSN)
		var got Error
		if !errors.As(err, &got) || got != tc.want {
			t.Errorf("issuer %v: err = %v, want %s", tc.err, err, tc.want)
		}
	}
}

// K5: an offer without the link a wallet scans is no offer.
func TestAnOfferWithoutTheLinkIsAnIssuerError(t *testing.T) {
	issuer := &fakeIssuer{links: map[string]string{"haip-vci": "haip-vci://?x"}}
	_, err := New([]Person{frouke}, issuer).Offer(context.Background(), frouke.BSN)
	if !errors.Is(err, ErrIssuer) {
		t.Errorf("err = %v, want issuer_error", err)
	}
}
