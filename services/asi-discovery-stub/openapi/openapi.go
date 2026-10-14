// Package openapi holds the contract of the Discover Interface: the OpenAPI
// of ETSI TS 119 478 Annex A and the two schemas it references, unchanged.
package openapi

import "embed"

//go:embed 19478-discover-interface-openapi.json 19478-dataservice-schema.json 19478-attribute-schema.json
var Files embed.FS

const ContractFile = "19478-discover-interface-openapi.json"
