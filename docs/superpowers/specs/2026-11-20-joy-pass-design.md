# Taskronin joy pass — design

Date: 2026-11-20. Status: approved by default (Sean asked for a direction to
be picked without questions; every choice below is the designer's call and is
listed in §12 for review).

## 1. Intent and why

Sean asked: "Make this a LOT more joyful to use." Taskronin is fast, safe and
correct, but it talks like a transaction log. After a day with the scratch
instance the flat moments are easy to name:

- A write answers with effect codes: `#3 −new`, `#5 updated`, `b1 cancelled ()`,
  `undone (op 6ea20dd3)` / `reverted op bb277cbf` / `#3 placed` / `#3 +new`.
  You have to decode what just happened.
- Finishing a task, the one moment a task manager should notice, prints
  `#2 '…' completed; ✓ logged Fri 2026-11-20` and `focus cleared`. Nothing
  tells you how the day is going.
- Empty screens are blank. `tkr ls` on a fresh instance prints `Triage`, a
  blank line and `Backlog`. `tkr triage` prints nothing. `tkr report --week`
  prints only its header. The TUI Today tab shows `Blocks` over nothing.
- Nothing has color except error lines, which are red. IDs, ✓ marks, due
  dates, reasons and section titles all look the same.
- Errors leak internals: `tkr: block: invalid block start: civil: "25:00" has
  hour out of range`.
- The web UI is browser-default HTML with a 70-line stylesheet. Every task row
  carries seven forms and nine buttons in a column; `Slips L/S: 0/0` is printed
  on every row; the login page is a bare field.

Joy, for a sysadmin who runs this dozens of times a day, is not confetti. It is
a tool that **says what it did in plain words, notices when you finish things,
never leaves you staring at a blank screen, and looks cared for.** Those four
sentences are the design.

## 2. Goals, non-goals, anti-goals

### Goals
- J1 **Plain-words outcomes.** Every write, in the CLI, the web flash and the
  TUI banner, reads as a short sentence a colleague would say. Consequences
  the headline already states are not repeated.
- J2 **Noticing progress.** Completion, a cleared triage, a finished plan and a
  finished recap each get one line that tells you where you stand.
- J3 **No blank screens.** Every empty list, in all three interfaces, says what
  the emptiness means and names the next action.
- J4 **Color and shape, with restraint.** One palette, ANSI 16 colors only,
  used the same way in the CLI and the TUI; a real stylesheet for the web.
  Everything color says, the text also says.
- J5 **Errors that help.** One context prefix at most, no package names, the
  valid form or the next command where there is one.
- J6 **Nothing behind your back.** No new reads that can turn a successful
  write into an error; `--json`, the API, MCP, exit codes and key bindings
  are unchanged.

### Non-goals
- New features: streaks, scores, gamification, reminders, sounds, emoji
  beyond the marks already in use (`✓ ▶ ▷ ◀ ⚠ · → ×`), animation.
- Changes to `tkr report` text (D§14), `tkr status --short` (D§9.1), `--json`
  output, the HTTP API, MCP tools, the behavior catalog, exit codes, key
  bindings, routes, or the web security posture (CSP, no inline styles, no JS
  beyond vendored htmx).
- True-color themes or background detection. ANSI 16 follows the user's
  terminal theme; that is the whole theming story.
- Completing the focus task with a bare `tkr done` (business logic; asked in
  §13).

### Anti-goals (failure even if it "works")
- Copy that is cute on day one and grating on day thirty. The voice is warm
  and dry, never jokey. No exclamation marks.
- Meaning carried only by color. `NO_COLOR` and `--color never` must read the
  same sentences.
- A write that succeeds on the server but exits non-zero because a decorative
  follow-up read failed.
- A script that parses `--json` breaking, or a golden file regenerated without
  a human-readable diff in the report.

## 3. Voice

- Sentences, lowercase after the ID mark, no trailing period on one-liners.
- ID marks stay: `#N` task, `bN` block, `eN` entry, `op xxxxxxxx` (8 hex).
- A date that is today, yesterday or tomorrow is written as that word in
  headlines, effect lines, empty-state lines and the TUI triage card; any
  other date is `Mon 2026-11-23`. List rows (CLI `taskLine`, TUI `taskRow`),
  `tkr report`, `tkr show`, `tkr history` and anything under `--json` keep
  their current date formats.
