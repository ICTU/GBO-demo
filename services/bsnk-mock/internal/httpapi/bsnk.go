package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"bsnk-mock/internal/polymorphic"
)

// bsnk serves BSNk's own interface. The real one is SOAP; these handlers
// carry the same requests and answers as JSON, with the element and
// attribute names of the real messages as field names.
type bsnk struct {
	mock             *polymorphic.Mock
	randomizeDefault bool
}

// requestBase is what every request to BSNk carries.
type requestBase struct {
	RequestID string `json:"RequestID"`
	DateTime  string `json:"DateTime"`
	Requester string `json:"Requester"`
}

func (b *requestBase) base() *requestBase { return b }

// responseBase is what every answer of BSNk carries.
type responseBase struct {
	ResponseID   string `json:"ResponseID"`
	DateTime     string `json:"DateTime"`
	InResponseTo string `json:"InResponseTo"`
}

func inResponseTo(requestID string) responseBase {
	id := make([]byte, 8)
	_, _ = rand.Read(id) // crypto/rand.Read does not fail on supported platforms
	return responseBase{
		ResponseID:   "_" + hex.EncodeToString(id),
		DateTime:     time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		InResponseTo: requestID,
	}
}

type activateRequest struct {
	requestBase
	RequesterKeySetVersion int              `json:"RequesterKeySetVersion"`
	BSN                    string           `json:"BSN"`
	EncryptedBSN           *json.RawMessage `json:"EncryptedBSN"`
	EncryptedIdentity      *json.RawMessage `json:"EncryptedIdentity"`
	EIDASUniquenessID      *json.RawMessage `json:"eIDAS-UniquenessID"`
	// The verification data BSNk checks against the population register.
	// The mock has no register to check against and takes them as given.
	DocumentType string `json:"DocumentType"`
	DocumentID   string `json:"DocumentID"`
	GivenNames   string `json:"GivenNames"`
	SurName      string `json:"SurName"`
	DateOfBirth  string `json:"DateOfBirth"`
}

type activateResponse struct {
	responseBase
	// PolymorphicPseudonym holds the signed PI and the signed PP, in that
	// order.
	PolymorphicPseudonym []string `json:"PolymorphicPseudonym"`
}

// activate turns a BSN into a signed PI and a signed PP for the requester.
func (b bsnk) activate(r *http.Request) (any, error) {
	var req activateRequest
	if err := decodeRequest(r, &req); err != nil {
		return nil, err
	}
	if req.EncryptedBSN != nil || req.EncryptedIdentity != nil || req.EIDASUniquenessID != nil {
		return nil, syntaxError("the mock takes a plain BSN only")
	}
	randomize, err := b.randomize(r)
	if err != nil {
		return nil, err
	}
	pi, pp, err := b.mock.Activate(polymorphic.Activation{
		Requester: req.Requester, RequesterKeySetVersion: req.RequesterKeySetVersion, BSN: req.BSN,
	}, randomize)
	if err != nil {
		return nil, err
	}
	return activateResponse{inResponseTo(req.RequestID), []string{pi, pp}}, nil
}

type transformRequest struct {
	requestBase
	RelyingParty []struct {
		EntityID         string           `json:"EntityID"`
		KeySetVersion    int              `json:"KeySetVersion"`
		IdentifierType   string           `json:"IdentifierType"`
		Nonce            string           `json:"Nonce"`
		LinkVerification *json.RawMessage `json:"LinkVerification"`
	} `json:"RelyingParty"`
	PolymorphicIdentity  string           `json:"PolymorphicIdentity"`
	PolymorphicPseudonym string           `json:"PolymorphicPseudonym"`
	Role                 *json.RawMessage `json:"Role"`
	TransactionID        *json.RawMessage `json:"TransactionID"`
}

type transformResponse struct {
	responseBase
	Encrypted []encrypted `json:"Encrypted"`
}

type encrypted struct {
	EntityID       string `json:"EntityID"`
	KeySetVersion  int    `json:"KeySetVersion"`
	IdentifierType string `json:"IdentifierType"`
	Value          string `json:"value"`
}

// transform turns a PI or PP into a VI or VP per relying party.
func (b bsnk) transform(r *http.Request) (any, error) {
	var req transformRequest
	if err := decodeRequest(r, &req); err != nil {
		return nil, err
	}
	if req.Role != nil || req.TransactionID != nil {
		return nil, syntaxError("Role and TransactionID are not supported")
	}
	t := polymorphic.Transformation{
		Requester:           req.Requester,
		PolymorphicIdentity: req.PolymorphicIdentity, PolymorphicPseudonym: req.PolymorphicPseudonym,
	}
	for _, rp := range req.RelyingParty {
		if rp.LinkVerification != nil {
			return nil, syntaxError("LinkVerification is not supported")
		}
		t.RelyingParties = append(t.RelyingParties, polymorphic.RelyingParty{
			EntityID: rp.EntityID, KeySetVersion: rp.KeySetVersion, IdentifierType: rp.IdentifierType, Nonce: rp.Nonce,
		})
	}
	randomize, err := b.randomize(r)
	if err != nil {
		return nil, err
	}
	issued, err := b.mock.Transform(t, randomize)
	if err != nil {
		return nil, err
	}
	resp := transformResponse{inResponseTo(req.RequestID), make([]encrypted, 0, len(issued))}
	for _, e := range issued {
		resp.Encrypted = append(resp.Encrypted, encrypted{e.EntityID, e.KeySetVersion, e.IdentifierType, e.Value})
	}
	return resp, nil
}

