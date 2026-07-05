# Multi-stage build: web app -> single Go binary (web embedded) -> slim
# runtime with ffmpeg. modernc sqlite is pure Go, so CGO stays off and the
# binary is fully static. On Linux the watcher uses its polling fallback
# (FSEvents is macOS-only).

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
ARG VERSION=docker
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -tags embedweb \
      -ldflags "-X main.version=${VERSION}" \
      -o /out/media-server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ffmpeg curl tzdata
COPY --from=build /out/media-server /usr/local/bin/media-server
COPY deploy/docker/config.yml /etc/media-server/config.yml
VOLUME /data
EXPOSE 8484
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD curl -fsS http://localhost:8484/api/health || exit 1
ENTRYPOINT ["media-server", "--config", "/etc/media-server/config.yml"]
