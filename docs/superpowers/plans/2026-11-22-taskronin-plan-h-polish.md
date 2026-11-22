# Taskronin Plan H — Polish residuals

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the residuals parked by Plans E–G: undo and delete print one line per task instead of three, restored time blocks are reported, web headings and labels are tidied, and the TUI header survives narrow frames.

**Architecture:** One new additive effect kind in `service` (`block_restored`) with a line in each of the three wording mirrors. One shared predicate in `domain` that the CLI and web mirrors use to drop lines a deletion or restore already implies; API and `--json` responses keep every effect. Web changes are template and stylesheet only. The TUI change is confined to `header`.

**Tech Stack:** Go 1.26 (toolchain go1.27.4), cobra, Bubble Tea v2 / lipgloss v2, `html/template`, plain CSS.

**Spec:** `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (the "J-design"): §4 table, §4.4, §4.5, §7, §9, §10, §12 items 19–25. Architecture authority: `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (A§1.3 import rules). Earlier rulings stay binding: `docs/superpowers/notes/2026-11-20-plan-{e,f,g}-execution-rulings.md`.

## Global Constraints

- Repo `/home/sean/aix/taskronin`, branch `main`, commit directly on main, never push. Go `1.26.0`, toolchain `go1.27.4`, module `taskronin`, Linux only. No new dependencies.
- A§1.3 holds: `cli` never imports `service` or `store`; `web` never imports `cli` or `api`; lipgloss only in `internal/cli/paint.go` within `cli`. `internal/archtest` enforces this.
- API and `--json`: the only change is the additive `block_restored` effect. No effect is removed from, renamed in, or reordered in an API response. Line filtering happens in the renderers only.
- With color off, every CLI and TUI byte not named in this plan is unchanged.
- Web security posture is frozen: CSP header, htmx config meta, the single script tag, no `<style>`, no `style=`, no `on*=`, no `hx-on`, no `javascript:`. Every mutating form keeps its hidden fields, `method`, `action`, field names and `hx-confirm` text. Routes and handlers are unchanged. Existing `data-*`, `aria-label` and `aria-current` attributes keep their names and values. `app.css` has no `@import`, no `url(`, no external reference.
- Every string that carries user text is sanitized as today (`textsafe.SanitizeLine` in CLI and TUI, `html/template` auto-escaping on the web). This plan adds no line that carries user text.
- Voice (J§3): no exclamation marks, no new marks or emoji.
- Never edit committed migration files. Never commit secrets.
- Anchors: grep `AIDEV-` in a file before editing it; never delete an `AIDEV-NOTE`; update a note when the code it describes changes; add a note where this plan says so.
- `make check` green before every commit; iterate with package-scoped `go test -race`, run `make check` once per task; re-run a timed-out package alone with `-timeout=30m` and say so.
- Commits: conventional, one per task, Codex trailer `Co-Authored-By: OpenAI Codex <noreply@openai.com>`.

## Review Focus

1. **Filtering is keyed by task.** A response that deletes task A and places task B still prints B's `placed` line. Test: Task 2 `TestImpliedLinesKeyedByTask` (CLI) and the same case in `TestFlashLinesImplied` (web).
2. **Filtering never eats a line that says something.** With a `deleted` effect present, `focus cleared`, `b1 cancelled`, entry lines, and `reason_added`/`deferred` on a restored task all still print. Test: Task 2 table cases.
3. **`block_restored` fires exactly when a block comes back.** Not on `planned → cancelled`, not on `cancelled → missed`, once when both `status` and `deleted_at` change in one event, and from an event that carries only the changed columns. Test: Task 1 rows in `TestEffectsFromEvents`.
4. **No heading or label change drops a form field or an attribute.** Test: existing `TestEveryFormKeepsHiddenFields`, `TestDangerFormsKeepConfirm`, `TestTemplatesLint` stay green; Task 3 adds `TestHeadingLevels`.
5. **The TUI header never exceeds the frame.** For every width 20–120, every active tab, color on and off, the first line's display width is at most the frame width and every tab number is present. Test: Task 4 `TestHeaderNeverOverflows`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/service/effects.go` | `EffBlockRestored`, emitted by `blockEffects` | 1 |
| `internal/cli/effects.go`, `internal/web/flash.go`, `internal/tui/banner.go` | the `b1 restored` line in each mirror | 1 |
| `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` | the `EffectKind` comment list gains `block_restored` | 1 |
| `internal/domain/effect.go` (new) | `EffectImplied` | 2 |
| `internal/cli/effects.go`, `internal/cli/cmd_ai.go`, `internal/web/flash.go` | apply `EffectImplied` when listing lines | 2 |
| `internal/web/templates/*.html`, `internal/web/static/app.css` | headings, meta paragraph, chips, pager links, copy | 3 |
| `internal/tui/render.go` | `header` degradation | 4 |
| `*_test.go` beside each | the tests named per task | 1–4 |