- Numbers are words only in `one` ("one done today" reads better than "1 done
  today"); otherwise digits.
- Title quoting in headlines stays `'title'` (it survives `NO_COLOR`); with
  color the title is rendered bold and the quotes stay.

## 4. Outcome wording (J1) — CLI `effectLine`, web `effectLine`, TUI banner

The CLI and web mirrors stay two functions (A§1.3 forbids web importing cli
or api) and keep the existing mirror test that walks `service.EffectKinds()`.
Both change to this table. `<day>` is the relative-day rule from §3.

| Kind | Now | New |
|---|---|---|
| `deferred` | `#4 deferred to Mon 2026-11-23` | `#4 deferred to <day>` |
| `undeferred` | `#4 no longer deferred` | `#4 defer cleared`, and **suppressed** whenever the headline is a placement (every placement of a deferred task clears its defer, §4.1) |
| `due_set` | `#4 due Fri 2026-11-27` | `#4 due <day>` |
| `due_cleared` | `#4 due cleared` | `#4 due date cleared` |
| `blocked` | `b1 blocked Sat 2026-11-21 09:00–11:00` | `b1 scheduled <day> 09:00–11:00` |
| `block_moved` | `b1 moved A → B` | unchanged |
| `block_cancelled` | `b1 cancelled (rescheduled)`, `b1 cancelled ()` | `b1 cancelled · rescheduled`; `b1 cancelled` when the reason is empty |
| `block_resolved` | `b1 resolved worked` | `b1 worked` / `b1 missed` / `b1 done` |
| `outcome_set` | `b1 outcome worked` | `b1 marked worked` |
| `slipped` | `b1 slipped` | unchanged |
| `unslipped` | `b1 no longer counts as slipped` | unchanged |
| `reason_added` | `#3 +new` | `#3 needs triage (new)` |
| `reason_removed` | `#3 −new` | `#3 no longer flagged new`, and **suppressed** whenever the headline is a placement (§4.1) |
| `focus_set` | `focus → #2` | unchanged |
| `focus_cleared` | `focus cleared` | unchanged |
| `interrupted` | `interrupted #1` | unchanged |
| `retitled` | `#2 retitled 'a' → 'b'` | unchanged |
| `entry_*` | `e3 logged` … | unchanged |
| `reviewed` | `AI changes reviewed through #9` | unchanged |
| `reverted` | `reverted op bb277cbf` | `undid complete (op bb277cbf)` / `redid …` / `reverted …` by `Via` — see §4.2 |
| `created` | `#5 created` | `#5 created` (only ever visible under undo/redo) |
| `updated` | `#5 updated` | **suppressed** when the same response's headline is an `added …` headline (the metadata is folded into that headline, §4.1); otherwise `#5 updated` |
| `placed`, `moved` | `#3 placed` | `#3 placed`, `#3 moved` (visible only under undo/redo/revert, and never for a task the same response deletes or restores, §4.4) |
| `block_restored` | (new kind, §4.5) | `b1 restored` |
| `deleted`, `undeleted`, `reopened`, `dropped`, `completed` | `#3 deleted` … | unchanged |

### 4.1 CLI headlines

| Command | Now | New |
|---|---|---|
| `add` (triage) | `#1 'T' added to triage (new)` | unchanged |
| `add --top` with `-t ci -P infra.ci` | `#5 'T' added to backlog (top)` + `#5 updated` | `#5 'T' added to backlog (top) · infra.ci +ci` and no `updated` line. Metadata order: project, tags (`+tag`), priority (`prio H`). |
| `add --defer mon` | `#4 'T' added, deferred to Mon 2026-11-23` | `#4 'T' added, deferred to <day>` |
| `done` | `#2 'T' completed; ✓ logged Fri 2026-11-20` + `focus cleared` | `✓ #2 'T' done · logged <day>`; then the usual consequence lines (`focus cleared`); then the tally line of §5.1 |
| `done --as` | as now with retitle effect | `✓ #2 'New title' done · logged <day>` + `#2 retitled 'old' → 'new'` (unchanged effect) |
| `keep` | `#3 kept` + `#3 −new` | `#3 kept in place` (reason line suppressed) |
| `move`, `block`, `defer`, `drop`, `done` on a triage task | headline + `#N −reason` lines | headline only; the placement implies the reasons cleared |
| `undo` / `redo` / `revert` | `undone (op <new>)` + `reverted op <old>` + effects | the `reverted` effect's own line (§4.2: `undid complete (op <root>)`, `redid …`, `reverted …`) + the remaining effect lines. The new op's ID is not printed; `tkr history` has it. |
| `defer ID none` | `#4 no longer deferred` | `#4 defer cleared` |
| `block` | `b1 Sat 2026-11-21 09:00–11:00 #1 'T'` + `#1 −new` | `b1 <day> 09:00–11:00 #1 'T'` |

"Placement headlines" for the suppression rule are exactly the headlines of
`add`, `keep`, `move`, `defer`, `block`, `reblock`, `done`, `drop`, `triage`
placements and `accept-new`; the implementer keeps the list in one function
next to `effectInHeadline`. Under a placement headline both `reason_removed`
and `undeferred` lines for the placed task are suppressed; `reason_added` and
`deferred` never are.

### 4.2 The reverted effect names what it reverted

`service` fills the `reverted` effect from the **root** of the reversal chain:
starting at `RevertsOpID`, it follows `RevertsOpID` while the target is itself
a reversal (`Via != ""`, at most 8 hops) and stops at the first user op. The
effect then carries `From` = that root op's 8-char ID, `To` = that root op's
kind, and a new `Via` field = the **net direction** of the chain: with an odd
number of reversal ops between the user and the root (a plain undo, a plain
revert) `Via` is the reversing op's own via (`undo` or `revert`); with an even
number (redo of an undo, undo of a revert, revert of an undo, revert of a
revert) the root op is back in effect and `Via` is `redo`. `Via` is added to
`service.Effect` and `api.Effect`
(`json:"via,omitempty"`); it is the one additive API change of this pass. The
lookup happens at read time (ops are never deleted), so history, replays and
live responses all carry it; no migration. Rendering, in every mirror:

| Via | line |
|---|---|
| `undo` | `undid <verb> (op <From>)` |
| `redo` | `redid <verb> (op <From>)` |
| `revert` | `reverted <verb> (op <From>)` |
| any, `To` empty (lookup failed) | `undone (op <From>)` / `redone (op <From>)` / `reverted op <From>` |

So `tkr redo` after undoing a completion prints `redid complete (op a0888c43)`
where `a0888c43` is the original completion. The CLI headline for undo, redo
and revert *is* this line, so it is never repeated below the headline. The
verb table:

| op kind | verb |
|---|---|
| task.add | add |
| task.update | edit |
| task.due | due |
| task.place | place |
| task.accept_new | accept new |
| task.defer | defer |
| task.manual | send to triage |
| task.complete | complete |
| task.reopen | reopen |
| task.drop | drop |
| task.delete | delete |
| task.undelete | undelete |
| block.create | block |
| block.reblock | reblock |
| block.unblock | unblock |
| block.outcome | outcome |
| entry.add | log |
| entry.update | edit entry |
| entry.relink | relink entry |
| entry.delete | delete entry |
| entry.undelete | restore entry |
| focus.set | focus |
| anything else | the raw kind |

`op.revert` itself never reaches the table: the chain walk skips reversal ops.

### 4.3 Web flash and TUI banner

The web flash shows the same lines as the CLI would print (headline is not
available there; the effect lines are). Flash lines render in a green banner
with a leading `✓` for a successful op and `no change` / `already applied`
unchanged. The TUI has a third, partial mirror (`effectsLine` in
`internal/tui/banner.go`, which shows only the kinds worth a one-line banner);
the kinds it shows take the same wording as the table (`reason_added` →
`#3 needs triage (new)`, `reverted` → `undid complete`), and it gets the
success style (§6) with a leading `✓`. All three mirrors change in the same
task so they cannot drift.

Effect `To`/`From` values that carry a moment are the raw strings the server
sends (`2026-10-30 09:00`, `2026-10-28 13:00-15:00`); the relative-day rule
is applied by parsing the leading `YYYY-MM-DD` and leaving anything that does
not parse untouched. "Today" for that rule comes from the CLI's cached
`/clock` read; when no clock has been read in this process the CLI reads it
once, and if that read fails the absolute form is printed (never an error:
the write already succeeded).

### 4.4 Lines implied by a deletion or a restore (added 2026-11-22)

`tkr undo` after `tkr add --top -t ci` used to print `#9 updated`, `#9 placed`
and `#9 deleted`; only the last says anything. The rule, wherever effect lines
are listed (CLI op output, `tkr ai-changes`, the web flash):

- When the same response reports a task `deleted`, that task's `placed`,
  `moved`, `updated`, `reason_removed` and `undeferred` lines are not printed.
- When it reports a task `undeleted`, that task's `placed`, `moved` and
  `updated` lines are not printed.
- Everything else is always printed: `reason_added`, `deferred`, `due_set`,
  focus lines, and every block and entry line. The rule is keyed by task ID,
  so another task's lines in the same response are untouched.

The predicate is `domain.EffectImplied`, shared by the CLI and web mirrors.
API and `--json` responses keep every effect; only the lines are filtered.

### 4.5 Restored blocks are reported (added 2026-11-22)

`tkr undo` after `tkr unblock b3` printed only `undid unblock (op …)` while
the block silently came back; the same happened to a task's blocks on undo of
a delete and on redo of an add. A block update whose status returns to
`planned` from any other status, or whose `deleted_at` is cleared, now emits
`block_restored` once for that block. Update events carry only the changed
columns, so the effect has no span; the line is `b3 restored` in all three
mirrors. This is the second additive effect kind of the joy work (after the
`via` field of §4.2).

## 5. Noticing progress (J2)

### 5.1 Completion tally (CLI)
After `tkr done` prints its headline and consequence lines, and only when the
completion entry's work date is the server's today (a backdated `--date`
prints no tally), the CLI makes one best-effort `GET /today` with a 2-second
context deadline and, on success, prints `<n> done today` (`one done today`
for 1), where n counts today's `complete` entries that are not reopened. On
any error the line is omitted and the exit code is unaffected. Not under
`--json`. Not for `reopen`.

### 5.2 Triage clear
- CLI `tkr triage` (non-interactive) with nothing to show prints
  `Triage clear` to stdout.
- TUI card mode, when the deck empties: `Triage clear ✓`, followed by
  ` · 2 skipped` only when skipped > 0.
- TUI Triage tab, empty: `Triage clear ✓ — nothing needs a decision`.
- Web Triage page, empty: `Triage clear. Nothing needs a decision.`

### 5.3 Plan summary (TUI step 3)
`Plan done · placed 3 · blocked 2 · triage clear` (or `· triage left 4`).
The three counters are what the screen already computes.

### 5.4 Recap summary (TUI)
Second header line becomes `W worked · M missed · R to review` where W counts
the date's blocks with status `worked` or `done`, M those with `missed`, and R
counts still-`planned` blocks plus the date's unlinked entries (the two things
a recap acts on); cancelled blocks are not counted. When R is 0 and W+M > 0:
`Recap complete · W worked · M missed`. When everything is 0: `Nothing to
recap for <day>`. A next-morning recap of an auto-resolved day therefore opens
as complete unless entries are left to link, which is the truth of that day.
The key legend moves to the help footer (it is already in `recapKeys`); the
line it occupied is this summary.

