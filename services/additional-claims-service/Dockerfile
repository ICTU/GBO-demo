FROM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /additional-claims-service .

FROM scratch
COPY --from=builder /additional-claims-service /additional-claims-service
USER 65532:65532
EXPOSE 4012
ENTRYPOINT ["/additional-claims-service"]
