# Taskronin Plan F — TUI shape, color and empty states

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `tkr tui`, `plan`, `recap` and `triage -i` a cared-for shape: one palette, a reverse-video cursor, a boxed triage card, success banners that look like success, summaries that say where you stand, and no blank screens.

**Architecture:** Presentation only. `styles.go` stays the single place that names a color and gains the roles of J§6. `render.go` learns to pad a row to the frame width before styling it. The triage card draws its own box with box-drawing characters (widths measured with `textsafe.Width`) and falls back to the plain layout on tiny frames. Banners gain a success kind. Every empty state and summary string of J§5 and J§7 is set in its screen's `view`. Keys, polling, overlays, forms, CF rules and `cardLegend` are untouched.

**Tech Stack:** Go 1.26 (toolchain go1.27.4), Bubble Tea v2, lipgloss v2 (`charm.land/lipgloss/v2 v2.0.6`), bubbles v2 `help`; existing `newTestApp`/`viewText`/teatest harness.

**Spec:** `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J§3 voice, J§4.3 banner, J§5.2–5.4 summaries, J§6 palette, J§7 empty states, J§9 TUI shape). Plan C rulings in `docs/superpowers/notes/2026-11-15-plan-c-execution-rulings.md` (CF01–CF10) remain binding.

## Global Constraints

- Repo `/home/sean/aix/taskronin`, branch `main`, commit directly on main, never push. Go `1.26.0`, toolchain `go1.27.4`, module `taskronin`, Linux only. No new dependencies. Plan E (CLI) is already merged; its `effectsLine` wording in `banner.go` is not changed here.
- Imports (A§1.3): `tui` imports `client`, `api`, `domain`, `civil`, `datespec`, `textsafe`, `editor`, `config`, Bubble Tea, bubbles, lipgloss. `styles.go` is the only file that names a color or a border character set; other files use `a.styles.<Role>`.
- TUI invariants (Plan C, enforced by existing tests): every rendered line has `lipgloss.Width(line) <= w` and the frame has fewer than `h` newlines; with `Color: false` the output contains no ESC byte; user text goes through `textsafe.SanitizeLine`/`Sanitize` before any style is applied; `view` functions are pure apart from scroll offsets (M2); Cmd closures never touch `*App`; polls never reload overlays (CF02); error-order rule CF03; key bindings and `cardLegend` text are unchanged.
- Meaning is never color-only: the cursor row keeps its `›`, success banners carry `✓ `, warnings `⚠ `, so the `Color: false` frame says the same thing.
- Voice (J§3): sentences, lowercase after the ID mark, no exclamation marks, no new emoji (`✓ ▶ ▷ ◀ ⚠ · → ×` and box-drawing characters only).
- `make check` green before every commit; iterate with `go test -race ./internal/tui`, full `make check` once per task; if the suite times out on the loaded host, re-run the package alone with `-timeout=30m` and say so.
- Anchors: grep `AIDEV-` before editing; never delete an `AIDEV-NOTE`; add anchors at the row padding, the box fallback rule and the banner kinds.
- Commits: conventional (`feat(tui): …`), one per task, Codex trailer `Co-Authored-By: OpenAI Codex <noreply@openai.com>`.

## Review Focus

1. **Padding and borders must respect the frame.** A reverse-video row padded to `w`, and a box at `w = 20`, `h = 6`, must never exceed `w` columns or `h` lines; below those sizes the plain layout returns. Tests: Task 2 `TestRowPaddingFitsWidth`, Task 3 `TestCardBoxSizes`.
2. **Hostile titles inside a box.** Wide glyphs, RTL marks and ESC bytes in a title must not break the right border or widen a line. Test: Task 3 `TestCardBoxHostileTitle`.
3. **No ESC with color off** on every new path (padded rows, box, banners, summaries). Tests: extend `TestCardRender`'s color loop and `TestColorFrame` (app_test.go ~286) to the new screens.
4. **Success and error banners stay distinguishable without color.** Test: Task 1 `TestBannerPrefixesWithoutColor`.
5. **Empty-state lines never hide load failures.** A failed load still shows `… not loaded · r retries`; the empty-state copy appears only after a successful empty load (M1 rule in `TestCardEmptyStateWaitsForSuccessfulLoad`). Test: Task 4 `TestEmptyStatesWaitForLoad`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/tui/styles.go` | palette roles, help styles, box characters | 1 |
| `internal/tui/banner.go`, `app.go` (opMsg → banner; `View`) | `bannerOK` kind, prefixes, banner styling | 1 |
| `internal/tui/render.go`, `keys.go` (`helpView`), `list.go` (`taskRow`), `tab_today.go` (rows) | header, padded cursor row, colored tokens, dim footer | 2 |
| `internal/tui/mode_card.go`, `card_test.go` | boxed card, reason lines, `Triage clear ✓` | 3 |
| `internal/tui/tab_today.go`, `tab_triage.go`, `tab_backlog.go`, `tab_upcoming.go`, `tab_log.go`, `mode_plan.go`, `mode_recap.go` | empty states and summaries | 4 |

