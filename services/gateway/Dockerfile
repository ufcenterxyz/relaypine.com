FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /gateway ./cmd/gateway
FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 relaypine
USER relaypine
COPY --from=build /gateway /gateway
ENV GATEWAY_ADDR=0.0.0.0:8080 APP_ENV=production
EXPOSE 8080
ENTRYPOINT ["/gateway"]
