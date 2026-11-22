# Plan H (polish residuals) — execution rulings

Plan: `docs/superpowers/plans/2026-11-22-taskronin-plan-h-polish.md`.
Design: `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J§4.4, §4.5, §7,
§9, §10, §12 items 19–25, all dated 2026-11-22). Executed 2026-11-22 with
Codex implementers and Claude reviewers (Opus for Tasks 1–3 and the
whole-branch review, Sonnet for Task 4).

## Scope

The plan closes the residuals that Plans E–G parked. Each parked item was
checked against a live instance first:

- **Dropped, already fixed:** the history page no longer scrolls sideways on
  a phone (`pre { overflow-x: auto; }`; measured at 360px).
- **Dropped, not a defect:** the anchor at `internal/service/op.go:75` is an
  accurate `AIDEV-NOTE` that mentions an old TODO, not an open TODO.
- **Added:** undo and redo brought cancelled time blocks back without a line
  (`tkr undo` after `tkr unblock` printed only `undid unblock (op …)`). The
  README says undo names what it put back, so Task 1 fixes it.

## Rulings

- R-H1: block update events carry only the changed columns, so
  `block_restored` has no span. The line is `b3 restored`.
- R-H2: `tkr outcome bN auto` on a block that re-derives to `planned` also
  prints `bN restored`. Kept: the block did return to the plan (J§4.5 says
  "from any other status"). A service test pins it.
- R-H3: the implied-line sets are built from the response's full effect
  list, so `#1 no longer flagged new` disappears under a `delete` headline
  too, not only under undo.
- R-H4: the `undeleted` line keeps its wording (`#4 undeleted`).
- R-H5: `Slips L/S` on the task page is replaced by the shared sentence and
  shown only when the task has slipped.
- R-H6: once the TUI tab strip is compact, no clock or brand is added back,
  even where it would fit.
- The web AI-changes page keeps listing every raw effect (kind and IDs). It
  is an audit view; J§4.4 names the CLI op output, `tkr ai-changes` and the
  web flash only.
- Final wave: the TUI banner's `unslipped` and `focus_set` lines take the
  J§4 table wording (`b4 no longer counts as slipped`, `focus → #42`); the
  web `due soon` chip is not shown on an overdue task.

## Review outcomes

No Critical or Important finding in any task review or in the whole-branch
review (verdict: ship). The whole-branch reviewer ran a live instance through
add (every flag), delete, unblock, done, outcome, block, reblock, drop, revert
by ID and `ai-changes`, each followed by undo and redo, and confirmed that
`--json` still carries every effect. All minors from the five reviews were
closed in one fix wave.

## Left for Sean

- The CLI ignores no-change results. `tkr undelete N` on a task that is not
  deleted prints `#N 'T' restored` and exits 0 while nothing happened (the
  response has no operation). The web flash already says `no change`. A
  later pass could print a dim `no change` in place of the headline for any
  response without an operation.
- The web AI-changes page shows raw effect kinds where `tkr ai-changes`
  shows sentences.
- Undo of a revert reads `redid add (op …)`. That is the J§4.2 net-direction
  rule working as designed; it can surprise on first read.
- The two questions in J§13 are still answered "no" by default.
