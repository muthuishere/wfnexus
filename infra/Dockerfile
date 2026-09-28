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
RUN go build -trimpath -ldflags="-s -w" -o /out/wfx-server . \
 && go build -trimpath -ldflags="-s -w" -o /out/wfx ./cmd/wfx

# ---- what actually ships --------------------------------------------------
FROM alpine:3.21
# git is not optional: a project is a repository, and the platform clones it.
# The shells are what a `run:` step executes with. node/npm are there for the
# one agent CLI the image carries (below).
RUN apk add --no-cache git bash curl ca-certificates tzdata nodejs npm \
 && adduser -D -u 10001 wfx

# opencode, so the default provider (opencode-acp) runs IN this container. Its
# free models (opencode Zen, `wfx models opencode-acp --free`) need no key and
# no login; a paid provider is `docker compose exec wfx-server opencode auth
# login`, kept on the opencode volume. Pinned: an agent CLI that moves under a
# running install changes what every step does. Checked on musl/arm64 and amd64.
ARG OPENCODE_VERSION=1.18.30
RUN npm install -g "opencode-ai@${OPENCODE_VERSION}" && npm cache clean --force \
 && opencode --version

WORKDIR /app
COPY --from=api /out/wfx-server /out/wfx /usr/local/bin/
COPY --from=ui /ui/dist /app/ui
COPY workflows /app/workflows
# The templates people copy and the skills a step (and `wfx install --skills`)
# loads. Without them a fresh container boots with nothing in it.
COPY templates /app/templates
COPY skills /app/skills
COPY registries.json /app/registries.json
# `server` is STATED, not inherited: the no-config default is `local` (sqlite
# and a folder) so a downloaded binary boots on its own. This image IS the
# deployed server, and it must keep getting postgres + s3 whatever the default
# does next.
#
# The secrets key lives on the /data volume: its default is under $HOME, which
# a recreated container loses — and with it every value in the env store.
ENV WFX_MODE=server \
    WFX_ADDR=:8090 \
    WFX_UI_DIR=/app/ui \
    WFX_WORKFLOWS_DIR=/app/workflows \
    WFX_TEMPLATES_DIR=/app/templates \
    WFX_SKILLS_DIR=/app/skills \
    WFX_REGISTRIES=/app/registries.json \
    WFX_WORKDIR=/data/runs \
    WFX_SECRET_KEY_PATH=/data/secret.key
# The opencode dirs exist in the image and belong to wfx, so a named volume
# mounted over them inherits that ownership instead of arriving root-owned.
RUN mkdir -p /data /home/wfx/.local/share/opencode /home/wfx/.config/opencode \
 && chown -R wfx /data /app /home/wfx
USER wfx
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
  CMD wget -qO- http://127.0.0.1:8090/api/health || exit 1
ENTRYPOINT ["wfx-server"]
