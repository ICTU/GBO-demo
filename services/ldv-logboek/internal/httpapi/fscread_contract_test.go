package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"
)

// The reduced answer of the FSC read listener (#686) against the response
// schema of the Logius read extension, OpenAPI 1.1.0 (testdata/README.md).
// The checks read the schema itself: required members, patterns, enums,
// date-times, and that nothing outside the defined properties is sent.

type schema struct {
	Ref        string             `json:"$ref"`
	Type       string             `json:"type"`
	Required   []string           `json:"required"`
	Properties map[string]*schema `json:"properties"`
	Items      *schema            `json:"items"`
	Pattern    string             `json:"pattern"`
	Format     string             `json:"format"`
	Enum       []string           `json:"enum"`
}

type contract struct {
	openapi    map[string]*schema
	attributes *schema
}

func loadContract(t *testing.T) contract {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]*schema `json:"schemas"`
		} `json:"components"`
	}
	raw, err := os.ReadFile("testdata/logboek-extensie-lezen-openapi-1.1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var attributes schema
	raw, err = os.ReadFile("testdata/logboek-extensie-lezen-attributes.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &attributes); err != nil {
		t.Fatal(err)
	}
	return contract{openapi: doc.Components.Schemas, attributes: &attributes}
}

func (c contract) resolve(s *schema) *schema {
	switch {
	case s == nil:
		return nil
	case s.Ref == "./attributes.schema.json":
		return c.attributes
	case len(s.Ref) > len("#/components/schemas/"):
		return c.resolve(c.openapi[s.Ref[len("#/components/schemas/"):]])
	}
	return s
}

func (c contract) check(t *testing.T, path string, s *schema, value any) {
	t.Helper()
	s = c.resolve(s)
	if s == nil {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for _, name := range s.Required {
			if _, ok := v[name]; !ok {
				t.Errorf("%s: required %q missing", path, name)
			}
		}
		for name, member := range v {
			property, defined := s.Properties[name]
			if !defined {
				t.Errorf("%s: %q is not defined by the schema", path, name)
				continue
			}
			c.check(t, path+"."+name, property, member)
		}
	case []any:
		for i, item := range v {
			c.check(t, path+"["+string(rune('0'+i))+"]", s.Items, item)
		}
	case string:
		if s.Pattern != "" && !regexp.MustCompile(s.Pattern).MatchString(v) {
			t.Errorf("%s: %q does not match %s", path, v, s.Pattern)
		}
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, v) {
			t.Errorf("%s: %q not in %v", path, v, s.Enum)
		}
		if s.Format == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
				t.Errorf("%s: %q is not a date-time", path, v)
			}
		}
	}
}

func TestTheReducedAnswerValidatesAgainstTheReadExtension(t *testing.T) {
	handler, _ := newTestHandler(t)
	if response := post(t, handler, writeToken, validBody()); response.Code != http.StatusCreated {
		t.Fatalf("seed write status = %d, body = %s", response.Code, response.Body)
	}
	fsc := NewFSCReadHandler(handler.logbook, testLogbookID)

	request := httptest.NewRequest(http.MethodPost, "/data-processing-operations",
		bytes.NewBufferString(`{"traceId":"0af7651916cd43dd8448eb211c80319c"}`))
	recorder := httptest.NewRecorder()
	fsc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if recorder.Header().Get("API-Version") != APIVersion {
		t.Errorf("API-Version = %q", recorder.Header().Get("API-Version"))
	}

	var answer map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	c := loadContract(t)
	c.check(t, "response", c.openapi["ListDataProcessingOperations"], answer)

	operations, _ := answer["dataProcessingOperations"].([]any)
	if len(operations) != 1 {
		t.Fatalf("operations = %v", operations)
	}
	// #686: no data subject, no resource, nothing but the activity.
	operation := operations[0].(map[string]any)
	for _, absent := range []string{"resource", "parentSpanId"} {
		if _, present := operation[absent]; present {
			t.Errorf("the reduced answer carries %q", absent)
		}
	}
	encoded, _ := json.Marshal(operation["attributes"])
	if string(encoded) != `{"dpl":{"core":{"processingActivityId":"https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-bronquery-doorgifte/v1"}}}` {
		t.Errorf("attributes = %s", encoded)
	}
}

// The FSC listener takes exactly one traceId, as the PDP rule does.
func TestTheFSCListenerTakesOneTraceIDAndNothingElse(t *testing.T) {
	handler, _ := newTestHandler(t)
	fsc := NewFSCReadHandler(handler.logbook, testLogbookID)
	for name, body := range map[string]string{
		"no trace":           `{}`,
		"uuid trace":         `{"traceId":"0af76519-16cd-43dd-8448-eb211c80319c"}`,
		"trace and selector": `{"traceId":"0af7651916cd43dd8448eb211c80319c","startTime":"2026-01-01T00:00:00Z"}`,
		"by subject":         `{"dpl":{"core":{"dataSubjectId":"LP-abc123"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			fsc.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/data-processing-operations", bytes.NewBufferString(body)))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
		})
	}
}
