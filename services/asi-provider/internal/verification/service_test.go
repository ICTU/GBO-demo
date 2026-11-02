package verification

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	asimapping "gbo-demo/asi-mapping"
)

// fakeSource is the BRP as its description makes it known: per BSN the
// answer at result_pointer, as the BRP gives it. With err set it has no
// description; with askErr it gives no answer. asked records each query.
type fakeSource struct {
	asi     *asimapping.Capability
	persons map[string]string
	err     error
	askErr  error
	asked   []string
}

func (f *fakeSource) Description() (*asimapping.Capability, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.asi, nil
}

func (f *fakeSource) Ask(_ context.Context, bsn string, _ *asimapping.Capability, document string, _ []string) (any, error) {
	f.asked = append(f.asked, document)
	if f.askErr != nil {
		return nil, f.askErr
	}
	raw, ok := f.persons[bsn]
	if !ok {
		return nil, nil
	}
	var person any
	if err := json.Unmarshal([]byte(raw), &person); err != nil {
		return nil, err
	}
	return person, nil
}

const (
	bsn           = "999991772"
	bsnTom        = "999993008"
	bsnUnknown    = "999990000"
	attributes    = "https://gbo.example/attributes/"
	idFamilyName  = attributes + "family_name/1"
	idGivenName   = attributes + "given_name/1"
	idBirthDate   = attributes + "birth_date/1"
	idNationality = attributes + "nationality/1"
	idAddress     = attributes + "address/1"
	idDiploma     = attributes + "education_qualification/1"
)

// frouke is the answer of the BRP for Frouke Jansen, a resident; tom's for
// Tom Willems, who is not.
const (
	frouke = `{"__typename": "Ingezetene", "voorvoegsel": null, "geslachtsnaam": "Jansen", "voornamen": "Frouke",
		"geboortedatum": "2000-03-24", "heeftNationaliteit": [{"nationaliteit": "NL"}, {"nationaliteit": "BE"}],
		"woontOp": {"straatnaam": "Coolsingel", "huisnummer": 12, "huisletter": "B", "huisnummertoevoeging": null,
			"postcode": "3011 AD", "woonplaatsnaam": "Rotterdam"}}`
	tom = `{"__typename": "NietIngezetene", "voorvoegsel": null, "geslachtsnaam": "Willems", "voornamen": "Tom",
		"geboortedatum": "1980-01-01", "heeftNationaliteit": [{"nationaliteit": "BE"}]}`
)