---

### Task 1: Palette roles and success banners

**Files:**
- Modify: `internal/tui/styles.go`, `internal/tui/banner.go`, `internal/tui/app.go` (opMsg handling ~line 226, `View` ~line 360, `help.Styles` ~line 102)
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Produces in `styles.go`:
```go
type Styles struct {
	Header, TabOn, TabOff, Selected, Dim, Warn, Err, Info, Current lipgloss.Style // existing
	ID, OK, Bad, Head lipgloss.Style                                               // new roles (J§6)
	Box boxChars                                                                   // "╭─╮│╰╯" when color or not; plain "+-+|++" is NOT used: box characters are not color
}
type boxChars struct{ TL, TR, BL, BR, H, V string }
```
  Colors (ANSI 16 only): `ID` 6, `OK` 2 bold, `Bad` 1, `Head` bold, `Warn` 3 (existing), `Err` 1 (existing), `Info` 6 (existing), `Dim` 8 (existing), `Selected` = `Reverse(true)` (replaces bold+cyan), `TabOn` = `Reverse(true)` + bold, `Current` = 2 bold (existing). With `color == false` every style is `lipgloss.NewStyle()` as today.
  `help.Styles`: when color, `ShortKey`/`FullKey` = `Dim`, `ShortDesc`/`FullDesc` = `Dim`, separators `Dim`; when not, zero styles (as today).
- Produces in `banner.go`: `bannerOK bannerKind` (after `bannerError`). `setBanner(bannerOK, text)` is used by the opMsg success path in `app.go` (where `setBanner(bannerInfo, msg.label)` is today). `View` renders: `bannerOK` → `OK.Render("✓ " + text)`; `bannerInfo` → `Info.Render(text)`; `bannerError` → `Err.Render("⚠ " + text)`. The `✓ `/`⚠ ` prefixes are added at render time, so `a.banner.text` and every existing assertion on it stay unchanged.

- [ ] **Step 1: Tests first** in `app_test.go`:
  - `TestBannerPrefixesWithoutColor`: after a successful mutation (use the fake API as other tests do), `viewText(a)` contains `✓ completed` (or the label of the op used); after `nothing to undo`, contains `⚠ nothing to undo`; no ESC byte anywhere (Color false).
  - Extend `TestColorFrame` (~app_test.go:286) to assert the frame with `Color: true` contains the reverse-video SGR (`\x1b[7m`) on the current tab.
  Run → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test -race ./internal/tui` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(tui): palette roles and success banners"`

---

### Task 2: Header, cursor row and colored tokens

**Files:**
- Modify: `internal/tui/render.go` (`header`, `row`, new `pad`), `internal/tui/keys.go` (`helpView` output through `Dim` when color), `internal/tui/list.go` (`taskRow`), `internal/tui/tab_today.go` (block/triage/log rows and section titles), `internal/tui/tab_log.go` (row), `internal/tui/mode_plan.go` / `mode_recap.go` (block rows)
- Test: `internal/tui/app_test.go`, `today_test.go`, `tabs_test.go`

