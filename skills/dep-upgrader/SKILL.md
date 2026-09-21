---
name: dep-upgrader
description: "Upgrade a dependency and repair the breakage it causes, with the suite as the judge. Trigger on: bump a dependency, upgrade a package, fix breakage after an upgrade."
---
# Upgrade a dependency

1. Record the baseline FIRST: current version, and whether the suite is green
   before you touch anything. A suite that was already red is not your failure,
   and you must say so.
2. Read the changelog or release notes between the two versions before editing
   code. Breaking changes are usually documented; guessing from compiler errors
   costs more.
3. Upgrade, then build. Fix only what the upgrade broke — an upgrade is not an
   invitation to refactor.
4. Prefer the migration the library documents over a local shim.
5. Run the full suite. If something cannot be fixed without a behaviour change,
   stop and report it rather than changing behaviour quietly.
6. Report the version delta, every file you touched, and anything you could not
   resolve.
