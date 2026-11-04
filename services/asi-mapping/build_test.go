package asimapping

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// resident is the result of the BRP query for a resident with every field
// the mapping uses.
const resident = `{
	"__typename": "Ingezetene",
	"voorvoegsel": "de", "geslachtsnaam": "Vries", "voornamen": "Frouke", "geboortedatum": "2000-03-24",
	"heeftNationaliteit": [{"nationaliteit": "NL"}, {"nationaliteit": "BE"}],
	"woontOp": {"straatnaam": "Coolsingel", "huisnummer": 12, "huisletter": "B", "huisnummertoevoeging": null,
		"postcode": "3011 AD", "woonplaatsnaam": "Rotterdam"}
}`

// result decodes resident with the changes in edit applied.
func result(t *testing.T, edit func(r map[string]any)) any {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(resident), &r); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(r)
	}
	return r
}

func woontOp(r map[string]any) map[string]any { return r["woontOp"].(map[string]any) }

func build(t *testing.T, identifier string, members []string, source any) (map[string]any, bool, error) {
	t.Helper()
	capability := brpCapability(t)
	catalogue := loadTestCatalogue(t)
	for _, attribute := range capability.Attributes {
		if attribute.Identifier == identifier {
			return attribute.Build(source, catalogue[identifier], members)
		}
	}
	t.Fatalf("no attribute %s", identifier)
	return nil, false, nil
}

func TestBuild(t *testing.T) {
	held := []struct {
		name       string
		identifier string
		members    []string
		source     any
		want       string
	}{
		{"M1 a pointer copies the field", givenName, nil, result(t, nil),
			`{"given_name": "Frouke"}`},
		{"M2 a join skips empty parts and writes numbers as text", address, []string{"house_number"}, result(t, nil),
			`{"house_number": "12 B"}`},
		{"M4 a const is part of the whole attribute", address, nil, result(t, nil),
			`{"street": "Coolsingel", "house_number": "12 B", "postal_code": "3011 AD", "city": "Rotterdam", "country": "NL"}`},
		{"M5 each takes a field per element", nationality, nil, result(t, nil),
			`{"nationalities": ["NL", "BE"]}`},
		{"M9 a fragment needs only its member", address, []string{"city"},
			result(t, func(r map[string]any) { woontOp(r)["postcode"] = nil }),
			`{"city": "Rotterdam"}`},
		{"M12 an answer of the type on", address, []string{"city"}, result(t, nil),
			`{"city": "Rotterdam"}`},
		{"M17 a list element without a value is left out", nationality, nil,
			result(t, func(r map[string]any) {
				r["heeftNationaliteit"] = []any{map[string]any{"nationaliteit": "NL"}, map[string]any{"nationaliteit": nil}, nil}
			}),
			`{"nationalities": ["NL"]}`},
	}
	for _, test := range held {
		t.Run(test.name, func(t *testing.T) {
			value, ok, err := build(t, test.identifier, test.members, test.source)
			if err != nil || !ok {
				t.Fatalf("Build() = %v, %v, %v; want held", value, ok, err)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, want) {
				t.Fatalf("Build() = %v, want %v", value, want)
			}
		})
	}

	notHeld := []struct {
		name       string
		identifier string
		members    []string
		source     any
	}{
		{"M3 every part of a join empty", familyName, nil,
			result(t, func(r map[string]any) { r["voorvoegsel"] = nil; r["geslachtsnaam"] = "" })},
		{"M6 an empty list", nationality, nil,
			result(t, func(r map[string]any) { r["heeftNationaliteit"] = []any{} })},
		{"M7 a required member missing", address, nil,
			result(t, func(r map[string]any) { woontOp(r)["postcode"] = nil })},
		{"M8 an unknown person", givenName, nil, nil},
		{"M10 an answer of another type than on, with the same field", address, nil,
			result(t, func(r map[string]any) { r["__typename"] = "NietIngezetene" })},
		{"M11 a null on the way", address, nil,
			result(t, func(r map[string]any) { r["woontOp"] = nil })},
		{"Q6 a fragment on a const member", address, []string{"country"}, result(t, nil)},
		{"Q6 a fragment on a const member, answered beside an attribute without a type", address, []string{"country"},
			map[string]any{"voornamen": "Frouke"}},
	}
	for _, test := range notHeld {
		t.Run(test.name, func(t *testing.T) {
			value, ok, err := build(t, test.identifier, test.members, test.source)
			if err != nil || ok {
				t.Fatalf("Build() = %v, %v, %v; want not held", value, ok, err)
			}
		})
	}

	invalid := []struct {
		name       string
		identifier string
		source     any
		reason     string
	}{
		{"V2 a number where the schema has text", givenName,
			result(t, func(r map[string]any) { r["voornamen"] = 42.0 }), "given_name"},
		{"V3 a list element that is not text", nationality,
			result(t, func(r map[string]any) { r["heeftNationaliteit"] = []any{map[string]any{"nationaliteit": 528.0}} }), "nationalities"},
		{"V4 a date that is not in the calendar", birthDate,
			result(t, func(r map[string]any) { r["geboortedatum"] = "2000-02-30" }), "birth_date"},
		{"V5 a date in another form", birthDate,
			result(t, func(r map[string]any) { r["geboortedatum"] = "24-03-2000" }), "birth_date"},
		{"M13 an attribute on a type, the answer without its type", address,
			result(t, func(r map[string]any) { delete(r, "__typename") }), "__typename"},
		{"M14 a list element that is not an object", nationality,
			result(t, func(r map[string]any) { r["heeftNationaliteit"] = []any{42.0} }), "heeftNationaliteit"},
		{"M15 a list with one element that is not an object", nationality,
			result(t, func(r map[string]any) { r["heeftNationaliteit"] = []any{map[string]any{"nationaliteit": "NL"}, 42.0} }), "heeftNationaliteit"},
		{"M16 a value where an object belongs", address,
			result(t, func(r map[string]any) { r["woontOp"] = "Coolsingel" }), "woontOp"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			value, _, err := build(t, test.identifier, nil, test.source)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("Build() = %v, %v; want an error naming %q", value, err, test.reason)
			}
		})
	}

	t.Run("V1 the whole address and a date conform", func(t *testing.T) {
		for _, identifier := range []string{address, birthDate} {
			if _, ok, err := build(t, identifier, nil, result(t, nil)); err != nil || !ok {
				t.Fatalf("Build(%s) = %v, %v; want a conforming value", identifier, ok, err)
			}
		}
	})
}

func TestResult(t *testing.T) {
	capability := brpCapability(t)
	var response any
	if err := json.Unmarshal([]byte(`{"data": {"ingeschrevenPersoon": {"voornamen": "Frouke"}}}`), &response); err != nil {
		t.Fatal(err)
	}
	if got, err := capability.Result(response); err != nil || !reflect.DeepEqual(got, map[string]any{"voornamen": "Frouke"}) {
		t.Fatalf("Result() = %v, %v", got, err)
	}
	if err := json.Unmarshal([]byte(`{"data": {"ingeschrevenPersoon": null}}`), &response); err != nil {
		t.Fatal(err)
	}
	if got, err := capability.Result(response); err != nil || got != nil {
		t.Fatalf("Result() for an unknown person = %v, %v; want nil", got, err)
	}
	if err := json.Unmarshal([]byte(`{"data": "unexpected"}`), &response); err != nil {
		t.Fatal(err)
	}
	if _, err := capability.Result(response); err == nil {
		t.Fatal("Result() accepted a response of another shape")
	}
}