**Interfaces:**
- `func (a *App) pad(s string, w int) string` — appends spaces so `textsafe.Width(result) == w` (never truncates; callers pass already-fitted text). AIDEV-NOTE: padding happens on sanitized, already-truncated text and before styling, so the reverse bar spans the frame without exceeding it.
- `func (a *App) row(s string, w int, selected bool) string` — selected: `Selected.Render(pad(line("› "+s, w), w))`; unselected: unchanged.
- Header line: `Head.Render("Taskronin · " + clock)` then tabs joined by two spaces: active `TabOn.Render(" 1 Today ")` (spaces inside the reverse block; the brackets `[1 Today]` are dropped **only when color is on**; with color off the bracket form stays so existing tests and screen readers keep the marker). Second status line unchanged.
- Token colors, applied where the token is formatted, never by post-processing:
  - `taskRow` (list.go): `#N` → `ID`; `[reasons]` → `Warn`; `slipped×N` inside reasons → `Bad`; project/`+tags` → `Dim`; `due …` → `Warn`; `overdue …` → `Bad`.
  - Today (tab_today.go): section titles `Blocks`, `Triage N`, `Overdue`, `Due soon`, `Log`, `Interruptions` → `Head`; `bN`/`#N`/`eN` → `ID`; `◀ now` → `OK`; `reblock?` → `Warn`; `missed`/`cancelled` status words → `Bad`/`Dim`; `✓` → `OK`; `{reasons}` → `Warn`.
  - Log tab, plan step-3 block list, recap rows: `bN`/`eN`/`#N` → `ID`; `✓` → `OK`; `worked` → `OK`; `missed` → `Bad`; `slipped` → `Bad`.
  Because `a.line` truncates before styling and styled segments are concatenated, implementers must build each row as plain text, truncate with `a.line`, **then** style segments whose boundaries are known (split on the plain tokens), or style tokens and truncate with `textsafe.Truncate` which is ANSI-aware (check `textsafe.Truncate`'s contract first; if it is not ANSI-aware, truncate first).

- [ ] **Step 1: Tests first**:
  - `TestRowPaddingFitsWidth`: for `w` in {1, 2, 5, 20, 100} and a 300-rune title, `lipgloss.Width(a.row(title, w, true)) == w` and `<= w` for unselected; with Color true the selected row starts with `\x1b[7m`.
  - Extend `TestTodayRender` (Color false): the pinned strings are unchanged; add a Color-true pass asserting no line exceeds 100 columns and that `#42` is wrapped in an SGR sequence.
  - `TestHeaderTabMarker`: Color false → header contains `[1 Today]`; Color true → contains `\x1b[7m` and ` 1 Today `.
  Run → FAIL.
- [ ] **Step 2: Implement.** Plain (Color false) frames must stay byte-identical to today except the header's `·` separator (`Taskronin  Mon …` → `Taskronin · Mon …`); update tests that pin the old header.
- [ ] **Step 3: Run** `go test -race ./internal/tui` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(tui): reverse-video cursor, header and token colors"`

---

### Task 3: The triage card gets a box

**Files:**
- Modify: `internal/tui/mode_card.go` (`view`, `screenView`), `internal/tui/dates.go` or `tab_today.go` helper for "since" formatting
- Test: `internal/tui/card_test.go`

**Interfaces:**
- `func (d *cardDeck) view(a *App, w, h int) string`: when `w >= 20 && h >= 6`, render
```
╭─ Card 2/5 ─────────────────────────╮
│ #42 Set up keycloak postgres cluster│
│ new · since 09:34                   │
│ slipped ×2 lifetime, ×1 since triage│
│ Due: 2026-10-29 13:00               │
│ note line…                          │
╰─────────────────────────────────────╯
k keep · t top · b bottom · p place · d defer · B block · x done · D drop · s skip · A accept new
```
  - Top border carries ` Card n/m ` after `╭─`; the rest is `─` to width; corners/edges from `a.styles.Box`; border characters styled `Dim` when color.
  - Inner width is `w - 4` (border, space, text, space, border); every inner line is `a.line(text, w-4)` then padded with `pad` to `w-4`.
  - Line order inside: title (`#42` via `ID`, title via `Head`), one line per reason `reason · since <when>` where `<when>` is `HH:MM` when `Since` is on `a.clock.Today`, else `Mon 2026-11-16` (reasons styled `Warn`), slips (`Bad`) if any, Due/Defer/Project/Tags/Priority as today, up to 5 note lines (`Dim`), tabs expanded as today.
  - The legend (`cardLegend`, unchanged text) sits below the box in `Dim`; the box shrinks (notes first, then fields) so box + legend fit `h`, exactly as the plain layout reserves the legend today.
  - Below `w >= 20 && h >= 6`, the current plain layout is used unchanged (AIDEV-NOTE at the branch).
  - Empty deck after a successful load: `Triage clear ✓` plus ` · N skipped` only when `d.skipped > 0` (replaces `Triage clear (N skipped)`; the e2e test's `contains("Triage clear")` keeps passing).
- Sanitization: titles, reasons, notes, tags, project, priority already pass through `textsafe` in this file; the box must measure the sanitized, truncated text with `textsafe.Width`.

- [ ] **Step 1: Tests first** in `card_test.go`:
  - Update `TestCardRender` expectations: `"new · since 2026-10-28"` becomes `"new · since 10:00"`? — No: the fixture clock is 2026-10-28 10:00 and `Since` is also 2026-10-28 (same day), so expect `"new · since 10:00"`; keep the ordering assertion (slipped before new); keep the width/height loop (it already covers `{20,5}` and `{1,1}`; add `{20,6}`, `{19,30}`, `{40,6}`).
  - `TestCardBoxSizes`: at `{100,30}` the first line starts with `╭─ Card 1/1 ─` and ends with `╮`, the last body line before the legend starts with `╰` and ends with `╯`, every line has width ≤ w; at `{19,30}` and `{100,5}` no box characters appear.
  - `TestCardBoxHostileTitle`: title `"bad\x1b\u009b‮" + strings.Repeat("宽", 100)`: every line width ≤ w, the right border `│` is the last cell of every inner line, no `\u009b`/`‮`.
  - `TestCardClearSkipped`: deck emptied with 0 skipped → `Triage clear ✓` and no `skipped`; with 2 skipped → `Triage clear ✓ · 2 skipped`.
  Run → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test -race ./internal/tui` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(tui): box the triage card"`

---

### Task 4: Empty states and summaries

**Files:**
- Modify: `internal/tui/tab_today.go`, `tab_triage.go`, `tab_backlog.go`, `tab_upcoming.go`, `tab_log.go`, `mode_plan.go`, `mode_recap.go`
- Test: `internal/tui/today_test.go`, `tabs_test.go`, `plan_test.go`, `log_test.go`, recap tests (find the file with `grep -ln recapMode internal/tui/*_test.go`)

**Interfaces:** binding strings (J§5, J§7); all through `a.line` and, where noted, `Dim`:
- Today: under `Blocks` when there are none → `  none planned today` (Dim). When there are no blocks, `TriageCount == 0`, no entries and no interruptions → after the focus line one line `Nothing scheduled, nothing to triage · a adds · 2 opens triage` (Dim) and the `Blocks`/`Triage 0`/`Log` headings are omitted. The backup line is printed only when `!td.Backup.Healthy` (same text as today).
- Triage tab (`listTab.view` empty text): `Triage clear ✓ — nothing needs a decision`.
- Backlog tab: `Backlog empty — a adds a task`.
- Upcoming tab: `Nothing deferred, nothing due soon`.
- Log tab: `No entries for <logDay(date)> — l logs work` where the date is the tab's selected day (if the tab shows a range, use the range's first day; say which in the report).
- Plan step 2, empty backlog: `Backlog empty — nothing to block`. Plan step 3 first line stays; second line becomes `Plan done · placed N · blocked N · triage clear` or `… · triage left N`.
- Recap: header line unchanged; the second line (today the key legend) becomes the summary `W worked · M missed · R to review` where W counts the date's blocks with status `worked` or `done`, M those with status `missed`, R counts still-`planned` blocks plus the date's unlinked entries; cancelled blocks are not counted. When `R == 0` and `W+M > 0`: `Recap complete · W worked · M missed`. When `W+M+R == 0`: `Nothing to recap for <logDay(date)>`. The key legend line is removed from the body (the keys are already in `recapKeys` and the `?` help).
- Rule: empty-state copy appears only after a successful load; while loading or failed, `loadState.notice` text shows as today.

- [ ] **Step 1: Tests first**: one assertion per string above (`viewText` contains), plus `TestEmptyStatesWaitForLoad` (failed load → `triage not loaded · r retries`, no `Triage clear`), `TestTodayQuietDay` (empty Today fixture → the quiet-day line, no `Blocks` heading, no backup line when healthy, backup line present when unhealthy), `TestRecapSummary` (fixture with 1 worked, 1 done, 1 missed, 1 planned, 1 cancelled, 2 unlinked entries → `2 worked · 1 missed · 3 to review`; with the planned block resolved and the entries linked → `Recap complete · 2 worked · 1 missed`; empty day → `Nothing to recap for …`). Run → FAIL.
- [ ] **Step 2: Implement.** Update the existing tests that pinned `nothing in triage`, `no log entries`, `backlog is empty`, `no blocks or unlinked entries`, `Backup: healthy`.
- [ ] **Step 3: Run** `go test -race ./internal/tui` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(tui): empty states and plan, recap, triage summaries"`
