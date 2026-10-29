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

const brpMetadataPath = "../brp-graphql-server/config/gbo-source-metadata.json"

// withASI raises the document to 1.1 and adds the BRP's published asi
// capability.
func withASI(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	asi := asiOf(readDocument(t, brpMetadataPath))
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
		document := readDocument(t, brpMetadataPath)
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

// storedSnapshot turns a validated source into the release candidate the
// registry stores, and returns its snapshot as the registry hands it back:
// jsonb re-sorts object keys.
func storedSnapshot(t *testing.T, validated *validatedSourceRegistration) onboarding.SourceCandidate {
	t.Helper()
	candidate, err := registryCandidateFromActivation(activationFromValidatedSource(validated), onboarding.PublicCertificateSet{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(candidate.Snapshot, &stored); err != nil {
		t.Fatal(err)
	}
	if candidate.Snapshot, err = json.Marshal(stored); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func snapshotVersion(t *testing.T, candidate onboarding.SourceCandidate) string {
	t.Helper()
	var snapshot struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(candidate.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot.SchemaVersion
}

// The release keeps asi, under a snapshot version an adapter without asi
// does not read: it would otherwise drop asi when it rewrites the candidate.
func TestASIReleaseSnapshot(t *testing.T) {
	t.Run("S1 a source without asi stays at 3.0", func(t *testing.T) {
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": unsecuredDocument(t)})
		candidate := storedSnapshot(t, result.activated(t, "demo"))
		if version := snapshotVersion(t, candidate); version != "3.0" {
			t.Fatalf("snapshot schema_version = %q, want 3.0", version)
		}
		if strings.Contains(string(candidate.Snapshot), `"asi"`) {
			t.Fatalf("snapshot without asi holds an asi field: %s", candidate.Snapshot)
		}
		restored, err := activationFromRegistryCandidate(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if restored.ASI != nil {
			t.Fatalf("restored asi = %+v, want none", restored.ASI)
		}
	})

	t.Run("S2 a source with asi is 4.0 and keeps asi", func(t *testing.T) {
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": withASI(t, unsecuredDocument(t))})
		validated := result.activated(t, "demo")
		candidate := storedSnapshot(t, validated)
		if version := snapshotVersion(t, candidate); version != "4.0" {
			t.Fatalf("snapshot schema_version = %q, want 4.0", version)
		}
		restored, err := activationFromRegistryCandidate(candidate)
		if err != nil {
			t.Fatal(err)
		}
		want := validated.Document.Capabilities.ASI
		if want == nil || !reflect.DeepEqual(restored.ASI, want) {
			t.Fatalf("asi after the release snapshot = %+v, want %+v", restored.ASI, want)
		}
	})

	t.Run("S3 an older reader does not take a snapshot with asi for 3.0", func(t *testing.T) {
		// An adapter without asi accepts exactly 3.0 and treats any other
		// version as absent (TestIncompatibleRegistrySnapshotIsTreatedAsAbsent).
		olderReaderAccepts := func(version string) bool { return version == "3.0" }
		result := reconcileDocuments(t, testSchemaPath, map[string]map[string]any{"demo": withASI(t, unsecuredDocument(t))})
		if version := snapshotVersion(t, storedSnapshot(t, result.activated(t, "demo"))); olderReaderAccepts(version) {
			t.Fatalf("snapshot with asi has schema_version %q, which an older reader would rewrite without asi", version)
		}
	})

	t.Run("S4 a stored 3.0 snapshot from before asi", func(t *testing.T) {
		release := runtimeTestRelease(t, strings.Repeat("a", 64), "belastingdienst", "offer", time.Now().UTC())
		source := release.Sources[0]
		if version := snapshotVersion(t, onboarding.SourceCandidate{Snapshot: source.Snapshot}); version != "3.0" {
			t.Fatalf("fixture schema_version = %q, want 3.0", version)
		}
		if _, err := activationFromRegistryCandidate(onboarding.SourceCandidate{SourceID: source.SourceID, Snapshot: source.Snapshot}); err != nil {
			t.Fatal(err)
		}
	})
}

// B1 and B4: the BRP's published document is valid with its asi, and the asi
// leaves its EUDI products as they were without it.
func TestBRPPublishesASI(t *testing.T) {
	registration := sourceRegistration{
		SourceID: "brp", SourceOIN: "99999999900000000200", Name: "BRP",
		MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: "brp-metadata"},
		DataAccess:       sourceDataAccess{Transport: sourceTransportFSC, ServiceReference: "brp"},
	}
	validate := func(document map[string]any) *validatedSourceRegistration {
		t.Helper()
		payload, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		validated, err := validateSourcePayload(registration, payload, "https://brp.example/.well-known/gbo", testSchemaPath, "https://issuer.example", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return validated
	}

	published := readDocument(t, brpMetadataPath)
	if published["schema_version"] != "1.1" {
		t.Fatalf("schema_version = %v, want 1.1", published["schema_version"])
	}
	withASI := validate(published)
	if withASI.Document.Capabilities.ASI == nil {
		t.Fatal("B1: the BRP publishes no asi")
	}

	withoutASI := readDocument(t, brpMetadataPath)
	withoutASI["schema_version"] = "1.0"
	delete(withoutASI["capabilities"].(map[string]any), "asi")
	if got, want := publicationsOf(withASI), publicationsOf(validate(withoutASI)); !reflect.DeepEqual(got, want) {
		t.Fatalf("B4: publications with asi = %+v, want those without %+v", got, want)
	}
}
