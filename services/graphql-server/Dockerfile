# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this bron depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder
WORKDIR /src/services/graphql-server
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/graphql-server/go.mod services/graphql-server/go.sum ./
RUN go mod download
COPY services/graphql-server/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /graphql-server .

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /graphql-server .
COPY services/graphql-server/mockdata/ mockdata/
COPY services/graphql-server/config/ config/
EXPOSE 4000
CMD ["./graphql-server"]
