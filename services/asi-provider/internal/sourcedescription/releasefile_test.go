package sourcedescription

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	asimapping "gbo-demo/asi-mapping"
)

func brpDocument(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testCatalogue(t *testing.T) asimapping.Catalogue {
	t.Helper()
	catalogue, err := asimapping.LoadCatalogue("../../../../schemas/asi-attributes.json")
	if err != nil {
		t.Fatal(err)
	}
	return catalogue
}

// Without a Source Registry the ASIP reads a release file, written from the
// source's published metadata.
func TestReleaseFile(t *testing.T) {
	t.Run("F1 a release file from the BRP's published metadata", func(t *testing.T) {
		release, err := ReleaseFromDocument("rvig", brpDocument(t), testCatalogue(t), start, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(release)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "release.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		f := &fixture{now: start.Add(23 * time.Hour), source: &asking{}}
		f.descriptions = New(func(context.Context) (Registry, error) { return ReadReleaseFile(path) })
		f.descriptions.now = func() time.Time { return f.now }
		f.refresh(t, false)
		if asi, ok := f.descriptions.Current("rvig"); !ok || len(asi.Attributes) != 5 {
			t.Fatalf("Current(rvig) = %+v, %v", asi, ok)
		}
		if !f.available(t) {
			t.Fatal("not available from the release file")
		}
	})

	t.Run("F2 a release file that is missing", func(t *testing.T) {
		f := &fixture{now: start, source: &asking{}}
		f.descriptions = New(func(context.Context) (Registry, error) {
			return ReadReleaseFile(filepath.Join(t.TempDir(), "missing.json"))
		})
		f.refresh(t, true)
		if f.available(t) {
			t.Fatal("available without a release file")
		}
	})

	t.Run("F3 metadata without asi, or with an asi the catalogue refuses", func(t *testing.T) {
		var document map[string]any
		if err := json.Unmarshal(brpDocument(t), &document); err != nil {
			t.Fatal(err)
		}
		capabilities := document["capabilities"].(map[string]any)
		asi := capabilities["asi"].(map[string]any)
		asi["attributes"].([]any)[0].(map[string]any)["identifier"] = "https://gbo.example/attributes/unknown/1"
		refused, _ := json.Marshal(document)
		delete(capabilities, "asi")
		without, _ := json.Marshal(document)
		for name, raw := range map[string][]byte{"without asi": without, "refused asi": refused} {
			if _, err := ReleaseFromDocument("rvig", raw, testCatalogue(t), start, time.Hour); err == nil {
				t.Errorf("%s: ReleaseFromDocument() made a release", name)
			}
		}
	})
}
