package ldvclient

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A keyless pseudonym is worse than none, because it looks like one: an HMAC
// under an empty key is deterministic and public, so the whole BSN range
// could be enumerated offline and matched against the logbook. The doc used
// to claim this panicked; it silently produced the keyless value instead.
func TestPseudonymisationWithoutAKeyIsAnError(t *testing.T) {
	client, err := New(Config{
		ServiceName: "bron-sidecar", LogbookURL: "http://logboek:4016", WriteToken: "t",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if _, err := client.LocalPseudonym("123456789"); !errors.Is(err, ErrNoPseudonymKey) {
		t.Fatalf("LocalPseudonym error = %v, want ErrNoPseudonymKey", err)
	}
	if _, _, err := client.Subject(http.Header{}, "123456789"); !errors.Is(err, ErrNoPseudonymKey) {
		t.Fatalf("Subject error = %v, want ErrNoPseudonymKey", err)
	}
}

// A component that was handed a pseudonym by an upstream component of the same
// Verantwoordelijke needs no key of its own, so that path still works.
func TestSubjectNeedsNoKeyWhenAPseudonymWasPassedOn(t *testing.T) {
	client, err := New(Config{
		ServiceName: "graphql-server", LogbookURL: "http://logboek:4016", WriteToken: "t",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	header := http.Header{}
	header.Set(HeaderSubjectID, "PI-abc123")
	header.Set(HeaderSubjectIDType, SubjectTypePI)

	id, idType, err := client.Subject(header, "123456789")
	if err != nil {
		t.Fatalf("Subject: %v", err)
	}
	if id != "PI-abc123" || idType != SubjectTypePI {
		t.Fatalf("Subject = (%q, %q)", id, idType)
	}
}

// With a key the pseudonym is stable, key-dependent, and free of the BSN.
func TestLocalPseudonymWithAKey(t *testing.T) {
	first, err := New(Config{ServiceName: "s", LogbookURL: "http://l", WriteToken: "t", PseudonymKey: "key-a"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	second, err := New(Config{ServiceName: "s", LogbookURL: "http://l", WriteToken: "t", PseudonymKey: "key-b"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const bsn = "123456789"
	a, err := first.LocalPseudonym(bsn)
	if err != nil {
		t.Fatalf("LocalPseudonym: %v", err)
	}
	again, _ := first.LocalPseudonym(bsn)
	b, _ := second.LocalPseudonym(bsn)
	if a != again {
		t.Error("the same BSN must map to the same pseudonym within one logbook")
	}
	if a == b {
		t.Error("two Verantwoordelijken must not derive the same pseudonym")
	}
	if strings.Contains(a, bsn) {
		t.Errorf("the pseudonym contains the BSN: %q", a)
	}
}
