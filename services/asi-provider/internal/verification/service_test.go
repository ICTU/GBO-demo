package verification

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// fakeSource is an in-memory source: BSN -> attribute name -> value. With
// err set it fails every call; calls records the names of each call.
type fakeSource struct {
	values map[string]map[string]string
	err    error
	calls  [][]string
}

func (f *fakeSource) Values(_ context.Context, bsn string, names []string) (map[string]json.RawMessage, error) {
	f.calls = append(f.calls, names)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]json.RawMessage{}
	for _, name := range names {
		if v, ok := f.values[bsn][name]; ok {
			out[name] = json.RawMessage(v)
		}
	}
	return out, nil
}

const (
	bsn          = "999991772"
	attributes   = "https://gbo.example/attributes/"
	idFamilyName = attributes + "family_name/1"
	idGivenName  = attributes + "given_name/1"
	idAddress    = attributes + "address/1"
	idDiploma    = attributes + "education_qualification/1"
	idNotInCat   = attributes + "shoe_size/1"
)

// catalogue is the BRP's: the diploma is another source's.
var catalogue = Catalogue{
	idFamilyName: {Name: "family_name"},
	idGivenName:  {Name: "given_name"},
	idAddress:    {Name: "address"},
}

func newTestService() *Service {
	return NewService(&fakeSource{values: map[string]map[string]string{bsn: {
		"family_name": `{"family_name":"Jansen"}`,
		"address":     `{"city":"Rotterdam"}`,
	}}}, catalogue)
}

func verifyOne(t *testing.T, s *Service, id, value string) string {
	t.Helper()
	res, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{{Identifier: id, Value: json.RawMessage(value)}}})
	if err != nil || len(res.Attributes) != 1 || res.Attributes[0].Identifier != id {
		t.Fatalf("Verify = %+v, %v; want one result for %s", res, err, id)
	}
	return res.Attributes[0].Result
}

func TestVerifyResults(t *testing.T) {
	s := newTestService()
	for _, tc := range []struct{ name, id, value, want string }{
		{"equal", idFamilyName, `{"family_name":"Jansen"}`, ResultMatch},
		{"documented variation", idFamilyName, `{"family_name":" JANSEN "}`, ResultVariation},
		{"different", idFamilyName, `{"family_name":"Bakker"}`, ResultNoMatch},
		{"no value for this person", idGivenName, `{"given_name":"Frouke"}`, ResultUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyOne(t, s, tc.id, tc.value); got != tc.want {
				t.Fatalf("result = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestVerifyFragment(t *testing.T) {
	res, err := newTestService().Verify(context.Background(), bsn, VerifyRequest{Fragments: []Fragment{{Identifier: idAddress, Location: "$.city", Value: json.RawMessage(`{"city":"Rotterdam"}`)}}})
	if err != nil || len(res.Fragments) != 1 || res.Fragments[0].Result != ResultMatch {
		t.Fatalf("Verify = %+v, %v; want one Match for the fragment", res, err)
	}
}

// Malformed before mandate before unknown attribute (REQ-ASIP-6.1.1.2-12).
func TestVerifyErrors(t *testing.T) {
	s := newTestService()
	var malformed *MalformedError
	var unknown *UnknownAttributeError
	if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Mandate: true}); !errors.As(err, &malformed) {
		t.Fatalf("empty request with mandate: err = %v, want MalformedError", err)
	}
	if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idNotInCat, Value: json.RawMessage(`{}`)}}, Mandate: true}); !errors.Is(err, ErrMandate) {
		t.Fatalf("mandate: err = %v, want ErrMandate", err)
	}
	if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idNotInCat, Value: json.RawMessage(`{}`)}}}); !errors.As(err, &unknown) || unknown.Identifier != idNotInCat {
		t.Fatalf("unknown attribute: err = %v, want UnknownAttributeError", err)
	}
	if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idFamilyName, Value: json.RawMessage(`"Jansen"`)}}}); !errors.As(err, &malformed) {
		t.Fatalf("value not an object: err = %v, want MalformedError", err)
	}
}

func TestRetrieve(t *testing.T) {
	s := newTestService()
	attrs, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName}, false)
	if err != nil || len(attrs) != 1 || attrs[0].Identifier != idFamilyName || string(attrs[0].Value) != `{"family_name":"Jansen"}` {
		t.Fatalf("Retrieve = %+v, %v; want the family name", attrs, err)
	}
	// R3: one of the attributes has no value: not found, and no partial
	// result (REQ-ASIP-6.1.2.2-06).
	var notHeld *NotHeldError
	if attrs, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName, idGivenName}, false); !errors.As(err, &notHeld) || notHeld.Identifier != idGivenName || attrs != nil {
		t.Fatalf("Retrieve with a missing value = %+v, %v; want NotHeldError for %s", attrs, err, idGivenName)
	}
	var malformed *MalformedError
	var unknown *UnknownAttributeError
	if _, err := s.Retrieve(context.Background(), bsn, nil, true); !errors.As(err, &malformed) {
		t.Fatalf("no identifiers: err = %v, want MalformedError", err)
	}
	if _, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName}, true); !errors.Is(err, ErrMandate) {
		t.Fatalf("mandate: err = %v, want ErrMandate", err)
	}
	if _, err := s.Retrieve(context.Background(), bsn, []string{idNotInCat}, false); !errors.As(err, &unknown) {
		t.Fatalf("unknown attribute: err = %v, want UnknownAttributeError", err)
	}
}

