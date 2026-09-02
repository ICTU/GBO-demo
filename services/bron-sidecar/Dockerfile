# Built from the repository root, like eudi-adapter and dev-portal-backend:
# the sidecar depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
WORKDIR /src/services/bron-sidecar
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/bron-sidecar/go.mod services/bron-sidecar/go.sum ./
RUN go mod download
COPY services/bron-sidecar/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bron-sidecar .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /bron-sidecar .
EXPOSE 4011
CMD ["./bron-sidecar"]
