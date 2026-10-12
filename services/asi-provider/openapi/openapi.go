// Package openapi holds the contract files: the OpenAPI of ETSI TS 119 478
// Annex B and its dataservice schema, both unchanged, and the completions.
package openapi

import "embed"

//go:embed 19478-authentic-source-interface-openapi.json 19478-dataservice-schema.json completions.json
var Files embed.FS

const (
	ETSIFile        = "19478-authentic-source-interface-openapi.json"
	DataserviceFile = "19478-dataservice-schema.json"
	CompletionsFile = "completions.json"
)
