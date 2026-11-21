# Plan G (web face) — execution rulings

Plan: `docs/superpowers/plans/2026-11-20-taskronin-plan-g-web-joy.md`.
Design: `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J§4.3, §5.2, §7,
§10). Executed 2026-11-20/21 after Plans E and F, with Codex implementers
and Claude reviewers (Sonnet for the stylesheet and card markup, Opus for the
form restructuring and the whole-branch review).

## Rulings

- Security posture frozen: CSP header, htmx config meta, single script tag,
  no inline styles or handlers, routes, handlers (R1), form field names,
  `data-*` and `aria-*` attributes. The stylesheet is self-contained (a test
  rejects `@import`, `url(`, external references).
- No add form on the Backlog page; its empty state says `Backlog empty. New
  tasks start in Triage.` (design item 17).
- Task page: Tags and Due are fields, not actions; they stay visible below
  the notes, and the top-level "Edit task" disclosure stays where it was.
  `More` holds only actions.
- Chips keep times: `due Fri 2026-11-27 13:00` when a due time is set,
  likewise for deferrals. `Slips L/S` leaves the rows (a red `slipped ×N`
  chip appears only when N > 0) and stays on the task page.
- Decorative marks (`✓` in the flash, `▶` on the focus card) carry
  `aria-hidden="true"`; the status role and section labels already name
  them.

- Final polish wave: button edges use a `--border-strong` token (≥ 3:1
  against the card), every button carries `btn` plus `primary`,
  `secondary` or `danger`, the task page's Complete button precedes its
  options disclosure, the flash shows `✓` only for applied effects
  (`no change` / `already applied` muted, warnings with `⚠ `), Today's
  banners are styled as warn boxes, and touch targets are 2.75rem (2.25rem
  for small top-bar buttons).

## Review outcomes

No Critical or Important findings in any task or in the whole-branch review
(verdict: ship). The whole-branch reviewer computed the palette's contrast
in both themes; every text pair is above 5.9:1. Its twelve minors were
visible polish; six were fixed in one wave (above). Left as is, for Sean:
heading levels on the task page and the title appearing twice, some label
wording, the history page's `<pre>` blocks on narrow phones, and subtest
names containing slashes.
