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