---

### Task 1: Report restored blocks (`block_restored`)

**Files:**
- Modify: `internal/service/effects.go`, `internal/cli/effects.go`, `internal/web/flash.go`, `internal/tui/banner.go`, `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (the `EffectKind` comment list near line 710)
- Test: `internal/service/effects_test.go`, `internal/cli/capture_test.go`, `internal/web/flash_test.go`, `internal/tui/app_test.go`

**Interfaces:**
- Produces: `service.EffBlockRestored EffectKind = "block_restored"`, listed in `service.EffectKinds()` directly after `EffBlockResolved`. The effect carries `TaskID` and `BlockID` like the other block effects, and empty `From`/`To`.
- Emission rule, in `blockEffects(b, a, add)`: emit `EffBlockRestored` once when either holds, and the existing `block_cancelled` branch did not fire: (a) `status` changed and the new status is `domain.BlockPlanned`; (b) `deleted_at` changed and is now null. Update events carry only changed columns; do not read any other column.
- Lines: CLI `formatEffect` → `<blockID> restored` using `p.blockID`; web `effectLine` → `b%d restored`; TUI `effectsLine` → `b%d restored`.

- [ ] **Step 1: Failing service test.** Add rows to the table in `TestEffectsFromEvents` (entity `domain.EntBlock`, action `domain.ActUpdate`, task 7, block 9, matching the neighbors' shape):

| name | before | after | want |
|---|---|---|---|
| `uncancel` | `{"status":"cancelled","cancel_reason":"user"}` | `{"status":"planned","cancel_reason":null}` | `block_restored` |
| `unresolve` | `{"status":"done"}` | `{"status":"planned"}` | `block_restored` |
| `block undelete` | `{"deleted_at":1}` | `{"deleted_at":null}` | `block_restored` |
| `uncancel and undelete` | `{"status":"cancelled","deleted_at":1}` | `{"status":"planned","deleted_at":null}` | exactly one `block_restored` |
| `cancelled to missed` | `{"status":"cancelled"}` | `{"status":"missed","outcome_source":"auto"}` | `block_resolved` with `To: "missed"`, and no `block_restored` |

  The existing `cancel` and `block delete` rows must still expect only `block_cancelled`.
- [ ] **Step 2:** Run `go test -race ./internal/service -run 'TestEffectsFromEvents|TestEffectKindsMatchDeclarations'`. Expected: FAIL (unknown kind / missing effect).
- [ ] **Step 3:** Implement the kind and the emission rule. Update the doc comment on `EffBlocked` only if it is wrong; give `EffBlockRestored` its own comment.
- [ ] **Step 4: Mirror tests.** `TestFlashLinesEveryEffectKind` (web) and the kind walk in `internal/cli/capture_test.go` both iterate `service.EffectKinds()` and will fail until each mirror has the line; add `block_restored` → `b9 restored` (use each table's own block ID) to both, and a `block_restored` case to `TestEffectsLine` in `internal/tui/app_test.go` expecting `b9 restored`. Run them, see them fail, add the three lines, see them pass.
- [ ] **Step 5: End-to-end CLI test.** In `internal/cli/capture_test.go`, next to `TestUndoRedo`, add `TestUndoUnblockReportsRestoredBlock`: add a task, block it tomorrow, `unblock` the block, `undo`. The undo output is exactly two lines: `undid unblock (op <8 hex>)` and `b1 restored`. Then `redo` prints `redid unblock (op <same>)` and `b1 cancelled`.
- [ ] **Step 6:** Add `block_restored` to the `EffectKind` comment list in the architecture spec, after `block_resolved`.
- [ ] **Step 7:** `make check`; commit `feat(service): report restored blocks with a block_restored effect`.

### Task 2: One line for a deleted or restored task

**Files:**
- Create: `internal/domain/effect.go`, `internal/domain/effect_test.go`
- Modify: `internal/cli/effects.go` (`printOp`), `internal/cli/cmd_ai.go` (the effect loop near line 114), `internal/web/flash.go` (`flashLines`)
- Test: `internal/cli/capture_test.go`, `internal/web/flash_test.go`, `internal/service/effects_test.go`

**Interfaces:**
- Produces: `func EffectImplied(kind string, deleted, undeleted bool) bool` in package `domain`. `deleted` and `undeleted` say whether the same response carries a `deleted` / `undeleted` effect for the effect's own task. It returns true for `placed`, `moved`, `updated` when either flag is set, and for `reason_removed`, `undeferred` when `deleted` is set; false for every other kind and whenever both flags are false. Effects with task ID 0 are never implied (callers pass false, false).
- Each mirror builds the two task-ID sets from the full effect list of the response (including effects its headline already covers), then skips a line when `EffectImplied` is true. The CLI applies it in `printOp` and in the `ai-changes` per-op listing; the web in `flashLines`. The TUI is unchanged: `effectsLine` shows none of the implied kinds.
- Add one `AIDEV-NOTE` at the predicate naming J§4.4 and the three call sites.

- [ ] **Step 1: Failing domain test.** `TestEffectImplied`: a table over every kind string in the rule plus `deleted`, `undeleted`, `reason_added`, `deferred`, `due_set`, `focus_cleared`, `block_cancelled`, `entry_deleted`, for the four flag combinations.
- [ ] **Step 2: Kind names cannot drift.** In `internal/service/effects_test.go` add `TestEffectImpliedKindsExist`: every kind for which `domain.EffectImplied(kind, true, true)` is true, found by walking `service.EffectKinds()`, is one of exactly `placed`, `moved`, `updated`, `reason_removed`, `undeferred`.
- [ ] **Step 3: Failing CLI tests** in `internal/cli/capture_test.go` (real server harness, as `TestUndoRedo` uses). Exact output, color off, `<op>` is 8 hex digits:

| Commands | Undo / second command output |
|---|---|
| `add "A"` then `undo` | `undid add (op <op>)`, `#1 deleted` |
| `add "A" --top -t ci` then `undo` | `undid add (op <op>)`, `#1 deleted` |
| `add "A" --now` then `undo` | `undid add (op <op>)`, `#1 deleted`, `focus cleared` |
| `add "A" --block "tomorrow 09:00-10:00"` then `undo` | `undid add (op <op>)`, `b1 cancelled`, `#1 deleted` |
| …then `redo` | `redid add (op <op>)`, `#1 undeleted`, `b1 restored` (line order as the service emits it) |
| `add "A" --defer <a future date>` then `undo`, `redo` | undo: `undid add (op <op>)`, `#1 deleted`; redo: `redid add (op <op>)`, `#1 undeleted`, `#1 deferred to <day>` |
| `add "A"` then `delete 1 -y` | `#1 'A' deleted` and nothing else |
| …then `undo` | `undid delete (op <op>)`, `#1 needs triage (new)`, `#1 undeleted` |

  And `TestImpliedLinesKeyedByTask`: call `printOp` (or the unit seam the neighbors use) with effects `deleted` for task 1, `placed` for task 2, `focus_cleared`; the output keeps `#2 placed` and `focus cleared`.
