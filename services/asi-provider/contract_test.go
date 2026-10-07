package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
)

var (
	contractOnce sync.Once
	contractDoc  *openapi3.T
	contractErr  error
)

// loadContract parses the published contract once for all tests.
func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	contractOnce.Do(func() { contractDoc, contractErr = loadContractDoc() })
	if contractErr != nil {
		t.Fatalf("published contract: %v", contractErr)
	}
	return contractDoc
}

// assertConformsToContract validates a response against the published
// contract: status code, headers (including the seal) and body. A body the
// contract does not describe fails too.
func assertConformsToContract(t *testing.T, req *http.Request, reqBody string, rec *httptest.ResponseRecorder, path string) {
	t.Helper()
	doc := loadContract(t)
	route, err := contractRoute(doc, path)
	if err != nil {
		t.Fatal(err)
	}
	if resp := route.Operation.Responses.Status(rec.Code); resp != nil && len(resp.Value.Content) == 0 && rec.Body.Len() > 0 {
		t.Fatalf("POST %s: response %d has a body the contract does not describe: %s", path, rec.Code, rec.Body)
	}
	req.Body = io.NopCloser(bytes.NewBufferString(reqBody))
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req,
			Route:   route,
		},
		Status: rec.Code,
		Header: rec.Header(),
		Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			MultiError:            true,
		},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Fatalf("POST %s: response %d does not conform to the contract: %v\nbody: %s", path, rec.Code, err, rec.Body)
	}
}

func TestPublishedContract(t *testing.T) {
	env := newTestServer(t)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json: status = %d", rec.Code)
	}
	doc := loadContract(t)
	if got := doc.Servers[0].URL; got != "http://localhost:4020"+basePath {
		t.Errorf("server = %s", got)
	}
	if doc.Components.SecuritySchemes["bearerAuth"] == nil {
		t.Errorf("contract has no bearer security scheme")
	}
	for _, path := range []string{"/verify", "/retrieve"} {
		op := doc.Paths.Find(path).Post
		if h := op.Responses.Status(http.StatusOK).Value.Headers["X-JWS-Signature"]; h == nil || !h.Value.Required {
			t.Errorf("%s: 200 has no required X-JWS-Signature header", path)
		}
		for _, status := range []int{400, 401, 404, 501} {
			resp := op.Responses.Status(status)
			if resp == nil {
				t.Errorf("%s: contract has no %d response", path, status)
				continue
			}
			if resp.Value.Content.Get("application/problem+json") == nil {
				t.Errorf("%s: %d has no problem details body", path, status)
			}
		}
	}

	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+dataserviceFileName, nil))
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("GET /%s: status = %d", dataserviceFileName, rec.Code)
	}
}

// The ETSI file is published unchanged: the completions only add.
func TestCompletionsOnlyAdd(t *testing.T) {
	var base, merged any
	_ = json.Unmarshal(mustReadOpenAPIFile(etsiOpenAPIFile), &base)
	_ = json.Unmarshal(publishedContract, &merged)
	var walk func(path string, b, m any)
	walk = func(path string, b, m any) {
		bm, ok := b.(map[string]any)
		if !ok {
			if path == "/servers" {
				return // the server template is filled in
			}
			bj, _ := json.Marshal(b)
			mj, _ := json.Marshal(m)
			if !bytes.Equal(bj, mj) {
				t.Errorf("%s changed: %s -> %s", path, bj, mj)
			}
			return
		}
		mm, _ := m.(map[string]any)
		for k, v := range bm {
			if _, ok := mm[k]; !ok {
				t.Errorf("%s/%s removed", path, k)
				continue
			}
			walk(path+"/"+k, v, mm[k])
		}
	}
	walk("", base, merged)
}
