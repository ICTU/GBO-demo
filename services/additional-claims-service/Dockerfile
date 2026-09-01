FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /additional-claims-service .

FROM scratch
COPY --from=builder /additional-claims-service /additional-claims-service
USER 65532:65532
EXPOSE 4012
ENTRYPOINT ["/additional-claims-service"]
