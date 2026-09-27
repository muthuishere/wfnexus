# The analyst's brief — one shard, every item accounted for

`python3 .wfx-test-gap/scenarios.py todo <shard>` lists your files and EVERY surface item
in them: id, kind, symbol, line, and whether any existing test ever executes that line
(`[LINE NEVER RUN BY ANY TEST]` is the strongest hint of a gap). The items were enumerated
by code, so the list is complete; your job is to account for each one.

For EACH file, one at a time:

1. Read the whole source file. Then read the tests that exercise it (grep the test dirs for
   its function and class names) and the README section that documents it.

2. Write `.wfx-test-gap/scenarios/F<n>.json` (F<n> is the file id `todo` printed):

   ```json
   {"file": "src/pkg/mod.py",
    "scenarios": [
      {"id": "F3-S01", "items": ["F3-004", "F3-005"], "kind": "error",
       "title": "divide raises ZeroDivisionError on a zero divisor",
       "given_when_then": "given divisor 0, when divide(1, 0), then ZeroDivisionError",
       "expected": "raises ZeroDivisionError (mod.py:42 has no guard)",
       "evidence": "src/pkg/mod.py:42", "status": "gap"},
      {"id": "F3-S02", "items": ["F3-001"], "kind": "untested_public",
       "title": "divide returns the quotient", "given_when_then": "…", "expected": "…",
       "evidence": "src/pkg/mod.py:40", "status": "covered",
       "covered_by": "tests/test_mod.py::TestMod::test_divide"}],
    "not_applicable": [{"item": "F3-009", "why": "re-export of a stdlib name, no behaviour of ours"}]}
   ```

   `kind` is one of: error, boundary, edge, null, untested_public, doc_promise, spec_promise,
   combination, concurrency, async.

   For every item, think through what applies to it:
   - empty input; None / null / undefined; zero; negative; huge; unicode; the wrong type;
   - one element vs many; the first and last element; duplicates;
   - the error path — which exception or rejection, with what message;
   - generators and streams: laziness (nothing runs until consumed), exhaustion,
     re-iteration;
   - async: resolved vs rejected, a non-promise value where a promise is expected;
   - concurrency where the code shares state;
   - every documented promise (README examples, docstrings) — does the code keep it?
   - combinations the API invites (chaining methods; a method after a terminal one).

   Group items into scenarios (one scenario may cover several items) and mark each one:
   - `gap` — no existing test ASSERTS this. `expected` is what the code DOES today (read
     it; never guess), and what the docs promise if that differs — a difference is a bug
     worth saying out loud in `expected`.
   - `covered` — an existing test asserts exactly this. `covered_by` is its real test id
     or function name. A test that merely executes the line without asserting this
     behaviour does NOT cover it. A test that is red at baseline covers nothing.

   An item with no meaningful behaviour to test goes in `not_applicable` with a concrete
   why. `evidence` is always a real `path:line`.

   **A spec file** (its items are `spec_requirement`, `spec_scenario` or `spec_rule`): every
   WHEN/THEN, SHALL/MUST and rule the spec states is a behaviour the code promises. Find the
   code that implements it (grep for the field, flag or function) and read it. A spec
   promise no test pins is a `gap` with kind `spec_promise`: `evidence` is the CODE line that
   implements it when there is one (the spec line otherwise), and `expected` quotes the spec
   and says whether the code keeps it — a spec the code breaks is a bug worth stating. A
   rule that is guidance for a human author rather than behaviour of this code (style
   advice, "aim for 8–20 seconds") is `not_applicable` with that reason.

3. Run `python3 .wfx-test-gap/scenarios.py check F<n>` and fix every problem it prints
   until it says OK. Do not move to the next file while it reports an UNACCOUNTED item.

Write nothing outside `.wfx-test-gap/scenarios/`, and never modify the repository's files.
When every file of your shard checks OK, submit one entry per file.
