package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sourceMetadataFileLimit mirrors the HTTP transports: a source document that
// does not fit is rejected rather than truncated.
const sourceMetadataFileLimit = 1 << 20

// readSourceMetadataFile serves a source document from operator-managed
// storage for sources that publish no metadata endpoint of their own.
//
// The document is still validated, still bound to the OIN in the provisioned
// certificate set, and still promoted explicitly. What this transport removes
// is the fetch, not the review: the operator who mounts the file takes over
// the description the source would otherwise have published itself.
func readSourceMetadataFile(root, relative, etag string) (sourceMetadataFetch, error) {
	if strings.TrimSpace(root) == "" {
		return sourceMetadataFetch{}, fmt.Errorf("source metadata directory is required for file transport")
	}
	if err := validateStorageRelativePath(relative); err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("source metadata path: %w", err)
	}
	resolved, err := resolveWithinRoot(root, relative)
	if err != nil {
		return sourceMetadataFetch{}, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("read source metadata file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("read source metadata file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return sourceMetadataFetch{}, fmt.Errorf("source metadata file must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, sourceMetadataFileLimit+1))
	if err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("read source metadata file: %w", err)
	}
	if len(body) > sourceMetadataFileLimit {
		return sourceMetadataFetch{}, fmt.Errorf("source metadata file exceeds %d bytes", sourceMetadataFileLimit)
	}
	// The bytes are the only version signal a file has, so the digest takes the
	// place of the server-issued ETag. That keeps the not-modified path, and
	// with it the freshness bookkeeping, identical across transports.
	tag := sourceMetadataFileETag(body)
	if etag != "" && etag == tag {
		return sourceMetadataFetch{ETag: tag, NotModified: true}, nil
	}
	return sourceMetadataFetch{Payload: body, ETag: tag}, nil
}

func sourceMetadataFileETag(body []byte) string {
	digest := sha256.Sum256(body)
	return `"sha256-` + hex.EncodeToString(digest[:]) + `"`
}

// resolveWithinRoot re-checks confinement after symlink resolution. The path
// itself is already validated as a clean relative path, so this only closes
// the case where a link inside the root points out of it.
func resolveWithinRoot(root, relative string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve source metadata directory: %w", err)
	}
	candidate := filepath.Join(realRoot, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve source metadata file: %w", err)
	}
	if resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("source metadata file resolves outside the metadata directory")
	}
	return resolved, nil
}
