package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"gbo-demo/asi-provider/internal/asmetadata"
	"gbo-demo/asi-provider/openapi"
)

// The published contract is the OpenAPI of ETSI TS 119 478 Annex B, unchanged,
// with completions.json applied as a JSON Merge Patch (RFC 7386).
//
// The completions are not deviations from ETSI. They fill in what Annex B
// leaves open and the normative text or the law requires: the concrete
// server (the base path is the ETSI default), the OAuth 2.0 authorization of
// clause 6.1.3 with the WWW-Authenticate challenge on 401, the seal header
// required by Implementing Regulation (EU) 2026/1735, and the 400 and 501
// responses of /retrieve that REQ-ASIP-6.1.2.2-06 lists but Annex B omits.

// The files are embedded by package openapi, next to them.
var openapiFiles = openapi.Files

const (
	etsiOpenAPIFile     = openapi.ETSIFile
	dataserviceFile     = openapi.DataserviceFile
	completionsFile     = openapi.CompletionsFile
	dataserviceFileName = openapi.DataserviceFile
)

// publishedContract is the contract with the server of the completions; each
// data service publishes it with its own server (contractFor). The files are
// embedded, so a failure here is a build defect, caught by the tests.
var publishedContract = func() []byte {
	c, err := contract()
	if err != nil {
		panic(err)
	}
	return c
}()

func mustReadOpenAPIFile(name string) []byte {
	b, err := openapiFiles.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

// contract returns the published OpenAPI: Annex B with the completions.
func contract() ([]byte, error) {
	base, err := openapiFiles.ReadFile(etsiOpenAPIFile)
	if err != nil {
		return nil, err
	}
	patch, err := openapiFiles.ReadFile(completionsFile)
	if err != nil {
		return nil, err
	}
	var b, p any
	if err := json.Unmarshal(base, &b); err != nil {
		return nil, fmt.Errorf("parse ETSI OpenAPI: %w", err)
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, fmt.Errorf("parse completions: %w", err)
	}
	return json.MarshalIndent(mergePatch(b, p), "", "  ")
}

// completionsIssuer is the authorization server the completions name: the
// standalone default. contractFor puts the configured issuer in its place.
const completionsIssuer = "http://localhost:4021"

// contractFor returns the published contract of a data service: its base URL
// is the server, and the OAuth URLs (authorization, token, metadata) are the
// issuer's, so a QTSP on another host finds endpoints it can reach.
func contractFor(serverURL, issuer string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(publishedContract, &doc); err != nil {
		return nil, err
	}
	servers, _ := doc["servers"].([]any)
	if len(servers) != 1 {
		return nil, errors.New("the completions name not exactly one server")
	}
	server, _ := servers[0].(map[string]any)
	server["url"] = serverURL
	components, _ := doc["components"].(map[string]any)
	schemes, _ := components["securitySchemes"].(map[string]any)
	if schemes["oauth2"] == nil {
		return nil, errors.New("the completions have no oauth2 security scheme")
	}
	oauth2, _ := withIssuer(schemes["oauth2"], issuer).(map[string]any)
	// The metadata of the authorization server, machine-readable
	// (REQ-DIP-5.3.3-03-02-01; RFC 8414).
	metadata, err := asmetadata.MetadataURL(issuer)
	if err != nil {
		return nil, err
	}
	oauth2["x-oauth-authorization-server"] = metadata
	schemes["oauth2"] = oauth2
	return json.MarshalIndent(doc, "", "  ")
}

// withIssuer replaces the completions' issuer in every string of v.
func withIssuer(v any, issuer string) any {
	switch t := v.(type) {
	case string:
		return strings.ReplaceAll(t, completionsIssuer, issuer)
	case map[string]any:
		for k, x := range t {
			t[k] = withIssuer(x, issuer)
		}
	case []any:
		for i, x := range t {
			t[i] = withIssuer(x, issuer)
		}
	}
	return v
}

// mergePatch applies an RFC 7386 JSON Merge Patch.
func mergePatch(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	}
	for k, v := range p {
		if v == nil {
			delete(t, k)
			continue
		}
		t[k] = mergePatch(t[k], v)
	}
	return t
}

func init() {
	// kin-openapi does not check format "uri" by itself. The ETSI schemas use
	// it for attribute identifiers: an absolute URI, with a scheme.
	openapi3.DefineStringFormatValidator("uri", openapi3.NewCallbackValidator(func(v string) error {
		u, err := url.Parse(v)
		if err != nil || !u.IsAbs() {
			return errors.New("not an absolute URI")
		}
		return nil
	}))
	// Keep validation errors short: they end up in problem details.
	openapi3.SchemaErrorDetailsDisabled = true
}

// loadContractDoc parses the published contract. The relative reference to
// the dataservice schema resolves to the embedded copy.
func loadContractDoc() (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		return openapiFiles.ReadFile(location.Path)
	}
	doc, err := loader.LoadFromDataWithPath(publishedContract, &url.URL{Path: "openapi.json"})
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background(), openapi3.EnableSchemaFormatValidation()); err != nil {
		return nil, err
	}
	return doc, nil
}

// contractRoute returns the POST operation of path (without the base path).
func contractRoute(doc *openapi3.T, path string) (*routers.Route, error) {
	item := doc.Paths.Find(path)
	if item == nil || item.Post == nil {
		return nil, fmt.Errorf("contract has no POST %s", path)
	}
	return &routers.Route{Spec: doc, Path: path, PathItem: item, Method: http.MethodPost, Operation: item.Post}, nil
}

// validateRequest checks a request against the published contract. The
// access token is checked before, by the server itself.
func validateRequest(r *http.Request, route *routers.Route) error {
	return openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
		Request: r,
		Route:   route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	})
}
