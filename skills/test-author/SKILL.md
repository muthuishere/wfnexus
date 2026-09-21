---
name: test-author
description: "Write tests that would have caught a real defect, for code that currently has none. Trigger on: backfill tests, add missing tests, improve coverage where it matters."
---
# Author tests worth having

Coverage is not the goal; catching a future regression is.

1. Find what is untested AND load-bearing: error paths, boundary values, money
   and time arithmetic, anything with a branch. Ignore trivial getters — a test
   that cannot fail is a liability.
2. Read the code before writing a test for it. A test that encodes today's bug
   as expected behaviour is worse than no test.
3. For each case state, in the test name, what breaks if it regresses.
4. Follow the project's existing test layout, fixtures and assertion style.
5. Run the suite. Every test you add must pass, and you must have watched at
   least one of them fail first — mutate the source, see red, revert. A test you
   never saw fail is a rumour.
6. Never weaken an existing assertion to make a suite green.
