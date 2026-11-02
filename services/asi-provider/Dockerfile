FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
WORKDIR /src/services/asi-provider
# The ASIP depends on the sibling asi-mapping and source-registry modules; the
# `replace ../...` directives in go.mod resolve because they are laid out here
# the way they are in the tree.
COPY services/asi-mapping/ /src/services/asi-mapping/
COPY services/source-registry/ /src/services/source-registry/
COPY services/asi-provider/go.mod services/asi-provider/go.sum ./
RUN go mod download
COPY services/asi-provider/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /asi-provider .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /asi-provider .
COPY services/asi-provider/config/dataservices.json ./config/dataservices.json
# GBO's attribute catalogue: the shape the ASIP builds each attribute into.
COPY schemas/asi-attributes.json ./config/asi-attributes.json
ENV ASIP_ATTRIBUTE_CATALOGUE_FILE=config/asi-attributes.json
EXPOSE 4020
CMD ["./asi-provider"]
