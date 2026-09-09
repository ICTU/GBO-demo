package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestMetadataFile(t *testing.T, root, relative, body string) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestReadSourceMetadataFileReturnsDigestETag(t *testing.T) {
	root := t.TempDir()
	writeTestMetadataFile(t, root, "centric/gbo.json", `{"schema_version":"1.0"}`)

	fetched, err := readSourceMetadataFile(root, "centric/gbo.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(fetched.Payload) != `{"schema_version":"1.0"}` || fetched.NotModified {
		t.Fatalf("fetched = %+v", fetched)
	}
	if !strings.HasPrefix(fetched.ETag, `"sha256-`) {
		t.Fatalf("etag = %q", fetched.ETag)
	}
}

// The digest stands in for a server-issued ETag, so unchanged bytes must take
// the same not-modified path the HTTP transports use to extend freshness.
func TestReadSourceMetadataFileReportsNotModifiedForUnchangedBytes(t *testing.T) {
	root := t.TempDir()
	writeTestMetadataFile(t, root, "centric/gbo.json", `{"schema_version":"1.0"}`)
	first, err := readSourceMetadataFile(root, "centric/gbo.json", "")
	if err != nil {
		t.Fatal(err)
	}

	second, err := readSourceMetadataFile(root, "centric/gbo.json", first.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if !second.NotModified || len(second.Payload) != 0 {
		t.Fatalf("second read = %+v", second)
	}

	writeTestMetadataFile(t, root, "centric/gbo.json", `{"schema_version":"1.0","version":"2"}`)
	third, err := readSourceMetadataFile(root, "centric/gbo.json", first.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if third.NotModified || third.ETag == first.ETag {
		t.Fatalf("third read = %+v", third)
	}
}

func TestReadSourceMetadataFileRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	writeTestMetadataFile(t, root, "centric/gbo.json", `{}`)
	outside := writeTestMetadataFile(t, t.TempDir(), "secret.json", `{}`)

	for _, relative := range []string{
		"../secret.json",
		"centric/../../secret.json",
		"/etc/passwd",
		"./centric/gbo.json",
		"",
	} {
		if _, err := readSourceMetadataFile(root, relative, ""); err == nil {
			t.Fatalf("path %q was accepted", relative)
		}
	}

	// A clean relative path is not enough on its own: a link inside the root
	// can still point out of it, so confinement is re-checked after symlink
	// resolution.
	link := filepath.Join(root, "linked.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readSourceMetadataFile(root, "linked.json", ""); err == nil {
		t.Fatal("symlink out of the metadata directory was accepted")
	}
}

func TestReadSourceMetadataFileRequiresConfiguredDirectory(t *testing.T) {
	if _, err := readSourceMetadataFile("", "centric/gbo.json", ""); err == nil {
		t.Fatal("missing metadata directory was accepted")
	}
}

func TestReadSourceMetadataFileRejectsOversizedDocument(t *testing.T) {
	root := t.TempDir()
	writeTestMetadataFile(t, root, "centric/gbo.json", strings.Repeat("a", sourceMetadataFileLimit+1))

	if _, err := readSourceMetadataFile(root, "centric/gbo.json", ""); err == nil {
		t.Fatal("oversized source document was accepted")
	}
}

func TestReadSourceMetadataFileRejectsDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestMetadataFile(t, root, "centric/gbo.json", `{}`)

	if _, err := readSourceMetadataFile(root, "centric", ""); err == nil {
		t.Fatal("directory was accepted as a source document")
	}
}
