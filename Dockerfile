# --- build stage -----------------------------------------------------------
FROM golang:1.27-alpine3.24 AS build

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binary; trimpath keeps the image free of host paths.
# Version comes from an optional build arg (defaults to "dev").
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/promui ./cmd/promui

# --- runtime stage --------------------------------------------------------
FROM alpine:3.24

# su-exec: drop privileges in the entrypoint (tiny sudo alternative).
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -S promui \
    && adduser -S -G promui -h /data -s /sbin/nologin promui

COPY --from=build /out/promui /usr/local/bin/promui

# Directory with file_sd files; mount a bind (or volume) here —
# see deploy/compose.yml, which mounts ./deploy/file_sd.
RUN mkdir -p /data/file_sd && chown -R promui:promui /data
VOLUME ["/data/file_sd"]

# Run as an unprivileged user; /data/file_sd is chowned at start
# (see docker-entrypoint.sh) so the volume stays writable.
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV PROMUI_FILES_SD_DIR=/data/file_sd \
    PROMUI_FILES_SD_MOUNT=/etc/prometheus/file_sd \
    PROMUI_PROMETHEUS_URL=http://prometheus:9090 \
    PROMUI_LISTEN=0.0.0.0:8080

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["promui"]
