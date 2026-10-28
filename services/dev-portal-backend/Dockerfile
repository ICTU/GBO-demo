FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /app
COPY services/dev-portal-backend/go.mod services/dev-portal-backend/go.sum ./
RUN go mod download
COPY services/dev-portal-backend/ ./
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache wget
COPY --from=build /server /server
COPY services/dev-portal-backend/scenarios /scenarios
COPY services/dev-portal-backend/rules.json /rules.json
COPY services/dev-portal-backend/organizations.json /organizations.json
COPY policies /policies
EXPOSE 4007
CMD ["/server"]
