# Task inputs (modules MVP, #82)

A reusable task — a `tasks/` file, or a workflow with `on: workflow_call` —
can declare a typed interface:

```yaml
name: load-context
on: workflow_call
version: 1
inputs:
  default_policy: { type: string, enum: [draft, approval, auto, none], default: auto }
  out:            { type: string, default: context.json }
outputs:
  context: { type: string, description: the file named by `out` }
steps:
  - id: gather
    run: python3 write_context.py --policy "{{ .Inputs.default_policy }}" --out "{{ .Inputs.out }}"
```

A caller passes values under `inputs:` on the use, and may pin the major:

```yaml
uses:
  - use: load-context@1        # local major pin; `@v1` works too
    as: ctx
    inputs: { default_policy: approval }
    with:                      # unchanged: per-step harness overrides
      "*": { model: small }
```

Rules:

- **`inputs:` holds values, `with:` holds overrides.** `with:` keeps its existing
  meaning (keyed by the task's step id or `*`), so no existing file changes.
- **Types:** `string`, `integer`, `number`, `boolean`, `array`, `object`; plus
  `required`, `default`, `enum`. Declarations are checked when the task loads.
- **Checked at load time.** Unknown input, missing required, wrong type, value
  not in the enum, a pin whose major differs from `version:`, or inputs passed to
  a task that declares none — each is a load error naming the workflow, the job,
  the use and the input, e.g.
  `wf job j: use "greet" (as greet): input "who" is required`.
- **Substitution.** `{{ .Inputs.<name> }}` in a step's `name`, `description`,
  `prompt`, `system`, `run` and `env` values is replaced with the value before
  any run-time render. Strings go in as-is; numbers and booleans as text; arrays
  and objects as JSON. Values are the author's literals, never run-time data.
- **Run on its own,** a workflow that declares inputs gets its defaults; an
  input with no default renders empty.
- `outputs:` is documentation in this MVP; a step's `output_schema` is still the
  enforced contract.
