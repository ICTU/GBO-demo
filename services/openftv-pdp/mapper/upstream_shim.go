package mapping

// This file is NOT copied into the image. It mirrors the few upstream
// declarations from eam/mapping that graphql.go references, so the mapper
// compiles and can be unit tested as a standalone module here. Inside the
// image graphql.go lands in eam/mapping/ next to upstream's own
// options.go, which provides the real definitions.
//
// Keep this in lockstep with eam/mapping/options.go + mapper.go upstream.
// If it drifts, the tests still pass while the image build breaks — which
// is why the Dockerfile's patch steps assert they matched (see #185 item 4a).

// Option mirrors upstream mapping.Option.
type Option func(m *base)

// base mirrors upstream mapping.base. graphql.go does not read it; it only
// needs the type to exist so the Option signature resolves. Upstream's
// field (headerKeys) is deliberately omitted — an unused field here would
// be lint noise, and nothing in this module reads it.
type base struct{}
