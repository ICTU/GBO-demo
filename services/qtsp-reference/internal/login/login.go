// Package login is who the citizen is at the reference QTSP. The QTSP
// identifies the citizen itself before a request starts (eIDAS Article
// 24(1); slice 14 of #336): with the NL Wallet, a means with a session on
// the citizen's phone, or with test data (a mock that checks nothing). A
// login gives the name, date of birth and nationalities, never the BSN,
// which a QTSP may not process.
package login

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// The means, as the QTSP names them.
const (
	MeansWallet = "nl-wallet"
	MeansMock   = "test-data-mock"
)

// Identity is the citizen as a login means gives them.
type Identity struct {
	FamilyName, GivenName, Birthdate string
	Nationalities                    []string
	Means                            string
}

// Name is how the pages show the citizen.
func (i Identity) Name() string { return strings.TrimSpace(i.GivenName + " " + i.FamilyName) }

// Mocked reports whether the identity comes from the mock.
func (i Identity) Mocked() bool { return i.Means == MeansMock }

// State is how far a wallet session is.
type State int

const (
	// Waiting: the citizen has not finished in the wallet.
	Waiting State = iota
	// Done: the wallet disclosed; Identify gives the identity.
	Done
	// Failed: declined in the wallet, or expired.
	Failed
)

// Progress is the state of a wallet session; Link is the universal link for
// the QR code while the wallet has not fetched the request.
type Progress struct {
	State State
	Link  string
}

// ErrUnavailable: the means cannot be reached now; the citizen can log in
// with the mock instead.
var ErrUnavailable = errors.New("the login means is unavailable")

// Wallet is the NL Wallet as login means: a disclosure session on the
// citizen's phone, its progress, and the identity it disclosed.
type Wallet interface {
	Start(ctx context.Context) (session string, err error)
	Progress(ctx context.Context, session string) (Progress, error)
	Identify(ctx context.Context, session string) (Identity, error)
}

var (
	countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
	isoDate     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// ErrInvalidMock: the test data is incomplete or malformed.
var ErrInvalidMock = errors.New("test data incomplete or malformed")

// Mock is a login with test data: all four fields, the date of birth as
// YYYY-MM-DD and the nationalities as ISO 3166-1 alpha-2 codes separated by
// commas. Nothing else is checked: it is not identity proofing.
func Mock(familyName, givenName, birthdate, nationalities string) (Identity, error) {
	id := Identity{FamilyName: strings.TrimSpace(familyName), GivenName: strings.TrimSpace(givenName), Birthdate: strings.TrimSpace(birthdate), Means: MeansMock}
	if id.FamilyName == "" || id.GivenName == "" || !isoDate.MatchString(id.Birthdate) {
		return Identity{}, ErrInvalidMock
	}
	if _, err := time.Parse(time.DateOnly, id.Birthdate); err != nil {
		return Identity{}, ErrInvalidMock
	}
	for _, c := range strings.Split(nationalities, ",") {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !countryCode.MatchString(c) {
			return Identity{}, ErrInvalidMock
		}
		id.Nationalities = append(id.Nationalities, c)
	}
	return id, nil
}
