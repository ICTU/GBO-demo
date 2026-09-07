package httpapi

import (
	"encoding/json"
	"testing"

	"ldv-logboek/internal/ldv"
)

// The read extension's schema nests attributes and spells them camelCase,
// while the core standard names them flat and snake_case. Both are normative
// for their own side, so this asserts the translation rather than a shape we
// chose.
func TestStoredAttributesRenderInTheExtensionsNestedShape(t *testing.T) {
	rendered := toReadAttributes(map[string]any{
		ldv.AttrProcessingActivityID:      "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2025/v1",
		ldv.AttrDataSubjectID:             "PI-abc123",
		ldv.AttrDataSubjectIDType:         "pi",
		ldv.AttrForeignOperationProcessor: "https://fsc.gbo.overheid.nl/peers/AAAABBBBCCCCDDDDEEEE",
		ldv.AttrNextLogbookID:             "https://logboek.rvig.nl/data-processing-operations",
	})

	encoded, err := json.Marshal(rendered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var shape struct {
		DPL struct {
			Core struct {
				ProcessingActivityID string `json:"processingActivityId"`
				DataSubjectID        string `json:"dataSubjectId"`
				DataSubjectIDType    string `json:"dataSubjectIdType"`
				ForeignOperation     struct {
					Processor string `json:"processor"`
				} `json:"foreignOperation"`
			} `json:"core"`
			Read struct {
				NextLogbookID string `json:"nextLogbookId"`
			} `json:"read"`
		} `json:"dpl"`
	}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if shape.DPL.Core.ProcessingActivityID == "" || shape.DPL.Core.DataSubjectID != "PI-abc123" ||
		shape.DPL.Core.DataSubjectIDType != "pi" || shape.DPL.Core.ForeignOperation.Processor == "" ||
		shape.DPL.Read.NextLogbookID == "" {
		t.Fatalf("attributes did not land in the schema's shape: %s", encoded)
	}
	// The flat spellings must not also be present, or a reader sees both.
	var flat map[string]any
	_ = json.Unmarshal(encoded, &flat)
	if _, present := flat[ldv.AttrProcessingActivityID]; present {
		t.Errorf("the flat key survived alongside the nested one: %s", encoded)
	}
}

// A local extension's attributes are not in the schema. Nesting them by
// splitting on dots would invent a structure nobody wrote, so they travel as
// the keys they are rather than being dropped or reshaped.
func TestUnknownAttributesTravelUnchanged(t *testing.T) {
	rendered := toReadAttributes(map[string]any{"dpl.gbo.belastingjaar": float64(2025)})
	if rendered["dpl.gbo.belastingjaar"] != float64(2025) {
		t.Fatalf("rendered = %#v", rendered)
	}
}

// A caller may send selectors in either spelling: nested, as the schema
// defines, or flat, as they appear on a record they are holding.
func TestSelectorsAreReadFromEitherSpelling(t *testing.T) {
	nested := map[string]any{
		"dpl": map[string]any{"core": map[string]any{
			"processingActivityId": "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2025/v1",
			"dataSubjectId":        "PI-abc123",
		}},
	}
	if got := fromReadAttributes(nested, ldv.AttrProcessingActivityID); got == "" {
		t.Error("the nested spelling was not read")
	}
	if got := fromReadAttributes(nested, ldv.AttrDataSubjectID); got != "PI-abc123" {
		t.Errorf("dataSubjectId = %q", got)
	}
	flat := map[string]any{ldv.AttrDataSubjectID: "PI-abc123"}
	if got := fromReadAttributes(flat, ldv.AttrDataSubjectID); got != "PI-abc123" {
		t.Errorf("the flat spelling was not read: %q", got)
	}
	if got := fromReadAttributes(map[string]any{}, ldv.AttrDataSubjectID); got != "" {
		t.Errorf("absent selector = %q, want empty", got)
	}
}

// The schema types traceId as a uuid; the record stores the W3C hex form.
func TestTraceIDRendersAsAUUID(t *testing.T) {
	if got := asUUID("0af7651916cd43dd8448eb211c80319c"); got != "0af76519-16cd-43dd-8448-eb211c80319c" {
		t.Fatalf("asUUID = %q", got)
	}
	// Anything that is not the 32-hex form is left alone rather than mangled.
	if got := asUUID("not-a-trace-id"); got != "not-a-trace-id" {
		t.Fatalf("asUUID = %q", got)
	}
}
