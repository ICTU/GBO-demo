# Built from the repository root, like graphql-server: the consumer writes
# Logboek Dataverwerkingen records through the sibling ldv-client module,
# which a build context scoped to this directory cannot see. The
# `replace ../ldv-client` in go.mod resolves because the two are laid out
# here the way they are in the tree.
FROM golang:1.27-alpine@sha256:4cb7ac979db5fcc41cae44b2227ba5ab8a51e8807f40d9ba4dee20a0ad960b5b AS build
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
