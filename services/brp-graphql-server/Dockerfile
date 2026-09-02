# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this bron depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
WORKDIR /src/services/brp-graphql-server
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/brp-graphql-server/go.mod services/brp-graphql-server/go.sum ./
RUN go mod download
COPY services/brp-graphql-server/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /brp-graphql-server .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /brp-graphql-server .
COPY services/brp-graphql-server/mockdata/ mockdata/
COPY services/brp-graphql-server/config/ config/
EXPOSE 4001
CMD ["./brp-graphql-server"]