- [ ] **Step 4: Failing web test** `TestFlashLinesImplied` in `internal/web/flash_test.go`: `flashLines` over `{deleted #4, reason_removed #4 new, placed #4, updated #4, placed #5, focus_cleared}` returns exactly `#4 deleted`, `#5 placed`, `focus cleared`; over `{undeleted #4, placed #4, updated #4, reason_added #4 new}` returns `#4 undeleted`, `#4 needs triage (new)`.
- [ ] **Step 5:** Implement the predicate and the three call sites. Run the tests; expected PASS.
- [ ] **Step 6:** `tkr --json` is untouched: extend `TestUndoJSONCarriesVia` (or add a sibling) to assert that undo of `add --top -t ci` still lists `updated`, `placed` and `deleted` in `effects`.
- [ ] **Step 7:** `make check`; commit `feat(cli,web): a deleted or restored task prints one line`.

### Task 3: Web headings, chips, labels and copy

**Files:**
- Modify: `internal/web/templates/partials.html`, `triage.html`, `backlog.html`, `task.html`, `today.html`, `recap.html`, `search.html`, `aichanges.html`, `history.html`, `log.html`, `internal/web/static/app.css`
- Test: `internal/web/views_test.go`, `internal/web/task_detail_test.go`, `internal/web/ops_test.go`, `internal/web/recap_report_test.go`, `internal/web/forms_test.go`

