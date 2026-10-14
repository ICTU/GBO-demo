package verification

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeSource is an in-memory source: BSN -> attribute name -> value. With
// err set it fails every call; asked records the names it was asked for.
type fakeSource struct {
	values map[string]map[string]string
	err    error
	asked  []string
}

func (f *fakeSource) Value(_ context.Context, bsn, name string) (json.RawMessage, bool, error) {
	f.asked = append(f.asked, name)
	if f.err != nil {
		return nil, false, f.err
	}
	v, ok := f.values[bsn][name]
	return json.RawMessage(v), ok, nil
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
	attrs, err := s.Retrieve(context.Background(), bsn, []string{idFamilyName, idGivenName}, false)
	if err != nil || len(attrs) != 1 || attrs[0].Identifier != idFamilyName || string(attrs[0].Value) != `{"family_name":"Jansen"}` {
		t.Fatalf("Retrieve = %+v, %v; want only the family name", attrs, err)
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
	if len(src.asked) != 0 {
		t.Fatalf("source asked for %v, want nothing", src.asked)
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
