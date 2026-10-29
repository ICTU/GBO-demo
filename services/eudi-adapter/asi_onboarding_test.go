package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

const testSchemaPath = "../../schemas/gbo-source-metadata-v1.schema.json"

// unsecuredDocument is the demo source's published metadata, without FSC
// service references so it can be served over unsecured transport.
func unsecuredDocument(t *testing.T) map[string]any {
	t.Helper()
	document := readDocument(t, "../graphql-server/config/gbo-source-metadata.json")
	for _, attestation := range eudiOf(document)["attestations"].([]any) {
		graphql := attestation.(map[string]any)["graphql"].(map[string]any)
		delete(graphql, "service_reference")
		graphql["endpoint"] = sourceURLPlaceholder + "/graphql"
	}
	return document
}

// sourceURLPlaceholder in a document is replaced by the URL of the server
// that serves it.
const sourceURLPlaceholder = "{{source}}"

func readDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

// withASI raises the document to 1.1 and adds the BRP's asi capability.
func withASI(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../asi-mapping/testdata/brp-asi.json")
	if err != nil {
		t.Fatal(err)
	}
	var asi map[string]any
	if err := json.Unmarshal(raw, &asi); err != nil {
		t.Fatal(err)
	}
	delete(asi["graphql"].(map[string]any), "service_reference")
	asi["graphql"].(map[string]any)["endpoint"] = sourceURLPlaceholder + "/graphql"
	document["schema_version"] = "1.1"
	document["capabilities"].(map[string]any)["asi"] = asi
	return document
}

func eudiOf(document map[string]any) map[string]any {
	return document["capabilities"].(map[string]any)["eudi"].(map[string]any)
}

func asiOf(document map[string]any) map[string]any {
	return document["capabilities"].(map[string]any)["asi"].(map[string]any)
}

// serveDocument publishes document over unsecured HTTP, with the source URL
// placeholder replaced by the server's own URL.
func serveDocument(t *testing.T, document map[string]any) *httptest.Server {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write([]byte(strings.ReplaceAll(string(raw), sourceURLPlaceholder, source.URL)))
	}))
	t.Cleanup(source.Close)
	return source
}

type asiReconcileResult struct {
	backend  *capturingActivationBackend
	statuses *capturingSourceStatusWriter
}

// reconcileDocuments reconciles one unsecured source per document, named by
// its key, once.
func reconcileDocuments(t *testing.T, schemaPath string, documents map[string]map[string]any) asiReconcileResult {
	t.Helper()
	var sources []sourceConfiguration
	for id, document := range documents {
		sources = append(sources, unsecuredSourceConfiguration(id, serveDocument(t, document).URL))
	}
	result := asiReconcileResult{backend: &capturingActivationBackend{}, statuses: &capturingSourceStatusWriter{}}
	reconciler := &sourceReconciler{
		sourceClient: http.DefaultClient, schemaPath: schemaPath, publicBaseURL: "https://issuer.example",
		sources: sources, store: staticCertificateStore{}, backend: result.backend, statuses: result.statuses,
	}
	// A failing source makes Reconcile return an error; the statuses tell
	// which source failed.
	_ = reconciler.Reconcile(context.Background(), time.Now().UTC())
	return result
}

func (r asiReconcileResult) activated(t *testing.T, sourceID string) *validatedSourceRegistration {
	t.Helper()
	if status := r.statuses.bySource[sourceID]; status.State != sourceStateActive {
		t.Fatalf("source %q status = %+v, want active", sourceID, status)
	}
	for _, validated := range r.backend.activations {
		if validated.Registration.SourceID == sourceID {
			return validated
		}
	}
	t.Fatalf("source %q was not activated", sourceID)
	return nil
}

func (r asiReconcileResult) rejected(t *testing.T, sourceID, reason string) {
	t.Helper()
	status := r.statuses.bySource[sourceID]
	if status.State == sourceStateActive || status.Reason != sourceReasonMetadataInvalid {
		t.Fatalf("source %q status = %+v, want rejected as %s", sourceID, status, sourceReasonMetadataInvalid)
	}
	if !strings.Contains(status.Message, reason) {
		t.Fatalf("source %q message = %q, want it to name %q", sourceID, status.Message, reason)
	}
	for _, validated := range r.backend.activations {
		if validated.Registration.SourceID == sourceID {
			t.Fatalf("source %q was activated", sourceID)
		}
	}
}

