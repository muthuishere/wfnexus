# devinadapter

Run a local agent CLI — `devin`, `opencode`, `claude`, `codex`, `copilot` — as the
model behind a toolnexus client. No API key, no hosted endpoint: the model is a
process on this machine.

```go
acp := devinadapter.NewACP(devinadapter.ACP{Model: "SWE-1.6 Slow"})
defer acp.Close()

a := devinadapter.New(devinadapter.Options{Agent: acp})
client := toolnexus.CreateInProcessClient(a.InProcessOptions())

res, err := client.Run(ctx, prompt, toolkit)
```

`InProcessOptions()` is a plain value — set `SystemPrompt`, `MaxTurns`, `Hooks`,
`OnMetric` and the rest on it before handing it over.

## Why it is small

toolnexus already owns the hard parts. This package is a `Generate`: one
assembled request in, one assistant message out. The agent loop, skills, MCP
servers, sub-agents, A2A, hooks, guardrails, the completion gate, suspension,
conversation memory and metrics are all toolnexus's, and **none of them needed a
line of code here** — each is verified in `capabilities_test.go`.

What is genuinely this package's problem is two things.

### 1. The command seam

`Agent` is the one interface:

```go
type Agent interface {
    Name() string
    Execute(ctx context.Context, t Turn) (string, error)
}
```

Two implementations ship:

| | what it does | when to use it |
|---|---|---|
| `NewACP(ACP{…})` | one long-lived `devin acp` process, JSON-RPC over stdio | **default** — far faster |
| `Devin(CLI{…})` | a fresh `devin -p` per turn | a CLI with no ACP mode |

`CommandAgent` drives any CLI from an argv template, so `Devin`, `Claude` and
`Copilot` are presets rather than special cases. `AgentFunc` covers anything
that is not a local process (an SSH hop, a queue, a fixture).

### 2. The response contract

A one-shot CLI has no tool-call channel, so it is handed the **verbatim** OpenAI
request inside an `<openai_request>` envelope and asked for the OpenAI response
back. The body is passed through byte for byte, which is why `tool_choice`,
`response_format` and anything toolnexus adds later arrive without this package
learning about them first.

Replies are **validated**. A malformed one goes back to the CLI with the specific
complaint, up to `Options.Repairs` times (default 2), and then the turn
**errors**. It is never massaged into something plausible — these drifts were all
observed live, and each corrupts a run silently if you guess:

- `kind:"answer"` **with** a populated `tool_calls` array.
- The structured payload placed in `content` as an object instead of in `arguments`.
- `arguments` arriving as a JSON-encoded string rather than an object.

## Speed: use ACP

Measured on macOS, `devin` (SWE-1.6 Slow), trivial prompts:

| | latency |
|---|---|
| `devin -p`, 17-byte prompt | 15.3s |
| `devin -p`, 12 KB prompt | 14.3s |
| ACP `session/new` | 3.0s (once) |
| ACP prompt #1 / #2 / #3 | 17.2s / 5.2s / **1.6s** |

Two conclusions. **Prompt size is free** — a 12 KB prompt costs the same as 17
bytes — so the entire per-turn cost is process startup. And a warm ACP session
amortises it: a real 6-turn step went from ~23s/turn to ~12s/turn.

The win is startup amortisation, *not* something intrinsic to ACP. Against
`opencode acp` (~4.7s cold) the gain is much smaller, and against a fake agent it
is nil.

The ACP client is not devin-specific — the same code drives `opencode acp`
unchanged (`TestLiveOpencodeACP`).

## Building: the `toolnexus_inprocess` tag

Every file here carries `//go:build toolnexus_inprocess`, so `go build ./...`
skips the package.

It depends on `toolnexus.InProcessTransport`, which is **not in v0.18.1** — it is
on the `issues-devin-acp` branch ([toolnexus#95]). Without the tag the whole
module would fail to build against the released version.

To work on it you need that branch on disk and a `replace`. Keep the `replace`
in a **`go.work`**, never in `go.mod`: a committed absolute path breaks the build
for every other machine and for CI.

```sh
# once, at the repo root (go.work is gitignored)
go work init ./apps/api
go work edit -replace github.com/muthuishere/toolnexus/golang=/path/to/toolnexus/golang

go test -tags toolnexus_inprocess ./internal/devinadapter/          # 71 tests, offline
go test -tags toolnexus_inprocess -race ./internal/devinadapter/
```

Once a toolnexus release carries `InProcessTransport`, drop the build tag from
every file and the `go.work` replace, and this becomes an ordinary package.

## Tests

- **71 offline** — no network, no CLI. Scripted backends and a **real** fake ACP
  subprocess over real pipes.
- **9 live**, skipped unless `DEVINADAPTER_LIVE=1`:

```sh
DEVINADAPTER_LIVE=1 go test -tags toolnexus_inprocess -run Live -timeout 40m ./internal/devinadapter/
```

`project_test.go` runs this repo's own `validate-bug` step — the six skills in
`/skills` plus the `output_schema` from `workflows/bug-fix.yaml` as a Go struct.

## Known limits

- **No token usage.** The CLI reports none, so `RunResult.Usage` is honest zeros
  rather than an estimate. Anything you bill or budget on is blind.
- **No streaming.** Refused rather than faked — a CLI returns a whole answer, and
  one chunk pretending to be many would pass a streaming assertion while lying.
- **One ACP session is one conversation.** Turns are serialized; concurrent
  prompts would interleave into a single transcript. Run one `ACPAgent` per lane.
- **Model compliance varies.** Keep `Repairs` at its default — on the evidence,
  the contract does get violated, just recoverably.

## Upstream

- [toolnexus#94] — `Retries: 0` could not mean zero. **Fixed**: `Retries: -1`.
- [toolnexus#95] — `InProcessTransport` exported. **Fixed**; `transport.go` went
  from 134 lines to 32.
- [toolnexus#96] — an ACP client in toolnexus. Proposed, with the measurements
  above and gate evidence from this package.
- [toolnexus#97] — a CLI-backed model source.

If #96 lands, most of `acp.go` should be deleted in favour of it.

[toolnexus#94]: https://github.com/muthuishere/toolnexus/issues/94
[toolnexus#95]: https://github.com/muthuishere/toolnexus/issues/95
[toolnexus#96]: https://github.com/muthuishere/toolnexus/issues/96
[toolnexus#97]: https://github.com/muthuishere/toolnexus/issues/97
