package consent

import (
	"errors"
	"strings"
	"testing"
)

func TestASubjectReferenceIsStablePerCitizen(t *testing.T) {
	first, err := testSubjectRefs.For("999991772")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	again, _ := testSubjectRefs.For(" 999991772 ")
	other, _ := testSubjectRefs.For("123456789")
	if first != again {
		t.Errorf("one BSN gave %q and %q", first, again)
	}
	if first == other {
		t.Error("two BSNs gave the same reference")
	}
	if strings.Contains(string(first), "999991772") {
		t.Errorf("the reference %q carries the BSN", first)
	}
}

// A BSN with or without its leading zero is the same BSN.
func TestALeadingZeroDoesNotMakeAnotherCitizen(t *testing.T) {
	short, _ := testSubjectRefs.For("12345678")
	padded, _ := testSubjectRefs.For("012345678")
	if short != padded {
		t.Errorf("12345678 gave %q, 012345678 gave %q", short, padded)
	}
}

// The key is what makes the reference unguessable, and the version in the
// reference says which key made it.
func TestTheReferenceDependsOnTheKeyAndNamesItsVersion(t *testing.T) {
	other := SubjectRefs{Key: []byte("another-subject-ref-key-of-32-by"), Version: "2"}
	a, _ := testSubjectRefs.For("999991772")
	b, _ := other.For("999991772")
	if a == b {
		t.Error("two keys gave the same reference")
	}
	if !strings.HasPrefix(string(a), "SR1-") || !strings.HasPrefix(string(b), "SR2-") {
		t.Errorf("references %q and %q do not name their key version", a, b)
	}
	// 256 bits of HMAC, hex-encoded.
	if hex := strings.TrimPrefix(string(a), "SR1-"); len(hex) != 64 {
		t.Errorf("reference %q carries %d hex characters, want 64", a, len(hex))
	}
}

func TestAReferenceNeedsAKeyAndABSN(t *testing.T) {
	if _, err := (SubjectRefs{Version: "1"}).For("999991772"); !errors.Is(err, ErrNoSubjectKey) {
		t.Errorf("without a key: err = %v, want ErrNoSubjectKey", err)
	}
	for _, value := range []BSN{"", "abc", "1234567890", "99999-772"} {
		if _, err := testSubjectRefs.For(value); !errors.Is(err, ErrInvalidBSN) {
			t.Errorf("%q: err = %v, want ErrInvalidBSN", value, err)
		}
	}
}
