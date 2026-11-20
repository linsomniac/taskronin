# Plan E (CLI voice, color and empty states) — execution rulings

Plan: `docs/superpowers/plans/2026-11-20-taskronin-plan-e-cli-joy.md`.
Design: `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J-design).
Executed 2026-11-20 with Codex implementers and Claude reviewers (Sonnet for
mechanical diffs, Opus for wording tables, suppression logic, color and
errors), one review per task plus a whole-branch review at the end.

## Builder-check rulings (before Task 1)

A fresh reviewer read the design and asked eight questions. The answers are
J-design §12 items 11–18; the short form:

- **Reversal effects name the root user op.** `service` walks `RevertsOpID`
  through reversal ops (max 8 hops) and emits `reverted{Via, From=root ID,
  To=root kind}`; `api.Effect` gained `via` (additive JSON). Every surface
  prints `undid complete (op a0888c43)` / `redid …` / `reverted …`.
- **"Today" for day words** is the CLI's cached `/clock` read, fetched once per
  process at format time, falling back to the absolute date on failure.
- **TUI `effectsLine`** adopted the §4 wording for the kinds it shows.
- **`undeferred`** says `defer cleared` and is suppressed under placement
  headlines, because every placement of a deferred task clears the defer.
- **Recap "to review"** counts planned blocks plus unlinked entries (Plan F).
- **The tally** counts today's non-reopened completion entries and is skipped
  for a backdated completion.
- **No add form on the web Backlog page** (Plan G).
- **List rows keep their compact date rule**; day words apply to headlines,
  effect lines, empty states and the triage card.

## Rulings during execution

- R-E1 (superseded by R-E7).
- R-E2/R-E7: on `reverted` effects `--json` gains `via` and `to`, and `from`
  now names the root op instead of the immediate target; no other JSON
  changes.
- R-E3: the completion tally and the post-write clock read run after the
  write with a 2 s deadline (`e.decorTimeout`); a failure prints nothing and
  exits 0; tests set the field to zero to force the omitted case.
- R-E4: Task 5 (color) was forbidden from changing a single plain byte; the
  plain goldens are unchanged across that commit.
- R-E5: `civil` error texts may change if `datespec` cannot otherwise name
  the field; in the end `datespec` carries the field name and `civil` is
  untouched.
- R-E6: web flash effect lines print absolute dates (no clock in the flash
  path); Plan G may revisit.
- R-E8: `defer ID none` prints `#4 defer cleared`.
- R-E9: tally only when the completion entry's work date is the server's
  today.
- R-E10: `taskLine` keeps `due Fri` / ISO beyond a week.
- Task 3: the TUI banner's reversal text omits `(op X)` (brief governs over
  the literal wording of R-E7); `tkr ai-changes` effect lines may use day
  words.
- Task 4: `#3 placed` is visible under undo (J§4 lists `placed` as visible
  only under undo/redo/revert); there is no "triage placement" headline in
  the CLI, so the design's mention of one adds nothing to the list.
- Task 6: the quiet-day line (`Nothing scheduled, nothing to triage — tkr
  plan shapes the day`) prints only when blocks, triage, overdue, due soon,
  entries and interruptions are all empty, so no heading can follow it;
  banners keep their position before the focus line on quiet days.
- Task 7: the service message `defer must be in the future` is unchanged on
  the wire; the CLI appends `(today is …)` in human output only. The
  `block:`/`--block:` context is kept for date errors that do not name the
  block field and dropped only for `block start "25:00": …`-style messages.
  A missing editor prints `editor "nosuch" not found in $PATH`.

## Review outcomes

No Critical findings in any task or in the whole-branch review. Two
Important findings, both fixed: Task 6's quiet-day hint could be followed
directly by a `Due soon` heading (scoped round), and the whole-branch review
found that the reversal root walk dropped chain parity, so undoing a revert
printed `undid log` while the entry came back. `Via` is now the chain's net
direction (`redo` whenever the root op is back in effect; J§4.2). The final
fix wave also aligned the `tkr log`, `unblock` and `outcome` headlines with
the effect vocabulary, gave effect lines the same sanitize fallback as
headlines, put the post-write clock read under the 2 s decorative deadline,
removed the TUI banner's `undone · undid …` stutter, and fixed the revert help
text, an overflow-message separator and the en dash in scheduled spans.
Sanctioned without code: the `reverted` effect's `from` is now the root op's
ID (it was the immediate target), and `tkr report` text gained the
`No work logged.` empty-range line (J§7) despite the plan's "report text
unchanged" constraint. Task 7 got a voluntary follow-up round for coverage gaps
the review listed as minor (token-save failure test, recap clock error test,
block context on date errors, editor message, anchors).

Minors carried forward into later tasks were all closed, except these
residuals, parked on purpose:

- Undo of an `add` prints `#9 updated`, `#9 placed`, `#9 deleted`; the first
  two are implied by the third.
- `datespec` exposes its "names the block field" flag as a public field; a
  method would match the package's style.
- `cmd_token.go` hardcodes `unknown scope name`; `civil` repeats the
  `expected HH:MM` reason literal at three sites.
- `rootOfReversal` performs one extra read for an eventless reversal; the
  loop already skips non-reversal ops.

## Incidental

- This repo's git uses difftastic as `diff.external`; review packages are
  built with `git diff --no-ext-diff` from Task 4 on.
