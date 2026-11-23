# Built from the repository root, like eudi-adapter and dev-portal-backend:
# the sidecar depends on the sibling ldv-client and ftv-graphql-mapper
# modules, which a build context scoped to this directory cannot see. The
# `replace ../…` lines in go.mod resolve because the modules are laid out here
# the way they are in the tree.
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder
WORKDIR /src/services/bron-sidecar
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/ftv-graphql-mapper/ /src/services/ftv-graphql-mapper/
COPY services/bron-sidecar/go.mod services/bron-sidecar/go.sum ./
RUN go mod download
COPY services/bron-sidecar/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bron-sidecar .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /bron-sidecar .
# Demo keys for the demo sources' OIN, as bsnk-mock issues them: they hold no
# key material. A deployment mounts its own directory over SUBJECT_KEYS_DIR.
COPY services/bron-sidecar/config/ ./config/
EXPOSE 4011
CMD ["./bron-sidecar"]