// brpASI is the description the BRP mock publishes.
func brpASI(t *testing.T) *asimapping.Capability {
	t.Helper()
	raw, err := os.ReadFile("../../../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Capabilities struct {
			ASI *asimapping.Capability `json:"asi"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Capabilities.ASI == nil {
		t.Fatalf("the BRP publishes no asi: %v", err)
	}
	return document.Capabilities.ASI
}

func testCatalogue(t *testing.T) asimapping.Catalogue {
	t.Helper()
	catalogue, err := asimapping.LoadCatalogue("../../../../schemas/asi-attributes.json")
	if err != nil {
		t.Fatal(err)
	}
	return catalogue
}

// brp is the BRP with Frouke and Tom; edit changes Frouke's answer.
func brp(t *testing.T, edit func(person map[string]any)) *fakeSource {
	t.Helper()
	answer := frouke
	if edit != nil {
		var person map[string]any
		if err := json.Unmarshal([]byte(frouke), &person); err != nil {
			t.Fatal(err)
		}
		edit(person)
		raw, err := json.Marshal(person)
		if err != nil {
			t.Fatal(err)
		}
		answer = string(raw)
	}
	return &fakeSource{asi: brpASI(t), persons: map[string]string{bsn: answer, bsnTom: tom}}
}

func address(person map[string]any) map[string]any { return person["woontOp"].(map[string]any) }

func attribute(id, value string) Attribute {
	return Attribute{Identifier: id, Value: json.RawMessage(value)}
}

func fragment(id, location, value string) Fragment {
	return Fragment{Identifier: id, Location: location, Value: json.RawMessage(value)}
}

const froukesAddress = `{"street":"Coolsingel","house_number":"12 B","postal_code":"3011 AD","city":"Rotterdam","country":"NL"}`

func verify(t *testing.T, src *fakeSource, person string, req VerifyRequest) VerifyResult {
	t.Helper()
	res, err := NewService(src, testCatalogue(t)).Verify(context.Background(), person, req)
	if err != nil {
		t.Fatalf("Verify = %v", err)
	}
	return res
}

func results(res VerifyResult) []string {
	var out []string
	for _, r := range res.Attributes {
		out = append(out, r.Result)
	}
	for _, r := range res.Fragments {
		out = append(out, r.Result)
	}
	return out
}

func TestVerifyFromTheDescription(t *testing.T) {
	t.Run("G1 all five attributes", func(t *testing.T) {
		res := verify(t, brp(t, nil), bsn, VerifyRequest{Attributes: []Attribute{
			attribute(idFamilyName, `{"family_name":"Jansen"}`),
			attribute(idGivenName, `{"given_name":"Frouke"}`),
			attribute(idBirthDate, `{"birth_date":"2000-03-24"}`),
			attribute(idNationality, `{"nationalities":["BE","NL"]}`),
			attribute(idAddress, froukesAddress),
		}})
		if got := results(res); !slices.Equal(got, []string{ResultMatch, ResultMatch, ResultMatch, ResultMatch, ResultMatch}) {
			t.Fatalf("results = %v", got)
		}
	})

	t.Run("G2 the query asks only the requested fields", func(t *testing.T) {
		src := brp(t, nil)
		verify(t, src, bsn, VerifyRequest{Attributes: []Attribute{attribute(idGivenName, `{"given_name":"Frouke"}`)}})
		if len(src.asked) != 1 || !strings.Contains(src.asked[0], "voornamen") ||
			strings.Contains(src.asked[0], "geslachtsnaam") || strings.Contains(src.asked[0], "woontOp") {
			t.Fatalf("asked %q", src.asked)
		}
	})

	t.Run("G3 a fragment needs only its member", func(t *testing.T) {
		src := brp(t, func(p map[string]any) { address(p)["postcode"] = nil })
		res := verify(t, src, bsn, VerifyRequest{Fragments: []Fragment{fragment(idAddress, "$.city", `{"city":"Rotterdam"}`)}})
		if got := results(res); !slices.Equal(got, []string{ResultMatch}) {
			t.Fatalf("results = %v", got)
		}
	})

	t.Run("G4 a fragment on a const member asks nothing", func(t *testing.T) {
		src := brp(t, nil)
		res := verify(t, src, bsn, VerifyRequest{Fragments: []Fragment{fragment(idAddress, "$.country", `{"country":"NL"}`)}})
		if got := results(res); !slices.Equal(got, []string{ResultUnknown}) || len(src.asked) != 0 {
			t.Fatalf("results = %v, asked %q", got, src.asked)
		}
	})

	t.Run("V4 a fragment on a member the attribute does not have", func(t *testing.T) {
		src := brp(t, nil)
		res := verify(t, src, bsn, VerifyRequest{Fragments: []Fragment{fragment(idAddress, "$.county", `{"county":"Zuid-Holland"}`)}})
		if got := results(res); !slices.Equal(got, []string{ResultUnknown}) || len(src.asked) != 0 {
			t.Fatalf("results = %v, asked %q", got, src.asked)
		}
	})

	t.Run("G5 attributes and fragments in one query", func(t *testing.T) {
		src := brp(t, nil)
		res := verify(t, src, bsn, VerifyRequest{
			Attributes: []Attribute{attribute(idFamilyName, `{"family_name":"Jansen"}`), attribute(idAddress, froukesAddress)},
			Fragments:  []Fragment{fragment(idAddress, "$.city", `{"city":"Rotterdam"}`), fragment(idNationality, "$.nationalities[0]", `{"nationalities":"NL"}`)},
		})
		if got := results(res); !slices.Equal(got, []string{ResultMatch, ResultMatch, ResultMatch, ResultMatch}) || len(src.asked) != 1 {
			t.Fatalf("results = %v, asked %d times", got, len(src.asked))
		}
	})

	t.Run("G6 a person the source does not know", func(t *testing.T) {
		res := verify(t, brp(t, nil), bsnUnknown, VerifyRequest{Attributes: []Attribute{attribute(idFamilyName, `{"family_name":"Jansen"}`)}})
		if got := results(res); !slices.Equal(got, []string{ResultUnknown}) {
			t.Fatalf("results = %v", got)
		}
		var notHeld *NotHeldError
		if _, err := NewService(brp(t, nil), testCatalogue(t)).Retrieve(context.Background(), bsnUnknown, []string{idFamilyName}, false); !errors.As(err, &notHeld) {
			t.Fatalf("Retrieve: err = %v, want NotHeldError", err)
		}
	})

	t.Run("G7 the address of a person who is not resident", func(t *testing.T) {
		res := verify(t, brp(t, nil), bsnTom, VerifyRequest{Attributes: []Attribute{attribute(idAddress, froukesAddress)}})
		if got := results(res); !slices.Equal(got, []string{ResultUnknown}) {
			t.Fatalf("results = %v", got)
		}
	})

	t.Run("a value the source does not hold", func(t *testing.T) {
		res := verify(t, brp(t, func(p map[string]any) { p["voornamen"] = nil }), bsn, VerifyRequest{Attributes: []Attribute{attribute(idGivenName, `{"given_name":"Frouke"}`)}})
		if got := results(res); !slices.Equal(got, []string{ResultUnknown}) {
			t.Fatalf("results = %v", got)
		}
	})
}

// An answer the description and the value schema do not allow is an error
// (500), not Unknown.
func TestAnAnswerOfAnotherShapeIsAnError(t *testing.T) {
	var malformed *MalformedError
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		req  VerifyRequest
	}{
		"G8 a date that is not in the calendar": {
			func(p map[string]any) { p["geboortedatum"] = "2000-02-30" },
			VerifyRequest{Attributes: []Attribute{attribute(idBirthDate, `{"birth_date":"2000-03-24"}`)}},
		},
		"G9 a list element that is not an object": {
			func(p map[string]any) { p["heeftNationaliteit"] = []any{42} },
			VerifyRequest{Attributes: []Attribute{attribute(idNationality, `{"nationalities":["NL"]}`)}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewService(brp(t, tc.edit), testCatalogue(t)).Verify(context.Background(), bsn, tc.req)
			if err == nil || errors.As(err, &malformed) || errors.Is(err, ErrSourceUnavailable) {
				t.Fatalf("Verify = %v, want an error of the answer", err)
			}
		})
	}
}

// G10: an attribute the source does not describe is unknown here (404), and
// the source is not asked; the same for an attribute of another source.
func TestAnAttributeTheSourceDoesNotDescribe(t *testing.T) {
	src := brp(t, nil)
	src.asi.Attributes = slices.DeleteFunc(src.asi.Attributes, func(a asimapping.Attribute) bool { return a.Identifier == idGivenName })
	s := NewService(src, testCatalogue(t))
	var unknown *UnknownAttributeError
	for _, id := range []string{idGivenName, idDiploma} {
		if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{
			attribute(idFamilyName, `{"family_name":"Jansen"}`), attribute(id, `{}`),
		}}); !errors.As(err, &unknown) || unknown.Identifier != id {
			t.Fatalf("Verify %s: err = %v, want UnknownAttributeError", id, err)
		}
		if _, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName, id}, false); !errors.As(err, &unknown) {
			t.Fatalf("Retrieve %s: err = %v, want UnknownAttributeError", id, err)
		}
	}
	if len(src.asked) != 0 {
		t.Fatalf("asked %q, want nothing", src.asked)
	}
}

// G12: retrieve returns the attributes as the description builds them.
func TestRetrieve(t *testing.T) {
	s := NewService(brp(t, nil), testCatalogue(t))
	ids := []string{idFamilyName, idGivenName, idBirthDate, idNationality, idAddress}
	attrs, err := s.Retrieve(context.Background(), bsn, ids, false)
	if err != nil || len(attrs) != len(ids) {
		t.Fatalf("Retrieve = %+v, %v", attrs, err)
	}
	want := []string{
		`{"family_name":"Jansen"}`, `{"given_name":"Frouke"}`, `{"birth_date":"2000-03-24"}`, `{"nationalities":["NL","BE"]}`,
		`{"city":"Rotterdam","country":"NL","house_number":"12 B","postal_code":"3011 AD","street":"Coolsingel"}`,
	}
	for i, a := range attrs {
		if a.Identifier != ids[i] || string(a.Value) != want[i] {
			t.Errorf("attribute %d = %s %s, want %s %s", i, a.Identifier, a.Value, ids[i], want[i])
		}
	}
	// One of the attributes has no value: not found, and no partial result
	// (REQ-ASIP-6.1.2.2-06).
	var notHeld *NotHeldError
	s = NewService(brp(t, func(p map[string]any) { p["voornamen"] = nil }), testCatalogue(t))
	if attrs, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName, idGivenName}, false); !errors.As(err, &notHeld) || notHeld.Identifier != idGivenName || attrs != nil {
		t.Fatalf("Retrieve with a missing value = %+v, %v; want NotHeldError for %s", attrs, err, idGivenName)
	}
}

// G13: a new description applies to the next request, without a restart.
func TestANewDescriptionAppliesToTheNextRequest(t *testing.T) {
	src := brp(t, nil)
	s := NewService(src, testCatalogue(t))
	req := VerifyRequest{Attributes: []Attribute{attribute(idGivenName, `{"given_name":"Jansen"}`)}}
	if res, err := s.Verify(context.Background(), bsn, req); err != nil || results(res)[0] != ResultNoMatch {
		t.Fatalf("Verify = %+v, %v; want NoMatch", res, err)
	}
	next := *brpASI(t)
	for i, a := range next.Attributes {
		if a.Identifier == idGivenName {
			next.Attributes[i].Fields = map[string]asimapping.Field{"given_name": {Pointer: "/geslachtsnaam"}}
		}
	}
	src.asi = &next
	if res, err := s.Verify(context.Background(), bsn, req); err != nil || results(res)[0] != ResultMatch {
		t.Fatalf("Verify = %+v, %v; want Match under the new description", res, err)
	}
}

// Variation applies to strings without a format; a date matches exactly.
func TestVariation(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    Attribute
		want string
	}{
		{"F1 a family name in another case", attribute(idFamilyName, `{"family_name":"jansen"}`), ResultVariation},
		{"F2 a date with a space", attribute(idBirthDate, `{"birth_date":"2000-03-24 "}`), ResultNoMatch},
		{"F3 nationalities in another case", attribute(idNationality, `{"nationalities":["nl","BE"]}`), ResultVariation},
		{"F4 an address in another way of writing", attribute(idAddress, `{"street":" coolsingel ","house_number":"12 b","postal_code":"3011 AD","city":"ROTTERDAM","country":"NL"}`), ResultVariation},
		{"a different family name", attribute(idFamilyName, `{"family_name":"Bakker"}`), ResultNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := results(verify(t, brp(t, nil), bsn, VerifyRequest{Attributes: []Attribute{tc.a}})); got[0] != tc.want {
				t.Fatalf("result = %s, want %s", got[0], tc.want)
			}
		})
	}
}

// Malformed before mandate before unknown attribute (REQ-ASIP-6.1.1.2-12).
func TestVerifyErrors(t *testing.T) {
	s := NewService(brp(t, nil), testCatalogue(t))
	ctx := context.Background()
	var malformed *MalformedError
	var unknown *UnknownAttributeError
	if _, err := s.Verify(ctx, bsn, VerifyRequest{Mandate: true}); !errors.As(err, &malformed) {
		t.Fatalf("empty request with mandate: err = %v, want MalformedError", err)
	}
	if _, err := s.Verify(ctx, bsn, VerifyRequest{Attributes: []Attribute{attribute(idDiploma, `{}`)}, Mandate: true}); !errors.Is(err, ErrMandate) {
		t.Fatalf("mandate: err = %v, want ErrMandate", err)
	}
	if _, err := s.Verify(ctx, bsn, VerifyRequest{Attributes: []Attribute{attribute(idDiploma, `{}`)}}); !errors.As(err, &unknown) || unknown.Identifier != idDiploma {
		t.Fatalf("unknown attribute: err = %v, want UnknownAttributeError", err)
	}
	if _, err := s.Verify(ctx, bsn, VerifyRequest{Attributes: []Attribute{attribute(idFamilyName, `"Jansen"`)}}); !errors.As(err, &malformed) {
		t.Fatalf("value not an object: err = %v, want MalformedError", err)
	}
	for _, location := range []string{"city", "$", "$[0]"} {
		if _, err := s.Verify(ctx, bsn, VerifyRequest{Fragments: []Fragment{fragment(idAddress, location, `{"city":"Rotterdam"}`)}}); !errors.As(err, &malformed) {
			t.Fatalf("location %q: err = %v, want MalformedError", location, err)
		}
	}
	var notHeld *NotHeldError
	if _, err := s.Retrieve(ctx, bsn, nil, true); !errors.As(err, &malformed) {
		t.Fatalf("no identifiers: err = %v, want MalformedError", err)
	}
	if _, err := s.Retrieve(ctx, bsn, []string{idFamilyName}, true); !errors.Is(err, ErrMandate) {
		t.Fatalf("mandate: err = %v, want ErrMandate", err)
	}
	if _, err := s.Retrieve(ctx, bsnUnknown, []string{idFamilyName}, false); !errors.As(err, &notHeld) {
		t.Fatalf("unknown person: err = %v, want NotHeldError", err)
	}
}

// A source without a description, or that gives no answer, is an error,
// never Unknown.
func TestSourceErrorIsNotUnknown(t *testing.T) {
	failure := errors.New("refused")
	for _, src := range []*fakeSource{
		{err: ErrSourceUnavailable}, {asi: brpASI(t), askErr: ErrSourceUnavailable}, {asi: brpASI(t), askErr: failure},
	} {
		want := src.err
		if want == nil {
			want = src.askErr
		}
		s := NewService(src, testCatalogue(t))
		ctx := context.Background()
		if _, got := s.Verify(ctx, bsn, VerifyRequest{Attributes: []Attribute{attribute(idFamilyName, `{"family_name":"Jansen"}`)}}); !errors.Is(got, want) {
			t.Fatalf("Verify attribute: err = %v, want %v", got, want)
		}
		if _, got := s.Verify(ctx, bsn, VerifyRequest{Fragments: []Fragment{fragment(idAddress, "$.city", `{"city":"Rotterdam"}`)}}); !errors.Is(got, want) {
			t.Fatalf("Verify fragment: err = %v, want %v", got, want)
		}
		if _, got := s.Retrieve(ctx, bsn, []string{idFamilyName}, false); !errors.Is(got, want) {
			t.Fatalf("Retrieve: err = %v, want %v", got, want)
		}
	}
}
