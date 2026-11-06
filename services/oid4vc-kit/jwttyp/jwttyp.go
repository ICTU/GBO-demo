// Package jwttyp compares the typ header of a JOSE object with the media
// type a check expects, as RFC 7515 section 4.1.9 reads it: without regard
// to case, and a value without "/" as application/<value>.
package jwttyp

import (
	"strings"

	"github.com/go-jose/go-jose/v4"
)

// Is tells whether the typ header of h is the media type want, given
// without the application/ prefix (for example "dpop+jwt").
func Is(h jose.Header, want string) bool {
	typ, _ := h.ExtraHeaders[jose.HeaderType].(string)
	if typ == "" {
		return false
	}
	if !strings.Contains(typ, "/") {
		typ = "application/" + typ
	}
	return strings.EqualFold(typ, "application/"+want)
}
