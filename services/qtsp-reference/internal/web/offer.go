package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gbo-demo/qtsp-reference/internal/login"
	"rsc.io/qr"
)

// Issuer is the own credential issuer, at which the QTSP makes a
// pre-authorized offer for an accepted result: one QR that a wallet scans
// without a second disclosure, as the QTSP has identified the citizen
// already. The issuer fetches the statement from the QTSP's callback with
// the reference the offer names.
type Issuer struct {
	// OffersURL is the issuer's internal offer endpoint.
	OffersURL string
	// Profile and Configuration are the issuer profile that serves both
	// wallets and the QTSP's credential configuration.
	Profile       string
	Configuration string
	HTTP          *http.Client
}

// Why an offer failed, by what it means for the result.
var (
	// errOfferNoData means the issuer found no result for the reference: it
	// expired, was replaced or was issued already.
	errOfferNoData = errors.New("no pending result for the offer")
	// errOfferUnavailable means the issuer, or the callback behind it, could
	// not be reached: the result is still there.
	errOfferUnavailable = errors.New("issuer unavailable")
	// errOfferRefused means the issuer refused the request itself (unknown
	// profile or configuration): trying again does not help.
	errOfferRefused = errors.New("issuer refused the offer request")
)

// Offer asks the issuer for an offer of the result with the reference and
// returns the link a wallet scans.
func (i *Issuer) Offer(ctx context.Context, ref string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"profile":                      i.Profile,
		"credential_configuration_ids": []string{i.Configuration},
		"subject":                      map[string]string{"qtsp_request": ref},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.OffersURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := i.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errOfferUnavailable, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Error string            `json:"error"`
		Links map[string]string `json:"links"`
	}
	_ = json.Unmarshal(answer, &out)
	// The issuer takes the result from the callback before it can fail
	// otherwise, so any other failure has used it.
	switch {
	case resp.StatusCode == http.StatusNotFound && out.Error == "no_data":
		return "", errOfferNoData
	case resp.StatusCode == http.StatusServiceUnavailable && out.Error == "source_unavailable":
		return "", errOfferUnavailable
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%w: %d %s", errOfferRefused, resp.StatusCode, out.Error)
	case resp.StatusCode != http.StatusCreated:
		return "", fmt.Errorf("issuer answered %d %s", resp.StatusCode, out.Error)
	}
	link := out.Links["openid-credential-offer"]
	if !strings.HasPrefix(link, "openid-credential-offer://") {
		return "", errors.New("issuer answered without an openid-credential-offer link")
	}
	return link, nil
}

// walletOfferView is the page after the citizen chose to put the statement
// in a wallet: the QR of the offer, or why there is none.
type walletOfferView struct {
	Who *login.Identity
	QR  template.URL
	// Message says why there is no offer; Ref is set when trying again may
	// help.
	Message string
	Ref     string
}

// walletOffer makes the offer for the result the form names and shows its
// QR. The issuer takes the result from the QTSP while making the offer, so
// the result is used once the offer exists, also for the NL Wallet's
// static link.
func (h *Handler) walletOffer(w http.ResponseWriter, r *http.Request) {
	if h.issuer == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		errorPage(w, http.StatusBadRequest, "Het formulier kon niet worden gelezen.")
		return
	}
	ref := r.PostForm.Get("ref")
	if ref == "" {
		errorPage(w, http.StatusBadRequest, "Het formulier kon niet worden gelezen.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	link, err := h.issuer.Offer(ctx, ref)
	if err != nil {
		slog.Warn("wallet offer failed", "err", err)
		view := walletOfferView{Who: h.loggedIn(r),
			Message: "Het klaarzetten is mislukt en de uitslag is daarbij gebruikt. Vraag de controle opnieuw aan."}
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, errOfferNoData):
			status = http.StatusNotFound
			view.Message = "Deze uitslag is verlopen of al gebruikt. Een uitslag is 10 minuten geldig en gaat één keer naar een wallet. Vraag de controle opnieuw aan."
		case errors.Is(err, errOfferUnavailable):
			status = http.StatusServiceUnavailable
			view.Message, view.Ref = "De verklaring kon nu niet voor uw wallet worden klaargezet. Probeer het opnieuw.", ref
		case errors.Is(err, errOfferRefused):
			view.Message = "De QTSP kan nu geen verklaring voor uw wallet klaarzetten: de koppeling met de uitgever is niet goed ingesteld."
		}
		render(w, status, walletOfferPage, view)
		return
	}
	code, err := qr.Encode(link, qr.M)
	if err != nil {
		errorPage(w, http.StatusInternalServerError, "De QR-code kon niet worden gemaakt.")
		return
	}
	render(w, http.StatusOK, walletOfferPage, walletOfferView{Who: h.loggedIn(r),
		QR: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())),
	})
}
