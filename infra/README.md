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

It asks for a job, checks out the project if it was sent a repository, and does
one of two things.

A **`run:` step** is a command: it runs in the interpreter the step named (or
the best one the machine has) and reports the exit code, stdout and stderr.

An **agent step** is the whole harness. The job carries the step's skills *as
files* — so the worker needs no skills directory, and adding a machine never
means deploying and syncing the platform's skill tree to it — along with its
tool and MCP allowlists, its sub-agent team, its guardrails, its budget and its
output schema. The worker then runs **the same engine code the platform runs**,
given no database and a sink that posts the agent's activity back, so a step
cannot mean one thing on the server and another here. Its tool calls appear in
the run log live, and its workspace diff is carried back as the step's artifact.

What does not travel is the **CLI**. `provider: devin` means the `devin` on this
machine's PATH, using the credential this machine already holds. That asymmetry
is the whole reason to put a worker somewhere: the toolchain lives with the
machine.

Either way the result has exactly the shape a local step produces, so a gate
reading `steps.build.ok` cannot tell where it ran — and must not care.

The step's identity is in the environment, for tools that want it:
`WFX_RUN_ID`, `WFX_STEP_ID`, `WFX_PROJECT`, `WFX_WORKSPACE`, `WFX_RUNNER`.

### Honest limits, today

- A step using the platform's **authoring tools** (`workflow_catalog`,
  `workflow_validate`, `workflow_dryrun`) cannot be placed — those tools *are*
  the server process. It is refused when the job is packed, not discovered as a
  missing tool mid-run.
- **Skills are capped at 8 MB per job.** A skill is documentation and a few
  scripts; a directory far past that is a mistake — a checked-in
  `node_modules`, a model file — and shipping it to every worker on every step
  would be a slow way to find out.
- An agent step's **model credentials are resolved on the worker**. An `http`
  provider needs its `apiKeyEnv` set *there*; a `cli` provider needs no key at
  all, because the CLI holds its own. Only the variable's NAME travels.
- **Plain `http://` is supported and is not secured.** Most installs are an
  internal address with no certificate, so refusing them would only teach people
  to skip verification elsewhere. The join command says so out loud, because the
  registration token and the worker's own token are bearer credentials.
- There is **no sandbox**. A worker runs the command as the user it runs as, on
  the machine it is on. That is the same bargain a self-hosted Actions runner or
  a Jenkins node makes, and it is why you put one on a machine you own.
- The registration token is a **bearer token**. Anyone who has it can join a
  machine to the pool. Rotate it from the Workers page when it has been shared
  too widely; machines that already joined hold their own token and keep working.
