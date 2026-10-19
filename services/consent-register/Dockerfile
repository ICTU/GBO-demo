# Built from the repository root, like eudi-adapter and dev-portal-backend:
# this register depends on the sibling ldv-client module, which a build context
# scoped to this directory cannot see. The `replace ../ldv-client` in go.mod
# resolves because the two are laid out here the way they are in the tree.
FROM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src/services/consent-register
COPY services/ldv-client/ /src/services/ldv-client/
COPY services/consent-register/go.mod services/consent-register/go.sum ./
RUN go mod download
COPY services/consent-register/ .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache wget
COPY --from=build /server /server
EXPOSE 4002
CMD ["/server"]
