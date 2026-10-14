package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// httpInterface is the conformsTo value of the HTTP-based authentic source
// interface (clause 6.1), the only kind of record an ASIP of this demo
// publishes (REQ-DIP-5.3.2-03-01).
const httpInterface = "urn:ietf:rfc:9110"

const (
	sourceTimeout  = 5 * time.Second
	maxRecordsBody = 1 << 20
)

// query is a retrieve query (clause 5.3.2) after validation.
type query struct {
	attributeIdentifier string
	country             string
	conformsTo          string
}

// directory is the stub of the Data Service Directory: it holds no records
// of its own but reads, per query, the records each configured ASIP
// publishes for its data services.
type directory struct {
	sources []string
	client  *http.Client
}

func newDirectory(sources []string) *directory {
	return &directory{sources: sources, client: &http.Client{Timeout: sourceTimeout}}
}

// retrieve returns the dataService records that match q. A source that
// cannot be read is left out and logged: the other sources still answer.
func (d *directory) retrieve(ctx context.Context, q query) []json.RawMessage {
	matches := []json.RawMessage{}
	if q.conformsTo != "" && q.conformsTo != httpInterface {
		return matches
	}
	for _, source := range d.sources {
		records, err := d.read(ctx, source)
		if err != nil {
			slog.Warn("data service records unavailable", "source", source, "err", err)
			continue
		}
		for _, raw := range records {
			var r struct {
				AttributeIdentifier string `json:"attributeIdentifier"`
				Country             string `json:"country"`
			}
			if json.Unmarshal(raw, &r) != nil || r.AttributeIdentifier != q.attributeIdentifier {
				continue
			}
			if q.country != "" && r.Country != q.country {
				continue
			}
			matches = append(matches, raw)
		}
	}
	return matches
}

// read fetches the records of one ASIP data service.
func (d *directory) read(ctx context.Context, source string) ([]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var doc struct {
		DataServices []json.RawMessage `json:"dataServices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRecordsBody)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode records: %w", err)
	}
	return doc.DataServices, nil
}