## 6. Palette (J4)

ANSI 16 only, so the terminal theme decides the actual colors.

| Role | Color | Used for |
|---|---|---|
| id | 6 cyan | `#N`, `bN`, `eN`, `op …` |
| ok | 2 green | `✓`, `done`, `worked`, focus line, `◀ now`, success banners |
| warn | 3 yellow | triage reasons, `due <day>`, `reblock?`, `⚠` notices, pending decisions |
| bad | 1 red | `overdue`, `slipped`, `missed`, errors |
| head | bold | section titles, TUI header, card title |
| dim | 8 bright black | metadata (project, tags), timestamps, help footer, notes preview |
| selected | reverse video | the TUI cursor row (plus the existing `›`) |

CLI: a `paint` helper in `internal/cli` (one struct with `id`, `ok`, `warn`,
`bad`, `head`, `dim` methods returning the string styled or not) is the only
place lipgloss is called; `colorEnabled()` is unchanged. Colored output is
sanitized before styling, exactly as `errorLine` does today.

TUI: `Styles` gains `OK`, `Bad`, `ID`, `Head` and `Selected` becomes reverse
video; `styles.go` stays the only file that names a color.

Color is applied to: IDs everywhere a line is printed for a human; the ✓
mark; due/overdue/slipped/missed/reblock words; triage reason labels; section
titles; metadata. It is not applied inside `--json`, `status --short`, or
`report` output (report is for pasting elsewhere).

