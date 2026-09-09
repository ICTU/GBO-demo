# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this register depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
WORKDIR /src/services/consent-register
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/consent-register/go.mod services/consent-register/go.sum ./
RUN go mod download
COPY services/consent-register/ .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache wget
COPY --from=build /server /server
EXPOSE 4002
CMD ["/server"]
