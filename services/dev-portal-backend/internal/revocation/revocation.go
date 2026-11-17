// Package revocation lets a tester revoke what the developer portal had the
// own credential issuer offer, and shows the public status lists of those
// offers.
//
// Every offer the portal makes carries a reference of its own; the issuer
// links the status list entries of the credentials it issues for the offer
// to that reference, and revokes them all by it. The portal keeps its
// offers in memory, so after a restart it no longer lists them.
//
// Demo shortcut: anyone who reaches the portal can revoke every offer it
// made, and any entry of any status list. In the EUDI Wallet ecosystem only
// the provider of an attestation revokes it (ARF VCR_03).
package revocation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"gbo-demo/dev-portal-backend/internal/testpid"
)

// Kind is what the portal offered.
type Kind string

const (
	KindTestPID     Kind = "test_pid"
	KindAttestation Kind = "attestation"
)

// The status list of the test PID: the provider and list names the issuer
// configuration gives it (eudi-adapter provision-test-pid).
const (
	testPIDProvider = "test-pid"
	testPIDList     = "pid"
)

// maxOffers bounds the offers kept in memory; the oldest go first.
const maxOffers = 500

// Offer is an offer the portal made, and its revocation.
type Offer struct {
	Reference string    `json:"reference"`
	Kind      Kind      `json:"kind"`
	Label     string    `json:"label"`
	BSN       string    `json:"bsn"`
	Provider  string    `json:"provider"`
	List      string    `json:"list"`
	OfferedAt time.Time `json:"offered_at"`
	// RevokedAt is set once the offer is revoked.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// RevokedEntries is how many status list entries the revocation set:
	// none when the wallet had not taken the offer yet, after which the
	// issuer issues nothing for it anymore.
	RevokedEntries int `json:"revoked_entries"`
}

// AttestationOffer is an offer a source publishes (the eudi-adapter's
// /eudi-offers.json): a credential type with its parameters.
type AttestationOffer struct {
	Key        string         `json:"key"`
	Label      string         `json:"label"`
	SourceID   string         `json:"source_id"`
	TypeID     string         `json:"type_id"`
	Parameters map[string]any `json:"parameters"`
}

// Catalog returns the offers the sources publish.
type Catalog interface {
	Offers(ctx context.Context) ([]AttestationOffer, error)
}

// Revoker revokes at the issuer. Errors are testpid.ErrUnreachable or a
// *testpid.Refusal, as for offers.
type Revoker interface {
	RevokeReference(ctx context.Context, reference string) (int, error)
	RevokeEntry(ctx context.Context, provider, list string, index int) error
}

// StatusFetcher returns the Status List Token served at uri.
type StatusFetcher func(ctx context.Context, uri string) (string, error)

// Error is why a request failed. Its value is the code the portal shows a
// message for.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidRequest     Error = "invalid_request"
	ErrUnknownOffer       Error = "unknown_offer"
	ErrUnknownReference   Error = "unknown_reference"
	ErrUnknownEntry       Error = "unknown_entry"
	ErrNotConfigured      Error = "offer_not_configured"
	ErrCatalogUnavailable Error = "catalog_unavailable"
)

// Config wires a Service.
type Config struct {
	Issuer  testpid.Issuer
	Revoker Revoker
	Catalog Catalog
	Fetch   StatusFetcher
	// StatusBaseURL is where the issuer serves its status lists, followed
	// by /status/{provider}/{list}.
	StatusBaseURL string
	// Persons are the test persons: an attestation offer is made for their
	// BSN only, as a test PID offer is.
	Persons      []testpid.Person
	Now          func() time.Time
	NewReference func() string
}

// Service makes offers with a reference, revokes them and shows their
// status lists.
type Service struct {
	cfg    Config
	known  map[string]bool
	mu     sync.Mutex
	offers []Offer
}

func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewReference == nil {
		cfg.NewReference = NewReference
	}
	known := make(map[string]bool, len(cfg.Persons))
	for _, p := range cfg.Persons {
		known[p.BSN] = true
	}
	return &Service{cfg: cfg, known: known}
}

// NewReference returns a fresh reference for an offer of the portal. The
// issuer keeps a credential with a reference valid for its configured
// days; one without is short-lived, as nobody could revoke it.
func NewReference() string { return "devportal-" + uuid.NewString() }

// NewReference returns a reference for an offer the portal is about to
// make.
func (s *Service) NewReference() string { return s.cfg.NewReference() }

// RecordTestPID lists a test PID offer the portal made with reference.
func (s *Service) RecordTestPID(reference, bsn string) {
	s.record(Offer{Reference: reference, Kind: KindTestPID, Label: "Test-PID", BSN: bsn,
		Provider: testPIDProvider, List: testPIDList})
}