## 7. Empty states (J3)

| Place | Now | New |
|---|---|---|
| CLI `ls` Triage | header only | `Triage` / `  clear` |
| CLI `ls` Backlog | header only | `Backlog` / `  empty — tkr add "…" captures a task` |
| CLI `ls --view X` other empty views | header only | `  none` |
| CLI `today` with no blocks, triage 0, no entries, no interruptions | `no focus` + blank | `no focus` then `Nothing scheduled, nothing to triage — tkr plan shapes the day` |
| CLI `today` Blocks | section omitted | unchanged (omitted) |
| CLI `upcoming` empty section | header only | `  none` under each header |
| CLI `triage` empty | nothing | `Triage clear` |
| CLI `report` empty range | header only | header, blank line, `No work logged.` |
| CLI `search` no hits | (builder checks) | `No matches for 'q'` |
| CLI `show` empty Reasons/Blocks/Entries | header over nothing | section omitted; `Notes` empty → `Notes: none`; `Tags: ` empty → `Tags: none` |
| CLI `history` | (unchanged) | unchanged |
| TUI Today, Blocks section empty | `Blocks` over nothing | `Blocks` + dim `none planned today` |
| TUI Today, everything empty | sparse | focus line, then `Nothing scheduled, nothing to triage · a adds · 2 opens triage` |
| TUI Today backup line | always `Backup: healthy · last success …` | shown only when unhealthy (`⚠ backup` is in the header anyway) |
| TUI Triage tab empty | (builder checks) | `Triage clear ✓ — nothing needs a decision` |
| TUI Backlog empty | (builder checks) | `Backlog empty — a adds a task` |
| TUI Upcoming empty | headers | `Nothing deferred, nothing due soon` |
| TUI Log empty | `no log entries` | `No entries for <day> — l logs work` |
| TUI Plan step 2 backlog empty | `backlog is empty` | `Backlog empty — nothing to block` |
| TUI Recap empty | `no blocks or unlinked entries` | `Nothing to recap for <day>` |
| Web Today Focus | `No focus task.` | `No focus. Set one by task ID, or use Focus on a task.` (rows on Today have no Focus button, so "pick a task below" pointed nowhere; revised 2026-11-22) |
| Web Today Blocks | `No blocks today.` | `No blocks today.` + muted `Schedule one from a task's actions.` |
| Web Triage | `Nothing to triage.` | `Triage clear. Nothing needs a decision.` |
| Web Backlog | `No backlog tasks.` | `Backlog empty. New tasks start in Triage.` (no add form is added to Backlog; its default placement is Triage) |
| Web Upcoming / Log / Search / Report | (builder checks) | one sentence each in the same voice |
| Web Recap, unlinked entries | `No unlinked entries.` | `No unlinked entries on this date.` (matches its three siblings; revised 2026-11-22) |

