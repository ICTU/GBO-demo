package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The BRP mock persons the demo runs with.
const personsFile = "../brp-graphql-server/mockdata/personen.json"

func post(t *testing.T, h http.Handler, body string) (int, []issuableDocument) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/attestations", strings.NewReader(body)))
	var docs []issuableDocument
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, docs
}

func newTestHandler(t *testing.T, today string) http.Handler {
	t.Helper()
	persons, err := loadPersons(personsFile)
	if err != nil {
		t.Fatal(err)
	}
	day, err := time.Parse(time.DateOnly, today)
	if err != nil {
		t.Fatal(err)
	}
	return newHandler(persons, func() time.Time { return day })
}

func TestPID(t *testing.T) {
	h := newTestHandler(t, "2026-11-03")

	t.Run("S1 a known BSN gets a PID with the mapped claims", func(t *testing.T) {
		status, docs := post(t, h, `{"subject": {"bsn": "999991772"}}`)
		if status != http.StatusOK || len(docs) != 1 {
			t.Fatalf("%d %+v", status, docs)
		}
		d := docs[0]
		if d.Format != "dc+sd-jwt" || d.AttestationType != "urn:eudi:pid:nl:1" {
			t.Fatalf("document %+v", d)
		}
		got, _ := json.Marshal(d.Attributes)
		want := `{"address":{"country":"Nederland","house_number":"12B","locality":"Rotterdam","postal_code":"3011AD","street_address":"Coolsingel"},` +
			`"age_over_18":true,"birthdate":"2000-03-24","bsn":"999991772","family_name":"Jansen","given_name":"Frouke","nationalities":["Nederlandse","Belgische"]}`
		if string(got) != want {
			t.Fatalf("attributes\n got %s\nwant %s", got, want)
		}
	})
	t.Run("S2 an unknown BSN gets an empty list", func(t *testing.T) {
		if status, docs := post(t, h, `{"subject": {"bsn": "111111110"}}`); status != http.StatusOK || docs == nil || len(docs) != 0 {
			t.Fatalf("%d %+v", status, docs)
		}
	})
	t.Run("S3 a body without a BSN is refused", func(t *testing.T) {
		for _, body := range []string{`{`, `{}`, `{"subject": {}}`, `[{"id": "pid"}]`} {
			if status, _ := post(t, h, body); status != http.StatusBadRequest {
				t.Fatalf("%s: %d", body, status)
			}
		}
	})
	t.Run("S5 no address in the Netherlands, no address", func(t *testing.T) {
		_, docs := post(t, h, `{"subject": {"bsn": "555555555"}}`)
		if _, ok := docs[0].Attributes["address"]; ok {
			t.Fatalf("attributes %+v", docs[0].Attributes)
		}
	})
	t.Run("S6 nationalities is a list", func(t *testing.T) {
		_, docs := post(t, h, `{"subject": {"bsn": "123456789"}}`)
		if n, ok := docs[0].Attributes["nationalities"].([]any); !ok || len(n) != 1 || n[0] != "Nederlandse" {
			t.Fatalf("nationalities %v", docs[0].Attributes["nationalities"])
		}
	})
}

// S4: age_over_18 compares calendar dates in Amsterdam; born on 29
// February, 18 on 1 March in a common year.
func TestOver18(t *testing.T) {
	ams := amsterdam
	for name, tc := range map[string]struct {
		birth string
		now   time.Time
		want  bool
	}{
		"the day before":                 {"2000-03-24", time.Date(2018, 3, 23, 12, 0, 0, 0, ams), false},
		"the birthday":                   {"2000-03-24", time.Date(2018, 3, 24, 0, 0, 0, 0, ams), true},
		"birthday in Amsterdam, not UTC": {"2000-03-24", time.Date(2018, 3, 23, 23, 30, 0, 0, time.UTC), true},
		"29 February, on 28 February":    {"2008-02-29", time.Date(2026, 2, 28, 12, 0, 0, 0, ams), false},
		"29 February, on 1 March":        {"2008-02-29", time.Date(2026, 3, 1, 0, 0, 0, 0, ams), true},
	} {
		birth, err := time.Parse(time.DateOnly, tc.birth)
		if err != nil {
			t.Fatal(err)
		}
		if got := over18(birth, tc.now); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// The forms of the NL Wallet's PID: house number, letter and addition
// without spaces, the family name with its prefix.
func TestNLForms(t *testing.T) {
	var p person
	if err := json.Unmarshal([]byte(`{"bsn":"1","voornamen":"Jan","voorvoegsel":"van der","geslachtsnaam":"Berg","geboortedatum":"1990-01-01",
		"heeftNationaliteit":[{"nationaliteit":"NL"}],"woontOpBinnenland":{"straatnaam":"Weg","huisnummer":1,"postcode":"1234 AB","woonplaatsnaam":"X"}}`), &p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ letter, addition, want string }{{"", "", "1"}, {"A", "", "1A"}, {"", "bis", "1bis"}, {"A", "bis", "1Abis"}} {
		p.Adres.Huisletter, p.Adres.Huisnummertoevoeging = tc.letter, tc.addition
		attrs, err := pidAttributes(p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got := attrs["address"].(map[string]any)["house_number"]; got != tc.want {
			t.Errorf("letter %q addition %q: %v, want %s", tc.letter, tc.addition, got, tc.want)
		}
		if attrs["family_name"] != "van der Berg" {
			t.Fatalf("family_name %v", attrs["family_name"])
		}
	}
}

// A mock person the stub cannot map is refused at startup.
func TestLoadPersonsRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate bsn":       `[{"bsn":"1","geboortedatum":"1990-01-01"},{"bsn":"1","geboortedatum":"1990-01-01"}]`,
		"bad birthdate":       `[{"bsn":"1","geboortedatum":"01-01-1990"}]`,
		"unknown nationality": `[{"bsn":"1","geboortedatum":"1990-01-01","heeftNationaliteit":[{"nationaliteit":"XX"}]}]`,
	} {
		path := filepath.Join(t.TempDir(), "personen.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadPersons(path); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}
