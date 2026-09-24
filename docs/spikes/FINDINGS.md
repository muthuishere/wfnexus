# Spike findings — toolnexus golang v0.18.1 against the real OpenRouter wire

Date: 2026-09-21 · module `github.com/muthuishere/toolnexus/golang v0.18.1`
Model for every live call: `anthropic/claude-haiku-4.5` (override with `SPIKE_MODEL`).
Source: `spikes/NN-name/main.go`. Build: `go build -o /tmp/spikeNN ./NN-name/`.

Everything below is **observed output**, not documentation. Where a capability did
not work, it says so.

---

## 01 — Sub-agent teams (`spikes/01-teams/main.go`)

### Observed

```
status               : done
turns (parent)       : 2
TaskResult.TotalTokens: 1667   <- PARENT LLM USAGE ONLY (r.Usage.TotalTokens)
rt.UsageTokens(Root) : 3091   <- WHOLE-TREE rollup (parent + every child)
tool calls (tree)    : [lookup_code task]
child tool called    : 1 time(s)
text                 : The bug in `apply_discount` is that it doesn't convert the percentage to decimal form...

-- runtime trace --
  root/lead.1: spawned (depth 1, tokens 40000)
  root/lead.1: idle→running (wake)
  root/lead.1/explore.1: spawned (depth 2, tokens 40000)
  root/lead.1/explore.1: idle→running (wake)
  root/lead.1/explore.1: running→idle (done, turns=2, tokens=1424)
  root/lead.1/explore.1: →closed (closed)
  root/lead.1: running→idle (done, turns=2, tokens=1667)
  root/lead.1: →closed (closed)

control: solo registry=[solo] team=[] (empty team => no task tool)
control: lead registry=[explore lead] team=[explore] (transitive closure incl. child)
```

### What is proven

- A parent with `Team` **does** get a `task` tool and **does** delegate — the parent
  had *no* tools of its own and still answered correctly, and the metric stream shows
  `task` alongside the child's `lookup_code`.