// OfferAttestation asks the issuer for an offer of a source's attestation
// for bsn, with a reference, and lists it. The source and key name an offer
// the source publishes; the configuration and its parameters are that
// offer's.
func (s *Service) OfferAttestation(ctx context.Context, sourceID, key, bsn string) (Offer, string, error) {
	if sourceID == "" || key == "" || bsn == "" {
		return Offer{}, "", ErrInvalidRequest
	}
	if !s.known[bsn] {
		return Offer{}, "", testpid.ErrUnknownPerson
	}
	published, err := s.cfg.Catalog.Offers(ctx)
	if err != nil {
		return Offer{}, "", fmt.Errorf("%w: %w", ErrCatalogUnavailable, err)
	}
	var chosen *AttestationOffer
	for i := range published {
		if published[i].SourceID == sourceID && published[i].Key == key {
			chosen = &published[i]
		}
	}
	if chosen == nil {
		return Offer{}, "", ErrUnknownOffer
	}
	parameters, err := offerParameters(chosen.Parameters)
	if err != nil {
		return Offer{}, "", fmt.Errorf("%w: %w", ErrUnknownOffer, err)
	}
	reference := s.cfg.NewReference()
	links, err := s.cfg.Issuer.Offer(ctx, testpid.OfferRequest{
		Profile: testpid.Profile,
		// The configuration the eudi-adapter derives for the offer.
		ConfigurationIDs: []string{chosen.SourceID + "-" + chosen.Key},
		BSN:              bsn, Parameters: parameters, Reference: reference,
	})
	if err != nil {
		err = testpid.Reason(err)
		if errors.Is(err, testpid.ErrNotConfigured) {
			err = fmt.Errorf("%w: %w", ErrNotConfigured, err)
		}
		return Offer{}, "", err
	}
	link, err := testpid.OfferLink(links)
	if err != nil {
		return Offer{}, "", err
	}
	offer := s.record(Offer{Reference: reference, Kind: KindAttestation, Label: chosen.Label, BSN: bsn,
		Provider: chosen.SourceID, List: chosen.TypeID})
	return offer, link, nil
}

// offerParameters writes the parameters of a published offer as the text
// the issuer passes on, as the eudi-adapter does for the issuer's own
// offers: strings as they are, booleans and integers in decimal.
func offerParameters(in map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for name, value := range in {
		switch v := value.(type) {
		case string:
			out[name] = v
		case bool:
			out[name] = strconv.FormatBool(v)
		case float64:
			if v != float64(int64(v)) {
				return nil, fmt.Errorf("parameter %s is not an integer", name)
			}
			out[name] = strconv.FormatInt(int64(v), 10)
		default:
			return nil, fmt.Errorf("parameter %s has an unsupported type %T", name, value)
		}
	}
	return out, nil
}

func (s *Service) record(o Offer) Offer {
	o.OfferedAt = s.cfg.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offers = append(s.offers, o)
	if len(s.offers) > maxOffers {
		s.offers = s.offers[len(s.offers)-maxOffers:]
	}
	return o
}

// Offers are the offers the portal made, newest first.
func (s *Service) Offers() []Offer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Offer, len(s.offers))
	for i, o := range s.offers {
		out[len(s.offers)-1-i] = o
	}
	return out
}

// RevokeOffer revokes every credential issued for an offer the portal made.
// A revocation cannot be undone; revoking again sets nothing new, and the
// offer keeps the count of its first revocation.
func (s *Service) RevokeOffer(ctx context.Context, reference string) (Offer, error) {
	if _, ok := s.find(reference); !ok {
		return Offer{}, ErrUnknownReference
	}
	n, err := s.cfg.Revoker.RevokeReference(ctx, reference)
	if err != nil {
		return Offer{}, revocationReason(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.offers {
		if s.offers[i].Reference == reference {
			if s.offers[i].RevokedAt == nil {
				now := s.cfg.Now().UTC()
				s.offers[i].RevokedAt = &now
				s.offers[i].RevokedEntries = n
			}
			return s.offers[i], nil
		}
	}
	// Dropped from memory meanwhile: the issuer revoked it all the same.
	return Offer{Reference: reference, RevokedEntries: n}, nil
}

// RevokeEntry revokes one entry of a provider's status list, which need not
// belong to an offer of the portal.
func (s *Service) RevokeEntry(ctx context.Context, provider, list string, index int) error {
	if provider == "" || list == "" || index < 0 {
		return ErrInvalidRequest
	}
	if err := s.cfg.Revoker.RevokeEntry(ctx, provider, list, index); err != nil {
		return revocationReason(err)
	}
	return nil
}

func (s *Service) find(reference string) (Offer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.offers {
		if o.Reference == reference {
			return o, true
		}
	}
	return Offer{}, false
}

// revocationReason translates the issuer's failure to revoke.
func revocationReason(err error) error {
	if errors.Is(err, testpid.ErrUnreachable) {
		return fmt.Errorf("%w: %w", testpid.ErrIssuerUnavailable, err)
	}
	var refusal *testpid.Refusal
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case "unknown_entry":
			return fmt.Errorf("%w: %w", ErrUnknownEntry, err)
		case "invalid_request":
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
	}
	return fmt.Errorf("%w: %w", testpid.ErrIssuer, err)
}
