package revocation

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"gbo-demo/dev-portal-backend/internal/testpid"
)

type fakeIssuer struct {
	calls []testpid.OfferRequest
	err   error
}

func (f *fakeIssuer) Offer(_ context.Context, req testpid.OfferRequest) (map[string]string, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return map[string]string{"openid-credential-offer": "openid-credential-offer://?" + req.Reference}, nil
}

type fakeRevoker struct {
	references []string
	entries    []string
	count      int
	err        error
}

func (f *fakeRevoker) RevokeReference(_ context.Context, reference string) (int, error) {
	f.references = append(f.references, reference)
	return f.count, f.err
}

func (f *fakeRevoker) RevokeEntry(_ context.Context, provider, list string, index int) error {
	f.entries = append(f.entries, provider+"/"+list+"/"+strconv.Itoa(index))
	return f.err
}

type fakeCatalog struct {
	offers []AttestationOffer
	err    error
}

func (f fakeCatalog) Offers(context.Context) ([]AttestationOffer, error) { return f.offers, f.err }

var inkomen = AttestationOffer{Key: "inkomensverklaring_2024", Label: "Inkomensverklaring 2024",
	SourceID: "belastingdienst", TypeID: "inkomensverklaring", Parameters: map[string]any{"belastingjaar": float64(2024), "definitief": true}}

var now = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func newService(issuer *fakeIssuer, revoker *fakeRevoker, fetch StatusFetcher) *Service {
	n := 0
	return New(Config{
		Issuer: issuer, Revoker: revoker, Catalog: fakeCatalog{offers: []AttestationOffer{inkomen}}, Fetch: fetch,
		Persons:       []testpid.Person{{BSN: "999991772", Name: "Frouke Jansen"}},
		StatusBaseURL: "http://issuer:8080/", Now: func() time.Time { return now },
		NewReference: func() string { n++; return "devportal-" + strconv.Itoa(n) },
	})
}

// B2: an attestation offer has the configuration and parameters of the
// published offer, the BSN and a reference, and is listed.
func TestAnAttestationOfferHasAReference(t *testing.T) {
	issuer := &fakeIssuer{}
	s := newService(issuer, &fakeRevoker{}, nil)
	offer, link, err := s.OfferAttestation(context.Background(), "belastingdienst", "inkomensverklaring_2024", "999991772")
	if err != nil {
		t.Fatal(err)
	}
	want := []testpid.OfferRequest{{Profile: "eudi", ConfigurationIDs: []string{"belastingdienst-inkomensverklaring_2024"},
		BSN: "999991772", Parameters: map[string]string{"belastingjaar": "2024", "definitief": "true"}, Reference: "devportal-1"}}
	if !reflect.DeepEqual(issuer.calls, want) {
		t.Errorf("issuer asked %v, want %v", issuer.calls, want)
	}
	if link != "openid-credential-offer://?devportal-1" {
		t.Errorf("link = %q", link)
	}
	wantOffer := Offer{Reference: "devportal-1", Kind: KindAttestation, Label: "Inkomensverklaring 2024", BSN: "999991772",
		Provider: "belastingdienst", List: "inkomensverklaring", OfferedAt: now}
	if offer != wantOffer || !reflect.DeepEqual(s.Offers(), []Offer{wantOffer}) {
		t.Errorf("offer = %v, offers = %v", offer, s.Offers())
	}
}

// B1: a test PID offer is listed under the test PID's status list.
func TestATestPIDOfferIsListed(t *testing.T) {
	s := newService(&fakeIssuer{}, &fakeRevoker{}, nil)
	s.RecordTestPID("devportal-9", "999991772")
	want := []Offer{{Reference: "devportal-9", Kind: KindTestPID, Label: "Test-PID", BSN: "999991772",
		Provider: "test-pid", List: "pid", OfferedAt: now}}
	if !reflect.DeepEqual(s.Offers(), want) {
		t.Errorf("offers = %v, want %v", s.Offers(), want)
	}
}