## 8. Errors that help (J5)

- At most one context prefix after `tkr: `; never a Go package name
  (`civil:`, `datespec:`, `block:` as a bare prefix). The builder audits every
  `fmt.Errorf("%s: %w")` chain reachable from a CLI command for user input and
  rewrites the message at the outermost CLI layer; inner packages are not
  changed except where they are the only place that knows the field name.
- Known rewrites:
  - `block: invalid block start: civil: "25:00" has hour out of range` →
    `block start "25:00": hour must be 00–24`
  - `defer must be in the future` → `defer must be in the future (today is <Mon 2026-11-23>)`
  - `task #99 not found` → `task #99 not found` + second line `  tkr ls lists open tasks`
  - `nothing to undo` → unchanged
  - `add requires a title` → `add needs a title: tkr add "…"`
- Hints are a second line indented two spaces, on stderr, same color as the
  error line. At most one hint per error.
- Exit codes and the `tkr: ` prefix do not change. Problem JSON under `--json`
  does not change.

## 9. TUI shape (J4)

- Header: one line, `Taskronin · Fri 2026-11-20 15:04` bold, tabs as
  `1 Today  2 Triage …` with the active tab in reverse video; the warning/AI
  status items stay on the optional second line. On narrow frames the
  brand, then the date, then the clock give way; when the five tab labels
  alone do not fit (below 45 columns) the inactive tabs show only their
  number, as in `1 [2 Triage] 3 4 5` (added 2026-11-22).
