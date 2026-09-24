# The Dockerfile INSIDE the release tarball.
#
# It copies a binary that is already in the tarball rather than building one.
# So `docker compose up` here needs no registry to pull from and no Go
# toolchain on the machine — which is the difference between a tarball someone
# can actually use and one that only works if they can reach your registry.
#
# The repository's own infra/Dockerfile still builds from source; this is the
# shipped artefact's, and they are deliberately not the same file.
FROM alpine:3.21

# git is not optional: a project is a repository and the platform clones it.
# The shells are what a `run:` step executes with.
RUN apk add --no-cache git bash ca-certificates tzdata \
 && adduser -D -u 10001 wfx

WORKDIR /app
COPY bin/wfx-server-linux-amd64 /usr/local/bin/wfx-server
COPY bin/wfx-linux-amd64        /usr/local/bin/wfx
RUN chmod +x /usr/local/bin/wfx-server /usr/local/bin/wfx

# The defaults the platform needs on disk: the built UI, the workflows it
# ships, the templates to copy, the skills a step may load, and the registry of
# providers. Without these a fresh container boots with nothing in it.
COPY ui/          /app/ui/
COPY workflows/   /app/workflows/
COPY templates/   /app/templates/
COPY skills/      /app/skills/
COPY registries.json /app/registries.json

# `server` is STATED, not inherited: the no-config default is `local` (sqlite
# and a folder) so the same binary run by hand needs nothing beside it. This
# image IS the deployed server and must keep getting postgres + s3.
ENV WFX_MODE=server \
    WFX_ADDR=:8090 \
    WFX_UI_DIR=/app/ui \
    WFX_WORKFLOWS_DIR=/app/workflows \
    WFX_TEMPLATES_DIR=/app/templates \
    WFX_SKILLS_DIR=/app/skills \
    WFX_REGISTRIES=/app/registries.json \
    WFX_WORKDIR=/data/runs \
    WFX_ARTIFACT_DIR=/data/artifacts

RUN mkdir -p /data && chown -R wfx /data /app
USER wfx
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
  CMD wget -qO- http://127.0.0.1:8090/api/health || exit 1
ENTRYPOINT ["wfx-server"]
