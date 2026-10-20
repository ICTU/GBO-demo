package login

import (
	"errors"
	"slices"
	"testing"
)

// The mock login takes all four fields, the date of birth as YYYY-MM-DD and
// the nationalities as country codes separated by commas; nothing more.
func TestMock(t *testing.T) {
	id, err := Mock(" Jansen ", "Frouke", "2000-03-24", "nl, BE")
	if err != nil || id.FamilyName != "Jansen" || id.GivenName != "Frouke" || id.Birthdate != "2000-03-24" ||
		!slices.Equal(id.Nationalities, []string{"NL", "BE"}) || !id.Mocked() || id.Name() != "Frouke Jansen" {
		t.Fatalf("Mock = %+v, %v", id, err)
	}
	for name, in := range map[string][4]string{
		"no family name":           {"", "Frouke", "2000-03-24", "NL"},
		"no given name":            {"Jansen", " ", "2000-03-24", "NL"},
		"no date of birth":         {"Jansen", "Frouke", "", "NL"},
		"date of birth DD-MM-YYYY": {"Jansen", "Frouke", "24-03-2000", "NL"},
		"no such date":             {"Jansen", "Frouke", "2000-02-30", "NL"},
		"no nationality":           {"Jansen", "Frouke", "2000-03-24", ""},
		"a country name":           {"Jansen", "Frouke", "2000-03-24", "Nederland"},
		"an empty code":            {"Jansen", "Frouke", "2000-03-24", "NL,"},
	} {
		if _, err := Mock(in[0], in[1], in[2], in[3]); !errors.Is(err, ErrInvalidMock) {
			t.Errorf("%s: Mock = %v, want invalid", name, err)
		}
	}
}
