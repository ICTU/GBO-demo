// The GraphQL request-mapper is patched into the OpenFTV tree at image
// build time (see Dockerfile), so it has no main package of its own.
// This module exists so the mapper is compiled, vetted, linted and unit
// tested by CI like any other Go code in this repo — before it is copied
// into a build that would otherwise be the only thing checking it.
//
// open-ftv is pinned to the same commit as OPENFTV_REF in the Dockerfile.
// Keep the two in lockstep: the tests then compile against exactly the
// eam/models the image builds against.
module gbo-demo/openftv-pdp

go 1.26.5

require (
	github.com/vektah/gqlparser/v2 v2.5.34
	gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv v1.4.6-0.20260723101629-5da6cf1e9c8a
)

require (
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/exp v0.0.0-20260718201538-764159d718ef // indirect
)
