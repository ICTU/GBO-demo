FROM golang:1.24-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /server .

FROM alpine:3.19
RUN apk add --no-cache wget
COPY --from=build /server /server
EXPOSE 4004
CMD ["/server"]
