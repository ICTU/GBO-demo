// Package ldv holds the Logboek Dataverwerkingen core: the rules a record must
// satisfy, the verwerkingsactiviteiten register it must reference, and the two
// use cases the logbook offers — append a record and confirm it, and answer a
// read.
//
// The record type itself comes from the ldv-client module, which owns the wire
// format the standard defines. One type, one place where the format lives:
// a logbook that hand-rolled its own DTO would be a second definition of the
// same contract, free to drift from the one every producer uses.
package ldv

import (
	"encoding/json"
	"time"

	ldvclient "gbo-demo/ldv-client"
)

// Record is the LDV log record (§3.2.2).
type Record = ldvclient.Record

// Attribute keys the standard reserves, re-exported so the core reads in its
// own vocabulary without a second definition of the strings.
const (
	AttrProcessingActivityID      = ldvclient.AttrProcessingActivityID
	AttrDataSubjectID             = ldvclient.AttrDataSubjectID
	AttrDataSubjectIDType         = ldvclient.AttrDataSubjectIDType
	AttrForeignOperationProcessor = ldvclient.AttrForeignOperationProcessor
	AttrNextLogbookID             = ldvclient.AttrNextLogbookID
)

// Status values. UNSET is what OTel uses for "the producer did not say"; a
// Dataverwerking that ran to completion says OK or ERROR.
const (
	StatusUnset = "UNSET"
	StatusOK    = ldvclient.StatusOK
	StatusError = ldvclient.StatusError
)

// attributeOf returns a string-valued attribute of a record, or "" when it is
// absent or of another type.
func attributeOf(record Record, key string) string {
	value, _ := record.Attributes[key].(string)
	return value
}

// Stored is a Record as the logbook holds it: the record itself plus what the
// logbook stamped on receipt. ReceivedAt is the logbook's own clock, kept
// apart from the producer's times so a clock skew stays visible rather than
// being smoothed away.
type Stored struct {
	Record
	ReceivedAt time.Time
}

// ProcessingActivityID is the reference into the Verantwoordelijke's register,
// as a URI.
func (s Stored) ProcessingActivityID() string { return attributeOf(s.Record, AttrProcessingActivityID) }

// DataSubjectID is the pseudonymous identifier of the Betrokkene. Never a BSN
// (REQ-60/72); DataSubjectIDType says which pseudonym space it lives in.
func (s Stored) DataSubjectID() string { return attributeOf(s.Record, AttrDataSubjectID) }

// DataSubjectIDType names that pseudonym space.
func (s Stored) DataSubjectIDType() string { return attributeOf(s.Record, AttrDataSubjectIDType) }

// MarshalJSON is defined explicitly because the embedded Record carries its
// own MarshalJSON. Without this, Go would promote that method and silently
// drop ReceivedAt — the field this type exists to add.
func (s Stored) MarshalJSON() ([]byte, error) {
	record, err := json.Marshal(s.Record)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(record, &fields); err != nil {
		return nil, err
	}
	fields["received_at"] = ldvclient.EpochMillis(s.ReceivedAt)
	return json.Marshal(fields)
}

// UnmarshalJSON is the counterpart, and defined for the same reason: the
// embedded Record's UnmarshalJSON would be promoted, and it rejects unknown
// fields — so a Stored would fail to decode on the very field it adds.
func (s *Stored) UnmarshalJSON(data []byte) error {
	var envelope struct {
		ReceivedAt uint64 `json:"received_at"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	delete(fields, "received_at")
	record, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(record, &s.Record); err != nil {
		return err
	}
	s.ReceivedAt = ldvclient.FromEpochMillis(envelope.ReceivedAt)
	return nil
}

// SameProcessingAs reports whether another record describes the same
// Dataverwerking. Used to tell a producer's retry from a genuine identity
// collision: the first may be confirmed, the second must not be.
//
// ReceivedAt is excluded — that is the logbook's own stamp and differs by
// definition between the original and the replay.
func (s Stored) SameProcessingAs(other Record) bool {
	if s.Name != other.Name || s.Status != other.Status ||
		s.ParentSpanID != other.ParentSpanID ||
		!s.StartTime.Equal(other.StartTime) || !s.EndTime.Equal(other.EndTime) {
		return false
	}
	return sameJSONMap(s.Resource, other.Resource) && sameJSONMap(s.Attributes, other.Attributes)
}

// sameJSONMap compares open maps by their JSON encoding, because the values
// are free-form — numbers, lists and nested objects all occur — and Go's ==
// is not defined over them.
func sameJSONMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(encodedA) == string(encodedB)
}
