package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"gbo-demo/asi-discovery-stub/openapi"
)

func init() {
	// kin-openapi does not check format "uri" by itself: an attribute
	// identifier is an absolute URI (REQ-DIP-5.3.2-01-02).
	openapi3.DefineStringFormatValidator("uri", openapi3.NewCallbackValidator(func(v string) error {
		u, err := url.Parse(v)
		if err != nil || !u.IsAbs() {
			return errors.New("not an absolute URI")
		}
		return nil
	}))
	openapi3.SchemaErrorDetailsDisabled = true
}

// loadContract parses the Annex A OpenAPI with the schemas it references.
func loadContract() (*openapi3.T, error) {
	raw, err := openapi.Files.ReadFile(openapi.ContractFile)
	if err != nil {
		return nil, err
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		return openapi.Files.ReadFile(location.Path)
	}
	doc, err := loader.LoadFromDataWithPath(raw, &url.URL{Path: openapi.ContractFile})
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	return doc, nil
}

func route(doc *openapi3.T, path string) (*routers.Route, error) {
	item := doc.Paths.Find(path)
	if item == nil || item.Get == nil {
		return nil, fmt.Errorf("contract has no GET %s", path)
	}
	return &routers.Route{Spec: doc, Path: path, PathItem: item, Method: http.MethodGet, Operation: item.Get}, nil
}

// newHandler serves the Discover Interface of ETSI TS 119 478 clause 5 as a
// stub: retrieve (clause 5.3) over the records of the configured ASIPs;
// search (clause 5.2, the semantic repository) is not offered.
func newHandler(dir *directory) (http.Handler, error) {
	doc, err := loadContract()
	if err != nil {
		return nil, fmt.Errorf("discover contract: %w", err)
	}
	retrieveRoute, err := route(doc, "/retrieve")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /retrieve", func(w http.ResponseWriter, r *http.Request) {
		if err := openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{Request: r, Route: retrieveRoute}); err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		q := r.URL.Query()
		records := dir.retrieve(r.Context(), query{
			attributeIdentifier: q.Get("attributeIdentifier"),
			country:             q.Get("country"),
			conformsTo:          q.Get("conformsTo"),
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"dataServices": records})
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotImplemented, "Not Implemented", "this stub offers only the retrieve query; search needs the semantic repository")
	})
	for _, name := range []string{openapi.ContractFile, "19478-dataservice-schema.json", "19478-attribute-schema.json"} {
		body, err := openapi.Files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		path := "/" + name
		if name == openapi.ContractFile {
			path = "/openapi.json"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux, nil
}

// writeProblem writes an RFC 9457 problem, the default response of Annex A.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}
