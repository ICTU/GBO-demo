package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	// The image has no zoneinfo; age_over_18 is computed on the date in
	// Amsterdam.
	_ "time/tzdata"
)

// PIDType is the attestation type of the test PID: the NL PID type of the
// NL Wallet, so the disclosure flow asks for what a real NL Wallet holds.
const PIDType = "urn:eudi:pid:nl:1"

// amsterdam is the time zone of the day of issuance.
var amsterdam = mustLoadLocation("Europe/Amsterdam")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// nationalities maps the nationality codes of the BRP mock to the
// descriptions the NL Wallet's PID carries.
var nationalities = map[string]string{"NL": "Nederlandse", "BE": "Belgische", "DE": "Duitse"}

// person is the part of a BRP mock person (brp-graphql-server/mockdata)
// that a PID carries.
type person struct {
	BSN            string `json:"bsn"`
	Voornamen      string `json:"voornamen"`
	Voorvoegsel    string `json:"voorvoegsel"`
	Geslachtsnaam  string `json:"geslachtsnaam"`
	Geboortedatum  string `json:"geboortedatum"`
	Nationaliteits []struct {
		Nationaliteit string `json:"nationaliteit"`
	} `json:"heeftNationaliteit"`
	Adres *struct {
		Straatnaam           string `json:"straatnaam"`
		Huisnummer           int    `json:"huisnummer"`
		Huisletter           string `json:"huisletter"`
		Huisnummertoevoeging string `json:"huisnummertoevoeging"`
		Postcode             string `json:"postcode"`
		Woonplaatsnaam       string `json:"woonplaatsnaam"`
	} `json:"woontOpBinnenland"`
}

// loadPersons reads the BRP mock persons, keyed by BSN. A person the stub
// could not map is refused here, at startup.
func loadPersons(path string) (map[string]person, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []person
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make(map[string]person, len(list))
	for _, p := range list {
		if p.BSN == "" {
			return nil, fmt.Errorf("%s: a person without bsn", path)
		}
		if _, ok := out[p.BSN]; ok {
			return nil, fmt.Errorf("%s: bsn %s occurs twice", path, p.BSN)
		}
		if _, err := pidAttributes(p, time.Now()); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[p.BSN] = p
	}
	return out, nil
}

// pidAttributes maps a person to the claims of the NL PID type in the forms
// the NL Wallet's PID issuer uses: the family name with its prefix, the
// house number with letter and addition without spaces, the postal code
// without a space, the country and the nationalities as descriptions.
// recovery_code, the NL Wallet's own, is left out. A person without an
// address in the Netherlands gets no address.
func pidAttributes(p person, now time.Time) (map[string]any, error) {
	birth, err := time.Parse(time.DateOnly, p.Geboortedatum)
	if err != nil {
		return nil, fmt.Errorf("person %s: birthdate: %w", p.BSN, err)
	}
	descriptions := make([]string, 0, len(p.Nationaliteits))
	for _, n := range p.Nationaliteits {
		d, ok := nationalities[n.Nationaliteit]
		if !ok {
			return nil, fmt.Errorf("person %s: no description for nationality %q", p.BSN, n.Nationaliteit)
		}
		descriptions = append(descriptions, d)
	}
	familyName := p.Geslachtsnaam
	if p.Voorvoegsel != "" {
		familyName = p.Voorvoegsel + " " + p.Geslachtsnaam
	}
	attrs := map[string]any{
		"bsn":           p.BSN,
		"given_name":    p.Voornamen,
		"family_name":   familyName,
		"birthdate":     p.Geboortedatum,
		"age_over_18":   over18(birth, now),
		"nationalities": descriptions,
	}
	if a := p.Adres; a != nil {
		attrs["address"] = map[string]any{
			"street_address": a.Straatnaam,
			"house_number":   strconv.Itoa(a.Huisnummer) + a.Huisletter + a.Huisnummertoevoeging,
			"postal_code":    strings.ReplaceAll(a.Postcode, " ", ""),
			"locality":       a.Woonplaatsnaam,
			"country":        "Nederland",
		}
	}
	return attrs, nil
}

// over18 compares calendar dates in Amsterdam. Someone born on 29 February
// turns 18 on 1 March in a common year: the birthday has not passed on 28
// February.
func over18(birth, now time.Time) bool {
	y, m, d := now.In(amsterdam).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return !today.Before(birth.AddDate(18, 0, 0))
}
