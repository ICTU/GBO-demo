package jwttyp_test

import (
	"testing"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/jwttyp"
)

// RFC 7515 section 4.1.9: typ is a media type, compared without regard to
// case, and a value without "/" stands for application/<value>.
func TestIs(t *testing.T) {
	header := func(typ any) jose.Header {
		h := jose.Header{ExtraHeaders: map[jose.HeaderKey]any{}}
		if typ != nil {
			h.ExtraHeaders[jose.HeaderType] = typ
		}
		return h
	}
	for typ, want := range map[any]bool{
		"dpop+jwt":             true,
		"DPoP+JWT":             true, // C-1
		"application/dpop+jwt": true, // C-2
		"Application/DPOP+jwt": true,
		"jwt":                  false, // C-3
		"text/dpop+jwt":        false,
		"application/jwt":      false,
		"":                     false,
		42:                     false,
	} {
		if got := jwttyp.Is(header(typ), "dpop+jwt"); got != want {
			t.Errorf("typ %v: %v, want %v", typ, got, want)
		}
	}
	if jwttyp.Is(header(nil), "dpop+jwt") {
		t.Error("no typ matched")
	}
}
