FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder
WORKDIR /src/services/eudi-adapter
# The adapter depends on the sibling ldv-client, asi-mapping, source-registry
# and oid4vc-kit modules; the `replace ../...` directives in go.mod resolve
# because they are laid out here the way they are in the tree.
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/asi-mapping/ /src/services/asi-mapping/
COPY services/source-registry/ /src/services/source-registry/
COPY services/oid4vc-kit/ /src/services/oid4vc-kit/
COPY services/eudi-adapter/go.mod services/eudi-adapter/go.sum ./
RUN go mod download
COPY services/eudi-adapter/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /eudi-adapter .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /eudi-adapter .
COPY services/eudi-adapter/config/issuance_server.toml.example ./issuance_server.toml.example
COPY services/eudi-adapter/config/qtsp_attestation_metadata.json ./qtsp_attestation_metadata.json
EXPOSE 4009
CMD ["./eudi-adapter"]
