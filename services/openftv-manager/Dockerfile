FROM golang:1.26.5-alpine AS builder

# Same pinned OpenFTV commit as the PDP — the bundle format travels between
# the two, so they must come from one tree. Keep in lockstep with
# services/openftv-pdp/Dockerfile and services/openftv-pdp/go.mod.
ARG OPENFTV_REF=5da6cf1e9c8a55385fbac61aa9eeb9fc889f2b8f

WORKDIR /build
ADD https://gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/-/archive/${OPENFTV_REF}/open-ftv-${OPENFTV_REF}.tar.gz /tmp/open-ftv.tar.gz
RUN tar -xzf /tmp/open-ftv.tar.gz -C /build --strip-components=1

# No patch here, unlike the PDP: the Manager is used as shipped.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /manager ./apps/manager/cmd/

FROM alpine:3.22
RUN apk add --no-cache curl
COPY --from=builder /manager /
COPY services/openftv-manager/bundles /bundles
COPY services/openftv-manager/tags /tags
EXPOSE 8443 9443 8080
ENTRYPOINT ["/manager"]
