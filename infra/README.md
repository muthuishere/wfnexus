# Deploying it, and adding machines to it

Two things live here, and they are the same story from both ends:

- **the platform** — one Go binary, a Postgres, an S3 bucket. Run it with Docker,
  Compose, or Kubernetes.
- **the workers** — machines that have joined it and take the steps whose
  `runs-on:` label they hold. A worker is one binary and one command.

Nothing here is enterprise machinery. There is no operator, no chart, no
control plane. If you can run a container and a Postgres, you can run this.

## One machine

```sh
docker compose -f infra/docker-compose.yml up -d
open http://localhost:8090
```

That brings up Postgres, MinIO, the platform, and one worker container holding
the labels `self-hosted,linux,docker` — so a workflow that says `runs-on: linux`
works on a fresh install with nothing else done.

## Kubernetes

```sh
kubectl create ns wfx
kubectl -n wfx create secret generic wfnexus \
  --from-literal=DATABASE_URL='postgres://…' \
  --from-literal=WFX_RUNNER_TOKEN="wfx_$(openssl rand -hex 24)" \
  --from-literal=S3_ACCESS_KEY=… --from-literal=S3_SECRET_KEY=…
kubectl -n wfx apply -f infra/k8s/wfnexus.yaml -f infra/k8s/runner.yaml
```

Set `WFX_PUBLIC_URL` to the address your ingress serves. It is the URL printed
in the join command, so if it is wrong, every machine you try to add is wrong
in the same way.

**Scale with workers, not replicas.** The engine holds a running step in memory,
so the Deployment is `replicas: 1` on purpose. More capacity means more
machines holding a label, which is what the queue is for.

## Adding a machine — the actual point

A machine joins by running one command. Copy it from the **Workers** page in
the dashboard, or from the CLI:

```sh
wfx workers            # who has joined, and the line to add another
```

On the machine you want to add — a build VM, a Windows box, a Mac on someone's
desk, a Jenkins node you already have:

```sh
wfx-runner join --url https://wfx.example.com --token wfx_… --labels windows,devin
wfx-runner run
```

That is the whole thing. From then on:

```yaml
jobs:
  build:
    runs-on: windows          # a LABEL, never a machine
    steps:
      - id: msbuild
        run: msbuild /p:Configuration=Release
```

The step runs on whichever machine holds `windows`, with the toolchain that is
installed *there* — the Devin CLI, a JDK, a signing certificate, a licence
dongle. The platform never learns about any of it, and never connects to that
machine: the worker polls out. So the platform can be a pod behind an ingress
and the machine can be a laptop behind NAT, and neither has to be reachable
from the other.

A label the platform serves itself (`WFX_RUNNER_LABELS`, default
`local,self-hosted`) runs in process, exactly as before workers existed. A
single-machine install therefore needs no worker at all.

### Running it as a service

**Linux (systemd):**

```ini
# /etc/systemd/system/wfx-runner.service
[Service]
ExecStart=/usr/local/bin/wfx-runner run
Restart=always
User=builder
[Install]
WantedBy=multi-user.target
```

**Windows:** `wfx-runner join …` once, then run `wfx-runner run` from a
scheduled task set to *Run whether user is logged on or not*, or wrap it with
`nssm install wfx-runner`.

**macOS:** a LaunchAgent with `KeepAlive`, so it comes back when the laptop wakes.

### What a worker actually does

It asks for a job, checks out the project if it was sent a repository, runs the
command in the interpreter the step named (or the best one the machine has),
and reports the exit code, stdout and stderr. The result has exactly the shape a
local `run:` step produces, so a gate reading `steps.build.ok` cannot tell where
it ran — and must not care.

The step's identity is in the environment, for tools that want it:
`WFX_RUN_ID`, `WFX_STEP_ID`, `WFX_PROJECT`, `WFX_WORKSPACE`, `WFX_RUNNER`.

### Honest limits, today

- A worker runs **`run:` steps**. Agent (`prompt:`) steps still execute on the
  platform, because that is where the model credentials and the tool loop are.
  Placing an agent step on a machine that holds its own CLI is the next step,
  and the protocol was shaped for it.
- There is **no sandbox**. A worker runs the command as the user it runs as, on
  the machine it is on. That is the same bargain a self-hosted Actions runner or
  a Jenkins node makes, and it is why you put one on a machine you own.
- The registration token is a **bearer token**. Anyone who has it can join a
  machine to the pool. Rotate it from the Workers page when it has been shared
  too widely; machines that already joined hold their own token and keep working.
