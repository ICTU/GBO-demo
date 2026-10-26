package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// address is the address product's form, filled with Frouke Jansen's address.
func address() url.Values {
	return url.Values{
		"product":                {"address"},
		"f-address-street":       {"Coolsingel"},
		"f-address-house_number": {"12 B"},
		"f-address-postal_code":  {"3011 AD"},
		"f-address-city":         {"Rotterdam"},
		"f-address-country":      {"NL"},
	}
}

func loggedInEnv(t *testing.T) (loginEnv, *http.Client) {
	t.Helper()
	e := newLoginEnv(t, nil)
	c := browser()
	e.do(t, c, http.MethodPost, "/login/mock", url.Values{"family_name": {"Jansen"}, "given_name": {"Frouke"}, "birthdate": {"2000-03-24"}, "nationalities": {"NL, BE"}})
	return e, c
}

// P1: the request page offers products, no choice between verify and
// retrieve, and no JSON fields.
func TestRequestPageOffersProducts(t *testing.T) {
	e, c := loggedInEnv(t)
	resp, page := e.do(t, c, http.MethodGet, "/", nil)
	for _, want := range []string{
		"Wat wilt u laten verifiëren?",
		`<input type="radio" name="product" value="address" checked>`,
		`<input type="radio" name="product" value="standplaatsvergunning" disabled>`, "Binnenkort",
		`name="f-address-street" value="Coolsingel"`, `name="f-address-house_number" value="12 B"`, `name="f-address-postal_code" value="3011 AD"`,
		`name="f-address-city" value="Rotterdam"`, `name="f-address-country" value="NL"`,
	} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Errorf("request page lacks %q", want)
		}
	}
	for _, unwanted := range []string{`name="operation"`, "Ophalen", "<textarea", "Familienaam"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("request page still has %q", unwanted)
		}
	}
}

// P2: the address product becomes a verification of the address attribute,
// its value composed from the fields.
func TestAddressProductComposesTheValue(t *testing.T) {
	attr, _, ok := readProduct(DemoProducts, address())
	if !ok || attr.Identifier != "https://gbo.example/attributes/address/1" {
		t.Fatalf("readProduct = %+v, %v", attr, ok)
	}
	var got map[string]string
	if err := json.Unmarshal(attr.Value, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"street": "Coolsingel", "house_number": "12 B", "postal_code": "3011 AD", "city": "Rotterdam", "country": "NL"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("value %s has other keys", attr.Value)
	}
}

// P3: an empty required field keeps the page open with a message at the field
// and the typed values.
func TestAddressWithAnEmptyRequiredField(t *testing.T) {
	for _, field := range []string{"street", "house_number", "postal_code", "city", "country"} {
		t.Run(field, func(t *testing.T) {
			e, c := loggedInEnv(t)
			form := address()
			form.Set("f-address-"+field, " ")
			resp, page := e.do(t, c, http.MethodPost, "/start", form)
			id := "f-address-" + field
			kept := `value="Rotterdam"`
			if field == "city" {
				kept = `value="Coolsingel"`
			}
			for _, want := range []string{"Controleer het formulier", `href="#` + id + `"`, `id="m-` + id + `"`, `aria-invalid="true"`, kept} {
				if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, want) {
					t.Errorf("status %d, page lacks %q", resp.StatusCode, want)
				}
			}
		})
	}
}

// P4 and P5: no product, or one that is not available, keeps the page open
// with a message; nothing is started.
func TestProductRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		product, want string
	}{
		"P4 no product":      {"", msgNoProduct},
		"P5 not available":   {"standplaatsvergunning", msgProductUnavailable},
		"P5 unknown product": {"shoe_size", msgUnknownProduct},
	} {
		t.Run(name, func(t *testing.T) {
			e, c := loggedInEnv(t)
			form := address()
			form.Set("product", tc.product)
			resp, page := e.do(t, c, http.MethodPost, "/start", form)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, tc.want) || !strings.Contains(page, `action="/start"`) {
				t.Errorf("status %d, page lacks %q", resp.StatusCode, tc.want)
			}
			if resp.Header.Get("Location") != "" {
				t.Errorf("redirected to %s", resp.Header.Get("Location"))
			}
		})
	}
}

// P7: the result page shows the address as submitted, the outcome in words,
// and the seal check.
func TestResultPageShowsTheAddress(t *testing.T) {
	for _, result := range []string{"Match", "MatchWithVariation", "NoMatch", "Unknown"} {
		rec := httptest.NewRecorder()
		render(rec, http.StatusOK, resultPage, resultView{outcome: outcome{
			Status: statusDone, Operation: "verify", Source: "Basisregistratie Personen (mock)", Seal: &sealView{Valid: true, Signer: "CN=ASIP seal"},
			Results: []row{{Identifier: "https://gbo.example/attributes/address/1", Label: "Adres", Result: result,
				Submitted: json.RawMessage(`{"street":"Coolsingel","house_number":"12 B","postal_code":"3011 AD","city":"Rotterdam","country":"NL"}`)}},
		}})
		page := rec.Body.String()
		for _, want := range []string{"Coolsingel 12 B, 3011 AD Rotterdam, NL", ">" + result + "</span>", "Zegel geldig"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: result page lacks %q", result, want)
			}
		}
	}
}

// A valid seal is a technical detail, folded away under "Technische details";
// an invalid seal stays in view, because the result is then not accepted.
func TestSealCheckIsATechnicalDetail(t *testing.T) {
	page := func(s *sealView) string {
		rec := httptest.NewRecorder()
		render(rec, http.StatusOK, resultPage, resultView{outcome: outcome{Status: statusDone, Operation: "verify", Seal: s}})
		return rec.Body.String()
	}
	valid := page(&sealView{Valid: true, Signer: "CN=ASIP seal,O=GBO demo", Organization: "GBO demo"})
	details := strings.Index(valid, `<details class="qp-card qp-details">`)
	if details < 0 || !strings.Contains(valid, "<summary>Technische details</summary>") || strings.Index(valid, "Zegel geldig.") < details ||
		!strings.Contains(valid, "verzegeld door GBO demo") {
		t.Errorf("valid seal not folded under Technische details:\n%s", valid)
	}
	invalid := page(&sealView{Error: "the seal does not verify over the response body"})
	if strings.Contains(invalid, "<details") || !strings.Contains(invalid, "Zegel ongeldig") {
		t.Errorf("invalid seal not in view:\n%s", invalid)
	}
}
