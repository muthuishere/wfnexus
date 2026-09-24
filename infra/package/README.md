# wfx-server

Everything in this tarball is self-contained: the Linux binaries are here, the
Dockerfile copies them rather than building them, and the UI, workflows,
templates, skills and provider registry travel with it. So nothing below needs
a registry to pull from, a Go toolchain, or network access to anything but your
model provider.

```
bin/                wfx-server, wfx, wfx-runner (linux/amd64)
ui/                 the built dashboard
workflows/          the workflows it ships with
templates/          starting points to copy
skills/             the agent skills a step may load
registries.json     providers, classifiers, MCP servers
Dockerfile          copies bin/ — no build step
docker-compose.yml  postgres + minio + the server + one worker
k8s/                plain manifests, no chart and no operator
config.example.yaml every setting, commented
```

## The whole thing in containers

```sh
docker compose up -d       # → http://localhost:8090
```

That brings up Postgres, MinIO, the server, and one worker holding the labels
`self-hosted,linux,docker` — so a workflow that says `runs-on: linux` works on
a fresh install with nothing else done.

## Just the server, no containers

```sh
cp bin/wfx-server-linux-amd64 /usr/local/bin/wfx-server
cp bin/wfx-linux-amd64        /usr/local/bin/wfx
cp config.example.yaml ~/.config/wfx/config.yaml    # edit it
wfx-server
```

With `mode: local` that is sqlite and a folder — no Postgres, no MinIO, nothing
else running.

## Kubernetes

```sh
kubectl create ns wfx
kubectl -n wfx create secret generic wfnexus \
  --from-literal=DATABASE_URL='postgres://…' \
  --from-literal=WFX_RUNNER_TOKEN="wfx_$(openssl rand -hex 24)" \
  --from-literal=S3_ACCESS_KEY=… --from-literal=S3_SECRET_KEY=…
kubectl -n wfx apply -f k8s/
```

Set `WFX_PUBLIC_URL` to the address your ingress serves — it is the URL in the
join command, so if it is wrong every machine you try to add is wrong the same
way. Scale with **workers**, not replicas: the engine holds a running step in
memory, which is why the Deployment is `replicas: 1` on purpose.

## Adding a machine

Copy `bin/wfx-runner-*` to the machine you want — a build VM, a Windows box, a
Mac on someone's desk — and run the line the **Workers** page gives you:

```sh
wfx-runner join --url https://wfx.example.com --token wfx_… --labels windows,devin
wfx-runner run
```

It takes the steps whose label it holds, with the toolchain installed *there*.
The platform never connects to it; the worker polls out, so the platform can be
a pod behind an ingress and the machine can be a laptop behind NAT.

Run it under the scheduler rather than from a shell — `wfx-runner run` blocks
for as long as the worker lives, so anything that starts it and waits on it is
held that long too. `infra/README.md` in the repository has the systemd,
`schtasks` and LaunchAgent forms.

## Other platforms

The binaries here are linux/amd64, because that is what the container needs.
Windows, macOS and arm64 builds are published beside this tarball.
