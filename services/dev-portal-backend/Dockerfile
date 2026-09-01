FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
WORKDIR /app
COPY services/dev-portal-backend/go.mod services/dev-portal-backend/go.sum ./
RUN go mod download
COPY services/dev-portal-backend/ ./
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache wget
COPY --from=build /server /server
COPY services/dev-portal-backend/scenarios /scenarios
COPY services/dev-portal-backend/rules.json /rules.json
COPY policies /policies
EXPOSE 4007
CMD ["/server"]
