FROM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /app
COPY services/dev-portal-backend/go.mod services/dev-portal-backend/go.sum ./
RUN go mod download
COPY services/dev-portal-backend/ ./
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache wget
COPY --from=build /server /server
COPY services/dev-portal-backend/scenarios /scenarios
COPY services/dev-portal-backend/rules.json /rules.json
COPY services/dev-portal-backend/organizations.json /organizations.json
COPY policies /policies
EXPOSE 4007
CMD ["/server"]
