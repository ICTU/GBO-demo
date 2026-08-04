FROM golang:1.26.5-alpine AS builder

# Pinned OpenFTV commit (main). Published registry images lag main and
# lack Rego deny-reason passthrough (FTV-474), so we build from source.
ARG OPENFTV_REF=5da6cf1e9c8a55385fbac61aa9eeb9fc889f2b8f

WORKDIR /build
ADD https://gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/-/archive/${OPENFTV_REF}/open-ftv-${OPENFTV_REF}.tar.gz /tmp/open-ftv.tar.gz
RUN tar -xzf /tmp/open-ftv.tar.gz -C /build --strip-components=1

# GBO patch: GraphQL request-mapper (replaces the pdp-service context-
# handler). Registered as the "graphqltocontext" mapping and wired into
# the PDP controller via pdp.WithMappings (unwired upstream at this ref).
COPY services/openftv-pdp/mapper/graphql.go eam/mapping/graphql.go
COPY services/openftv-pdp/mapper/graphql_test.go eam/mapping/graphql_test.go
RUN sed -i '/mappers = append(mappers, BodyToContext)/a\ \t\tcase "graphqltocontext":\n\t\t\tmappers = append(mappers, GraphQLToContext)' eam/mapping/mapper.go \
  && sed -i '/pdp.WithADL(decisionLog),/a\ \t\tpdp.WithMappings(mapping.MappingsFromConfig(s.cfg.RequestMappings)...),' apps/pdp/server/auth.go \
  && sed -i 's|handlers "gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/handlers/fiber"|handlers "gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/handlers/fiber"\n\t"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/mapping"|' apps/pdp/server/auth.go \
  && go get github.com/vektah/gqlparser/v2@v2.5.30 \
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
