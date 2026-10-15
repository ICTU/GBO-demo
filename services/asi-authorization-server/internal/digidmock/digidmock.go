// Package digidmock is a mock of a DigiD login at the authorization server
// (D7 of #336, slice 10): a page in the style of DigiD, like the one of the
// toestemmingsportaal, where the username is cosmetic and the password field
// carries the BSN. It checks only that the BSN has nine digits; it is not
// real DigiD. It is these two files, behind the authorization server's
// identification, so it can move to a service of its own or give way to a
// real DigiD connection.
package digidmock

import (
	_ "embed"
	"html/template"
	"regexp"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Path is where the login page is served: under the authorization endpoint,
// so the browser's transaction cookie reaches it.
const Path = authorization.AuthorizePath + "/digid"

// Means names DigiD (mock) in the identity, and so in the token's acr and
// amr. DigiD provides the BSN only.
const Means = "digid-mock"

var bsnPattern = regexp.MustCompile(`^[0-9]{9}$`)

// ValidBSN reports whether s has the form of a BSN: nine digits. There is no
// eleven-test, because not every BSN of the demo's test data passes it. A
// BSN the source does not know still logs in; the ASIP answers Unknown.
func ValidBSN(s string) bool { return bsnPattern.MatchString(s) }

//go:embed page.html
var pageSource string

// Page is the login page. Its data: Action (Path), Cancel (where Annuleren
// denies the request), TransactionID, Username and Message (why the BSN was
// refused, if it was).
var Page = template.Must(template.New("digid").Parse(pageSource))
