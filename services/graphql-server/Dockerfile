# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this bron depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
WORKDIR /src/services/graphql-server
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/graphql-server/go.mod services/graphql-server/go.sum ./
RUN go mod download
COPY services/graphql-server/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /graphql-server .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /graphql-server .
COPY services/graphql-server/mockdata/ mockdata/
COPY services/graphql-server/config/ config/
EXPOSE 4000
CMD ["./graphql-server"]
