FROM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
WORKDIR /src/services/eudi-adapter
# The adapter depends on the sibling ldv-client, asi-mapping and
# source-registry modules; the `replace ../...` directives in go.mod resolve
# because they are laid out here the way they are in the tree.
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/asi-mapping/ /src/services/asi-mapping/
COPY services/source-registry/ /src/services/source-registry/
COPY services/eudi-adapter/go.mod services/eudi-adapter/go.sum ./
RUN go mod download
COPY services/eudi-adapter/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /eudi-adapter .

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /eudi-adapter .
COPY services/eudi-adapter/config/issuance_server.toml.example ./issuance_server.toml.example
COPY services/eudi-adapter/config/qtsp_attestation_metadata.json ./qtsp_attestation_metadata.json
EXPOSE 4009
CMD ["./eudi-adapter"]
