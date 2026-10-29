package asimapping

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const cataloguePath = "../../schemas/asi-attributes.json"

func loadTestCatalogue(t *testing.T) Catalogue {
	t.Helper()
	catalogue, err := LoadCatalogue(cataloguePath)
	if err != nil {
		t.Fatalf("LoadCatalogue() error = %v", err)
	}
	return catalogue
}

func brpCapability(t *testing.T) Capability {
	t.Helper()
	data, err := os.ReadFile("testdata/brp-asi.json")
	if err != nil {
		t.Fatal(err)
	}
	var capability Capability
	if err := json.Unmarshal(data, &capability); err != nil {
		t.Fatal(err)
	}
	return capability
}

func (c Capability) attribute(name string) *Attribute {
	for i := range c.Attributes {
		if strings.Contains(c.Attributes[i].Identifier, "/attributes/"+name+"/") {
			return &c.Attributes[i]
		}
	}
	panic("no attribute " + name)
}

func TestValidateAcceptsBRP(t *testing.T) {
	if err := brpCapability(t).Validate(loadTestCatalogue(t)); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	catalogue := loadTestCatalogue(t)

	cases := []struct {
		name   string
		mutate func(c *Capability)
		reason string
	}{
		{"W2 document does not parse", func(c *Capability) {
			c.GraphQL.Document = "query Asi($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { ...Selected }"
		}, "parse"},
		{"W2 more than one operation", func(c *Capability) {
			c.GraphQL.Document += " query Other($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { burgerservicenummer } }"
		}, "exactly one"},
		{"W2 no operation", func(c *Capability) {
			c.GraphQL.Document = "fragment F on IngeschrevenPersoon { ...Selected }"
		}, "exactly one"},
		{"W2 mutation", func(c *Capability) {
			c.GraphQL.Document = "mutation Asi($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { ...Selected } }"
		}, "query"},
		{"W3 no Selected", func(c *Capability) {
			c.GraphQL.Document = "query Asi($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { voornamen } }"
		}, "Selected"},
		{"W3 Selected twice", func(c *Capability) {
			c.GraphQL.Document = "query Asi($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { ...Selected } ander: ingeschrevenPersoon(bsn: $bsn) { ...Selected } }"
		}, "Selected"},
		{"W4 subject variable not declared", func(c *Capability) {
			c.GraphQL.SubjectVariable = "burgerservicenummer"
		}, "subject_variable"},
		{"W5 unknown identifier", func(c *Capability) {
			c.attribute("given_name").Identifier = "https://gbo.example/attributes/given_names/1"
		}, "catalogue"},
		{"W6 duplicate identifier", func(c *Capability) {
			c.Attributes = append(c.Attributes, *c.attribute("given_name"))
		}, "duplicate"},
		{"W7 field outside value_schema", func(c *Capability) {
			c.attribute("address").Fields["county"] = Field{Pointer: "/woontOp/gemeente"}
		}, "county"},
		{"W8 required member without mapping", func(c *Capability) {
			delete(c.attribute("address").Fields, "postal_code")
		}, "postal_code"},
		{"W12 const of the wrong type", func(c *Capability) {
			c.attribute("address").Fields["country"] = Field{Const: json.RawMessage(`528`)}
		}, "country"},
		{"W13 each on a member that is not a list", func(c *Capability) {
			c.attribute("address").Fields["street"] = Field{Each: "/woontOp", Pointer: "/straatnaam"}
		}, "street"},
		{"W13 list member without each", func(c *Capability) {
			c.attribute("nationality").Fields["nationalities"] = Field{Pointer: "/heeftNationaliteit/nationaliteit"}
		}, "nationalities"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			capability := brpCapability(t)
			test.mutate(&capability)
			err := capability.Validate(catalogue)
			if err == nil {
				t.Fatal("Validate() accepted the capability")
			}
			if !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("Validate() error = %q, want it to name %q", err, test.reason)
			}
		})
	}
}
