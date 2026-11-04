package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/issuancecallback"
	"gbo-demo/qtsp-reference/internal/login"
	"gbo-demo/qtsp-reference/internal/qtsp"
)

// fakeIssuer stands in for the own credential issuer: for an offer it
// fetches the statement from the QTSP's callback, as the issuer does, and
// answers no_data when the callback has none.
func fakeIssuer(t *testing.T, store *issuance.Store, requests *[]map[string]any) *httptest.Server {
	t.Helper()
	const token = "test-callback-token"
	callback := issuancecallback.Handler(store, token)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("offer request: %v", err)
		}
		*requests = append(*requests, req)
		body, _ := json.Marshal(map[string]any{"subject": req["subject"]})
		fetched := httptest.NewRecorder()
		callback.ServeHTTP(fetched, httptest.NewRequest(http.MethodPost, issuancecallback.Path(token), bytes.NewReader(body)))
		w.Header().Set("Content-Type", "application/json")
		if fetched.Code == http.StatusNotFound {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no_data"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"links":{"openid-credential-offer":"openid-credential-offer://?credential_offer=%7B%7D","haip-vci":"haip-vci://?credential_offer=%7B%7D"}}`))
	}))
}

func acceptedResult(t *testing.T, h *Handler) (resultView, login.Identity) {
	t.Helper()
	identity := login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL"}}
	requested := []qtsp.Attribute{{Identifier: DemoAttributes[0].Identifier}}
	out := outcome{
		Status: statusDone, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: true},
		Results:  []row{{Identifier: DemoAttributes[0].Identifier, Label: "Familienaam", Result: "Match"}},
		Evidence: []issuance.Evidence{{Name: "Familienaam", Outcome: "Match", Value: []byte(`"Jansen"`)}},
	}
	view := resultView{outcome: out}
	view.WalletQR, view.OfferRef = h.offerIssuance(identity, requested, out)
	return view, identity
}

func postOffer(h *Handler, ref string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/wallet-offer", strings.NewReader(url.Values{"ref": {ref}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, req)
	return rec
}

// P4: with the issuer configured, an accepted result offers one QR for both
// wallets, and the NL Wallet's static link as the small alternative. The
// offer names the result by its reference; once made, the result is used.
func TestAcceptedResultOffersOneQRAtTheIssuer(t *testing.T) {
	store := issuance.NewStore(time.Now)
	var requests []map[string]any
	server := fakeIssuer(t, store, &requests)
	defer server.Close()
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
	h.issuance, h.issuanceLink = store, "https://wallet.example/issue"
	h.issuer = &Issuer{OffersURL: server.URL, Profile: "wallet", Configuration: "qtsp-reference-attestation", HTTP: server.Client()}

	view, identity := acceptedResult(t, h)
	if view.OfferRef == "" || view.WalletQR == "" {
		t.Fatalf("view = ref %q qr %q, want both", view.OfferRef, view.WalletQR)
	}
	page := httptest.NewRecorder()
	render(page, http.StatusOK, resultPage, view)
	for _, text := range []string{`action="/wallet-offer"`, `value="` + view.OfferRef + `"`, "Zet de verklaring in uw wallet", "NL Wallet (huidige app)", "geen gekwalificeerde"} {
		if !strings.Contains(page.Body.String(), text) {
			t.Errorf("result page does not contain %q", text)
		}
	}

	offered := postOffer(h, view.OfferRef)
	if offered.Code != http.StatusOK || !strings.Contains(offered.Body.String(), "data:image/png;base64,") {
		t.Fatalf("offer page = %d %s", offered.Code, offered.Body.String())
	}
	subject, _ := requests[0]["subject"].(map[string]any)
	ids, _ := requests[0]["credential_configuration_ids"].([]any)
	if requests[0]["profile"] != "wallet" || len(ids) != 1 || ids[0] != "qtsp-reference-attestation" || subject["qtsp_request"] != view.OfferRef {
		t.Fatalf("offer request = %v", requests[0])
	}
	if store.Take(identity) != nil {
		t.Fatal("the static link can still issue a result that was offered")
	}

	// P6: a used (or expired) result gives no second offer.
	again := postOffer(h, view.OfferRef)
	if again.Code != http.StatusNotFound || !strings.Contains(again.Body.String(), "verlopen of al gebruikt") || strings.Contains(again.Body.String(), "Opnieuw proberen") {
		t.Fatalf("second offer = %d %s", again.Code, again.Body.String())
	}
}

