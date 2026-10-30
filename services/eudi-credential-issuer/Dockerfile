FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder
WORKDIR /src/services/eudi-credential-issuer
# The issuer depends on the sibling oid4vc-kit module; the `replace ../...`
# directive in go.mod resolves because it is laid out here the way it is in
# the tree.
COPY services/oid4vc-kit/ /src/services/oid4vc-kit/
COPY services/eudi-credential-issuer/go.mod services/eudi-credential-issuer/go.sum ./
RUN go mod download
COPY services/eudi-credential-issuer/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /eudi-credential-issuer .

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /eudi-credential-issuer .
USER 1000
EXPOSE 8080 8081
CMD ["./eudi-credential-issuer", "serve"]
