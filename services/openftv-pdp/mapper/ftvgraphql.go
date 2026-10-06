package mapping

// FTVGraphQL is the OpenFTV adapter of the FTV GraphQL profile mapper
// (services/ftv-graphql-mapper). It translates the PARC into the mapper's
// request, picks the bundled schema of the FSC service the request is for,
// and adds the mapper's output to the resource as the attribute "graphql".
// OpenFTV passes resource attributes to the policy as
// input.resource.attributes, so the profile's resource.properties.graphql
// arrives there.
//
// The adapter holds no mapping logic of its own. It does not change the
// PARC it receives: the resource it returns is a new entity with the same
// type, id, parents and attributes, plus "graphql".

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"sync"

	ftvgraphql "gbo-demo/ftv-graphql-mapper"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

// AttrGraphQL is the resource attribute the mapper output is placed in.
const AttrGraphQL = "graphql"

// attrServiceName is the FSC service the request is for. The Inway passes
// the claims of the FSC access token as subject properties; the token is
// signed by the source's Manager, so the service name is trusted.
const attrServiceName = "service_name"

var (
	ftvCatalogOnce sync.Once
	ftvCatalog     *ftvgraphql.Catalog
)

// ftvGraphQLCatalog loads the schemas and their manifest once, from
// GBO_FTV_GRAPHQL_DIR (default /etc/gbo/ftv-graphql). A change needs a PDP
// restart. When the catalog does not load, every GraphQL request fails
// closed with SCHEMA_UNAVAILABLE.
func ftvGraphQLCatalog() *ftvgraphql.Catalog {
	ftvCatalogOnce.Do(func() {
		dir := os.Getenv("GBO_FTV_GRAPHQL_DIR")
		if dir == "" {
			dir = "/etc/gbo/ftv-graphql"
		}
		catalog, err := ftvgraphql.LoadCatalog(os.DirFS(dir))
		if err != nil {
			slog.Error("FTV GraphQL catalog not loaded: every GraphQL request fails closed", "dir", dir, "error", err)
			return
		}
		for _, name := range catalog.Names() {
			if s, _ := catalog.Service(name); s.Err != nil {
				slog.Error("FTV GraphQL schema not loaded: requests for this service fail closed", "service", name, "error", s.Err)
			}
		}
		ftvCatalog = catalog
	})
	return ftvCatalog
}

// FTVGraphQL maps the GraphQL request in the PARC. Requests that are not
// HTTP requests through an Inway (resource type "uri") pass unchanged:
// contract autosigning uses the same PDP.
func FTVGraphQL(parc *models.PARC, _ ...Option) *models.PARC {
	return mapFTVGraphQL(parc, ftvGraphQLCatalog())
}

func mapFTVGraphQL(parc *models.PARC, catalog *ftvgraphql.Catalog) *models.PARC {
	if parc == nil || parc.Resource == nil || parc.Resource.Type() != "uri" {
		return parc
	}
	out := catalog.Map(serviceName(parc), ftvRequest(parc))
	value, err := toInput(out)
	if err != nil {
		// Without the attribute the policy denies (MAPPER_OUTPUT_MISSING).
		slog.Error("FTV GraphQL mapper output not added", "error", err)
		return parc
	}
	attrs := models.NewAttributeSet(parc.Resource.Attributes())
	attrs.AddAttributeKV(AttrGraphQL, value)
	resource := models.NewEntity(parc.Resource.Type(), parc.Resource.ID(), attrs, parc.Resource.Parents()...)
	return &models.PARC{Principal: parc.Principal, Action: parc.Action, Resource: resource, Context: parc.Context}
}

// ftvRequest reads the request slots of the profile (Section 5.2) from the
// PARC: action.name, resource.id, context.headers, action.properties.body.
func ftvRequest(parc *models.PARC) ftvgraphql.Request {
	req := ftvgraphql.Request{Target: parc.Resource.ID()}
	if parc.Action != nil {
		req.Method = parc.Action.ID()
		if body := parc.Action.Attributes().GetAttribute(models.AttrBody); body != nil {
			req.Body = body.Value()
		}
	}
	if parc.Context != nil {
		req.Headers = headerValues(parc.Context.GetAttributeValue(models.AttrHeaders))
	}
	return req
}

// headerValues passes the headers on as they are. A value that is not a
// string stays what it is, and the mapper fails closed on it.
func headerValues(v any) map[string]any {
	out := map[string]any{}
	switch h := v.(type) {
	case map[string]any:
		for k, val := range h {
			out[k] = val
		}
	case map[string]string:
		for k, val := range h {
			out[k] = val
		}
	}
	return out
}

func serviceName(parc *models.PARC) string {
	if parc.Principal == nil {
		return ""
	}
	name, _ := parc.Principal.Attributes().GetAttributeValue(attrServiceName).(string)
	return name
}

// toInput turns the output into plain JSON values for the policy input.
// Numbers stay json.Number, so an argument value is never rounded.
func toInput(out ftvgraphql.Output) (map[string]any, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value map[string]any
	err = dec.Decode(&value)
	return value, err
}
