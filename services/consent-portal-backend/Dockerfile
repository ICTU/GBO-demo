# Built from the repository root, like the other LDV components: the portal
# shares the wire DTO and the spool with them through the sibling ldv-client
# module, which a build context scoped to this directory cannot see.
FROM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src/services/consent-portal-backend
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/consent-portal-backend/go.mod services/consent-portal-backend/go.sum ./
RUN go mod download
COPY services/consent-portal-backend/ .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache wget
COPY --from=build /server /server
EXPOSE 4005
CMD ["/server"]
