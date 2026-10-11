# syntax=docker/dockerfile:1

# 1) Build the React UI (outputs to internal/webui/dist)
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2) Build the Go binary with the UI embedded
FROM golang:1.26-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/webui/dist internal/webui/dist
RUN CGO_ENABLED=0 go build -tags embedui -trimpath -ldflags="-s -w" -o /out/powerposter ./cmd/powerposter

# 3) Runtime: Debian slim + FFmpeg (Debian's FFmpeg includes NVENC support)
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 1000 --home-dir /data powerposter \
 && mkdir -p /data /inbox \
 && chown powerposter:powerposter /data /inbox
COPY --from=server /out/powerposter /usr/local/bin/powerposter
USER powerposter
ENV PP_DATA_DIR=/data \
    PP_INBOX_DIR=/inbox \
    PP_PRIVATE_ADDR=:4000 \
    PP_PUBLIC_ADDR=:4001
VOLUME ["/data"]
EXPOSE 4000 4001
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["powerposter", "healthcheck"]
ENTRYPOINT ["powerposter"]
CMD ["serve"]
