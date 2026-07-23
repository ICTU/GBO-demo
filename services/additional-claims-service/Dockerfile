FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /additional-claims-service .

FROM scratch
COPY --from=builder /additional-claims-service /additional-claims-service
USER 65532:65532
EXPOSE 4012
ENTRYPOINT ["/additional-claims-service"]