type dvKeysRequest struct {
	requestBase
	RelyingParty                string `json:"RelyingParty"`
	RelyingPartyPKIoCertificate *struct {
		X509Data struct {
			X509Certificate string `json:"X509Certificate"`
		} `json:"X509Data"`
	} `json:"RelyingPartyPKIoCertificate"`
	SchemeKeySetVersion    *int   `json:"SchemeKeySetVersion"`
	ProvideEIDecryptionKey string `json:"ProvideEIDecryptionKey"`
}

type dvKeysResponse struct {
	responseBase
	EncryptedDVKey []dvKey `json:"EncryptedDVKey"`
}

type dvKey struct {
	KeyType             string `json:"KeyType"`
	RecipientKeyVersion string `json:"RecipientKeyVersion"`
	SchemeKeySetVersion string `json:"SchemeKeySetVersion"`
	// Value is the key file in base64. The real BSNk encrypts it to the
	// party's certificate first.
	Value string `json:"value"`
}

// provideDVKeys issues the keys with which a party reads its values. A
// broker asks for them on the party's behalf.
func (b bsnk) provideDVKeys(r *http.Request) (any, error) {
	var req dvKeysRequest
	if err := decodeRequest(r, &req); err != nil {
		return nil, err
	}
	keyRequest := polymorphic.KeyRequest{
		Requester:       req.Requester,
		RelyingParty:    req.RelyingParty,
		EIDecryptionKey: req.ProvideEIDecryptionKey,
		// A test without a certificate names the key set version itself.
		KeySetVersion: r.URL.Query().Get("key_set_version"),
	}
	if req.SchemeKeySetVersion != nil {
		keyRequest.SchemeKeySetVersion = strconv.Itoa(*req.SchemeKeySetVersion)
	}
	if req.RelyingPartyPKIoCertificate != nil {
		der, err := base64.StdEncoding.DecodeString(req.RelyingPartyPKIoCertificate.X509Data.X509Certificate)
		if err != nil {
			return nil, &polymorphic.Fault{Reason: polymorphic.InvalidRequest, Description: "X509Certificate is not base64"}
		}
		keyRequest.Certificate = der
	}
	keys, err := b.mock.ProvideDVKeys(keyRequest)
	if err != nil {
		return nil, err
	}
	resp := dvKeysResponse{inResponseTo(req.RequestID), make([]dvKey, 0, len(keys))}
	for _, k := range keys {
		resp.EncryptedDVKey = append(resp.EncryptedDVKey, dvKey{
			k.KeyType, k.RecipientKeyVersion, k.SchemeKeySetVersion, base64.StdEncoding.EncodeToString([]byte(k.File)),
		})
	}
	return resp, nil
}

type authorizedOrganization struct {
	OIN string `json:"OIN"`
}

// authorisationList gives the list a requester consults before it asks for
// an identity.
func (b bsnk) authorisationList() any {
	list := []authorizedOrganization{}
	for _, oin := range b.mock.BSNAuthorisedOINs() {
		list = append(list, authorizedOrganization{oin})
	}
	return map[string][]authorizedOrganization{"AuthorizedOrganization": list}
}

// schemeKeys gives the scheme keys a party needs to verify its values.
func (b bsnk) schemeKeys() any { return polymorphic.SchemeKeys() }

// randomize returns whether this request wants randomized values.
func (b bsnk) randomize(r *http.Request) (bool, error) {
	v := r.URL.Query().Get("randomize")
	if v == "" {
		return b.randomizeDefault, nil
	}
	randomize, err := strconv.ParseBool(v)
	if err != nil {
		return false, syntaxError("randomize must be true or false")
	}
	return randomize, nil
}

func syntaxError(description string) error {
	return &polymorphic.Fault{Reason: polymorphic.SyntaxError, Description: description}
}

// decodeRequest reads the JSON body and checks the fields every request
// carries. An unknown field is an error, so a misspelled name fails loudly
// instead of being ignored.
func decodeRequest(r *http.Request, dst interface{ base() *requestBase }) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return syntaxError("request body: " + err.Error())
	}
	if dst.base().RequestID == "" {
		return syntaxError("RequestID is required")
	}
	if _, err := time.Parse(time.RFC3339, dst.base().DateTime); err != nil {
		return syntaxError("DateTime is required, as yyyy-mm-ddThh:mm:ssZ")
	}
	return nil
}

// post wraps a use case as a POST-only JSON handler. A refusal becomes a
// fault with BSNk's reason: 400 when the request cannot be understood, 403
// when it is understood and not allowed.
func post(handle func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, http.MethodPost) {
			return
		}
		answer, err := handle(r)
		if err != nil {
			writeFault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	}
}

func writeFault(w http.ResponseWriter, err error) {
	reason, description := polymorphic.SyntaxError, err.Error()
	var f *polymorphic.Fault
	if errors.As(err, &f) {
		reason, description = f.Reason, f.Description
	}
	status := http.StatusBadRequest
	if reason == polymorphic.AuthorizationError || reason == polymorphic.ProvisioningRefused {
		status = http.StatusForbidden
	}
	writeJSON(w, status, map[string]string{"FaultReason": string(reason), "FaultDescription": description})
}

// get wraps a lookup as a GET-only JSON handler.
func get(answer func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, answer())
	}
}
