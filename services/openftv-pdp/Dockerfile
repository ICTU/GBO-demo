FROM golang:1.26.5-alpine AS builder

# Pinned OpenFTV commit (main). Published registry images lag main and
# lack Rego deny-reason passthrough (FTV-474), so we build from source.
# Keep in lockstep with the open-ftv require in services/openftv-pdp/go.mod:
# that module is what compiles and unit-tests the mapper in CI.
ARG OPENFTV_REF=5da6cf1e9c8a55385fbac61aa9eeb9fc889f2b8f

WORKDIR /build
ADD https://gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/-/archive/${OPENFTV_REF}/open-ftv-${OPENFTV_REF}.tar.gz /tmp/open-ftv.tar.gz
RUN tar -xzf /tmp/open-ftv.tar.gz -C /build --strip-components=1

# GBO patch: GraphQL request-mapper (replaces the pdp-service context-
# handler). Registered as the "graphqltocontext" mapping and wired into
# the PDP controller via pdp.WithMappings (unwired upstream at this ref).
#
# Only graphql.go is copied — upstream_shim.go exists solely so the mapper
# compiles as a standalone module for CI, and would collide with
# eam/mapping/options.go here.
COPY services/openftv-pdp/mapper/graphql.go eam/mapping/graphql.go

# Each patch step asserts it changed something. `sed -i` exits 0 when its
# pattern does not match, so an upstream rename would otherwise produce a
# PDP in which the mapper is never registered — every policy then
# evaluating an empty context, with nothing reporting it. A published
# image is not gated on a build failure either, so this has to fail here.
RUN set -eux; \
    patch_assert() { \
      file="$1"; shift; \
      before="$(sha256sum "$file" | cut -d' ' -f1)"; \
      sed -i "$@" "$file"; \
      after="$(sha256sum "$file" | cut -d' ' -f1)"; \
      if [ "$before" = "$after" ]; then \
        echo "PATCH FAILED: no change to $file — upstream anchor moved (OPENFTV_REF=${OPENFTV_REF})" >&2; \
        exit 1; \
      fi; \
    }; \
    patch_assert eam/mapping/mapper.go \
      '/mappers = append(mappers, BodyToContext)/a\ \t\tcase "graphqltocontext":\n\t\t\tmappers = append(mappers, GraphQLToContext)'; \
    patch_assert apps/pdp/server/auth.go \
      '/pdp.WithADL(decisionLog),/a\ \t\tpdp.WithMappings(mapping.MappingsFromConfig(s.cfg.RequestMappings)...),'; \
    patch_assert apps/pdp/server/auth.go \
      's|handlers "gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/handlers/fiber"|handlers "gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/handlers/fiber"\n\t"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/mapping"|'; \
    grep -q 'case "graphqltocontext":' eam/mapping/mapper.go; \
    grep -q 'pdp.WithMappings(' apps/pdp/server/auth.go

# gqlparser is already required by open-ftv; go mod tidy resolves it.
RUN go mod tidy \
  && CGO_ENABLED=0 go build -ldflags="-s -w" -o /pdp ./apps/pdp/cmd/

FROM alpine:3.22
RUN apk add --no-cache curl
COPY --from=builder /pdp /
# Demo policies baked in (PDP_POLICIES_STORE points at /policies). The
# upstream image carries no policies, so without this every runtime would
# have to supply them separately — compose bind-mounts ./policies over
# this directory for hot-reload. Build context is the repo root so this
# COPY can reach ./policies.
#
# The GraphQL SDLs travel with the policies: the mapper reads them from
# here (GBO_SCHEMA_DIR, default /policies) to resolve field→type, so there
# is no generated field-map artifact to keep in sync.
COPY policies /policies
EXPOSE 8443
ENTRYPOINT ["/pdp"]
