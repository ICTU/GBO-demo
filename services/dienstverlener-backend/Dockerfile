# Built from the repository root, like graphql-server: the consumer writes
# Logboek Dataverwerkingen records through the sibling ldv-client module,
# which a build context scoped to this directory cannot see. The
# `replace ../ldv-client` in go.mod resolves because the two are laid out
# here the way they are in the tree.
FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
WORKDIR /src/services/dienstverlener-backend
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/dienstverlener-backend/go.mod services/dienstverlener-backend/go.sum ./
RUN go mod download
COPY services/dienstverlener-backend/ .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache wget
COPY --from=build /server /server
EXPOSE 4006
CMD ["/server"]
