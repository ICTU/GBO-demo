# Built from the repository root, like the other LDV components: the portal
# shares the wire DTO and the spool with them through the sibling ldv-client
# module, which a build context scoped to this directory cannot see.
FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src/services/consent-portal-backend
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/consent-portal-backend/go.mod services/consent-portal-backend/go.sum ./
RUN go mod download
COPY services/consent-portal-backend/ .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache wget
COPY --from=build /server /server
EXPOSE 4005
CMD ["/server"]
