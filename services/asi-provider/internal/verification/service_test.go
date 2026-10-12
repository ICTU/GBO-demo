package verification

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeSource is an in-memory source: BSN -> attribute name -> value.
type fakeSource map[string]map[string]string

func (f fakeSource) Value(bsn, name string) (json.RawMessage, bool) {
	v, ok := f[bsn][name]
	return json.RawMessage(v), ok
}

const (
	bsn          = "999991772"
	idFamilyName = attributeBase + "family_name/1"
	idAddress    = attributeBase + "address/1"
	idDiploma    = attributeBase + "education_qualification/1"
	idNotInCat   = attributeBase + "shoe_size/1"
)

func newTestService() *Service {
	return NewService(fakeSource{bsn: {
		"family_name": `{"family_name":"Jansen"}`,
		"address":     `{"city":"Rotterdam"}`,
	}}, DefaultCatalogue())
}

func verifyOne(t *testing.T, s *Service, id, value string) string {
	t.Helper()
	res, err := s.Verify(bsn, VerifyRequest{Attributes: []Attribute{{Identifier: id, Value: json.RawMessage(value)}}})
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
		{"no value for this person", attributeBase + "given_name/1", `{"given_name":"Frouke"}`, ResultUnknown},
		{"not the authentic source", idDiploma, `{"qualification":"MSc"}`, ResultUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyOne(t, s, tc.id, tc.value); got != tc.want {
				t.Fatalf("result = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestVerifyFragment(t *testing.T) {
	res, err := newTestService().Verify(bsn, VerifyRequest{Fragments: []Fragment{{Identifier: idAddress, Location: "$.city", Value: json.RawMessage(`{"city":"Rotterdam"}`)}}})
	if err != nil || len(res.Fragments) != 1 || res.Fragments[0].Result != ResultMatch {
		t.Fatalf("Verify = %+v, %v; want one Match for the fragment", res, err)
	}
}

// Malformed before mandate before unknown attribute (REQ-ASIP-6.1.1.2-12).
func TestVerifyErrors(t *testing.T) {
	s := newTestService()
	var malformed *MalformedError
	var unknown *UnknownAttributeError
	if _, err := s.Verify(bsn, VerifyRequest{Mandate: true}); !errors.As(err, &malformed) {
		t.Fatalf("empty request with mandate: err = %v, want MalformedError", err)
	}
	if _, err := s.Verify(bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idNotInCat, Value: json.RawMessage(`{}`)}}, Mandate: true}); !errors.Is(err, ErrMandate) {
		t.Fatalf("mandate: err = %v, want ErrMandate", err)
	}
	if _, err := s.Verify(bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idNotInCat, Value: json.RawMessage(`{}`)}}}); !errors.As(err, &unknown) || unknown.Identifier != idNotInCat {
		t.Fatalf("unknown attribute: err = %v, want UnknownAttributeError", err)
	}
	if _, err := s.Verify(bsn, VerifyRequest{Attributes: []Attribute{{Identifier: idFamilyName, Value: json.RawMessage(`"Jansen"`)}}}); !errors.As(err, &malformed) {
		t.Fatalf("value not an object: err = %v, want MalformedError", err)
	}
}

func TestRetrieve(t *testing.T) {
	s := newTestService()
	attrs, err := s.Retrieve(bsn, []string{idFamilyName, idDiploma, attributeBase + "given_name/1"}, false)
	if err != nil || len(attrs) != 1 || attrs[0].Identifier != idFamilyName || string(attrs[0].Value) != `{"family_name":"Jansen"}` {
		t.Fatalf("Retrieve = %+v, %v; want only the family name", attrs, err)
	}
	var malformed *MalformedError
	var unknown *UnknownAttributeError
	if _, err := s.Retrieve(bsn, nil, true); !errors.As(err, &malformed) {
		t.Fatalf("no identifiers: err = %v, want MalformedError", err)
	}
	if _, err := s.Retrieve(bsn, []string{idFamilyName}, true); !errors.Is(err, ErrMandate) {
		t.Fatalf("mandate: err = %v, want ErrMandate", err)
	}
	if _, err := s.Retrieve(bsn, []string{idNotInCat}, false); !errors.As(err, &unknown) {
		t.Fatalf("unknown attribute: err = %v, want UnknownAttributeError", err)
	}
}
