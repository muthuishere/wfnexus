---
name: doc-syncer
description: "Make docs true to the code again with the smallest possible edit: for each doc statement a machine flagged (a path, symbol, flag or behaviour the code no longer has), prove from the code whether it is really stale, and if so rewrite only that statement, citing the code line that makes the new text true. Never touches history (ADRs, changelogs, proposals). Trigger on: fix stale docs, docs drift, the README is out of date, sync docs with code."
---
# Sync a doc with the code — minimally, and with a citation

You get candidates: doc lines a machine flagged because they name something the
code no longer has, or something a recent commit renamed or deleted. A flag is
a SUSPICION, not a verdict. Most flags are not drift.

## For each candidate, decide first: is it really stale?

It is **not stale** (leave it, say why) when the text:
- describes ANOTHER product, a competitor, an example repo or a hypothetical
  ("Mastra's `createStep()`…", "e.g. `docs/runbooks/`");
- is a generic instruction meant for any repository (a skill telling the reader
  to look for `.ctxoptimize/` or `dist/` in *their* repo);
- is history — a dated decision, a changelog, a "we used to…" paragraph;
- names something that still exists under a path relative to a module
  (`cmd/wfx/main.go` inside `apps/api/`).

It **is stale** when the text states something about THIS repository that the
code contradicts: a renamed tool, flag, function, file or default; a behaviour
the code no longer has. Prove it: find the code that is true now (`git grep`,
read the file), and — when a recent commit caused it — the commit
(`git log -S'<old>' --oneline`).

## The edit

- Change only the words that are wrong. Keep the sentence, the voice, the
  formatting and the line breaks. No rewording of text that was already true,
  no new sections, no "improvements".
- The new text must be provable from ONE place in the code: cite it as
  `path:line` (the line that defines the tool/flag/function/default).
- If the truth is "this no longer exists", delete the claim rather than invent a
  replacement.
- If you cannot find what is true now, do NOT edit — report it as `unsure`.
- Edit the working tree only. Never commit: a separate step re-checks every
  edit against the code and commits only the ones that hold.

## Report per candidate

`stale` (with doc, line, the exact old text, the exact new text, the code
`path:line` that makes it true, the commit that caused the drift if known),
`not_stale` (with the reason from the list above), or `unsure`.
