package legacy

import "testing"

func TestAPseudonymIsPerCitizenAndPerRecipient(t *testing.T) {
	const portal, other = "00000000000000000002", "99999999900000000300"

	first, again := Pseudonym("987654321", portal), Pseudonym("987654321", portal)
	if first != again {
		t.Error("one citizen got two pseudonyms at one recipient")
	}
	if Pseudonym("987654321", portal) == Pseudonym("987654321", other) {
		t.Error("two recipients got the same pseudonym")
	}
	if Pseudonym("987654321", portal) == Pseudonym("123456789", portal) {
		t.Error("two citizens got the same pseudonym")
	}
}

// The portal's reference to a citizen is stored with every consent, so the
// value may not change under it.
func TestThePseudonymIsStable(t *testing.T) {
	if got := Pseudonym("123456789", "00000000000000000002"); got != "EP-c44cade392924e3b" {
		t.Errorf("pseudonym = %s, want EP-c44cade392924e3b", got)
	}
}
