package legacy

import "testing"

func TestAPIResolvesToTheBSNItWasMadeFor(t *testing.T) {
	s := NewStore()
	pi, pseudonym := s.Pseudonymize("987654321", "99999999900000000200")
	if pi == "" || pseudonym == "" || pi == pseudonym {
		t.Fatalf("pi %q, pseudonym %q", pi, pseudonym)
	}
	if bsn, ok := s.Resolve(pi); !ok || bsn != "987654321" {
		t.Errorf("Resolve = %q, %v; want the BSN", bsn, ok)
	}
	if _, ok := s.Resolve("PI-0000000000000000"); ok {
		t.Error("a PI the store never made resolved")
	}
}

func TestThePIIsPerBSNAndThePseudonymPerRecipient(t *testing.T) {
	s := NewStore()
	pi1, ps1 := s.Pseudonymize("987654321", "99999999900000000200")
	pi2, ps2 := s.Pseudonymize("987654321", "99999999900000000300")
	if pi1 != pi2 {
		t.Error("one BSN gave two PIs")
	}
	if ps1 == ps2 {
		t.Error("two recipients got the same pseudonym")
	}
}

// A PI made for the demo BSN before a restart still resolves after it.
func TestAFreshStoreKnowsTheDemoBSN(t *testing.T) {
	pi, _ := NewStore().Pseudonymize(demoBSN, "99999999900000000200")
	if bsn, ok := NewStore().Resolve(pi); !ok || bsn != demoBSN {
		t.Errorf("Resolve on a fresh store = %q, %v", bsn, ok)
	}
}