- Rows: the cursor row is reverse video across the frame width (existing `›`
  kept); IDs cyan; reasons `{new, slipped}` yellow; `◀ now` green; `reblock?`
  yellow; `missed`/`slipped` red; `✓` green.
- Triage card: a rounded-border box (lipgloss `RoundedBorder`) the width of the
  frame; the top border carries `Card 2/5`; inside: bold title line, one line
  per reason as `new · since 09:34` (today) or `new · since Mon 2026-11-16`,
  slips in red, Due/Defer/Project/Tags/Priority as now, up to 5 dim note lines;
  the legend line (`cardLegend`, unchanged text) below the box in dim. On
  frames narrower than 20 columns or shorter than 6 rows the box is dropped and
  the current plain layout is used.
- Help footer dim; the banner line keeps its text and gets `✓ ` + green for
  success, `⚠ ` + yellow for info notices, red for errors.
- Everything else (keys, polling, overlays, forms, CF rules) unchanged.

## 10. Web face (J3, J4)

One stylesheet, `internal/web/static/app.css`, rewritten; CSP, htmx config,
routes, handlers and form field names unchanged; no inline styles (the
existing template test enforces it). Template changes are structural only.

- **Frame.** `max-width: 56rem`; a top bar with the word `Taskronin`, nav links
  as pills with the current page filled, Undo/Redo/Log out as small secondary
  buttons at the right. Dark mode via `prefers-color-scheme`.
- **Cards.** Task, block and entry rows become cards: title line with the ID
  muted, a meta line of chips. Reasons are yellow chips, tags gray chips,
  project muted text, `due <day>` a yellow badge, `overdue` red, `slipped ×N`
  red and shown only when N > 0 (the `Slips L/S` text is removed from rows and
  kept on the task page).
  Revised 2026-11-22: the overdue chip keeps its date (`overdue · due
  <day>`); the slips chip's `title` and the task page's line both read
  `slipped ×L lifetime, ×S since last triage`, and the task page shows the
  line only when L > 0; a task with no metadata gets no empty meta
  paragraph. Card titles are `h2` where cards sit directly under the page
  `h1` (Triage, Backlog) and `h3` under a section `h2`; the task page shows
  its title once, in the `h1`. Pager links name what they load (`More
  tasks`, `More results`, `More changes`, `More history`, `More entries`),
  so they cannot be confused with a card's `More` disclosure.
  An overdue task shows no `due soon` chip; the two contradict each other.
- **Actions.** Each card has one primary row: on Triage `Keep · Top · Bottom`
  and `Complete`; on Backlog `Up · Down · Focus · Complete`; the rest
  (`Before/After ID`, `Defer`, `Block`, `Drop`, `Send to triage`) sit in a
  `<details><summary>More</summary>` inside the card. Button styles: primary
  (filled) for Complete and Add, secondary (outlined) for the rest, danger
  (red outline) for Drop/Delete.
