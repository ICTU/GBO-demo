// Package digidmock is a mock of a DigiD login at the authorization server
// (D7 of #336, slice 10): a page in the style of DigiD, like the one of the
// toestemmingsportaal, where the username is cosmetic and the password field
// carries the BSN. It checks only that the BSN has nine digits; it is not
// real DigiD. It is an adapter of the core's Identifier port, in these two
// files, so it can move to a service of its own or give way to a real DigiD
// connection.
package digidmock

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"regexp"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Path is where the login page is served: under the authorization endpoint,
// so the browser's transaction cookie reaches it.
const Path = authorization.AuthorizePath + "/digid"

// Means is the name the core gives DigiD (mock): in the identity, and so in
// the token's acr and amr.
const Means = "digid-mock"

// Identifier identifies the user by the BSN typed as the password. DigiD
// provides the BSN only, no PID attributes.
type Identifier struct{}

func (Identifier) Identify(_ context.Context, bsn string) (authorization.Identity, error) {
	if !ValidBSN(bsn) {
		return authorization.Identity{}, errors.New("not a BSN of nine digits")
	}
	return authorization.Identity{BSN: bsn}, nil
}

var bsnPattern = regexp.MustCompile(`^[0-9]{9}$`)

// ValidBSN reports whether s has the form of a BSN: nine digits. There is no
// eleven-test, because not every BSN of the demo's test data passes it. A
// BSN the source does not know still logs in; the ASIP answers Unknown.
func ValidBSN(s string) bool { return bsnPattern.MatchString(s) }

//go:embed page.html
var pageSource string

// Page is the login page, laid out like the DigiD mock of the
// toestemmingsportaal: first the choice of a method, then, for the chosen
// method, the form. Its data: Action (Path), Cancel (where Annuleren denies
// the request), TransactionID, Methods, Method (the chosen one, or none),
// Username, BSN and Message (why the BSN was refused, if it was).
var Page = template.Must(template.New("digid").Parse(pageSource))

// Method is a login method as DigiD offers it. The mock treats them all the
// same: a username, and the BSN as the password.
type Method struct{ ID, Title, Subtitle string }

// Methods are the methods the page offers, as in the toestemmingsportaal.
var Methods = []Method{
	{"app", "Met de DigiD app", "De makkelijkste manier om veilig in te loggen"},
	{"sms", "Met een sms-controle", ""},
	{"rijbewijs", "Met mijn rijbewijs", ""},
	{"id-kaart", "Met mijn identiteitskaart", ""},
}

// KnownMethod reports whether id is one of Methods.
func KnownMethod(id string) bool {
	for _, m := range Methods {
		if m.ID == id {
			return true
		}
	}
	return false
}

// The form's prefill: the username does nothing, and the BSN is that of
// test person Frouke Jansen, whom most ASI scenarios of the demo use.
const (
	DefaultUsername = "j.dejong"
	DefaultBSN      = "999991772"
)
