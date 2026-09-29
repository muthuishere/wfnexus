# eval — the portability matrix

"Build the workflow once, run it on any backend" is only a claim until a table
says so. This package produces that table ([ADR 0019](../../../../docs/adr/0019-evals-are-the-proof-of-portability.md)).

```bash
wfx eval mock-demo --corpus examples/evals/mock-demo.eval.yaml --providers mock,ollama-http,sonnet
```

```
CASE                              mock      ollama-http   sonnet
survey-reports-a-graded-finding   pass      pass          pass
finding-is-not-blank              pass      FAIL 1/1      pass

pass rate                         2/2 100%  1/2 50%       2/2 100%
turns                             4         9             6
tokens                            0/0       18k/900       22k/1.1k
cost                              $0.00     $0.00         $0.09
```

(Illustrative numbers. Run it for yours.)

## Corpus

```yaml
workflow: mock-demo
cases:
  - name: survey-reports-a-graded-finding
    input: {title: "…"}          # the run's input, as `wfx run -i` would give it
    status: awaiting_approval    # expected end status; default done
    assert:
      - {step: survey, path: severity, op: in, value: [low, medium, high]}
      - {step: survey, path: "areas.#", op: gte, value: 2}   # len(areas) >= 2
```

An assertion reads one step's **validated typed output** (`step_runs.output`) —
never prose, never the transcript — so checking it costs a comparison, not an
inference. `path` is dotted (`areas.0.name`); a `#` segment is a length.

| op | passes when |
|---|---|
| `eq` / `ne` | the value equals / does not equal `value` (numbers compare as numbers) |
| `contains` | a string contains the substring, a list contains the element, an object has the key |
| `matches` | a string matches the RE2 regular expression |
| `exists` | the path is present (`value: false` — absent) |
| `gte` / `lte` | a number is ≥ / ≤ `value` |
| `in` | the value is one of the list |

A step with no output fails every assertion except `exists: false`. A case
passes when the run ends in its expected status **and** every assertion passes.

## How it runs

- **Same workflow, different backend.** One engine per provider, with every
  agent step pinned to it (`Engine.UseProvider`); the step's `model:` is dropped
  because it names a model inside the provider it was written for.
- **In this process, on the real engine**, against a throwaway SQLite store: a
  cell means what a production run means, and the samples never enter the run
  history. No server or account is needed.
- **Nothing is approved for you.** A gated workflow parks at
  `awaiting_approval`; say so with `status:` and assert on the steps before it.
- **Sequential**, so wall time is not two backends sharing a CPU.
- **Cost** comes from the per-step usage aggregate (ADR 0020). A price nobody
  set prints `?`, never `$0.00`; `cli`/`acp`/`mock` providers are a known zero.

`wfx dryrun` is free and structural; an eval is a real run and costs real money
on a paid backend. One cell is one sample — how many runs make a trustworthy
cell is ADR 0019's open question.
