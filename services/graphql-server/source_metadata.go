package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
)

type sourceMetadataPublisher struct {
	payload []byte
	etag    string
}

// newSourceMetadataPublisher validates the source-owned payload once at
// startup and publishes those immutable bytes through the source's dedicated
// FSC metadata service. FSC authenticates the source at transport level.
// In production this handler is an internal backend of the source's dedicated
// FSC metadata service; it is not an internet-public endpoint.
func newSourceMetadataPublisher(payload []byte) (*sourceMetadataPublisher, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("source metadata payload is not valid JSON")
	}
	digest := sha256.Sum256(payload)
	return &sourceMetadataPublisher{
		payload: append([]byte(nil), payload...),
		etag:    `"` + fmt.Sprintf("%x", digest) + `"`,
	}, nil
}

func (p *sourceMetadataPublisher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Header().Set("ETag", p.etag)
	if r.Header.Get("If-None-Match") == p.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(p.payload)
}
