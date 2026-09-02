package ldv

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidRecord is the class of every rejection below. The logbook answers
// 422 on it: the record is well-formed JSON but not a lawful LDV record, and
// retrying the same bytes will not help.
var ErrInvalidRecord = errors.New("invalid log record")

// SubjectIDTypes are the pseudonym spaces a Betrokkene may be named in.
// The list is closed on purpose: an unknown type is unreadable to whoever
// has to resolve the record later, and leaving it open is exactly how a raw
// identifier ends up in the field.
//
//   - pi                  polymorphic identity from BSNk, the identifier the
//     DvTP chain works with end to end
//   - logboek-pseudoniem  a logbook-local, key-derived pseudonym, used where a
//     component holds only a BSN (the EUDI flow) and therefore has nothing
//     else to name the Betrokkene by. A demo stand-in: it is stable within one
//     Verantwoordelijke and meaningless outside it.
//   - portal-subject      the portal-scoped subject reference of the
//     consent-register (phase 2)
var SubjectIDTypes = map[string]bool{
	"pi":                 true,
	"logboek-pseudoniem": true,
	"portal-subject":     true,
}

var (
	traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDPattern  = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// bsnCandidate finds any nine-digit run in a value. Whether it really is
	// a BSN is then decided by the elfproef.
	bsnCandidate = regexp.MustCompile(`\b[0-9]{9}\b`)
)

// maxAttributes bounds a record so one producer cannot turn the logbook into
// a document store. Generous enough for the demo's records, which carry ten.
const maxAttributes = 64

// Validate applies every rule the logbook enforces at write time. It is the
// whole reason the logbook is a service rather than a table: a Verantwoordelijke
// gets to reject a record that would pollute its own logboek, and the producer
// finds out synchronously.
//
// resolve reports whether a processing-activity reference exists in this
// logbook's register; passing nil skips that check (used by the register's own
// tests, never in production wiring).
func Validate(record Record, resolve func(string) bool) error {
	if !traceIDPattern.MatchString(record.TraceID) {
		return fmt.Errorf("%w: trace_id must be 32 lowercase hex characters", ErrInvalidRecord)
	}
	if !spanIDPattern.MatchString(record.SpanID) {
		return fmt.Errorf("%w: span_id must be 16 lowercase hex characters", ErrInvalidRecord)
	}
	if record.ParentSpanID != "" && !spanIDPattern.MatchString(record.ParentSpanID) {
		return fmt.Errorf("%w: parent_span_id must be 16 lowercase hex characters", ErrInvalidRecord)
	}
	if strings.TrimSpace(record.Name) == "" {
		return fmt.Errorf("%w: name is mandatory", ErrInvalidRecord)
	}
	switch record.Status {
	case StatusOK, StatusError, StatusUnset:
	default:
		return fmt.Errorf("%w: status must be one of OK, ERROR, UNSET", ErrInvalidRecord)
	}
	if record.StartTime.IsZero() || record.EndTime.IsZero() {
		return fmt.Errorf("%w: start_time and end_time are mandatory", ErrInvalidRecord)
	}
	if record.EndTime.Before(record.StartTime) {
		return fmt.Errorf("%w: end_time precedes start_time", ErrInvalidRecord)
	}
	if len(record.Attributes) > maxAttributes {
		return fmt.Errorf("%w: at most %d attributes", ErrInvalidRecord, maxAttributes)
	}
	if err := validateMandatoryAttributes(record, resolve); err != nil {
		return err
	}
	return validateNoBSN(record)
}

func validateMandatoryAttributes(record Record, resolve func(string) bool) error {
	for _, key := range []string{AttrProcessingActivityID, AttrDataSubjectID, AttrDataSubjectIDType} {
		value, present := record.Attributes[key]
		if !present {
			return fmt.Errorf("%w: %s is mandatory", ErrInvalidRecord, key)
		}
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			return fmt.Errorf("%w: %s must be a non-empty string", ErrInvalidRecord, key)
		}
	}
	if !SubjectIDTypes[record.DataSubjectIDType()] {
		return fmt.Errorf("%w: %s %q is not a known pseudonym space", ErrInvalidRecord, AttrDataSubjectIDType, record.DataSubjectIDType())
	}
	reference := record.ProcessingActivityID()
	if !referencePattern.MatchString(reference) {
		return fmt.Errorf("%w: %s must be a versioned reference like 'bd-ib-2025@v1', got %q", ErrInvalidRecord, AttrProcessingActivityID, reference)
	}
	if resolve != nil && !resolve(reference) {
		return fmt.Errorf("%w: %s %q does not resolve in this logbook's verwerkingsactiviteiten register", ErrInvalidRecord, AttrProcessingActivityID, reference)
	}
	return nil
}

// validateNoBSN is the logbook's own guard on REQ-60/REQ-72. The rule "never
// the BSN" is a property of the record, so the logbook checks it rather than
// trusting each producer to have got its pseudonymisation right — a bug in one
// component then fails loudly at the boundary instead of writing a BSN into a
// store that is meant to be retained.
//
// It scans the serialised record rather than walking its fields, which is both
// shorter and stricter: a BSN hidden in a nested attribute, sent as a JSON
// number, or put in the record name is caught the same way.
//
// The check is deliberately blunt: any nine-digit run that passes the elfproef
// is refused. False positives are possible and preferable to the alternative.
//
// Word-boundary anchored, and that matters: a 20-digit OIN such as
// 00000001234567890000 contains a nine-digit run that passes the elfproef, and
// an unanchored check would refuse every record naming a dienstverlener. An
// organisation identifier is not a BSN.
func validateNoBSN(record Record) error {
	serialised, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("%w: record cannot be serialised: %v", ErrInvalidRecord, err)
	}
	if found := findBSN(string(serialised)); found != "" {
		return fmt.Errorf("%w: the record contains what looks like a BSN", ErrInvalidRecord)
	}
	return nil
}

// findBSN returns the first nine-digit run that satisfies the elfproef, or "".
func findBSN(text string) string {
	for _, candidate := range bsnCandidate.FindAllString(text, -1) {
		if passesElfproef(candidate) {
			return candidate
		}
	}
	return ""
}

// passesElfproef applies the BSN checksum: digits weighted 9..2 and the last
// weighted -1 must sum to a multiple of eleven.
func passesElfproef(digits string) bool {
	if len(digits) != 9 {
		return false
	}
	sum := 0
	for index, character := range digits {
		value := int(character - '0')
		if index == 8 {
			sum -= value
			continue
		}
		sum += value * (9 - index)
	}
	return sum%11 == 0
}