// B3: an unknown offer or a missing field is refused before the issuer is
// asked.
func TestAnUnknownOfferIsRefused(t *testing.T) {
	issuer := &fakeIssuer{}
	s := newService(issuer, &fakeRevoker{}, nil)
	for _, tc := range []struct {
		source, key, bsn string
		want             error
	}{
		{"belastingdienst", "onbekend", "999991772", ErrUnknownOffer},
		{"rvig", "inkomensverklaring_2024", "999991772", ErrUnknownOffer},
		{"belastingdienst", "inkomensverklaring_2024", "", ErrInvalidRequest},
		// B14: only a test person's BSN, as for the test PID.
		{"belastingdienst", "inkomensverklaring_2024", "123456782", testpid.ErrUnknownPerson},
		{"", "inkomensverklaring_2024", "999991772", ErrInvalidRequest},
	} {
		if _, _, err := s.OfferAttestation(context.Background(), tc.source, tc.key, tc.bsn); !errors.Is(err, tc.want) {
			t.Errorf("%s/%s %q: %v, want %v", tc.source, tc.key, tc.bsn, err, tc.want)
		}
	}
	if len(issuer.calls) != 0 || len(s.Offers()) != 0 {
		t.Errorf("issuer asked %v, offers %v; want neither", issuer.calls, s.Offers())
	}
	broken := New(Config{Issuer: issuer, Catalog: fakeCatalog{err: errors.New("down")}, Persons: []testpid.Person{{BSN: "999991772"}}})
	if _, _, err := broken.OfferAttestation(context.Background(), "belastingdienst", "inkomensverklaring_2024", "999991772"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Errorf("without the catalog: %v", err)
	}
}

// B4: the issuer's refusal is the reason, and nothing is listed.
func TestARefusedOfferIsNotListed(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{&testpid.Refusal{Status: 404, Code: "no_data"}, testpid.ErrNoData},
		{&testpid.Refusal{Status: 503, Code: "source_unavailable"}, testpid.ErrSourceUnavailable},
		{&testpid.Refusal{Status: 400, Code: "unknown_configuration"}, ErrNotConfigured},
		{testpid.ErrUnreachable, testpid.ErrIssuerUnavailable},
	} {
		s := newService(&fakeIssuer{err: tc.err}, &fakeRevoker{}, nil)
		if _, _, err := s.OfferAttestation(context.Background(), "belastingdienst", "inkomensverklaring_2024", "999991772"); !errors.Is(err, tc.want) {
			t.Errorf("%v: %v, want %v", tc.err, err, tc.want)
		}
		if len(s.Offers()) != 0 {
			t.Errorf("%v: offers %v, want none", tc.err, s.Offers())
		}
	}
}

// B5 and B9: revoking an offer revokes its reference at the issuer and
// marks the offer; revoking it again keeps the first count.
func TestRevokingAnOffer(t *testing.T) {
	revoker := &fakeRevoker{count: 4}
	s := newService(&fakeIssuer{}, revoker, nil)
	s.RecordTestPID("devportal-9", "999991772")
	offer, err := s.RevokeOffer(context.Background(), "devportal-9")
	if err != nil {
		t.Fatal(err)
	}
	if offer.RevokedAt == nil || !offer.RevokedAt.Equal(now) || offer.RevokedEntries != 4 {
		t.Errorf("offer = %+v, want revoked now with 4 entries", offer)
	}
	revoker.count = 0
	again, err := s.RevokeOffer(context.Background(), "devportal-9")
	if err != nil || again.RevokedEntries != 4 {
		t.Errorf("again = %+v, %v; want the first count", again, err)
	}
	if !reflect.DeepEqual(revoker.references, []string{"devportal-9", "devportal-9"}) {
		t.Errorf("issuer asked %v", revoker.references)
	}
}

// B6: a reference the portal did not hand out is not sent.
func TestAnUnknownReferenceIsNotSent(t *testing.T) {
	revoker := &fakeRevoker{}
	s := newService(&fakeIssuer{}, revoker, nil)
	if _, err := s.RevokeOffer(context.Background(), "someone-else"); !errors.Is(err, ErrUnknownReference) {
		t.Errorf("err = %v", err)
	}
	if len(revoker.references) != 0 {
		t.Errorf("issuer asked %v", revoker.references)
	}
}

// B7: an entry is revoked by provider, list and index; one the issuer does
// not know is unknown_entry; a negative index or a missing name is not sent.
func TestRevokingAnEntry(t *testing.T) {
	revoker := &fakeRevoker{}
	s := newService(&fakeIssuer{}, revoker, nil)
	if err := s.RevokeEntry(context.Background(), "test-pid", "pid", 7); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider, list string
		index          int
	}{{"", "pid", 1}, {"test-pid", "", 1}, {"test-pid", "pid", -1}} {
		if err := s.RevokeEntry(context.Background(), tc.provider, tc.list, tc.index); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%v: %v, want invalid_request", tc, err)
		}
	}
	if !reflect.DeepEqual(revoker.entries, []string{"test-pid/pid/7"}) {
		t.Errorf("issuer asked %v", revoker.entries)
	}
	revoker.err = &testpid.Refusal{Status: 404, Code: "unknown_entry"}
	if err := s.RevokeEntry(context.Background(), "test-pid", "pid", 70000); !errors.Is(err, ErrUnknownEntry) {
		t.Errorf("err = %v, want unknown_entry", err)
	}
}