**Interfaces (exact markup and copy):**
- **Headings.** `taskheading` keeps `<h3 class="card-title">` for cards under a section `h2` (Today, Upcoming, Report). On Triage and Backlog, where cards sit directly under the page `h1`, the card title is `<h2 class="card-title">` with the same `tasklink` and `taskmeta` content; do this with a second partial `taskheading2` (TaskView in, like `taskheading`) and extend the `AIDEV-NOTE` in `partials.html` that lists the display partials. On the task page the card no longer repeats the title: replace `{{template "taskheading" .TaskView}}` with `{{template "taskmeta" .TaskView}}`. Add `h2.card-title, h3.card-title { font-size: 1.125rem; margin-block: 0 0.25rem; }` to `app.css` so both levels render alike; block and entry cards use `<p class="card-title">` and must not change.
- **Meta paragraph.** `taskmeta` emits `<p class="meta">` only when the task has at least one of: a reason, lifetime slips > 0, a due date, a defer date, a tag, a project, a priority. Otherwise it emits nothing.
- **Overdue chip.** `<span class="chip bad">overdue · due {{day .DueDate.V}}{{if .DueMin.Valid}} {{hhmm .DueMin.V}}{{end}}</span>`.
- **Slips.** Chip: `title="slipped ×{{.SlipsLifetime}} lifetime, ×{{.SlipsSinceTriage}} since last triage"` (the visible text stays `slipped ×N`). Task page: replace `<p>Slips L/S: …</p>` with `<p>Slipped ×{{.SlipsLifetime}} lifetime, ×{{.SlipsSinceTriage}} since last triage.</p>`, rendered only when `SlipsLifetime > 0`.
- **Pager links.** Each `{{if .Data.More}}<a href="{{.Data.More}}">More</a>{{end}}` becomes `{{if .Data.More}}<p><a class="btn secondary" href="{{.Data.More}}">LABEL</a></p>{{end}}` with LABEL: Triage and Backlog `More tasks`; Search `More results`; AI changes `More changes`; History `More history`; Log `More entries`. The `href` expression is unchanged. Card disclosures keep the summary `More`.
- **Copy.** Today, empty focus: `No focus. Set one by task ID, or use Focus on a task.` Recap, unlinked: `No unlinked entries on this date.`
- **Test hygiene.** The two `t.Run(tc.page+"/"+…)` names in `forms_test.go` are built without `/` (replace the separator with a space and strip slashes from `tc.page`), so `-run` selects them by one path element.

