# Releasing

A release is built and published from a maintainer's machine. The repository runs
no GitHub Actions.

```sh
task release:publish -- v0.2.0      # binaries, server and worker images, Helm chart
SKIP_IMAGES=1 task release:publish -- v0.2.0   # binaries only
```

`scripts/release-local.sh` does, in order, and publishes nothing unless every
earlier step passed:

1. Preflight: on `main`, clean, equal to `origin/main`, not a shallow clone, the tag
   unused locally and on the remote; `gh`, `task`, `go`, `node`, and for images
   `docker buildx`, `helm` and a `gh` token with `write:packages`.
2. The gate: `task ci`.
3. Tag locally, `task release`.
4. The host binary reports the tag; every artifact is listed in `checksums.txt`.
5. Push the tag, create the GitHub release, push the images and the chart to ghcr.io.

A failure before the tag is pushed deletes the local tag again.

## What a download can be checked against

- `checksums.txt` (SHA-256 of every artifact), attached to the release. The
  installers refuse a file whose digest is not in it.
- The version every binary reports (`wfx version`), asserted against the tag.

## What is not provided

Nothing was dropped when releases moved off GitHub Actions: the old workflow did
not sign artifacts and produced no provenance attestation either. What did change
is where the build runs: a GitHub-hosted runner before, a maintainer's machine now.
Neither is a reproducible or attested build. Signing (for example cosign for the
images and a signed `checksums.txt`) and build provenance are not done yet.

macOS binaries are unsigned and not notarised (see the release notes).
