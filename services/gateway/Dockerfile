# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/gateway
FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 relaypine
USER relaypine
COPY --from=build /gateway /gateway
ENV GATEWAY_ADDR=0.0.0.0:4811 APP_ENV=production
EXPOSE 4811
ENTRYPOINT ["/gateway"]
