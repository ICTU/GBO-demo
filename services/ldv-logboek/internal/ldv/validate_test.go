package ldv

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// validRecord is the shape every test below mutates one field of.
func validRecord() Record {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	return Record{
		TraceID:   "0af7651916cd43dd8448eb211c80319c",
		SpanID:    "b7ad6b7169203331",
		Name:      "bronquery.doorgifte",
		Status:    StatusOK,
		StartTime: start,
		EndTime:   start.Add(12 * time.Millisecond),
		Resource:  map[string]string{"service.name": "bron-sidecar"},
		Attributes: map[string]any{
			AttrProcessingActivityID: "bd-ib-2025@v1",
			AttrDataSubjectID:        "PI-abc123",
			AttrDataSubjectIDType:    "pi",
		},
	}
}

func alwaysResolves(string) bool { return true }

func TestValidateAcceptsAWellFormedRecord(t *testing.T) {
	if err := Validate(validRecord(), alwaysResolves); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

func TestValidateRejectsMalformedRecords(t *testing.T) {
	cases := map[string]func(*Record){
		"short trace id":         func(r *Record) { r.TraceID = "0af7651916cd43dd" },
		"uppercase trace id":     func(r *Record) { r.TraceID = strings.ToUpper(r.TraceID) },
		"short span id":          func(r *Record) { r.SpanID = "b7ad6b71" },
		"bad parent span id":     func(r *Record) { r.ParentSpanID = "nope" },
		"empty name":             func(r *Record) { r.Name = "   " },
		"unknown status":         func(r *Record) { r.Status = "SUCCEEDED" },
		"zero start time":        func(r *Record) { r.StartTime = time.Time{} },
		"zero end time":          func(r *Record) { r.EndTime = time.Time{} },
		"end before start":       func(r *Record) { r.EndTime = r.StartTime.Add(-time.Second) },
		"no processing activity": func(r *Record) { delete(r.Attributes, AttrProcessingActivityID) },
		"no data subject":        func(r *Record) { delete(r.Attributes, AttrDataSubjectID) },
		"no subject id type":     func(r *Record) { delete(r.Attributes, AttrDataSubjectIDType) },
		"blank data subject":     func(r *Record) { r.Attributes[AttrDataSubjectID] = "  " },
		"numeric data subject":   func(r *Record) { r.Attributes[AttrDataSubjectID] = 42 },
		"unknown subject type":   func(r *Record) { r.Attributes[AttrDataSubjectIDType] = "bsn" },
		"unversioned activity":   func(r *Record) { r.Attributes[AttrProcessingActivityID] = "bd-ib-2025" },
		"activity with slash":    func(r *Record) { r.Attributes[AttrProcessingActivityID] = "../secrets@v1" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := validRecord()
			mutate(&record)
			err := Validate(record, alwaysResolves)
			if !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("expected ErrInvalidRecord, got %v", err)
			}
		})
	}
}

func TestValidateRejectsAnUnresolvableProcessingActivity(t *testing.T) {
	err := Validate(validRecord(), func(string) bool { return false })
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("expected ErrInvalidRecord, got %v", err)
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("error should name the unresolvable reference, got %q", err)
	}
}

func TestValidateRejectsAnOversizedAttributeMap(t *testing.T) {
	record := validRecord()
	for index := 0; index < maxAttributes; index++ {
		record.Attributes["gbo.filler."+strconv.Itoa(index)] = "x"
	}
	if err := Validate(record, alwaysResolves); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("expected ErrInvalidRecord for oversized attribute map, got %v", err)
	}
}

// The BSN guard is the logbook's own enforcement of REQ-60/REQ-72. It has to
// hold wherever the value hides, which is why these cases walk the record
// rather than only the data_subject_id.
func TestValidateRejectsABSNAnywhereInTheRecord(t *testing.T) {
	// 111222333 satisfies the elfproef: 9+8*1+7*1+6*2+5*2+4*2+3*3+2*3-3 = 66.
	const bsn = "111222333"

	cases := map[string]func(*Record){
		"as the data subject id": func(r *Record) { r.Attributes[AttrDataSubjectID] = bsn },
		"in a free attribute":    func(r *Record) { r.Attributes["gbo.note"] = "resolved to " + bsn },
		"as a JSON number":       func(r *Record) { r.Attributes["gbo.subject"] = float64(111222333) },
		"nested in a list":       func(r *Record) { r.Attributes["gbo.subjects"] = []any{"PI-1", bsn} },
		"nested in an object":    func(r *Record) { r.Attributes["gbo.debug"] = map[string]any{"in": bsn} },
		"in the record name":     func(r *Record) { r.Name = "resolve." + bsn },
		"in a resource value":    func(r *Record) { r.Resource["host.name"] = "node-" + bsn },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := validRecord()
			mutate(&record)
			if err := Validate(record, alwaysResolves); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("a BSN %s was accepted: %v", name, err)
			}
		})
	}
}

// A nine-digit run that fails the elfproef is not a BSN, and rejecting it
// would make the guard unusable for ordinary identifiers.
func TestValidateAllowsNineDigitsThatAreNotABSN(t *testing.T) {
	record := validRecord()
	record.Attributes["gbo.aangifte_identificatie"] = "111222334"
	if err := Validate(record, alwaysResolves); err != nil {
		t.Fatalf("non-BSN nine-digit value rejected: %v", err)
	}
}

func TestPassesElfproef(t *testing.T) {
	for _, valid := range []string{"111222333", "123456782"} {
		if !passesElfproef(valid) {
			t.Errorf("%s should pass the elfproef", valid)
		}
	}
	// Note that all-zeroes passes arithmetically and is therefore treated as a
	// BSN. It is not one, but the guard deliberately errs towards rejecting.
	for _, invalid := range []string{"111222334", "12345678"} {
		if passesElfproef(invalid) {
			t.Errorf("%s should fail the elfproef", invalid)
		}
	}
}
