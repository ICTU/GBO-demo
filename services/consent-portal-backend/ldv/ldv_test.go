package ldv

import (
	"context"
	"testing"
	"time"

	ldvclient "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"

	"gbo-demo/consent-portal-backend/consent"
)

const activity = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-bsn-pseudonimisering/v1"

func record(t *testing.T, processing consent.Processing) map[string]any {
	t.Helper()
	fake := ldvtest.New(t, activity)
	logbook, _, err := New("consent-portal-backend", fake.URL(), "test-token")
	if err != nil || logbook == nil {
		t.Fatalf("wire the adapter: %v", err)
	}
	if err := logbook.Record(context.Background(), processing); err != nil {
		t.Fatalf("record: %v", err)
	}
	written := fake.Written()
	if len(written) != 1 {
		t.Fatalf("wrote %d records, want 1", len(written))
	}
	return written[0].Attributes
}

// A processing that called another party carries dpl.read.nextLogbookId, so a
// reader can follow it there.
func TestAProcessingThatCalledOutPointsOnwards(t *testing.T) {
	const bsnk = "https://example.test/bsnk"
	attributes := record(t, consent.Processing{
		Activity: activity, Name: "dataverwerking.bsn-pseudonimisering", Subject: "EP-abc",
		Start: time.Now(), End: time.Now(), NextLogbook: bsnk,
		Attributes: map[string]any{"dpl.gbo.pseudonimiseringAanleiding": "toestemming-verlenen"},
	})
	if got := attributes[ldvclient.AttrNextLogbookID]; got != bsnk {
		t.Errorf("nextLogbookId = %v, want %s", got, bsnk)
	}
	if got := attributes["dpl.gbo.pseudonimiseringAanleiding"]; got != "toestemming-verlenen" {
		t.Errorf("the core's own attributes were lost: %v", attributes)
	}
}

// The read extension requires the attribute to be left out when nothing else
// was called, rather than written empty.
func TestAProcessingThatCalledNoOneHasNoPointer(t *testing.T) {
	attributes := record(t, consent.Processing{
		Activity: activity, Name: "dataverwerking.bsn-pseudonimisering", Subject: "EP-abc",
		Start: time.Now(), End: time.Now(),
	})
	if _, present := attributes[ldvclient.AttrNextLogbookID]; present {
		t.Errorf("nextLogbookId present without a call: %v", attributes)
	}
}
