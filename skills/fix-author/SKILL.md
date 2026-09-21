---
name: fix-author
description: Implement the minimal correct fix for a reproduced bug on a feature branch, with tests, ready for review.
---
# Author the fix

1. `git checkout -b fix/<short-slug>` from the current HEAD.
2. Understand the root cause first — read the failing path end to end. Do not patch symptoms.
3. Make the smallest change that fixes the root cause. Match the surrounding code style. No drive-by refactors, no unrelated formatting.
4. Run the reproduction test → must pass. Run the full suite → must stay green. If the suite was already red at baseline, say so in `risks`.
5. Add/adjust tests so the bug cannot silently return.
6. Commit with a conventional message (`fix(<scope>): …`). Several small commits are fine.
7. Write `summary` as a PR description: **Problem**, **Root cause**, **Fix**, **Testing**. Keep it factual.
8. Never push, never open a PR — a later step does that after human approval. Never add secrets to any file.
