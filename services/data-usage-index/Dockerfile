# The index has no sibling modules, so its own directory is the build context.
FROM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache wget
COPY --from=build /server /server
# 4019: the FSC listener behind cr-inway. 4020: the lookup, for the register.
EXPOSE 4019 4020
CMD ["/server"]
