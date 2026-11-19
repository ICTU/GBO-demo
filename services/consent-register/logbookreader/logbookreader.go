// Package logbookreader reads the logbook of a source's LDV component for
// the register, through the register's Outway (cr-outway) and the FSC read
// service of that source (#686): POST /data-processing-operations with one
// traceId. The source's Inway puts the request to the source's PDP.
package logbookreader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"consent-register/uses"
)

// Client implements uses.Logbooks.
type Client struct {
	// Services maps a logbook ID (peer ID plus read service name) to the
	// Outway's grant link for that read service, for example
	// http://cr-outway:8080/ldv-lezen-bd. No discovery: static, as #686
	// says.
	Services map[string]string
	HTTP     *http.Client
}

// Read implements uses.Logbooks.
func (c Client) Read(ctx context.Context, logbookID, traceID string) ([]uses.Record, error) {
	base, ok := c.Services[logbookID]
	if !ok {
		return nil, uses.ErrNoReadService
	}
	body, err := json.Marshal(map[string]string{"traceId": traceID})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/data-processing-operations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build read request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read the logbook: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("the logbook answered %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	var answer struct {
		Operations []struct {
			TraceID   string `json:"traceId"`
			StartTime string `json:"startTime"`
		} `json:"dataProcessingOperations"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("decode the logbook answer: %w", err)
	}
	records := make([]uses.Record, 0, len(answer.Operations))
	for _, operation := range answer.Operations {
		// The read is by trace-id; a record of another trace is not a use
		// of this registration.
		if operation.TraceID != traceID {
			continue
		}
		start, err := time.Parse(time.RFC3339Nano, operation.StartTime)
		if err != nil {
			return nil, fmt.Errorf("the logbook sent startTime %q: %w", operation.StartTime, err)
		}
		records = append(records, uses.Record{StartTime: start})
	}
	return records, nil
}
