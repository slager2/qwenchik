# syntax=docker/dockerfile:1.7

FROM node:22-bookworm-slim AS frontend
WORKDIR /src/frontend

COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --ignore-scripts

COPY frontend/ ./
RUN npm run build

FROM golang:1.23-bookworm AS build
WORKDIR /src

ENV CGO_ENABLED=1
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
COPY --from=frontend /src/frontend/dist ./internal/api/web/dist
RUN go build -tags sqlite_fts5 -trimpath -ldflags='-s -w' -o /out/qagent ./cmd/qagent

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/qagent /usr/local/bin/qagent

RUN mkdir -p /app/data \
    && useradd --create-home --home-dir /app --uid 10001 --shell /usr/sbin/nologin qwenchik \
    && chown -R qwenchik:qwenchik /app

ENV HOME=/app
USER qwenchik:qwenchik
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/qagent"]
CMD ["serve"]