- **Focus.** The Focus card on Today has a green left border and a `▶` mark.
- **Flash.** A green banner with `✓` and the lines; error pages a red-bordered
  card with the same copy as now.
- **Forms.** Labels above inputs, inputs full width on phones, the Add-task
  form keeps all fields but groups Placement/Reference/Defer/Block under a
  `Placement` `<details>`.
- **Login.** Centered card: `Taskronin`, the line "Your tasks, your focus, your
  work log.", the password field, the button.
- Empty-state copy per §7.

## 11. Constraints

- Go 1.26 language, toolchain go1.27.4; `make check` green (fmt, vet, lint,
  race tests, govulncheck, mod verify, asset sums).
- Never remove `AIDEV-NOTE`s; update the ones next to changed code; add anchors
  where wording tables live (`effectLine` mirror, `paint`, `Styles`).
- Golden files are regenerated with the test suite's `-update` flag and the
  resulting diff is pasted into the implementer's report for review.
- `docs/quickstart.md` examples are updated to the new output in the same
  plan that changes the CLI.
- Web: `templates_test.go` forbids `<style`, `hx-on`, `javascript:` and
  inline `style=`; `assets-verify` checks only `htmx-2.0.11.min.js`.
- No new dependencies. lipgloss v2 is already present in CLI and TUI.

## 12. Calls made by the designer (for review)

1. Scope is three plans, in this order: E CLI voice, color, empty states,
   errors (plus the `reverted` effect kind in service and the web flash
   mirror); F TUI shape and empty states; G web face. CLI first because it is
   touched most often.
2. The completion tally is a best-effort extra read with a 2 s deadline (§5.1).
3. `reason_removed` lines are suppressed under placement headlines; the
   reason is implied by the placement.
4. Relative day words (today/yesterday/tomorrow) in interactive output only.
5. Undo/redo/revert print the reverted op's verb and the old op ID, not the
   new op ID.
6. ANSI 16 palette, reverse-video selection, rounded card box; no true color.
7. The TUI backup line is shown only when unhealthy.
8. `tkr show` omits empty sections.
9. Web list pages hide secondary actions behind `More`; `Slips L/S` leaves
   the rows.
10. Voice: warm and dry, no exclamation marks, no new emoji.

Builder-check rulings (2026-11-20, after a fresh reviewer read this document):
11. The `reverted` effect walks to the root user op and carries `Via`
    (§4.2), so redo and chained reversals name the right verb and op.
12. "Today" for day words is the CLI's cached `/clock` read, fetched once per
    process at format time, falling back to the absolute date on failure.
13. The TUI `effectsLine` adopts the §4 wording for the kinds it shows
    (Plan E Task 3), not just the ✓ style.
14. `undeferred` says `defer cleared` and is suppressed under placement
    headlines (§4, §4.1).
15. Recap "to review" counts planned blocks plus unlinked entries (§5.4).
16. The tally counts today's non-reopened completion entries and is skipped
    for a backdated completion (§5.1).
17. No add form on the web Backlog page (§7).
18. CLI list rows (`taskLine`) and TUI list rows keep their compact date
    rule; day words apply to headlines, effect lines, empty states and the
    triage card only. The two interfaces share the palette, not the row
    format.

Polish residuals (2026-11-22, Plan H):
19. A deleted or restored task prints one line, not three (§4.4).
20. Restored blocks get a line, through the new `block_restored` effect kind
    (§4.5). Additive in `--json`.
21. The web overdue chip keeps its date; the cryptic `Slips L/S: 0/0` line
    on the task page becomes the shared slips sentence, shown only when the
    task has slipped (§10).
22. Web headings no longer skip a level, and the task page shows its title
    once (§10).
23. Pager links are named for what they load (§10).
24. Two web empty states reworded for accuracy and consistency (§7).
25. The TUI header degrades to numbered tabs on frames under 45 columns
    (§9).

## 13. Questions for Sean (answered by default; change any later)

- Should a bare `tkr done` complete the focus task? Default: no (not built).
- Should `tkr add` echo the triage count (`· triage 3`)? Default: no.
- Is `8 bright black` legible on your terminal theme? Default: yes (the TUI
  already uses it).