// An attribute of another source is not in this catalogue: 404, and the
// source is not asked.
func TestAnotherSourcesAttributeIsUnknownHere(t *testing.T) {
	src := &fakeSource{}
	s := NewService(src, catalogue)
	var unknown *UnknownAttributeError
	if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idDiploma, Value: json.RawMessage(`{"qualification":"MSc"}`)}}}); !errors.As(err, &unknown) {
		t.Fatalf("Verify: err = %v, want UnknownAttributeError", err)
	}
	if _, err := s.Retrieve(context.Background(), bsn, []string{idDiploma}, false); !errors.As(err, &unknown) {
		t.Fatalf("Retrieve: err = %v, want UnknownAttributeError", err)
	}
	if len(src.calls) != 0 {
		t.Fatalf("source asked for %v, want nothing", src.calls)
	}
}

// One request asks the source once, for the attributes of its attributes
// and fragments together, each name once (Q1, Q4, Q5).
func TestOneQuestionToTheSourcePerRequest(t *testing.T) {
	src := &fakeSource{values: map[string]map[string]string{bsn: {
		"family_name": `{"family_name":"Jansen"}`,
		"address":     `{"city":"Rotterdam"}`,
	}}}
	s := NewService(src, catalogue)
	res, err := s.Verify(context.Background(), bsn, VerifyRequest{
		Attributes: []Attribute{
			{Identifier: idFamilyName, Value: json.RawMessage(`{"family_name":"Jansen"}`)},
			{Identifier: idGivenName, Value: json.RawMessage(`{"given_name":"Frouke"}`)},
			{Identifier: idAddress, Value: json.RawMessage(`{"city":"Rotterdam"}`)},
		},
		Fragments: []Fragment{{Identifier: idAddress, Location: "$.city", Value: json.RawMessage(`{"city":"Rotterdam"}`)}},
	})
	if err != nil || len(src.calls) != 1 || !slices.Equal(src.calls[0], []string{"family_name", "given_name", "address"}) {
		t.Fatalf("Verify = %v; calls %v, want one call for family_name, given_name and address", err, src.calls)
	}
	got := []string{res.Attributes[0].Result, res.Attributes[1].Result, res.Attributes[2].Result, res.Fragments[0].Result}
	if !slices.Equal(got, []string{ResultMatch, ResultUnknown, ResultMatch, ResultMatch}) {
		t.Fatalf("results = %v", got)
	}

	src.calls = nil
	attrs, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName, idAddress}, false)
	if err != nil || len(src.calls) != 1 || len(attrs) != 2 {
		t.Fatalf("Retrieve = %+v, %v; calls %v, want one call and two values", attrs, err, src.calls)
	}
}

// An attribute outside the catalogue next to a valid one: 404, and the
// source is not asked (Q7).
func TestAnUnknownAttributeAsksNothing(t *testing.T) {
	src := &fakeSource{}
	s := NewService(src, catalogue)
	var unknown *UnknownAttributeError
	if _, err := s.Verify(context.Background(), bsn, VerifyRequest{Attributes: []Attribute{
		{Identifier: idFamilyName, Value: json.RawMessage(`{"family_name":"Jansen"}`)},
		{Identifier: idNotInCat, Value: json.RawMessage(`{}`)},
	}}); !errors.As(err, &unknown) {
		t.Fatalf("Verify: err = %v, want UnknownAttributeError", err)
	}
	if _, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName, idNotInCat}, false); !errors.As(err, &unknown) {
		t.Fatalf("Retrieve: err = %v, want UnknownAttributeError", err)
	}
	if len(src.calls) != 0 {
		t.Fatalf("source asked for %v, want nothing", src.calls)
	}
}

// A source that gives no answer is an error, never Unknown.
func TestSourceErrorIsNotUnknown(t *testing.T) {
	failure := errors.New("refused")
	for _, err := range []error{ErrSourceUnavailable, failure} {
		s := NewService(&fakeSource{err: err}, catalogue)
		ctx := context.Background()
		if _, got := s.Verify(ctx, bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idFamilyName, Value: json.RawMessage(`{"family_name":"Jansen"}`)}}}); !errors.Is(got, err) {
			t.Fatalf("Verify attribute: err = %v, want %v", got, err)
		}
		if _, got := s.Verify(ctx, bsn, VerifyRequest{Fragments: []Fragment{{Identifier: idAddress, Location: "$.city", Value: json.RawMessage(`{"city":"Rotterdam"}`)}}}); !errors.Is(got, err) {
			t.Fatalf("Verify fragment: err = %v, want %v", got, err)
		}
		if _, got := s.Retrieve(ctx, bsn, []string{idFamilyName}, false); !errors.Is(got, err) {
			t.Fatalf("Retrieve: err = %v, want %v", got, err)
		}
	}
}