func publicationsOf(validated *validatedSourceRegistration) []typeMetadataPublication {
	publications := make([]typeMetadataPublication, 0, len(validated.Publications))
	for _, publication := range validated.Publications {
		publications = append(publications, *publication)
	}
	return publications
}

func TestASIOnboarding(t *testing.T) {
	t.Run("O1 eudi and a valid asi at 1.1 publish the same EUDI products", func(t *testing.T) {
		before := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": unsecuredDocument(t)})
		after := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": withASI(t, unsecuredDocument(t))})
		want := publicationsOf(before.activated(t, "demo"))
		got := publicationsOf(after.activated(t, "demo"))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("publications at 1.1 = %+v, want those at 1.0 %+v", got, want)
		}
	})

	t.Run("O2 a published 1.0 document", func(t *testing.T) {
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": unsecuredDocument(t)})
		result.activated(t, "demo")
	})

	t.Run("O3 eudi only at 1.1", func(t *testing.T) {
		document := unsecuredDocument(t)
		document["schema_version"] = "1.1"
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": document})
		result.activated(t, "demo")
	})

	t.Run("O4 an invalid asi rejects the whole source", func(t *testing.T) {
		document := withASI(t, unsecuredDocument(t))
		attribute := asiOf(document)["attributes"].([]any)[0].(map[string]any)
		attribute["identifier"] = "https://gbo.example/attributes/unknown/1"
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": document})
		result.rejected(t, "demo", "asi capability: attribute")
	})

	t.Run("O5 asi only is not supported yet", func(t *testing.T) {
		document := withASI(t, unsecuredDocument(t))
		delete(document["capabilities"].(map[string]any), "eudi")
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": document})
		result.rejected(t, "demo", "asi only")
	})

	t.Run("O7a asi on another data service than eudi", func(t *testing.T) {
		document := withASI(t, readDocument(t, "../brp-graphql-server/config/gbo-source-metadata.json"))
		asiOf(document)["graphql"].(map[string]any)["service_reference"] = "brp-asi"
		payload, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		_, err = metadataCompilerAdapter{schemaPath: testSchemaPath}.Describe(payload)
		if err == nil || !strings.Contains(err.Error(), "one FSC data service") {
			t.Fatalf("Describe() error = %v, want one FSC data service", err)
		}
	})

	t.Run("O7b asi endpoint that does not fit the transport", func(t *testing.T) {
		document := withASI(t, unsecuredDocument(t))
		asiOf(document)["graphql"].(map[string]any)["endpoint"] = "/graphql"
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": document})
		result.rejected(t, "demo", "asi capability: graphql.endpoint")
	})

	t.Run("O6 an unreadable catalogue blocks only sources with asi", func(t *testing.T) {
		// The catalogue sits next to the schema; this directory has none.
		directory := t.TempDir()
		for _, name := range []string{"gbo-source-metadata-v1.schema.json", "gbo-simple-v1.schema.json"} {
			data, err := os.ReadFile(filepath.Join("../../schemas", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		result := reconcileDocuments(t, filepath.Join(directory, "gbo-source-metadata-v1.schema.json"), map[string]map[string]any{
			"demo":    unsecuredDocument(t),
			"demoasi": withASI(t, unsecuredDocument(t)),
		})
		result.activated(t, "demo")
		result.rejected(t, "demoasi", "attribute catalogue")
	})
}

// O8: the release keeps asi. The capturing backend sees the validated
// document; this goes through the snapshot the registry stores.
func TestASISurvivesTheReleaseSnapshot(t *testing.T) {
	result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": withASI(t, unsecuredDocument(t))})
	validated := result.activated(t, "demo")
	want := validated.Document.Capabilities.ASI
	if want == nil {
		t.Fatal("validated document has no asi")
	}

	candidate, err := registryCandidateFromActivation(activationFromValidatedSource(validated), onboarding.PublicCertificateSet{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The registry stores the snapshot as jsonb; decode it the way a read
	// back from the registry does.
	var stored map[string]any
	if err := json.Unmarshal(candidate.Snapshot, &stored); err != nil {
		t.Fatal(err)
	}
	if candidate.Snapshot, err = json.Marshal(stored); err != nil {
		t.Fatal(err)
	}
	restored, err := activationFromRegistryCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.ASI, want) {
		t.Fatalf("asi after the release snapshot = %+v, want %+v", restored.ASI, want)
	}
}