// P5: when the issuer cannot be reached the result stays, and the page
// offers to try again.
func TestUnreachableIssuerKeepsTheResult(t *testing.T) {
	store := issuance.NewStore(time.Now)
	var requests []map[string]any
	server := fakeIssuer(t, store, &requests)
	server.Close()
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
	h.issuance = store
	h.issuer = &Issuer{OffersURL: server.URL, Profile: "wallet", Configuration: "qtsp-reference-attestation"}

	view, _ := acceptedResult(t, h)
	if view.OfferRef == "" || view.WalletQR != "" {
		t.Fatalf("view = ref %q qr %q, want the offer only without the static link", view.OfferRef, view.WalletQR)
	}
	failed := postOffer(h, view.OfferRef)
	if failed.Code != http.StatusServiceUnavailable || !strings.Contains(failed.Body.String(), "Opnieuw proberen") || !strings.Contains(failed.Body.String(), view.OfferRef) {
		t.Fatalf("offer page = %d %s", failed.Code, failed.Body.String())
	}
	if store.TakeRef(view.OfferRef) == nil {
		t.Fatal("the result was lost although no offer was made")
	}
}

// A refusal of the issuer is no reason to try again: a 400 is a setup
// error, and any other failure came after the issuer took the result. The
// callback unreachable behind the issuer is.
func TestIssuerFailuresByWhatTheyMeanForTheResult(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
		text   string
		retry  bool
	}{
		{http.StatusServiceUnavailable, `{"error":"source_unavailable"}`, http.StatusServiceUnavailable, "Probeer het opnieuw", true},
		{http.StatusBadRequest, `{"error":"unknown_profile"}`, http.StatusBadGateway, "niet goed ingesteld", false},
		{http.StatusNotFound, `404 page not found`, http.StatusBadGateway, "niet goed ingesteld", false},
		{http.StatusBadGateway, `{"error":"bad_source_data"}`, http.StatusBadGateway, "uitslag is daarbij gebruikt", false},
		{http.StatusInternalServerError, `{"error":"server_error"}`, http.StatusBadGateway, "uitslag is daarbij gebruikt", false},
		{http.StatusCreated, `{"links":{"haip-vci":"haip-vci://?x"}}`, http.StatusBadGateway, "uitslag is daarbij gebruikt", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
		h.issuance = issuance.NewStore(time.Now)
		h.issuer = &Issuer{OffersURL: server.URL, Profile: "wallet", Configuration: "qtsp-reference-attestation", HTTP: server.Client()}
		view, _ := acceptedResult(t, h)
		rec := postOffer(h, view.OfferRef)
		server.Close()
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.text) || strings.Contains(rec.Body.String(), "Opnieuw proberen") != tc.retry {
			t.Errorf("issuer %d %s: page %d, want %d with %q (retry %v)", tc.status, tc.body, rec.Code, tc.want, tc.text, tc.retry)
		}
	}
}

// P7: without the issuer the page is as before: the NL Wallet's static
// link only, and no offer route.
func TestWithoutIssuerOnlyTheStaticLink(t *testing.T) {
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
	h.issuance, h.issuanceLink = issuance.NewStore(time.Now), "https://wallet.example/issue"
	view, _ := acceptedResult(t, h)
	if view.OfferRef != "" || view.WalletQR == "" {
		t.Fatalf("view = ref %q qr %q, want the static link only", view.OfferRef, view.WalletQR)
	}
	page := httptest.NewRecorder()
	render(page, http.StatusOK, resultPage, view)
	if !strings.Contains(page.Body.String(), "Zet de verklaring in uw NL Wallet") || strings.Contains(page.Body.String(), "/wallet-offer") {
		t.Fatal("result page without issuer differs from the static-link page")
	}
	if rec := postOffer(h, "anything"); rec.Code != http.StatusNotFound {
		t.Fatalf("offer route without issuer = %d, want 404", rec.Code)
	}
}
