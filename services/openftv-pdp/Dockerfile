FROM golang:1.27-alpine@sha256:26402d86be3d72e6a9410afa0108f03529f51f0c1b5eb7f503d0bc44cc7857ac AS builder

# Released OpenFTV. Built from source rather than pulled, because the
# GraphQL request-mapper below still has to be patched in (#331); the
# published pdp image becomes usable once that patch is empty. Keep in
# lockstep with the manager and management-interface image tags in
# docker-compose.yml — CI fails when the three drift apart.
ARG OPENFTV_REF=v0.1.0

WORKDIR /build
ADD https://gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/-/archive/${OPENFTV_REF}/open-ftv-${OPENFTV_REF}.tar.gz /tmp/open-ftv.tar.gz
RUN tar -xzf /tmp/open-ftv.tar.gz -C /build --strip-components=1

# GBO patch: GraphQL request-mapper (replaces the pdp-service context-
# handler). Registered as the "graphqltocontext" mapping and wired into
# the PDP controller via pdp.WithMappings (unwired upstream at this ref:
# PDP_REQUEST_MAPPINGS is parsed into cfg.RequestMappings, but no app
# reads it).
COPY services/openftv-pdp/mapper/graphql.go eam/mapping/graphql.go
COPY services/openftv-pdp/mapper/graphql_test.go eam/mapping/graphql_test.go
# Each patch step asserts it changed something. `sed -i` exits 0 when its
# pattern does not match, so an upstream rename would otherwise produce a
# PDP in which the mapper is never registered — every policy then
# evaluating against an empty context, with nothing reporting it. Release
# images are not gated on a PR either, so this has to fail at build time.
RUN set -eux; \
    patch_assert() { \
      file="$1"; shift; \
      before="$(sha256sum "$file" | cut -d' ' -f1)"; \
      sed -i "$@" "$file"; \
      if [ "$before" = "$(sha256sum "$file" | cut -d' ' -f1)" ]; then \
        echo "PATCH FAILED: no change to $file — upstream anchor moved (OPENFTV_REF=${OPENFTV_REF})" >&2; \
        exit 1; \
      fi; \
    }; \
    patch_assert eam/mapping/mapper.go \
      '/mappers = append(mappers, BodyToContext)/a\ \t\tcase "graphqltocontext":\n\t\t\tmappers = append(mappers, GraphQLToContext)'; \
    patch_assert apps/pdp/server/auth.go \
      's|WithOptions(pdp.WithADL(decisionLog))|WithOptions(pdp.WithADL(decisionLog), pdp.WithMappings(mapping.MappingsFromConfig(s.cfg.RequestMappings)...))|'; \
    patch_assert apps/pdp/server/auth.go \
      's|"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/log/decisions"|"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/log/decisions"\n\t"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/mapping"|'; \
    grep -q 'case "graphqltocontext":' eam/mapping/mapper.go; \
    grep -q 'pdp.WithMappings(' apps/pdp/server/auth.go

RUN go get github.com/vektah/gqlparser/v2@v2.5.30 github.com/golang-jwt/jwt/v5@v5.2.2 \
  && go mod tidy \
  && go test ./eam/mapping \
  && CGO_ENABLED=0 go build -ldflags="-s -w" -o /pdp ./apps/pdp/cmd/

FROM alpine:3.22
RUN apk add --no-cache curl
COPY --from=builder /pdp /
COPY services/openftv-pdp/field-map.json /etc/gbo/field-map.json
# Demo policies baked in (PDP_POLICIES_STORE points at /policies). The
# upstream image carries no policies, so without this every runtime
# would have to supply them separately — compose bind-mounts ./policies
# over this directory for hot-reload. Build context is the repo root so
# this COPY can reach ./policies.
COPY policies /policies
EXPOSE 8443
ENTRYPOINT ["/pdp"]
