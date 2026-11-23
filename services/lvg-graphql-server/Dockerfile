# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this bron depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder
WORKDIR /src/services/lvg-graphql-server
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/lvg-graphql-server/go.mod services/lvg-graphql-server/go.sum ./
RUN go mod download
COPY services/lvg-graphql-server/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /lvg-graphql-server .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /lvg-graphql-server .
COPY services/lvg-graphql-server/mockdata/ mockdata/
EXPOSE 4008
CMD ["./lvg-graphql-server"]
