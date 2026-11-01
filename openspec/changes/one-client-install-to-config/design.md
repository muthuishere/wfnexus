## Context

carry (github.com/muthuishere/carry) restores a container's tools, config files and env defaults from
an encrypted, versioned S3 state before the process starts (`carry run --`, already the ENTRYPOINT of
`infra/Dockerfile` and `Dockerfile.runner`). Every change today is the `carry` CLI run on an operator
machine that holds the bucket credentials.

## Decisions

1. **The server is the carry client, not the laptop.** The server already holds `CARRY_REPO`, the key
   file and the S3 credentials (carry hands them to children through `CARRY_LAUNCHER_ENV`). Driving
   carry there means a wfx token is the only credential an operator needs, checked by the
   authorization wfx already has. Rejected: the client calling carry/S3 directly, which spreads the
   bucket key to every laptop.
2. **Shell out to the carry binary over its JSON contract** instead of importing a Go module. The
   contract (`docs/cli-contract.md` v0.1) is carry's stable API, the binary is in both images, and
   go.mod does not change. `CARRY_BIN` overrides the path (bare installs, tests).
3. **States**: empty means the server's own (`CARRY_STATE`, else `wfnexus`, the images' default). A
   worker is its name, the same default `Dockerfile.runner` uses. There is no lookup of a worker's
   actual CARRY_STATE; `--state` covers an override.
4. **Permissions**: `carry` is `admin:read|write`, because installing a binary is code execution on
   the box.
5. **Env values are never printed.** A listing returns names. `?values=1` exists solely for
   `wfx sync`, and the CLI writes those values only to a 0600 `env.defaults` file or to another
   context. Values reach carry on stdin (`env import <state> -`).
6. **Install reuses instead of inventing.** `install.sh` gains `WFX_BIN` (allowlisted to the three
   binaries), so the same checksum-verified path installs the server and runner, locally or as the
   remote command of `ssh`. `--image` runs the GHCR image with `-e NAME` pass-through. k8s is a single
   `helm upgrade --install`. The chart is owned elsewhere, so only `--set image.tag` is assumed.
7. **Sync is additive and covers files and env only.** Deletes stay explicit (`wfx config rm`).
   Workflows and skills already move with `wfx publish`/`wfx pull`, and folding them into `sync` is
   a follow-up.

## Risks

- carry's `tool ls` is the bucket catalog, not what a state has. Per-state tool listing needs a carry
  addition (follow-up).
- A server image in `--image` local/ssh mode boots in its baked `WFX_MODE=server`, which needs
  Postgres. This is documented, and compose remains the full-stack path.
