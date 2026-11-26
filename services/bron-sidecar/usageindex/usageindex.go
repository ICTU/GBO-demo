// Package usageindex registers a delivery at GBO's data usage index
// (#687), through the source peer's own FSC Outway.
//
// The index is another organisation's service (the consent register's peer),
// reached over FSC like any other: the Outway resolves the grant link in the
// path to the connection contract and calls the register's Inway. The
// registration carries pointers only: which logbook holds the record, under
// which trace, and for which consent. No data of the citizen.
package usageindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client registers deliveries for one logbook of this source.
type Client struct {
	// URL is the Outway's grant link for the index, for example
	// http://bd-outway:8080/data-usage-index.
	URL string
	// LogbookID names the logbook that holds this source's records, as the
	// source's Peer ID, a slash and its FSC read service name:
	// 99999999900000000200/ldv-lezen-bd.
	LogbookID string
	HTTP      *http.Client
}

type registration struct {
	LogbookID     string `json:"logbook_id"`
	TraceID       string `json:"trace_id"`
	GrondslagType string `json:"grondslag_type"`
	GrondslagRef  string `json:"grondslag_ref"`
}

// Register records that this source delivered under the consent in the
// trace. A retry of the same registration is answered with the stored one,
// so calling it twice is harmless. Anything but 200 or 201 is an error: the
// caller withholds the data.
func (c Client) Register(ctx context.Context, traceID, consentID string) error {
	body, err := json.Marshal(registration{
		LogbookID:     c.LogbookID,
		TraceID:       traceID,
		GrondslagType: "consent",
		GrondslagRef:  consentID,
	})
	if err != nil {
		return fmt.Errorf("encode registration: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.URL, "/")+"/registrations", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build registration request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("register at the data usage index: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return fmt.Errorf("the data usage index answered %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
}
