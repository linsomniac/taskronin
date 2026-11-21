# Plan F (TUI shape, color and empty states) — execution rulings

Plan: `docs/superpowers/plans/2026-11-20-taskronin-plan-f-tui-joy.md`.
Design: `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J§4.3, §5.2–5.4,
§6, §7, §9). Executed 2026-11-20 after Plan E, with Codex implementers and
Claude reviewers (Sonnet for Task 1, Opus for Tasks 2–4 and the whole-branch
review).

## Rulings

- R-F1 (Task 1): Plan E's reversal banner baked a `✓ ` into the banner text;
  with `bannerOK` adding the mark at render time, that branch now uses
  `bannerOK` and the baked prefix is gone (a test counts exactly one mark).
- Task 2: with color off the active tab keeps its `[1 Today]` bracket form and
  the header separator becomes `Taskronin · <clock>`; everything else in a
  plain frame is byte-identical to before. The selection bar is produced by
  padding sanitized, already-truncated text and re-applying the reverse style
  after every SGR reset a token emits (`renderOver`).
- Task 3: inside the boxed card the slips line reads exactly as the behavior
  catalog (SLP-12) and the plain fallback do, `slipped ×2 lifetime, ×1 since
  last triage`; the plan's ASCII mock had squeezed it to `since triage` and
  was not a wording decision. Box below 20 columns or 6 rows → plain layout.
  Reason times show `HH:MM` when the reason's `since` falls on the server's
  today in the server's zone, else `Mon 2026-11-16`.
- Task 4: the Today quiet-day line prints only when blocks, triage, overdue,
  due soon, entries and interruptions are all empty (same rule as the CLI);
  recap "to review" counts still-planned blocks plus unlinked entries,
  worked counts `worked` and `done`, cancelled blocks are not counted.

- Final fix wave: banner marks are `✓ ` for success and `⚠ ` for errors;
  info banners carry no mark (the plan's mapping governs over the design's
  sentence). TUI list rows keep their compact date format. In recap, plan
  and card modes the short help footer shows the screen's own keys, because
  `a` means "auto outcome" there, not "add". The colored header must fit 80
  columns with all five tabs: one-space separators, then the brand, then the
  clock give way before the tab strip does. A recap whose blocks are all
  cancelled reads `Nothing to review · N cancelled`.

## Review outcomes

No Critical findings. Important findings, all fixed: Task 3's slips wording;
Task 4's quiet-day trigger (the same overdue/due-soon gap the CLI had);
and from the whole-branch review, the add-task banner missing the success
style, the colored header overflowing 80 columns, and the recap footer
advertising `a add` where `a` sets an outcome. Carry-over minors from each
task were folded into the next (renderOver anchor and test,
truncation-inside-token test, detail overlay IDs through `a.styles.ID`,
mixed-kind banners, label/effects stutter, the Log tab's empty line over a
multi-day range, remaining section titles in `Head`).
