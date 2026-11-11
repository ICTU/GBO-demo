package statuslist_test

import (
	"testing"

	"gbo-demo/oid4vc-kit/statuslist"
)

func TestEncodeDecode(t *testing.T) {
	lst, err := statuslist.Encode(1024, []int{0, 9, 1023})
	if err != nil {
		t.Fatal(err)
	}
	bits, err := statuslist.Decode(lst)
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{0, 9, 1023} {
		if !statuslist.Revoked(bits, idx) {
			t.Fatalf("entry %d should be revoked", idx)
		}
	}
	for _, idx := range []int{1, 8, 10, 1022} {
		if statuslist.Revoked(bits, idx) {
			t.Fatalf("entry %d should be valid", idx)
		}
	}
	// Bit order: index 9 is the second bit of the second byte.
	if bits[1] != 0b10 {
		t.Fatalf("unexpected bit layout %08b", bits[1])
	}
	if _, err := statuslist.Encode(8, []int{8}); err == nil {
		t.Fatal("an index beyond the list size must be refused")
	}
}
