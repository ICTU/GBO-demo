package digidmock

import (
	"context"
	"testing"
)

// The adapter identifies the user by a BSN of nine digits, and provides
// nothing else: no means (the core names it) and no PID attributes.
func TestIdentify(t *testing.T) {
	id, err := Identifier{}.Identify(context.Background(), "555555555")
	if err != nil || id.BSN != "555555555" || id.Means != "" || id.FamilyName != "" {
		t.Fatalf("Identify = %+v, %v; want the BSN only", id, err)
	}
	for _, bsn := range []string{"", "12345678", "1234567890", "12345678a", " 999991772"} {
		if _, err := (Identifier{}).Identify(context.Background(), bsn); err == nil {
			t.Errorf("Identify(%q) identified the user", bsn)
		}
	}
}