- The child ran with **only** its scoped tool (`lookup_code` fired exactly once, from
  the child's transcript; the parent could never have called it).
- An agent with no `Team` gets no `task` tool: `runtime.go:1135` adds it only
  `if len(def.Team) > 0`, and `Registry()["solo"].Team` is empty.
- Child transcripts are isolated: the trace shows a separate handle
  `root/lead.1/explore.1`, spawned → woken → closed inside the parent's turn.

### API shape that worked

```go
child := agents.New("explore", agents.Spec{
    Does:  "read-only research: looks up source code by symbol name", // the ROUTING description
    Soul:  "You look up code and report exactly what you found.",
    Tools: []tn.Tool{lookup},
})
lead := agents.New("lead", agents.Spec{
    Does:   "diagnoses bugs by delegating research",
    Soul:   "...",
    Team:   []*agents.Agent{child},
    Budget: &agents.Budget{MaxTokens: 40000, MaxTurns: 8},
})
llm := &agents.LLMOptions{BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, APIKey: key, Model: model}
res, rt := lead.Run(agents.Options{LLM: llm, OnMetric: onMetric}, prompt) // (agents.TaskResult, *agents.Runtime)
```

### GOTCHA — `TaskResult.TotalTokens` is **not** the tree total

`runtime.go:execute` returns `TotalTokens: r.Usage.TotalTokens` — **this agent's own
LLM usage only**. The roll-up to ancestors happens in `rollupLocked` into
`Handle.usageTokens`, which is reachable only via `rt.UsageTokens(h)`.

Measured: `res.TotalTokens = 1667`, child = `1424`, `rt.UsageTokens(rt.Root) = 3091`
(= 1667 + 1424). Billing a step off `TaskResult.TotalTokens` under-counts by the
entire sub-agent tree. **Use `rt.UsageTokens(rt.Root)`.**

Second gotcha: `res.Turns` is the parent's turns only, for the same reason.
Third: `Spec.Does` is what the delegating model reads in the `task` tool description —
it is routing copy, not a comment. Budgets are declared per-agent but inherited as a
ceiling; the trace shows the child spawned with the parent's 40000 as its own pool.

---

## 02 — Guardrails (`spikes/02-guardrails/main.go`)

### Observed

```
status            : done
turns             : 3
tool body ran for : [ls -1]   <- the denied command is ABSENT
guardrails fired  : [FIRST]   <- first-deny-wins
final text        :
**Results:**
1. `ls -1` ran successfully and listed files in single-column format.
2. `git push origin main` was denied because git push operations are restricted in this environment.

-- the exact tool messages the model saw --
  {"content":"(simulated) ok: ls -1","role":"tool","tool_call_id":"toolu_bdrk_015JcAe..."}
  {"content":"denied: outward-facing git operations are owner-gated in this workflow","role":"tool","tool_call_id":"toolu_bdrk_0165LdM..."}
```

### What is proven

- The denial reaches the model **as an ordinary `role:"tool"` message**, verbatim
  `"denied: " + reason`. The model read it, reacted, and finished normally —
  no crash, no run failure, status `done`.
- The denied tool's body **never ran** (`executed == ["ls -1"]` only).
- **First-deny-wins is real**: two guardrails matched the same call, only `FIRST`
  recorded a hit — `loop.go:guardedHooks` returns on the first non-empty reason, so
  the second guardrail is never even evaluated for that call.
- An allowed call in the same transcript still runs normally.

### API shape that worked

```go
// agents.Guardrail = func(ev tn.BeforeToolEvent) string   ("" or "allow" ⇒ permit)
rail := func(reason string) agents.Guardrail {
    return func(ev tn.BeforeToolEvent) string {
        cmd, _ := ev.Args["command"].(string)
        if ev.Name == "bash" && strings.Contains(cmd, "git push") { return reason }
        return ""
    }
}
agents.Spec{ Guardrails: []agents.Guardrail{rail(a), rail(b)}, Hooks: &tn.Hooks{...} }
```

The exact denial text the model saw:
`denied: outward-facing git operations are owner-gated in this workflow`
(prefix `denied: ` is hard-coded in `agents/loop.go`, `IsError: true`.)

### GOTCHAs

1. **`Spec.Guardrails` are applied only on the RUNTIME path** (`Agent.Run` /
   `Runtime`, via `Registry()` → `guardedHooks`). `Agent.Loop(clientOpts, tk)` reads
   `Spec.Completion` but **never** `Spec.Guardrails` — a step run through `Loop`
   has no policy at all. If the engine uses `Loop`, it must compile the guardrails
   into `ClientOptions.Hooks.BeforeTool` itself.
2. `Hooks.AfterTool` does **not** fire for a denied call (`client.go:runTool`
   short-circuits before it). Audit the denial from inside the guardrail, not from
   `AfterTool`.
3. `Spec.Hooks` is **replaced, never merged** with `Options.Hooks`. Guardrails
   compose ahead of `Spec.Hooks.BeforeTool`; `BeforeLLM`/`AfterLLM`/`AfterTool`
   from `Spec.Hooks` survive untouched (that is how the transcript above was captured).
4. The denial text is model-visible prose. It is the whole UX of a refusal — write
   it for the model, not for a log line.

---

## 03 — Suspension & durable resume (`spikes/03-suspension/main.go`) — THE important one

### A. `tn.Client`, no `WaitFor` → durable halt

```
RunResult.Status   : "pending"
RunResult.Text     : "Which environment should I deploy to?"   <- Text = Request.Prompt
RunResult.Pending  : {
    "id": "pnd-mubfhq68-1",
    "kind": "input",
    "prompt": "Which environment should I deploy to?",
    "data": { "field": "db_password", "format": "string" }
  }
messages persisted : 4 (this []any IS the durable transcript)
tool body executed : 1 time(s)
```

### C. `agents` runtime, no `WaitFor` → durable park (same `Request`, plus a path)

```
TaskResult.Status  : "pending"
TaskResult.Pending : {
    "id": "pnd-mubfhtuh-3", "kind": "input",
    "prompt": "Which environment should I deploy to?",
    "data": { "field": "db_password", "format": "string",
              "path": ["root","worker.1"] }            <- the runtime stamps data.path
  }
handles            : [{ "ID":"root/worker.1", "State":"suspended", "Tokens":656,
                        "Turns":1, "Inbox":0, "PendingKind":"input" }]
```

### B. Client-path resume — `Client.RunWithAnswer`

The documented client resume is **`(*tn.Client).RunWithAnswer`** (`relay.go:373`),
not a replay you build yourself:

```go
func (c *Client) RunWithAnswer(ctx context.Context, tk *Toolkit,
    history []any, pending Request, answer Answer) (RunResult, error)
```

It repairs the halted assistant turn (replacing the halt's placeholder tool_result),
then continues with `resumeNoPrompt` — **no new user turn**.

First attempt, answer carrying a custom key (`Data{"value":"staging"}`):

```
Status   : "done"
Text     : I'm waiting for your response. Which environment would you like to deploy to?
tool body executed total: 1
```

→ **It "succeeded" and the human's answer was silently lost.** The model was fed
`"no result supplied on resume for ask_human"`, marked as an error result, and
politely asked again. This is the single most dangerous behaviour found in these spikes.

Second attempt, the shape that actually works:

```go
ans := tn.Answer{ID: res.Pending.ID, Ok: true, Data: map[string]any{
    tn.RelayOutputKey:  "human answered: staging", // == "output"
    tn.RelayIsErrorKey: false,                     // == "isError"
}}
res2, err := client.RunWithAnswer(ctx, tk, res.Messages, *res.Pending, ans)
```

```
Status: "done"  text: The deployment target is staging.
tool body executed: 1 (0 extra => spliced, not re-run)
stale-answer guard : toolnexus: cannot resume — answer id "bogus" does not echo the pending request id "pnd-mubfhs2j-2"
```

So on the client path the answer is **spliced in as the tool result**; the tool body
is **not** re-executed and `ToolContext.Answer` is **never** consulted. The host must
render the human's answer into the tool's output string itself.
(Multi-call form: `tn.RelayAnswer(requestID, []tn.RelayResult{{ID, Output, IsError}})`.)

### D. Runtime-path resume — `Runtime.Resume(tn.Answer)`

```go
err := rt.Resume(tn.Answer{ID: res.Pending.ID, Ok: true,
    Data: map[string]any{"value": "staging"}})   // arbitrary keys DO work here
```

```
Resume err   : <nil>
handles after: [{ "ID":"root/worker.1", "State":"idle", "Tokens":1999, "Turns":3, "PendingKind":"" }]
tool body executed total: 3  <- >1 means the TURN REPLAYS and the tool RE-RUNS
  trace: root/worker.1: running→suspended DURABLE (pending "input", path preserved)
  trace: root/worker.1: suspended→idle (resume with Answer(ok=true) at checkpoint (turns so far: 1))
  trace: root/worker.1: idle→running (wake)
  trace: root/worker.1: running→idle (done, turns=2, tokens=1343)
```

The two resume paths are **semantically different**:

| | client path (`RunWithAnswer`) | runtime path (`Runtime.Resume`) |
|---|---|---|
| what resumes | the persisted transcript, repaired | the **original prompt**, re-run from scratch |
| tool body | **not** re-run (answer spliced as output) | **re-run**, twice: once re-suspending, once with `ToolContext.Answer` |
| where the answer lands | `Answer.Data["output"]` → tool_result text | `ToolContext.Answer.Data[...]` inside the tool |
| tokens | continues from history | pays for the whole turn again (656 → 1999) |
| returns | `RunResult` | `error` only |

### E. The built-in `question` tool, client path, no `WaitFor`

```
toolkit tools: [question]
Status : "pending"
Pending: {
    "id": "pnd-mubfhwc4-5", "kind": "question",
    "prompt": "Which environment should you deploy to? (options: Development, Staging, Production)",
    "data": { "questions": [ { "question": "Which environment should you deploy to?",
                               "options": ["Development","Staging","Production"] } ] }
  }
```

`Request.Data["questions"]` is exactly the form generator the UI needs — question text
plus options, already structured. `Request.Prompt` is the pre-rendered human string.

### API shape that worked (the suspending tool)

```go
// tn.NativeTool CANNOT do this: its fn is func(ctx, args) (string, error) —
// no access to ToolContext.Answer. Use a raw tn.Tool.
tn.Tool{
    Name: "ask_human", Description: "...", Source: tn.SourceCustom,
    InputSchema: tn.JSONSchema{"type":"object", "properties": ..., "required": []any{"question"}},
    Execute: func(args map[string]any, tc *tn.ToolContext) (tn.ToolResult, error) {
        if tc != nil && tc.Answer != nil {                 // the post-Answer retry
            v, _ := tc.Answer.Data["value"].(string)
            return tn.ToolResult{Output: "human answered: " + v}, nil
        }
        return tn.Pending(tn.Request{Kind: "input", Prompt: q,
            Data: map[string]any{"field": "db_password"}}), nil
    },
}
```

### GOTCHAs our implementation must handle

1. **`Answer.Data` keys are not free-form on the client path.** Only `"output"` /
   `"isError"` (or a `"results"` array via `tn.RelayAnswer`) are read. Anything else
   resumes "successfully" with the tool result
   `no result supplied on resume for <tool>` and the human's answer discarded, with
   **no error**. Our `POST /runs/{id}/answer` must build the Answer with
   `tn.RelayOutputKey`.
2. **`Runtime.Resume` returns only `error`** — there is no resumed `TaskResult`, and
   the child `*Handle` is not exposed from `Agent.Run` (only `rt.List()` HandleViews,
   which carry no text). To read the resumed step's output we must capture it
   out-of-band — from our own `submit_output` tool, or a `Spec.Hooks.AfterLLM`.
3. **`Runtime.Resume` replays the whole turn.** `execute` only calls `store.Save` on
   `done`/`incomplete`, never on `pending`, so the resumed run starts with an empty
   history and re-drives the model from the original prompt. Tools invoked before the
   suspension **run again** — they must be idempotent, and it costs the turn twice.
4. **First suspension wins.** With two concurrent suspending tool calls in one turn,
   only the first (in call order) surfaces; the second's placeholder never enters the
   transcript and it re-suspends on resume (`pending_test.go:TestConcurrentSuspensionsSurfaceFirst`).
   Our UI can therefore only ever show one open question per step at a time.
5. `RunResult.Text` on a pending halt is the **Request prompt**, not model prose.
   Don't store it as the step's answer.
6. `Pending.ID` is generated (`pnd-<base36>-<seq>`) unless we set it. Persist it —
   `RunWithAnswer` rejects a mismatched id (good), and the id is our correlation key.
7. `Request.Data["path"]` (`["root","worker.1"]`) is stamped only on the runtime path.
   It is the portable pointer to *which* agent in the tree is waiting.
8. The client path halt hands back `res.Messages` (4 entries here). **That `[]any` is
   the durable artifact** — persist it verbatim as the step transcript.

---

## 04 — The classifier (`spikes/04-classifier/main.go`)

### (a) `StyleSystemOne` over OpenRouter — **YES, it is reachable**

```
latency: 687ms
  [systemone] model=typesafe/jev-1.13-20260917 calibrated=true usage=in:673/out:77 cost=2.8266e-05
    noul   actionable = 0.920
    choice component  = "pricing" conf=1.00 nearUniform=false probs={checkout=0.00 frontend=0.00 infra=0.00 pricing=1.00}
    score  risk       = 2.76 conf=0.79 probs={0=0.00 1=0.00 2=0.24 3=0.75 4=0.01}
```

`BaseURL: "https://openrouter.ai/api/v1"` + `Model: "typesafe/jev-1.13"` +
`APIKeyEnv: "OPENROUTER_API_KEY"` works. The classifier POSTs to
`{BaseURL}/systemone`; OpenRouter serves that route. **Cost is reported**
(`$0.0000283` per 3-question call) and `Calibrated == true`.

Control against the library default base: `POST https://api.typesafe.ai/v1/systemone:
HTTP 400: {"detail":{"error_type":"api_usage_error","message":"Unknown model: jev-1.13"}}`
— i.e. the model alias `jev-1.13` is an OpenRouter-side name; the defaults
(`api.typesafe.ai` + `jev-latest` + `TYPESAFE_API_KEY`) are a different account path
we do not have. **We go through OpenRouter.**

### (b) `StyleLLM` over the same base with the cheap chat model

```
latency: 2.197s
  [llm] model=anthropic/claude-haiku-4.5 calibrated=false usage=in:572/out:324 cost=nil
    noul   actionable = 0.950
    choice component  = "pricing" conf=0.85 nearUniform=false probs={checkout=0.15 frontend=0.02 infra=0.02 pricing=0.81}
    score  risk       = 2.50 conf=0.75 probs={0=0.02 1=0.15 2=0.65 3=0.15 4=0.03}
```

3.2× slower, `Calibrated == false`, no cost reported (`ClassifierUsage.Cost` is nil,
and **nil ≠ 0** — the library deliberately distinguishes them). Both backends agreed
on `pricing`, but the *risk score* differed materially: **2.76 (systemone) vs 2.50
(llm) vs 1.40 (our recording)**. A threshold tuned on one backend does not transfer.

### (c) `StyleStatic` — no network, confirmed

```
latency: 24.417µs (offline)
  [static] model=jev-1.13 calibrated=true usage=in:411/out:37 cost=nil
unrecorded state -> classifier: static: no recorded decision for this request+state
CanonicalRequest bytes: 1243 (this + state is the static key)
```

24µs and no socket. An unrecorded state **errors**, never guesses at a neighbour.

```go
tn.CreateClassifier(tn.ClassifierOptions{
    Style: tn.StyleStatic, Model: "jev-1.13",
    Decisions: []tn.RecordedDecision{{State: bug, Questions: qs, Response: recordedJSON}},
})
```

`Response` is the raw backend JSON:
`{"model":…,"calibrated":true,"usage":{…},"answers":{"k":{"type":"noul","noul":0.94}, …}}`.

### (d/e) The encoding control

Same question, options described **by consequence** vs **by their own id**:

| backend | option ids | encoding | choice | p(top) | confidence | nearUniform |
|---|---|---|---|---|---|---|
| llm | meaningful (`pricing`…) | by consequence | pricing | 0.81 | 0.85 | false |
| llm | meaningful (`pricing`…) | by id | pricing | 0.78 | 0.78 | false |
| systemone | opaque (`opt_a`…) | by consequence | opt_a | **0.99** | **0.99** | false |
| systemone | opaque (`opt_a`…) | by id | opt_a | **0.74** | **0.65** | false |

**Honest reading:** with *meaningful* ids the by-id encoding barely degrades (0.81 →
0.78) — the id text is itself a description, so the "obligation" is accidentally met.
With *opaque* ids the degradation is large (conf 0.99 → 0.65) but it did **not**
collapse to `nearUniform` at n=4 — the ADR's "ranks at chance" was measured on 17
options, and we did not reproduce a flat distribution at four. The rule still holds
(describe by consequence) but `NearUniform` is **not** a reliable detector of a badly
encoded small choice.

### GOTCHAs

- `NoulQuestion.Criteria` is a `*NoulCriteria` — nil omits the field entirely, and
  `&NoulCriteria{}` sends two empty strings. Different wire, different answer.
- `ScoreQuestion.Criteria []string` — **slice order IS the level numbering** and is
  never sorted. `ScoreAnswer.Legend` echoes the rubric strings back verbatim, so our
  rubric text ends up in the UI; keep each level short.
- `Score` is fractional (2.76 on a 0..4 rubric), not an index.
- `Usage.Cost` is `*float64`: nil means "this backend does not say", not free.
- Static keying is `staticKey(model, state, questions)` — the model string and the
  *exact* state and question set. Any prompt tweak invalidates every recording.
- `Timeout` bounds one request and defaults to 10s; `Retries` defaults to 2.

---

## 05 — Fail-fast and retries (`spikes/05-failfast/main.go`)

### Observed

```
== (a) TimeoutMs: 1ms whole-run deadline against the live wire ==
elapsed : 1ms     attempts: 1
err     : context deadline exceeded
result  : status="" text=""

== (b) bad model name -> HTTP 400: must NOT be retried ==
elapsed : 47ms    attempts: 1  (1 == fail-fast)
err     : LLM 400: {"error":{"message":"acme/definitely-not-a-model-v9 is not a valid model ID","code":400},"user_id":"user_2Zef…"}

== (c) 503 then 200 from an httptest origin: MUST be retried ==
elapsed : 259ms   origin hits: 3
status  : "done" text="hi" err=<nil>

-- (c2) permanent 503: Retries=2 --
elapsed : 159ms   origin hits: 3      err: LLM 503: {"error":"always down"}

-- (c3) permanent 400 from the same origin --
elapsed : 0s      origin hits: 1      err: LLM 400: {"error":{"message":"no such model"}}
```

### What is proven

- `TimeoutMs: 1` aborts **loudly**: a non-nil `error` (`context deadline exceeded`)
  and a **zero-value** `RunResult` — not a silent empty `done`.
- A 400 is **not** retried: 1 HTTP attempt, 47ms, with `Retries: 4, RetryBaseMs: 400`
  configured (a retried 400 would have cost >6s). Confirmed offline too (c3: 1 hit).
- 429/5xx **is** retried: two 503s then a 200 → 3 origin hits, `done`. With a
  permanent 503 and `Retries: 2` the origin saw `1 + 2 = 3` hits and the run then
  failed loudly with the status and body.

### API shape

```go
tn.CreateClient(tn.ClientOptions{
    BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, Model: m, APIKey: k,
    Retries: 3, RetryBaseMs: 50, TimeoutMs: 60000,
    RetryableStatuses: []int{520, 521},          // ADDs to 429/500/502/503/504/529, cannot remove
    OnError: func(i tn.ErrorInfo) tn.Tier { … }, // final say per attempt
    HTTPClient: &http.Client{Transport: myCountingTransport},
})
```

### GOTCHAs

- `Retries` is the number of **extra** attempts: `Retries: N` ⇒ up to `N+1` HTTP calls.
- On a timeout/HTTP error the `RunResult` is the **zero value** — `Status == ""`,
  not `"error"`. Only `TaskResult` (runtime path) normalises that to
  `Status: "error"`. Branch on `err != nil` first on the client path.
- The error text is `LLM <status>: <raw body>` — the provider body is echoed
  verbatim. OpenRouter's 400 body contains the account's `user_id`; scrub before it
  reaches a run event or the UI.
- `TimeoutMs` is the **whole-run** deadline (all turns + all retries), not per request.
  Sizing it per step matters: a 10-turn coding step needs minutes, not seconds.
- `HTTPClient` scope is the LLM path only; MCP transports use their own clients, so a
  per-run proxy/timeout set here does not cover MCP.

---

## 06 — The completion gate (`spikes/06-completion/main.go`)

### (1) Verify fails once, then passes (`MaxAttempts: 3`)

```
Outcome.Status : "done"
Outcome.Attempts: 2
Outcome.Turns  : 4
Outcome.StoppedBy: ""
Result.Limit   : ""
Outcome.Text   :
submissions    : [{"summary":"Checkout returns 500 errors when coupon codes have trailing spaces"},
                  {"summary":"Checkout returns 500 errors when coupon codes have trailing spaces","severity":"high"}]
user turns the model saw:
   [0] Summarise: checkout 500s on coupon codes with a trailing space. Call submit_output with just a summary.
   [1] Your work did not verify: the submission is missing the required `severity` field (low|medium|high). Fix it and finish.
```

The loop **re-ran with the reason fed back verbatim**, and the model fixed the
submission (`severity: "high"` appears only in the second submission).

### (2) Verify never passes (`MaxAttempts: 2`)

```
Outcome.Status : "incomplete"
Outcome.Attempts: 2
Outcome.StoppedBy: "completion.verify failed 2×: the output must also carry a `confidence_interval`, which the schema does not allow"
Result.Limit   : "completion"
Result.Status  : "incomplete"
```

Exactly as specified: `status: "incomplete"`, `Result.Limit == "completion"`, and a
human-readable `StoppedBy` naming the last failure reason.

### API shape that worked

```go
agent := agents.New("reporter", agents.Spec{
    Does: "...", Soul: "...", Tools: []tn.Tool{submitOutput},
    Completion: &agents.Completion{
        Verify:      func(r tn.RunResult) (ok bool, reason string) { ... },
        MaxAttempts: 3, // REQUIRED >= 1
    },
})
loop := agent.Loop(clientOpts, toolkit)              // *agents.Loop
out, err := loop.Run(ctx, prompt, agents.RunOpts{})  // agents.Outcome
// out.Status / out.StoppedBy / out.Attempts / out.Turns / out.Result (tn.RunResult)
```

The re-prompt is a **fixed** string built by `agents/loop.go:runGated`:
`"Your work did not verify: " + reason + ". Fix it and finish."` — we do not control
its wording, only `reason`.

### GOTCHAs

- **On success `Outcome.Text` was empty** (the final model turn was a tool call, so
  there is no assistant prose). Read the accepted object from our own
  `submit_output` tool's recorded args — never from `Text`.
- `Verify` receives `tn.RunResult`, whose `ToolCalls` are **accumulated across
  attempts** (`runGated` appends), so a verifier scanning tool calls must take the
  *last* matching call, not the first.
- `MaxAttempts < 1` or `Verify == nil` is an **error return**, not a panic — but the
  run never starts. Validate the workflow YAML up front.
- A `pending` (suspension) run is **not** re-judged by the gate: `runGated` returns it
  untouched. Good — a question does not burn a completion attempt. But if a *budget*
  stop happens mid-retry, the text is annotated
  `… [while verifying: attempt N last failed: <reason>]`; parse defensively.
- On the runtime path (`Agent.Run`) the structured `Limit` is **lost**: `execute`
  returns `TaskResult{Status:"incomplete", Text: r.Text}` with no `Limit` field on
  `TaskResult` at all. To branch on "completion" vs "maxTurns" we must use the
  `Loop` path, or re-derive it from the text. **`TaskResult` has no `Limit`.**

---

## 07 — Live usage and cost (2026-09-24)

Not a library spike: this one was written into the engine
(`internal/engine/usage.go`, wired at `internal/engine/step.go`) and then run,
because the question ADR 0020 asks — "can the aggregate be written *as it
accrues*" — is a question about our own write path, not toolnexus's.

### (1) A running step now reports progress

One agent step, `provider: haiku`, told to run six `echo`s as six separate tool
calls. `~/.local/share/wfnexus/wfnexus.db` polled once a second **during** the
run (`select status, turns, usage from step_runs`):

```
16:20:03|running|0|{}
16:20:04|running|1|{"completionTokens":70,"costUsd":0.001357,"elapsedMs":1622,"llmCalls":1,"llmMs":1621,
                    "models":{"anthropic/claude-haiku-4.5":{"calls":1,"promptTokens":1007,…}},
                    "pricePerMIn":1,"pricePerMOut":5,"promptTokens":1007,"toolCalls":0,"totalTokens":1077}
16:20:05|running|2|… llmCalls:2 toolCalls:1 promptTokens:2098  costUsd:0.002743
16:20:06|running|3|… llmCalls:3 toolCalls:2 promptTokens:3262  costUsd:0.004202
16:20:07|running|4|… llmCalls:4 toolCalls:3 promptTokens:4499  costUsd:0.005734
16:20:09|running|5|… llmCalls:5 toolCalls:4 promptTokens:5809  costUsd:0.007339
16:20:12|running|6|… llmCalls:6 toolCalls:5 promptTokens:7192  costUsd:0.009017
16:20:14|running|7|… llmCalls:7 toolCalls:6 promptTokens:8648  costUsd:0.010878
16:20:16|done   |8|… llmCalls:8 toolCalls:7 promptTokens:10198 completionTokens:476 costUsd:0.012578
```

`turns` and `usage` move while `status` is still `running`. The behaviour ADR
0020 names as the defect — `turns=0` through 29 calls, then a jump at the end —
is gone: the row is never more than one second behind the work.

### (2) The flush trigger, and the write it costs

**Decision: coalesce on a 1-second deadline, on the metric callback itself.** No
ticker, so no goroutine and no shutdown path; the flush rides the only thing
that can have changed the numbers, and a quiet step stops writing by itself. A
count-of-events trigger was rejected in both directions: at "every 5 events" a
step making one 90-second call writes nothing for 90 seconds, and a step
hammering a local tool writes hundreds of times a second.

The write was **measured, not assumed** — real SQLite store, WAL, the actual
`PatchStep(usage+turns)` with a full aggregate blob:

```
SPIKE sqlite PatchStep(usage+turns): 2000 writes in 41.08ms => 0.021 ms/write (48,684 writes/s)
SPIKE concurrency=1:  500 writes in  10.86ms => 0.022 ms/write
SPIKE concurrency=4: 2000 writes in  47.67ms => 0.024 ms/write
SPIKE concurrency=8: 4000 writes in  98.46ms => 0.025 ms/write
```

**The naive per-event write is NOT too costly** — the honest result, and the
opposite of what was suspected. 21µs per write, and `MaxOpenConns(1)` serialises
8 concurrent writers at 25µs each with no lock errors, so even a very chatty
step could not have moved the needle. The cadence is therefore kept for a
different reason than the one it was proposed for: it bounds the write rate to
1/s *per running step whatever the step does*, which is the rate a human reading
a progress column can perceive anyway, and it keeps the same guarantee when the
store is Postgres over a network, where 21µs is not the number.

Observed in the run above: 8 LLM + 7 tool events produced 8 writes, not 15.

### (3) Cost, and the difference between free and unknown

Price lives on the registry provider entry (ADR 0011/0020) as
`pricePerMIn` / `pricePerMOut`, **pointers** — so three states are distinct.
Both ends of the question were run live, in one workflow, two jobs:

```
free.cli          |done|2|{"costUsd":0,        "pricePerMIn":0,"pricePerMOut":0, "promptTokens":0,
                           "models":{"cli-default":{"calls":2,…}}, "totalTokens":0}
unpriced.nodefault|done|2|{"costUnknown":true,                    "promptTokens":1688,
                           "models":{"anthropic/claude-sonnet-4.5":{"calls":2,…}}, "totalTokens":1787}
```

A `claude-cli` step reports a **true `costUsd: 0`** — the number the ADR calls a
feature — while an unpriced HTTP provider reports `costUnknown: true` and **no
`costUsd` key at all**, so no UI can accidentally render it as `$0.00`.

**The arithmetic agrees with the hand-computed figure.** Fed the 2026-09-24
`code-review` totals — 1,424,154 prompt + 6,541 completion at $3/$15 per
million — the accumulator computes:

```
costUsd = 1424154/1e6*3 + 6541/1e6*15 = 4.272462 + 0.098115 = 4.370577  ($4.37 by hand)
```

pinned in `TestUsageAccumCostMatchesTheHandComputedRun`.

### (4) Backwards compatibility: the jsonb was EXTENDED, no migration

`usage` keeps `totalTokens` at the top level with exactly its old meaning, and
gains `promptTokens`, `completionTokens`, `llmCalls`, `toolCalls`, `elapsedMs`,
`llmMs`, `toolMs`, `models{}`, and `costUsd`/`costUnknown`. **No migration was
written**, in either dialect. The reasons, in order:

- Columns would need a *pair* of migrations (`migrations/NNNN_*.up.sql` and a
  `migrations/sqlite/` counterpart — note the sqlite tree is squashed and its
  version line is its own, so the two numbers do not correspond), for a shape
  that is still moving: `models` is a map, and cache-read / cache-write tokens
  are the obvious next arrivals.
- The column one would most want — cost — is **derived**, not measured. Storing
  it as a column freezes a number that changes when the price does.
- Nothing had to change in `PatchStep`, `StepPatch`, the API or the UI: the
  write-once blob and the live aggregate are the same column, so every existing
  reader keeps working and a new reader gets more.

`turns` stays a column and is now written live from the accumulator's LLM-call
count, with the runtime's own `res.Turns` still winning at the end if it is
larger.

### GOTCHAs

- **The provider must be resolved BEFORE the metric sink is built.** Cost is
  computed as the tokens arrive, so the sink needs the price; `step.go`
  resolves `prov` above `onMetric` now, where it used to sit twenty lines below.
- **The step's default provider can never be priced today.** A step with no
  `provider:` runs on the process-wide default (`WFX_MODEL`), which is **not a
  registry entry**, so it has nowhere to carry a price — that is exactly what
  `unpriced.nodefault` shows above. Any step that wants a cost must name a
  registry provider. This is the practical face of ADR 0020's open question
  about the pricing table's source of truth.
- **The accumulator counts only what the LLM reported per call**, which is
  parent-only in the same way `TaskResult.TotalTokens` is (spikes/01). Where the
  runtime's whole-tree rollup is larger, it wins, and the aggregate records it
  separately as `treeTokens` — so a sub-agent team's spend is visible but the
  per-model breakdown is honestly marked as parent-only.
- The metric sink is called from the agent's own goroutines (a team has several
  in flight), so the accumulator is mutex-guarded and the store write happens
  outside the lock.


## Implications for the platform

1. **Token accounting must come from `rt.UsageTokens(rt.Root)`**, never
   `TaskResult.TotalTokens` — the latter excludes every sub-agent (measured: 54% of
   the true total).
2. **Pick one execution path per step and know what it costs you.**
   `Agent.Run` (runtime) gives teams, guardrails, budgets and durable parking, but
   loses `Result.Limit` and returns no result from `Resume`.
   `Agent.Loop` gives `Limit`/`StoppedBy` and the `RunResult`, but silently ignores
   `Spec.Guardrails`. We need both behaviours, so the engine must either use the
   runtime and re-derive the limit, or use `Loop` and compile guardrails into
   `ClientOptions.Hooks` itself. **This is an architectural decision to take before
   the engine is written.**
3. **`needs_input` is real and durable on both paths**, but the resume contracts
   differ. The client path (`RunWithAnswer` + persisted `res.Messages`) is the better
   fit for us: it does not re-run tools, does not re-pay for the turn, and returns a
   `RunResult`. Its price is that the human's answer must be rendered into
   `Answer.Data["output"]` by *us*.
4. **Guard the answer shape in code.** A wrong `Answer.Data` key resumes with
   status `done` and the answer thrown away, with no error anywhere. A unit test
   asserting `tn.RelayOutputKey` is used is worth more than a comment.
5. **Every step must be idempotent in effect** if we ever use `Runtime.Resume` —
   it replays the turn from the original prompt with an empty history, so
   pre-suspension tool calls execute a second (and third) time.
6. **The judge tier is affordable and fast**: systemone over OpenRouter, 687ms and
   $0.000028 for three typed questions, calibrated. Use it for `decide:` blocks;
   use `StyleStatic` (24µs, offline) in CI, keyed on model+state+questions.
7. **Never transfer a threshold between backends.** systemone and the chat-model
   `llm` backend disagreed by 0.26 on the same 0..4 risk rubric, and only systemone
   reports `Calibrated: true`.
8. **Describe options by consequence** — but do not rely on `NearUniform` to catch a
   violation on a small option set; it did not fire even when confidence fell from
   0.99 to 0.65.
9. **Retry/timeout behaviour is already correct**; do not wrap it. Set `TimeoutMs`
   per step (whole-run, all turns) and let 400s fail fast. Scrub provider error
   bodies (they contain account identifiers) before persisting a run event.
10. **The UI's question form can be generated directly** from
    `Request.Data["questions"]` (built-in `question`) or our own `Request.Data`
    (custom tool). Only one question can be outstanding per step at a time —
    concurrent suspensions collapse to the first.

---

## 08 — provider adapters (2026-09-24)

Against ADR 0016 ("an adapter is a registry entry, not code"), and its harder
clause: *we do not ship an adapter whose non-interactive invocation we have not
verified by running it.* Everything below is observed output. Two defects, six
backends run for real, and one claim that turned out to be false.

### (0) Two defects, verified before they were changed

**(a) `kind` was decided by `command`, not by `kind`.** `localAgent`
(`internal/engine/provider.go`) tested `len(p.Command) > 0` before
`p.Kind == KindACP`. So an `acp` entry that named its binary was built as a
one-shot `CommandAgent` — `bin <prompt>`, one process per turn — and never
spoke ACP at all. The entry's own `kind` was read, validated, and then ignored;
it is exactly the ADR 0004 failure the file's own header comment describes for
`provider:`, one layer down. The regression test
(`TestAnACPEntryWithACommandStillSpeaksACP`) asserts on the concrete type,
which is decidable without starting a process:

```
--- PASS: TestAnACPEntryWithACommandStillSpeaksACP (0.00s)
```

Fixed by testing kind first; an `acp` command is now argv (`Bin` +
`ExtraArgs`), and `validateProvider` gained the mirror-image guard — a
`{{prompt}}` in an **acp** command is refused, because the prompt travels over
the protocol and substituting it would pin turn one's question into the argv of
a process that then serves every later turn.

**(b) The Gemini defect does not exist, and the fix is the opposite one.** The
report was that `style` accepts only `openai`/`anthropic` even though toolnexus
ships `StyleGemini`. It does not. In
`github.com/muthuishere/toolnexus/golang@v0.19.0`:

```
$ grep -rn "Style[A-Z]" client.go
client.go:30:	StyleOpenAI    ClientStyle = "openai"
client.go:31:	StyleAnthropic ClientStyle = "anthropic"

$ grep -rn "Gemini" --include "*.go" . | grep -v _test
toolkit.go:401:// ToGemini returns the Gemini tool schema for all tools.
adapters.go:69:// ToGemini maps tools to the Gemini tool schema (one element wrapping all).
```

`ToGemini` maps **tool schemas**, not a wire format. There is no Gemini
`ClientStyle` and nothing that would speak `generateContent`. The second half
of the report — "`provider.go` only ever sets `tn.StyleOpenAI`" — is also
false: the http branch already passes `tn.ClientStyle(p.Style)` straight
through. `StyleOpenAI` is hard-coded only in `localProvider`, where it is
correct, because that "endpoint" is an in-process round tripper.

So widening the list would have *created* the bug rather than fixed one. The
client tests the style only against `StyleAnthropic` (`client.go:603`, `:737`,
`:1507`) and frames everything else as OpenAI — an unknown style is not
rejected downstream, it is silently mis-framed. The fix is therefore to say
**why** the list is closed and tie it to what the client implements
(`httpStyles` / `HTTPStyles()`), so the next person does not widen it either.
A Gemini model is reached through Google's OpenAI-compatible endpoint with
`style: "openai"` — which is not in `registries.json`, because that endpoint
was not verified in this pass.

```
--- PASS: TestOnlyImplementedWireStylesAreAccepted (0.00s)
--- PASS: TestAnACPCommandIsArgvNotAPromptTemplate (0.00s)
```

### (1) The help output each argv came from

Never guessed. Each is the CLI's own `--help`, trimmed to the lines the argv
rests on.

`codex exec --help` — this is why the prompt is positional and the model flag
is `-m`, not `--model`:

```
Run Codex non-interactively

Usage: codex exec [OPTIONS] [PROMPT]

Arguments:
  [PROMPT]
          Initial instructions for the agent. If not provided as an argument (or if `-` is
          used), instructions are read from stdin. If stdin is piped and a prompt is also
          provided, stdin is appended as a `<stdin>` block
Options:
  -m, --model <MODEL>           Model the agent should use
      --skip-git-repo-check     Allow running Codex outside a Git repository
      --color <COLOR>           [default: auto] [possible values: always, never, auto]
  -o, --output-last-message <FILE>
```

`ollama run --help` — **the important one.** The model is POSITIONAL and there
is no `--model` flag anywhere:

```
Run a model
Usage:
  ollama run MODEL [PROMPT] [flags]
Flags:
      --hidethinking            Hide thinking output (if provided)
      --format string           Response format (e.g. json)
```

`copilot --help` — the phrase that decides the entry is "required for
non-interactive mode":

```
  -p, --prompt <text>       Execute a prompt in non-interactive mode (exits after completion)
      --allow-all-tools     Allow all tools to run automatically without confirmation;
                            required for non-interactive mode (env: COPILOT_ALLOW_ALL)
      --model <model>       Set the AI model to use (use 'auto' to let Copilot pick automatically)
      --log-level <level>   (choices: "none", "error", ...)
      --no-color            Disable all color output
```

`claude --help`:

```
  -p, --print       Print response and exit (useful for pipes).
  --model <model>   Model for the current session.
```

`devin --help` — `acp` is a subcommand, which is what `kind: acp` starts:

```
  acp        Run as an ACP (Agent Client Protocol) server over stdio
  -p, --print [<PROMPT>]    Print response and exit
      --prompt-file <FILE>  Load the initial prompt from a file
```

`opencode run --help` (the worked example, unchanged):

```
opencode run [message..]
Positionals:
  message  message to send
Options:
  -m, --model  model to use in the format of provider/model
```

### (2) The runs — all six, through the shipped argv

`TestLiveRegistryArgvs` (`internal/devinadapter/live_registry_test.go`) is the
registry's argv table executed as the engine builds it, so the entry and its
proof cannot drift. Prompt: *"Reply with exactly this one word and nothing
else: PONG"*.

```
$ DEVINADAPTER_LIVE=1 go test -run TestLiveRegistryArgvs -v ./internal/devinadapter/

ollama-cli  answered in  2.9s: "PONG"     --- PASS
opencode    answered in  3.8s: "PONG"     --- PASS
claude-cli  answered in  4.7s: "PONG"     --- PASS
codex-cli   answered in 12.7s: "PONG"     --- PASS
copilot-cli answered in 18.3s: "PONG"     --- PASS
```

and the ACP kind, on the fixed `localAgent` path:

```
devin acp answered in 14.8s: "PONG"       --- PASS
```

`ollama` is the one that needs no account, no key and no network, so it is the
entry any machine can re-prove. It was already pulled here:

```
$ ollama list
qwen3:4b         359d7dd4bcda    2.5 GB    10 days ago
llama3.1:8b      46e0c10c039e    4.9 GB    10 days ago
qwen2.5:7b       845dbda0ea48    4.7 GB    10 days ago
```

Nothing was pulled for this spike.

**stdout is clean; the noise is on stderr.** Worth recording because
`CommandAgent` reads stdout and only touches stderr on failure, so a CLI that
draws a spinner is harmless — but only once you have checked which stream it
draws on:

```
$ ollama run qwen3:4b "Reply with exactly the word OK and nothing else." --hidethinking >out 2>err
rc=0
--stdout--
OK
--stderr--
^[[?2026h^[[?25l^[[1G⠹ ^[[K^[[?25h^[[?2026l …
```

`codex exec` likewise returned bare `PONG` on stdout with no preamble, so
`--output-last-message` was not needed (and `Provider` has no field for it —
`CommandAgent.OutputFile` is not reachable from the registry, noted as a gap).

### (3) What did NOT work, and the gaps

- **`ollama` cannot honour a step's `model:`.** The generic mechanism appends
  `ModelFlag <model>` at the END of argv, and ollama takes the model
  positionally with no flag to append. The entry therefore bakes `qwen3:4b`
  into `command` and leaves `modelFlag` empty. This is a real limit of the
  generic template: **it assumes the model is a trailing flag.** A step wanting
  to choose an ollama model must use the `ollama-http` entry instead. Named in
  the entry's own `description` rather than left to be discovered.
- **No stdin path.** `CommandAgent.Execute` sets `cmd.Stdout`/`cmd.Stderr` and
  never `cmd.Stdin`, so a CLI that only reads a prompt from stdin cannot be
  driven by a registry entry at all. `codex exec` is the one that offers it
  ("instructions are read from stdin") and it does not need it, since the
  positional form works. Not hit in this pass; recorded because the next CLI
  that has no argv form will hit it.
- **Self-hosted http entries still require a key that does not exist.**
  `resolveLLMWithEnv` refuses any `http` provider whose `apiKeyEnv` is unset.
  ollama, vLLM, LM Studio and llama.cpp need no key. The entries therefore name
  a variable the operator sets to any placeholder, and say so. The honest fix is
  a registry flag meaning "this endpoint is unauthenticated" — not made here.
- **`copilot-cli`'s preset is wrong for automation.** `devinadapter.Copilot()`
  builds `-p {{prompt}} --log-level none --no-color` with no
  `--allow-all-tools`, which copilot's help calls required for non-interactive
  mode. The registry entry was switched from `preset` to an explicit `command`
  — ship what was actually run — rather than editing Go, which is the ADR 0016
  move.

### (4) The whole shipped registry loads with zero skips

23 providers, every one through `catalog.Load` on the real file:

```
providers=23 [anthropic cerebras claude-cli codex-cli copilot-cli deepseek devin
              fireworks gpt groq haiku llamacpp lmstudio mistral ollama-cli
              ollama-http openai opencode openrouter sonnet together vllm xai]
skips: []
```

The `http` base URLs were read off each vendor's own documentation on
2026-09-24 and not one from memory. Two are worth calling out because a
from-memory guess gets them wrong: **Groq requires the `/openai` path segment**
(`https://api.groq.com/openai/v1`), and **Anthropic's is the bare host**, with
`/v1/messages` as the path, under `style: "anthropic"`. `ollama-http` was run:

```
$ curl -s http://localhost:11434/v1/chat/completions -d '{"model":"qwen3:4b",…}'
{"id":"chatcmpl-968","object":"chat.completion","model":"qwen3:4b",
 "choices":[{"index":0,"message":{"role":"assistant",…},"finish_reason":"length"}],
 "usage":{"prompt_tokens":21,"completion_tokens":20,"total_tokens":41}}
```

A real 200 with an OpenAI-shaped body from a local process — which is the whole
enterprise argument in ADR 0016's three-scale test, on one machine.

**Unverified, and marked as such in the entries:** the model ids. They are what
each vendor's page showed on 2026-09-24 and they move fast; the base URLs are
the durable part. LM Studio and llama.cpp document no model id at all — theirs
is whichever model is loaded — so those entries carry the placeholder
`local-model`, which an operator MUST change. Fireworks is the one vendor whose
own compatibility page names `OPENAI_API_KEY` rather than a vendor-specific
variable; `FIREWORKS_API_KEY` appears elsewhere in their docs but was not
confirmed on a fetched page, so the entry ships the name that was.

### (5) Build and tests

```
$ cd apps/api && go build ./... && go test ./internal/catalog/... ./internal/engine/...
ok  	github.com/muthuishere/wfnexus/apps/api/internal/catalog	0.42s
ok  	github.com/muthuishere/wfnexus/apps/api/internal/engine	27.8s
```

### (6) The legal line — what the terms actually say

ADR 0016 makes the `description` the mechanism, so the descriptions had to be
checked rather than written. Vendors' own pages, read 2026-09-24. Only one of
six produced a clause that clearly reads on this use:

**Anthropic — RESTRICTED, verbatim from the Consumer Terms, Prohibited Uses:**

> "Except when you are accessing our Services via an Anthropic API Key or where
> we otherwise explicitly permit it, to access the Services through automated or
> non-human means, whether through a bot, script, or otherwise."

A pipeline exec'ing `claude` is script-driven access on a subscription seat.
The clause's own carve-out is an API key, which is what the `anthropic` http
entry is. `claude-cli`'s description now quotes this.

**GitHub Copilot — UNCLEAR.** Neither the Copilot Product Specific Terms nor
GitHub's AUP has a clause on programmatic use of a seat. The nearest is the
AUP's ban on "excessive automated bulk activity" — a volume ceiling, not a
prohibition. Written conservatively and flagged.

**OpenAI Codex — UNVERIFIED, and this is a real gap.** Every OpenAI policy URL
returned **HTTP 403** to the checking machine (`openai.com/policies/terms-of-use`,
`/service-terms/`, `/usage-policies/`, `chatgpt.com/policies/terms-of-use`,
`help.openai.com`), and no Wayback snapshot came back. No clause was read, so
none is asserted. The widely-repeated "extract Output programmatically" line was
deliberately NOT written into the entry, because repeating a remembered clause
as a finding is the same error as guessing an argv.

**Cognition / Devin — UNCLEAR.** §2.3 restricts competing products and making
the Services available to non-Authorized-Users; nothing either way on the seat
holder's own programmatic access. Their AUP was not fetched.

**opencode — MIT, restricts nothing** — but it is a *client*, so whatever
credential it is configured with carries that provider's terms. Pointing it at a
Claude seat inherits the first clause above. The entry says so.

**ollama — no restriction for local models.** MIT, and local inference touches
no vendor service. ollama.com's own terms do ban automated access to their site
and registry, which reads on `ollama pull`, not on a model already on disk.

Nothing was refused on legal grounds — we redistribute nothing and exec only
what the operator installed — but `claude-cli`, `codex-cli`, `copilot-cli` and
`devin` all now carry a description that names the risk and, where we could not
read the page, says plainly that we could not.

---

## 09 — pause, notify, and who answered (2026-09-24)

Against ADR 0021. Everything below is observed on a real server
(`go build -o /tmp/wfx-server-spike .`, sqlite at
`~/.local/share/wfnexus/wfnexus.db`), with a local Go sink printing what it
received. Two spike workflows: `pause-spike` (a `run:` step behind
`requires_approval` — **no model at all**) and `ask-spike` (one `haiku` step
whose only tool is `ask_human`).

### (1) `ask_human` is an ordinary allowlisted tool

It is now a **platform tool name** (`skills/platform.go`), so it validates
through `MissingBuiltins` like every other name and is granted by
`tools: [ask_human]` — the ADR 0004 allowlist, not a second door. Observed: the
step below declares no boolean anywhere, and the model still got the tool and
used it.

```yaml
  - id: gather
    provider: haiku
    tools: [ask_human]        # <- the whole grant
```

```
sqlite> select step_id,status,pending from step_runs where run_id='a4ecb0d9…';
gather|needs_input|{"id":"9ee4e267-73a1-4b0f-b403-d18aad2fe06e","kind":"input",
  "prompt":"Which environment do you want deployed to?",
  "url":"http://127.0.0.1:18490/runs/a4ecb0d9-dab2-4eb4-9caa-fe5100ab18d9",
  "data":{"step":"gather","why":"I need to know the target environment…"}}
```

`Request.URL` was previously stored empty and is now filled by the engine
(`schedule.go`) — the field the ADR says an answer-here link belongs in.

**The deprecation call: accept both, warn on the run, do not fail the load.**
`workflows/bug-fix.yaml:120` ships with `ask_human: true`, and a loud failure
would break an installed workflow on upgrade — a workflow file the operator did
not write and may not own. The boolean still grants the tool; when a step
arrives with the boolean and *not* the tool name, the engine emits on the run's
own event stream:

```
deprecated: `ask_human: true` on step gather — name `ask_human` in this step's `tools:` instead (ADR 0021)
```

That puts the warning where the person running the workflow will actually see
it, rather than in a changelog. `workflows/*.yaml` was checked: `bug-fix.yaml`
is the ONLY shipped user of the boolean.

### (2) The approver has an identity, and a decline is not a timeout

`Approve` / `Reject` / `AnswerQuestion` take an `engine.Actor{ID, Via}` and
**refuse an empty one** — "approved by nobody" is exactly the audit record the
ADR exists to stop. `AnswerQuestion` now takes a `tn.Answer`, so `Ok` and
`Reason` survive. Migration `000009_step_resolution` (Postgres) with its
`sqlite/000003_step_resolution` pair adds `resolved_by / resolved_at /
resolution / resolution_reason` to `step_runs`.

```
$ curl -XPOST …/runs/a4ff6695…/approve -d '{"stepId":"publish"}'
{"error":"an actor is required: who is resolving this pause?"}

$ curl -XPOST …/runs/a4ff6695…/approve -H 'X-WFX-Actor: muthu@deemwar.com' -d '{"stepId":"publish"}'
{"ok":true}

sqlite> select step_id,status,resolved_by,resolved_at,resolution from step_runs where run_id='a4ff6695…';
prepare|done||||
publish|done|muthu@deemwar.com (via api)|2026-09-24 16:23:13.295343 +0000 UTC|approved|
```

An answer and a decline are now different rows:

```
# answered: {"stepId":"gather","actor":"muthu@deemwar.com","answer":"staging"}
gather|done|muthu@deemwar.com (via api)|2026-09-24 16:24:46.750092 +0000 UTC|answered|{"environment":"staging"}
run: done

# declined: {"stepId":"gather","actor":"oncall@deemwar.com","ok":false,"reason":"declined"}
gather|declined|oncall@deemwar.com (via api)|declined|Which environment do you want deployed to?
run: cancelled | question declined by oncall@deemwar.com (via api)
```

`expired` fails the run, `declined` / `cancelled` cancel it, and the step does
not re-run — nobody answered it. The actor is a CLAIM, not an authenticated
identity: there is no auth yet. The point is the SHAPE. `actorOf()` in
`api.go` is the single place a resolve endpoint learns who is acting; under ADR
0017 it reads the authenticated subject and no caller changes.

**GOTCHA found by running it:** the audit row must be written **after**
`Retry`, because `Retry` → `ResetStepsFrom` blanks the step row — recording
first wipes exactly what you came to keep.

### (3) A notifier fires on pause — and carries a pointer, not a capability

ONE interface, ONE method, ONE adapter (`internal/notify`), declared in
`registries.json` beside providers:

```json
"notifiers": { "local-sink": { "kind": "webhook", "url": "http://127.0.0.1:18420/pause" } }
```

Received by the sink, verbatim:

```
--- POST /pause   content-type: application/json
{"kind":"approval","runId":"a4ff6695-7b5a-47e6-83ec-239699aa1756","workflow":"pause-spike",
 "project":"local","stepId":"publish","prompt":"step publish needs approval before it runs",
 "url":"http://127.0.0.1:18490/runs/a4ff6695-7b5a-47e6-83ec-239699aa1756",
 "at":"2026-09-24T16:23:04.669643Z"}

--- POST /pause   content-type: application/json
{"kind":"input","runId":"a4ecb0d9-dab2-4eb4-9caa-fe5100ab18d9","workflow":"ask-spike",
 "project":"local","stepId":"gather","prompt":"Which environment do you want deployed to?",
 "url":"http://127.0.0.1:18490/runs/a4ecb0d9-dab2-4eb4-9caa-fe5100ab18d9",
 "requestId":"9ee4e267-73a1-4b0f-b403-d18aad2fe06e","at":"2026-09-24T16:23:36.677059Z"}
```

**THE NOTIFICATION IS NOT AN AUTHORIZATION PATH, and that is a property of the
payload, not a policy.** What travels is a POINTER — run id, step id, the
question, and the URL of the page where a person answers. There is no token, no
signature, no one-click resolve link, and no `/approve` endpoint in the body: a
recipient holding this message can do exactly nothing with it except open our
UI and authenticate. If a link in a chat message could resolve the pause, the
approval control would be worth precisely the membership list of a chat
workspace we do not administer, and every person who can post into that room
would become an approver. A `secretEnv` on the entry authenticates **us to the
receiver**, never a person back to us. A test asserts the payload contains no
`token` / `secret` / `signature` / `/approve` / `/answer`.

Also observed and deliberate: a missing notifier is **not** an error (zero
config stays zero config), and delivery happens **after** the run has parked —
`notifier … failed: … (the run is still paused)` goes on the event stream and
the pause stands.

### GOTCHAs

- **The approval gate is implemented TWICE.** `engine.go:641` (sequential path)
  halts before `runOneStep` ever sees the step, so the notify added to
  `schedule.go:161` fired for nothing on the first live run — an observed
  silent no-op, not a theory. Both sites now announce; they should be one.
- **An answer is not automatically visible to the step that asked.** The engine
  merges it into `Input.answers` and re-runs the step from its prompt; a prompt
  that does not render `{{ .Input.answers }}` asks the *same question again*,
  forever. Observed live: haiku re-asked with a fresh `Request.ID` and the run
  parked a second time. Any step naming `ask_human` must render the answers
  block — the engine should do this for it.
- `Request.ExpiresAt` is an RFC3339 **string** on the wire, not a `*time.Time`.
  Parsed defensively; still nothing enforces it (ADR 0021 leaves timeout open).
- `ProvideInput` (the gate's `needs_input` resolve path) still takes no actor.
  Only `Approve`/`Reject`/`AnswerQuestion` were given one.

### The real result

```
$ cd apps/api && go build ./... && go test ./internal/engine/... ./internal/workflow/...
ok  	github.com/muthuishere/wfnexus/apps/api/internal/engine	30.445s
ok  	github.com/muthuishere/wfnexus/apps/api/internal/workflow	1.023s
```

## 10 — a self-hosted model needs no key (2026-09-24)

Two enforcement sites required an API key for EVERY `http` provider, so the four
self-hosted entries — `ollama-http`, `vllm`, `lmstudio`, `llamacpp` — could not
start. `apiKeyEnv` is already `omitempty` in the catalog, so an entry that omits
it is well-formed; requiring one anyway reported

```
NOT READY: provider llamacpp:  is not set
```

naming no variable, because there is none to name. Fixed at both sites
(`engine/provider.go` resolution, `engine/doctor.go` readiness) and the four
entries dropped `apiKeyEnv`.

### Observed — one step against qwen3:4b on this machine, via ollama-http

```
STATUS: done
 step answer done turns=2
 usage: {"llmCalls":2,"promptTokens":740,"completionTokens":907,"totalTokens":1647,
         "toolCalls":1,"elapsedMs":21419,"costUsd":0,"pricePerMIn":0,"pricePerMOut":0,
         "models":{"qwen3:4b":{"calls":2,"promptTokens":740,"completionTokens":907,"ms":21413}}}
 output: {"sentence":"A Git worktree is the directory in a Git repository where
          changes are made and staged for commits."}
```

The typed contract held on a 4B local model — `submit_output` validated and the
step could not finish without it. `costUsd` is a real **0**, not a missing value:
§07's free-vs-unknown distinction, on the path that makes the claim.

## Publishing — a bundle runs on a machine holding none of its skills

Task 11.4 of `publish-from-your-own-agent`. The portability claim asserted with
real code and observed output, not argued.

**The mechanism, stated so it is not mistaken for magic:** a published bundle is
materialised into a *bundle-scoped skill root* — one directory holding exactly
that bundle's carried skills — which is **prepended** to the machine's own
roots. `skills.Load`'s existing first-root-wins then resolves every step's
`skills:` to the carried copy, and a same-named machine skill is recorded in
`Shadowed`. The resolution rule is used, not bypassed: it is *why* bundling
works.

### Observed — publish on this machine, run on a server holding zero skills

```
$ wfx publish workflows/code-review.yaml --version 1.0.0 --tag latest
published code-review@1.0.0
  digest   sha256:2764b00f0dc57cf2623e32e0ce0feae7878026302a29e0b59d84e2e32b80df4d
  skills   2
  by       admin at 2026-09-24T19:47:47.719Z
  skill    change-reviewer      sha256:398cb6d7b2d0c8c129047cf783281c37ff079356bc5479d8532c265dcfab7f6d
  skill    repo-navigator       sha256:ef9ec9b429e9aba55427e283bcaefca634f0d72949a9219e5b2ffe01193cd883
```

The server had booted with **`skill registry: 0 skills`**. The stored manifest
hashes, under `shasum -a 256`, to exactly the bundle digest — content addressing
checked with a tool that knows nothing about this codebase:

```
$ tar tzf .../bundles/2764b00f…/bundle.tar.gz
manifest.json
skills/change-reviewer/SKILL.md
skills/repo-navigator/SKILL.md
workflow.yaml
$ shasum -a 256 .../bundles/2764b00f…/manifest.json
2764b00f0dc57cf2623e32e0ce0feae7878026302a29e0b59d84e2e32b80df4d
```

After `wfx pull`, the server's registry reports the bundle root **first** and
both skills resolving out of it, and the workflow then ran there:

```
$ wfx show 2aed77d5-8ac2-43f3-a0f3-a04b9b57c037
2aed77d5-…  code-review  [running]
 ✓ survey           done               turns=18
 ▸ review           running            turns=8
artifacts:
  survey/final.txt  1.0 KB
```

A step that names `repo-navigator` completed on a host whose only copy of it
came out of the bundle.

### The identical-declarations half, in a test

`TestABundleRunsIdenticallyOnAMachineHoldingNoneOfItsSkills` stands up two
machines with separate skill roots, publishes from the one that has the skill,
installs on the one that has nothing, and compares the dry-run step declarations
(prompt, scoped skills, tools, model, budget) and the per-step output contracts
with `reflect.DeepEqual`. It then plants a **different** skill of the same name
on the receiving machine and asserts the run is unchanged, the carried copy
still wins, and the local one appears in `Shadowed`.

### What publishing refuses, observed

```
$ wfx publish workflows/code-review.yaml --version 1.0.0     # already published
error: code-review@1.0.0 is already published with sha256:2764b00f… (the same
digest — this is a no-op, not a conflict); publish a new version

$ wfx publish planted.yaml --version 2.0.0                   # a pasted key
error: step survey: an `env:` key must be the NAME of an environment variable,
not a value

$ wfx publish missing.yaml --version 3.0.0                   # an unknown skill
error: cannot publish: step skill "no-such-skill-anywhere" does not resolve on
this machine; roots searched: [skills ~/.claude/skills ~/.agents/skills]
```

None of the three echoes the offending value, and after all three the index held
exactly one bundle and the blob store exactly one directory.

## 11 — context compaction (2026-09-25)

The ROADMAP named `agents.Compactor` and a `BeforeLLM` hook. Both are real in the
pinned v0.19.0, with those exact names:

- `agents/compaction.go` — `func Compactor(CompactorOptions) func(context.Context, tn.BeforeLLMEvent) (*tn.LLMOverride, error)`.
  `CompactorOptions{MaxTokens, KeepTail, Summarize, CountTokens, FlushToMemory}`.
  `Summarize func(older []any) (string, error)` is **required**: the library makes
  no model call on the host's behalf, so the host supplies the summarizer.
- `client.go:259` — `Hooks.BeforeLLM`; returning `*tn.LLMOverride{Messages: …}`
  REPLACES the working transcript, and the loop sets `messages = ov.Messages`.
  Also `agents.EstimateTokens([]any) int` — ceil(chars/4) over the JSON.

So it is on toolnexus's side of ADR 0001 and we call it; nothing is reimplemented
here. It does not touch ADR 0005 either: the rewrite is in memory, for the next
model call **inside the same step**. No `resume`, no durability boundary moved.

### What we wired

`budget: { compact_at_tokens: N }` on a step — the existing budget surface
(`workflow.Budget`), not a new one. **0 is the default and means off.** Off by
default because compaction rewrites the transcript the typed contract lives in; a
step that does not name it is byte-identical to before. `engine/compaction.go`
composes the compactor onto the SAME `BeforeLLM` seam the turn-budget warning
already uses (`chainBeforeLLM`, compaction first so the warning is not summarized
away moments after being added), and emits a `log` event on the run each time it
fires. Its summarizer runs on the step's own provider, and its usage lands in the
step accumulator like every other call.

### Observed: the same workload, before and after

`code-review` against this repo, `--base_branch main~3`, provider `haiku`
(anthropic/claude-haiku-4.5 via OpenRouter, 200k context). Identical workflow in
both arms except `compact_at_tokens: 8000`.

**Before** — the step dies at the context limit, exactly as the ROADMAP said:

```
survey  step → failed  LLM 400: This endpoint's maximum context length is 200000
        tokens. However, you requested about 253267 tokens (252383 of text input,
        884 of tool input).
```

**After** — it compacts and keeps going:

```
survey  compacted context at turn 2:  6 messages ~276509 tokens → 2 messages ~1043 tokens
survey  compacted context at turn 9:  20 messages  ~8290 tokens → 2 messages  ~880 tokens
survey  compacted context at turn 13: 24 messages  ~8317 tokens → 2 messages ~1484 tokens
review  compacted context at turn 18: 20 messages  ~8218 tokens → 2 messages ~1787 tokens
review  compacted context at turn 27: 22 messages  ~8372 tokens → 8 messages ~2996 tokens
```

`step_runs.usage` is the accumulator, so the comparison is a query:

```sql
select r.workflow, s.step_id, s.status, s.turns,
       json_extract(s.usage,'$.llmCalls')         calls,
       json_extract(s.usage,'$.promptTokens')     prompt,
       json_extract(s.usage,'$.completionTokens') completion,
       round(json_extract(s.usage,'$.costUsd'),4) cost,
       (s.output is not null)                     submitted
from step_runs s join workflow_runs r on r.id = s.run_id
where r.workflow like 'cr-%' order by r.created_at, s.position;
```

```
arm        step_id  status   turns  calls  prompt  completion  cost    submitted
---------  -------  -------  -----  -----  ------  ----------  ------  ---------
baseline   survey   failed   4      4      6281    344         0.008   0
baseline   review   pending  0                                         0
compacted  survey   done     21     21     161860  7221        0.198   1
compacted  review   done     31     31     211073  7890        0.2505  1
```

The run that compacts costs more *because it does not die*. That is the point:
the baseline's $0.008 buys a failed run and no output, and the $0.45 buys two
typed, schema-validated submissions and a verdict. Per call, compaction is what
holds the transcript flat — the compacted `survey` averages 7.7k prompt tokens
across 21 calls where the baseline's 4th call alone asked for 253k.

The typed contract survived: both steps' `submit_output` was accepted and both
rows have an `output`, across five transcript rewrites. That is the thing worth
checking — the Completion gate and the schema are carried in the messages
compaction rewrites.

### What did not work, and what it cost

**The summarizer has to fit in the same context the step just overflowed.** The
first run with compaction on failed anyway:

```
survey  compaction failed at turn 1 (~276010 tokens), continuing uncompacted:
        LLM 400: … you requested about 280224 tokens …
```

The thing that overflows a step is usually ONE oversized tool result — a 117 KB
`git diff` — and summarizing it means *sending* it, so the summary call blew the
same 200k ceiling and compaction rescued nothing. Fixed by bounding the
transcript handed to the summarizer to the step's own threshold, middle-elided
and saying so in the gap (`elideMiddle`). A failed summary is also non-fatal now:
the turn proceeds uncompacted and the run says so, rather than a rescue mechanism
becoming a new way to die.

A threshold of 40,000 never fired on this workload — per-call context peaked
around 19k when the agent happened not to read the whole diff at once — and the
run completed normally, which is the no-op path behaving as documented. The
demonstration above uses 8,000 to make it fire deterministically.

`ollama-http` (qwen3:4b) was tried first for a zero-cost loop and abandoned: one
turn on the 117 KB diff took 434 seconds, so a 37-turn workload was hours.

**Spent: $1.19** across all arms (sum of `step_runs.usage->costUsd` for `cr-%`).
