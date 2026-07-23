FROM golang:1.26.5-alpine AS builder

# Pinned OpenFTV commit (main). Published registry images lag main and
# lack Rego deny-reason passthrough (FTV-474), so we build from source.
ARG OPENFTV_REF=5da6cf1e9c8a55385fbac61aa9eeb9fc889f2b8f

WORKDIR /build
ADD https://gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/-/archive/${OPENFTV_REF}/open-ftv-${OPENFTV_REF}.tar.gz /tmp/open-ftv.tar.gz
RUN tar -xzf /tmp/open-ftv.tar.gz -C /build --strip-components=1 \
  && CGO_ENABLED=0 go build -ldflags="-s -w" -o /pdp ./apps/pdp/cmd/

FROM alpine:3.22
RUN apk add --no-cache curl
COPY --from=builder /pdp /
EXPOSE 8443
ENTRYPOINT ["/pdp"]
