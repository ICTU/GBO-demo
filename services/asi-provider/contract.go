package main

import (
	"embed"
	"encoding/json"
	"fmt"
)

// The published contract is the OpenAPI of ETSI TS 119 478 Annex B, unchanged,
// with completions.json applied as a JSON Merge Patch (RFC 7386).
//
// The completions are not deviations from ETSI. They fill in what Annex B
// leaves open and the normative text or the law requires: the concrete
// server (the base path is the ETSI default), the bearer token of
// clause 6.1.3, the seal header required by Implementing Regulation (EU)
// 2026/1735, and the 400 and 501 responses of /retrieve that
// REQ-ASIP-6.1.2.2-06 lists but Annex B omits.

//go:embed openapi/19478-authentic-source-interface-openapi.json openapi/19478-dataservice-schema.json openapi/completions.json
var openapiFiles embed.FS

const (
	etsiOpenAPIFile     = "openapi/19478-authentic-source-interface-openapi.json"
	dataserviceFile     = "openapi/19478-dataservice-schema.json"
	completionsFile     = "openapi/completions.json"
	dataserviceFileName = "19478-dataservice-schema.json"
)

// publishedContract is served on /openapi.json. The files are embedded, so a
// failure here is a build defect, caught by the tests.
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
