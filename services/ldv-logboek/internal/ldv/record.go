// Package ldv holds the Logboek Dataverwerkingen core: the log record, the
// verwerkingsactiviteiten register it must reference, and the single use case
// the logbook offers — append a record and confirm it.
//
// The core owns the rules and the ports; HTTP and SQLite are adapters.
//
// The record shape follows LDV v1.0.0, which reuses the OpenTelemetry log
// record. That reuse is deliberate on the standard's side and deliberately
// *not* an invitation to reuse our OTel pipeline: a span is observability
// exhaust of a technical operation, sampled and short-lived, while an LDV
// record is an administrative record of a Dataverwerking that MUST exist,
// MUST be confirmed and MUST NOT be sampled (REQ-32). Same shape, different
// guarantees — hence a separate store with its own service.
package ldv

import "time"

// Attribute keys the standard reserves. Everything a component wants to add
// beyond these lives under its own prefix (we use `gbo.`), so a reader can
// tell normative fields from demo colour at a glance.
const (
	AttrProcessingActivityID = "dpl.core.processing_activity_id"
	AttrDataSubjectID        = "dpl.core.data_subject_id"
	AttrDataSubjectIDType    = "dpl.core.data_subject_id_type"

	// AttrForeignOperationProcessor names the application that initiated the
	// processing when it was not this Verantwoordelijke — the FSC peer on the
	// other side of the boundary.
	AttrForeignOperationProcessor = "dpl.core.foreign_operation.processor"
)

// Status values. UNSET is what OTel uses for "the producer did not say";
// a Dataverwerking that ran to completion says OK or ERROR.
const (
	StatusUnset = "UNSET"
	StatusOK    = "OK"
	StatusError = "ERROR"
)

// Record is one Dataverwerking, about one Betrokkene, at one Verantwoordelijke.
//
// TraceID ties it to every other record of the same request — the LDV records
// of the other components, the ADL decision and the FSC transaction log all
// carry the same value, because in this chain the trace id *is* the
// Fsc-Transaction-Id (REQ-55).
type Record struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Status       string            `json:"status"`
	Resource     map[string]string `json:"resource,omitempty"`
	Attributes   map[string]any    `json:"attributes"`
}

// Attribute returns a string-valued attribute, or "" when it is absent or of
// another type. Attributes are free-form JSON; the mandatory dpl.core ones are
// strings and validation rejects anything else.
func (r Record) Attribute(key string) string {
	value, _ := r.Attributes[key].(string)
	return value
}

// ProcessingActivityID is the reference into the Verantwoordelijke's register
// of verwerkingsactiviteiten, in `<id>@<version>` form.
func (r Record) ProcessingActivityID() string { return r.Attribute(AttrProcessingActivityID) }

// DataSubjectID is the pseudonymous identifier of the Betrokkene. Never a BSN
// (REQ-60/REQ-72) — DataSubjectIDType says which pseudonym space it lives in.
func (r Record) DataSubjectID() string { return r.Attribute(AttrDataSubjectID) }

// DataSubjectIDType names that pseudonym space. Mandatory: a bare identifier
// without its type cannot be resolved by a later reader, and two components
// may legitimately refer to the same Betrokkene by different pseudonyms.
func (r Record) DataSubjectIDType() string { return r.Attribute(AttrDataSubjectIDType) }

// Stored is a Record as the logbook holds it: the record itself plus what the
// logbook stamped on receipt. ReceivedAt is the logbook's own clock, kept
// apart from the producer's StartTime/EndTime so a clock skew is visible
// rather than smoothed away.
type Stored struct {
	Record
	ReceivedAt time.Time `json:"received_at"`
}
