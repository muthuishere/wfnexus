# Staged, not written by hand

Everything beside this file is COPIED here by `task assets:stage` and then
compiled into `wfx-server` by `//go:embed` — the built UI bundle
(`apps/ui/dist` → `ui/`), plus the repository's default `templates/`, `skills/`
and `registries.json`.

None of it is committed; the `.gitignore` in this directory keeps it out. This
file IS committed, for one reason: `//go:embed all:embedded` needs the
directory to exist and to be non-empty, so a fresh clone that has never run a
build still compiles and still passes `go test ./...`.

A binary built without staging serves no UI and carries no defaults — which is
why `task build` and `task release` stage first, and why staging FAILS when
`apps/ui/dist/index.html` is missing rather than embedding an empty tree.
