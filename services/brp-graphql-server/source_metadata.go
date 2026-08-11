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

func newSourceMetadataPublisher(payload []byte) (*sourceMetadataPublisher, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("source metadata payload is not valid JSON")
	}
	digest := sha256.Sum256(payload)
	return &sourceMetadataPublisher{payload: append([]byte(nil), payload...), etag: `"` + fmt.Sprintf("%x", digest) + `"`}, nil
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
	_, _ = w.Write(p.payload)
}
