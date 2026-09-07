FROM golang:1.27-alpine@sha256:26402d86be3d72e6a9410afa0108f03529f51f0c1b5eb7f503d0bc44cc7857ac AS builder
WORKDIR /app
COPY services/eudi-adapter/go.mod services/eudi-adapter/go.sum ./
RUN go mod download
COPY services/eudi-adapter/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o eudi-adapter .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /app/eudi-adapter .
COPY services/eudi-adapter/config/issuance_server.toml.example ./issuance_server.toml.example
EXPOSE 4009
CMD ["./eudi-adapter"]
