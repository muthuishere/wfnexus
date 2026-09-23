# The platform: API, engine and the built UI in one image.
#
# It is one binary and a static bundle, so the image is a scratch-adjacent
# runtime rather than a toolchain. Postgres and S3 are its only dependencies
# and both are configured by environment variable.

# ---- the UI bundle --------------------------------------------------------
FROM node:24-alpine AS ui
WORKDIR /ui
COPY apps/ui/package*.json ./
RUN npm ci
COPY apps/ui ./
RUN npm run build

# ---- the server -----------------------------------------------------------
FROM golang:1.26-alpine AS api
WORKDIR /src
RUN apk add --no-cache git
COPY apps/api/go.mod apps/api/go.sum ./
RUN go mod download
COPY apps/api ./
# Static, so the runtime image needs no libc and the same binary runs anywhere.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/wfnexus . \
 && go build -trimpath -ldflags="-s -w" -o /out/wfx ./cmd/wfx

# ---- what actually ships --------------------------------------------------
FROM alpine:3.21
# git is not optional: a project is a repository, and the platform clones it.
# The shells are what a `run:` step executes with.
RUN apk add --no-cache git bash ca-certificates tzdata \
 && adduser -D -u 10001 wfx
WORKDIR /app
COPY --from=api /out/wfnexus /out/wfx /usr/local/bin/
COPY --from=ui /ui/dist /app/ui
COPY workflows /app/workflows
COPY registries.json /app/registries.json
ENV WFX_ADDR=:8090 \
    WFX_UI_DIR=/app/ui \
    WFX_WORKFLOWS_DIR=/app/workflows \
    WFX_REGISTRIES=/app/registries.json \
    WFX_WORKDIR=/data/runs
RUN mkdir -p /data && chown -R wfx /data /app
USER wfx
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
  CMD wget -qO- http://127.0.0.1:8090/api/health || exit 1
ENTRYPOINT ["wfnexus"]
