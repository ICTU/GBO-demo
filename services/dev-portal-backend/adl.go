package main

// ── Authorization Decision Log ───────────────────────────────────────────
//
// The Authorization Decision Log (ADL) is the audit record of every PDP
// decision. OpenFTV writes one record per evaluation to Postgres (ftv_adl),
// following the Logius ADL standard: the AuthZEN request and response in
// `body`, source references in `attributes`. The dev-portal reads it with a
// read-only role (services/openftv-manager/adl/adl-reader.sql) and presents it
// as the record of the decision; the engine's console decision log in Loki
// is observability next to it.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const adlLookupLimit = 20

// adlRecord is one ADL log record as the dev-portal serves it. Decision and
// Reason are read from the recorded response; everything else is the record
// as stored.
type adlRecord struct {
	Timestamp        int64           `json:"timestamp"` // ms since the epoch
	TraceID          string          `json:"trace_id"`
	SpanID           string          `json:"span_id"`
	ParentSpanID     string          `json:"parent_span_id,omitempty"`
	EventName        string          `json:"event_name"`
	Status           string          `json:"status"`
	Policies         int64           `json:"policies"`
	FscTransactionID string          `json:"fsc_transaction_id,omitempty"`
	MatchedOn        string          `json:"matched_on"`
	Decision         *bool           `json:"decision,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Request          json.RawMessage `json:"request,omitempty"`
	Response         json.RawMessage `json:"response,omitempty"`
}

// adlStore finds the ADL records of one FSC transaction.
type adlStore interface {
	DecisionsForTransaction(ctx context.Context, txID string, since time.Time) ([]adlRecord, error)
}

// adlDecisionsQuery finds the records of one FSC transaction.
//
// The ADL names the transaction in attributes.adl.fsc.transaction_id, but
// behind the FSC Inway OpenFTV leaves that empty: it reads the id from an
// Fsc-Transaction-Id header on the PDP call, and the Inway sends it as
// X-Request-Id instead. The Inway does copy the original request's headers
// into the AuthZEN context, and the ADL stores that request, so the query
// falls back to the header there. matched_on says which of the two found
// the record; once the Inway sends the header, the first one does.
const adlDecisionsQuery = `
SELECT timestamp, trace_id, span_id, parent_span_id, event_name, status, policies, body, attributes,
       CASE WHEN attributes->>'adl.fsc.transaction_id' = $1
            THEN 'adl.fsc.transaction_id'
            ELSE 'adl.core.request.context.headers'
       END
FROM decision
WHERE timestamp >= $2
  AND (attributes->>'adl.fsc.transaction_id' = $1
       OR body->'adl.core.request'->'context'->'headers'->>'Fsc-Transaction-Id' = $1)
ORDER BY timestamp, id
LIMIT $3`

type pgADLStore struct{ pool *pgxpool.Pool }

// newPgADLStore prepares a connection pool. It does not connect: the first
// query does, so a portal started before the database still comes up.
func newPgADLStore(url string) (*pgADLStore, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse ADL_DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("ADL pool: %w", err)
	}
	return &pgADLStore{pool: pool}, nil
}

func (s *pgADLStore) DecisionsForTransaction(ctx context.Context, txID string, since time.Time) ([]adlRecord, error) {
	rows, err := s.pool.Query(ctx, adlDecisionsQuery, txID, since.UnixMilli(), adlLookupLimit)
	if err != nil {
		return nil, fmt.Errorf("query ADL: %w", err)
	}
	defer rows.Close()
	out := []adlRecord{}
	for rows.Next() {
		var r adlRow
		if err := rows.Scan(&r.Timestamp, &r.TraceID, &r.SpanID, &r.ParentSpanID, &r.EventName,
			&r.Status, &r.Policies, &r.Body, &r.Attributes, &r.MatchedOn); err != nil {
			return nil, fmt.Errorf("scan ADL record: %w", err)
		}
		rec, err := adlRecordFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// adlRow is a row of OpenFTV's decision table, nullable columns as pointers.
type adlRow struct {
	Timestamp    int64
	TraceID      *string
	SpanID       *string
	ParentSpanID *string
	EventName    string
	Status       string
	Policies     *int64
	Body         []byte
	Attributes   []byte
	MatchedOn    string
}

func adlRecordFromRow(r adlRow) (adlRecord, error) {
	rec := adlRecord{
		Timestamp:    r.Timestamp,
		TraceID:      stringOrEmpty(r.TraceID),
		SpanID:       stringOrEmpty(r.SpanID),
		ParentSpanID: stringOrEmpty(r.ParentSpanID),
		EventName:    r.EventName,
		Status:       r.Status,
		MatchedOn:    r.MatchedOn,
	}
	if r.Policies != nil {
		rec.Policies = *r.Policies
	}
	if len(r.Body) > 0 {
		var body struct {
			Request  json.RawMessage `json:"adl.core.request"`
			Response json.RawMessage `json:"adl.core.response"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return adlRecord{}, fmt.Errorf("decode ADL body: %w", err)
		}
		rec.Request, rec.Response = body.Request, body.Response
		rec.Decision, rec.Reason = decisionFromResponse(body.Response)
	}
	if len(r.Attributes) > 0 {
		var attrs map[string]any
		if err := json.Unmarshal(r.Attributes, &attrs); err != nil {
			return adlRecord{}, fmt.Errorf("decode ADL attributes: %w", err)
		}
		rec.FscTransactionID, _ = attrs["adl.fsc.transaction_id"].(string)
	}
	return rec, nil
}

// decisionFromResponse reads the outcome of a recorded Access Evaluation
// response: the decision, and on a denial the reason. OpenFTV puts the
// policy's reason code in reason_user.en and answers "ok" there on an allow,
// which is not a reason and is left out.
func decisionFromResponse(raw json.RawMessage) (*bool, string) {
	var resp struct {
		Decision *bool `json:"decision"`
		Context  struct {
			ReasonUser map[string]any `json:"reason_user"`
		} `json:"context"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &resp) != nil {
		return nil, ""
	}
	if resp.Decision == nil || *resp.Decision {
		return resp.Decision, ""
	}
	reason, _ := resp.Context.ReasonUser["en"].(string)
	return resp.Decision, reason
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
