# Built from the repository root, like eudi-adapter and dev-portal-backend:
# the sidecar depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:4cb7ac979db5fcc41cae44b2227ba5ab8a51e8807f40d9ba4dee20a0ad960b5b AS builder
WORKDIR /src/services/bron-sidecar
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/bron-sidecar/go.mod services/bron-sidecar/go.sum ./
RUN go mod download
COPY services/bron-sidecar/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bron-sidecar .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /bron-sidecar .
EXPOSE 4011
CMD ["./bron-sidecar"]
