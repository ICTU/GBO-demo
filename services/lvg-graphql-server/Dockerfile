# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this bron depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
WORKDIR /src/services/lvg-graphql-server
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/lvg-graphql-server/go.mod services/lvg-graphql-server/go.sum ./
RUN go mod download
COPY services/lvg-graphql-server/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /lvg-graphql-server .

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /lvg-graphql-server .
COPY services/lvg-graphql-server/mockdata/ mockdata/
EXPOSE 4008
CMD ["./lvg-graphql-server"]