- [ ] **Step 1: Failing tests.**
  - `TestHeadingLevels` (`views_test.go`): for `/ui/`, `/ui/triage`, `/ui/backlog`, `/ui/upcoming`, `/ui/report`, a task page, its history page and `/ui/search?q=…` with seeded data, walk the headings inside `<main>` in document order: the first is an `h1`, there is exactly one `h1`, and no heading is more than one level deeper than the heading before it. On the task page no heading other than the `h1` contains the task title.
  - `TestTaskMetaOmittedWhenEmpty`: a bare backlog task's card has no `p.meta`; a tagged task's card has one.
  - Update the pinned strings: `views_test.go` overdue chip text is `overdue · due <day>` for the seeded date; `task_detail_test.go` expects no `Slips` paragraph for a task that never slipped and the new sentence for one with slips (use the existing slipped fixture if there is one, else seed one the way `views_test.go` does for the chip); `ops_test.go` and the `views_test.go` empty-state table use the new focus copy; `recap_report_test.go` and the table use the new recap copy.
  - `TestPagerLabels`: on a page seeded past its page size, the pager link is an `a.btn.secondary` with the label for that page, and no element other than a `<summary>` has the exact text `More`.
- [ ] **Step 2:** Run `go test -race ./internal/web`; expected: the new and updated tests FAIL.
- [ ] **Step 3:** Make the template and stylesheet changes above.
- [ ] **Step 4:** Run `go test -race ./internal/web`; expected PASS, including `TestEveryFormKeepsHiddenFields`, `TestDangerFormsKeepConfirm`, `TestTemplatesLint`, `TestCSSSelfContained`, `TestEveryButtonHasClass`, `TestPrimaryActionRows`.
- [ ] **Step 5:** `make check`; commit `feat(web): heading levels, named pagers, clearer chips and copy`.

### Task 4: TUI header on narrow frames

**Files:**
- Modify: `internal/tui/render.go` (`header`)
- Test: `internal/tui/final_display_test.go`

**Interfaces:**
- The first header line is chosen in this order, taking the first that fits in `w` cells (`lipgloss.Width`):
  1. `Taskronin · <Mon 2006-01-02 15:04>` + space + full strip
  2. `<Mon 2006-01-02 15:04>` + space + full strip
  3. `<Mon 15:04>` + space + full strip
  4. full strip alone, no leading space
  5. compact strip alone: inactive tabs are their number only, the active tab keeps its full label and its current styling (`[2 Triage]` with color off, the reverse-video ` 2 Triage ` with color on); one space between tabs.
  If even 5 does not fit, return 5 and let the existing clipping handle it.
- The full strip, the three prefixes at widths ≥ 55, the optional second status line, and every style are unchanged.

- [ ] **Step 1: Failing tests** in `final_display_test.go`:
  - `TestHeaderNeverOverflows`: for color off and on, every width 20–120, every active tab: the first line of `viewText(a)` (ANSI stripped for the text checks, `lipgloss.Width` for the width) is at most `width` cells, and contains each of `1`–`5` as a tab number.
  - `TestHeaderNarrowForms` (color off): width 54 and 45 with Today active → exactly `[1 Today] 2 Triage 3 Backlog 4 Upcoming 5 Log`; width 44 with Today active → `[1 Today] 2 3 4 5`; width 30 with Upcoming active → `1 2 3 [4 Upcoming] 5`. Color on, width 44, Triage active, ANSI stripped → `1  2 Triage  3 4 5`.
  - `TestHeaderFitsTabStrip` keeps passing unchanged.
- [ ] **Step 2:** Run `go test -race ./internal/tui -run 'TestHeader'`; expected FAIL at widths below 55.
- [ ] **Step 3:** Implement the selection in `header`; add an `AIDEV-NOTE` stating the give-way order (brand, date, clock, then tab names) and that J§9 owns it.
- [ ] **Step 4:** Run `go test -race ./internal/tui`; expected PASS.
- [ ] **Step 5:** `make check`; commit `feat(tui): header keeps every tab reachable on narrow frames`.
