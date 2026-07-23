FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY services/pdp-service/go.mod services/pdp-service/go.sum ./
RUN go mod download
COPY services/pdp-service/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o pdp-service .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /app/pdp-service .
# Demo GraphQL mirror-schemas baked in (SCHEMA_DIR defaults to /schemas).
# A volume mount at /schemas shadows these files — drop the mount to use
# the baked-in versions.
COPY policies/dvtp/schemas /schemas
EXPOSE 4008
CMD ["./pdp-service"]