// B8: an issuer that does not answer leaves the offer as it was.
func TestAnUnreachableIssuerRevokesNothing(t *testing.T) {
	s := newService(&fakeIssuer{}, &fakeRevoker{err: testpid.ErrUnreachable}, nil)
	s.RecordTestPID("devportal-9", "999991772")
	if _, err := s.RevokeOffer(context.Background(), "devportal-9"); !errors.Is(err, testpid.ErrIssuerUnavailable) {
		t.Errorf("err = %v", err)
	}
	if s.Offers()[0].RevokedAt != nil {
		t.Errorf("offer = %+v, want not revoked", s.Offers()[0])
	}
}

// statusListToken is a Status List Token whose entries at revoked are 1,
// unsigned: the portal does not verify.
func statusListToken(t *testing.T, size int, revoked ...int) string {
	t.Helper()
	bits := make([]byte, size/8)
	for _, i := range revoked {
		bits[i/8] |= 1 << (i % 8)
	}
	var lst bytes.Buffer
	w := zlib.NewWriter(&lst)
	_, _ = w.Write(bits)
	_ = w.Close()
	payload, _ := json.Marshal(map[string]any{"sub": "https://issuer.example/status/test-pid/pid", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "ttl": 300,
		"status_list": map[string]any{"bits": 1, "lst": base64.RawURLEncoding.EncodeToString(lst.Bytes())}})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"typ":"statuslist+jwt"}`)) + "." + enc(payload) + ".c2ln"
}

// B10 and B11: each list the offers use is fetched and decoded, and shown
// under the URI it names; one that cannot be read has its error and the
// others are shown.
func TestStatusLists(t *testing.T) {
	var fetched []string
	fetch := func(_ context.Context, uri string) (string, error) {
		fetched = append(fetched, uri)
		if uri == "http://issuer:8080/status/belastingdienst/inkomensverklaring" {
			return "", errors.New("the issuer answered 404")
		}
		return statusListToken(t, 64, 3, 17, 63), nil
	}
	s := newService(&fakeIssuer{}, &fakeRevoker{}, fetch)
	s.RecordTestPID("devportal-8", "999991772")
	s.RecordTestPID("devportal-9", "999991772")
	if _, _, err := s.OfferAttestation(context.Background(), "belastingdienst", "inkomensverklaring_2024", "999991772"); err != nil {
		t.Fatal(err)
	}
	exp := now.Add(time.Hour)
	want := []StatusList{
		{Provider: "belastingdienst", List: "inkomensverklaring", URI: "http://issuer:8080/status/belastingdienst/inkomensverklaring",
			Offers: 1, Revoked: []int{}, Error: "the issuer answered 404"},
		{Provider: "test-pid", List: "pid", URI: "https://issuer.example/status/test-pid/pid", Offers: 2,
			IssuedAt: &now, Expires: &exp, TTL: 300, Bits: 1, Size: 64, Revoked: []int{3, 17, 63}},
	}
	if got := s.StatusLists(context.Background()); !reflect.DeepEqual(got, want) {
		t.Errorf("lists = %+v,\nwant %+v", got, want)
	}
	if len(fetched) != 2 {
		t.Errorf("fetched %v, want each list once", fetched)
	}
}

// B10: a list with two bits per entry counts any non-zero status.
func TestDecodeTwoBitStatuses(t *testing.T) {
	var lst bytes.Buffer
	w := zlib.NewWriter(&lst)
	_, _ = w.Write([]byte{0b10_00_01_00}) // entries 1 and 3 are not valid
	_ = w.Close()
	payload, _ := json.Marshal(map[string]any{"status_list": map[string]any{"bits": 2, "lst": base64.RawURLEncoding.EncodeToString(lst.Bytes())}})
	var list StatusList
	if err := decodeStatusList("e30."+base64.RawURLEncoding.EncodeToString(payload)+".x", &list); err != nil {
		t.Fatal(err)
	}
	if list.Size != 4 || !reflect.DeepEqual(list.Revoked, []int{1, 3}) {
		t.Errorf("list = %+v", list)
	}
	for _, bad := range []string{"not-a-jwt", "e30.!!.x", "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"status_list":{"bits":3,"lst":""}}`)) + ".x"} {
		if err := decodeStatusList(bad, &StatusList{}); err == nil {
			t.Errorf("%q decoded, want an error", bad)
		}
	}
}
