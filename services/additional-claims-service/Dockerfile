FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /additional-claims-service .

FROM scratch
COPY --from=builder /additional-claims-service /additional-claims-service
USER 65532:65532
EXPOSE 4012
ENTRYPOINT ["/additional-claims-service"]
