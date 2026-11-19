package ldv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ldvclient "gbo-demo/ldv-client"
)

// RecordDelivery is the use case of the proxy mode (#689): a source answered
// a request under a consent with data, and before that data leaves, the
// delivery is recorded in this logbook and its trace is registered at the
// consent usage index (#687). Both must succeed or the data does not go out.
//
// It is the second driving use case of the logbook, next to Write: the proxy
// is a second driving adapter, so the rule lives here rather than in it.

// UsageIndex is the consent usage index seen from the component. Register
// returns nil only once the index has confirmed; a retry of the same
// registration is a success.
type UsageIndex interface {
	Register(ctx context.Context, traceID, consentID string) error
}

// TraceClaims ties a trace to the consent it carries deliveries under.
// ClaimTrace returns false when the trace already carries a delivery under
// another consent.
//
// The read-back finds a consent's uses by trace (#687), and the record does
// not name the consent. Without this, a caller that reused one trace ID under
// two consents would make each delivery show as a use of both, across
// citizens. The trace's first consent owns it; a delivery under another is
// refused like any delivery that cannot be recorded.
type TraceClaims interface {
	ClaimTrace(ctx context.Context, traceID, consentID string) (bool, error)
}

// Delivery is one response under a consent, as the proxy saw it.
type Delivery struct {
	// TraceID is the trace the request arrived in (traceparent), and
	// SpanID the span the proxy forwarded under: the source's own records
	// sit below it in the same trace.
	TraceID      string
	SpanID       string
	ParentSpanID string
	Start        time.Time
	End          time.Time
	// ConsentID comes from the consent token. It goes to the index only,
	// never into the record: the record names the citizen, not the consent.
	ConsentID string
	// SubjectID is the citizen as the source's sidecar named it, in the
	// source's own pseudonym space (D1), and SubjectIDType that space.
	SubjectID     string
	SubjectIDType string
	// Processor is the FSC peer that asked, when known.
	Processor string
}

// DeliveryRecorder runs RecordDelivery.
type DeliveryRecorder struct {
	logbook *Logbook
	index   UsageIndex
	claims  TraceClaims
	// activity is the processing activity this instance logs under, set
	// per deployment (LDV_PROXY_ACTIVITY): one per source.
	activity string
}

// ErrTraceUnderAnotherConsent is a delivery in a trace that already carries
// one under another consent.
var ErrTraceUnderAnotherConsent = errors.New("the trace already carries a delivery under another consent")

// ErrDeliveryNotRecorded wraps every reason a delivery is withheld.
var ErrDeliveryNotRecorded = errors.New("the delivery could not be recorded")

// RecordName is the name of the record the proxy writes.
const RecordName = "dataverwerking.levering-onder-toestemming"

// NewDeliveryRecorder wires the use case. The activity must resolve in the
// logbook's register: a component that would refuse every record should not
// start.
func NewDeliveryRecorder(logbook *Logbook, index UsageIndex, claims TraceClaims, activity string) (*DeliveryRecorder, error) {
	if logbook == nil || index == nil || claims == nil {
		return nil, errors.New("a delivery recorder needs a logbook, a usage index and trace claims")
	}
	if _, ok := logbook.Register().Resolve(activity); !ok {
		return nil, fmt.Errorf("processing activity %q is not in this logbook's register", activity)
	}
	return &DeliveryRecorder{logbook: logbook, index: index, claims: claims, activity: activity}, nil
}

// Record registers the trace, then commits the record, and only a delivery
// that got both may return its data.
//
// The order is the reverse of the TransparantieApp's push model and of #689
// ("after the record is committed, register"), on purpose. The register
// counts every record under a registered trace as a use. Record first meant
// that a delivery whose registration then failed left an OK record behind:
// harmless for a new trace, but counted as a use when the trace was already
// registered by an earlier delivery, although this one was withheld. With
// the registration first, a record exists only for a delivery the index
// confirmed. The opposite leftover, a registration whose record never landed,
// gives no use (#687), which is the right answer for a withheld delivery.
//
// The record is validated before anything is registered, so a record the
// logbook would refuse never leaves a registration behind.
func (d *DeliveryRecorder) Record(ctx context.Context, delivery Delivery) error {
	if strings.TrimSpace(delivery.ConsentID) == "" {
		return fmt.Errorf("%w: no consent_id in the consent token", ErrDeliveryNotRecorded)
	}
	if strings.TrimSpace(delivery.SubjectID) == "" {
		// The record names the citizen (#689). Without the sidecar's name for
		// them there is no lawful record, so there is no delivery either.
		return fmt.Errorf("%w: the source named no data subject", ErrDeliveryNotRecorded)
	}
	owned, err := d.claims.ClaimTrace(ctx, delivery.TraceID, delivery.ConsentID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDeliveryNotRecorded, err)
	}
	if !owned {
		return fmt.Errorf("%w: %w", ErrDeliveryNotRecorded, ErrTraceUnderAnotherConsent)
	}
	subjectType := delivery.SubjectIDType
	if subjectType == "" {
		subjectType = ldvclient.SubjectTypePseudonym
	}
	record := Record{
		TraceID:      delivery.TraceID,
		SpanID:       delivery.SpanID,
		ParentSpanID: delivery.ParentSpanID,
		Name:         RecordName,
		Status:       StatusOK,
		StartTime:    delivery.Start.UTC(),
		EndTime:      delivery.End.UTC(),
		Attributes:   ldvclient.Attributes(d.activity, delivery.SubjectID, subjectType, delivery.Processor, nil),
	}
	if err := Validate(record, d.logbook.resolves); err != nil {
		return fmt.Errorf("%w: %w", ErrDeliveryNotRecorded, err)
	}
	if err := d.index.Register(ctx, delivery.TraceID, delivery.ConsentID); err != nil {
		return fmt.Errorf("%w: register the trace: %w", ErrDeliveryNotRecorded, err)
	}
	if _, err := d.logbook.Write(ctx, record); err != nil {
		return fmt.Errorf("%w: %w", ErrDeliveryNotRecorded, err)
	}
	return nil
}
