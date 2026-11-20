# Taskronin Plan E — CLI voice, color and empty states

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `tkr` talk like a colleague: plain-words outcomes, a completion tally, restrained color, empty states that name the next step, and errors with one prefix and a hint.

**Architecture:** All changes are presentation. `service` adds one field value (the reverted op's kind on the `reverted` effect); `domain.OpKind` gains a `Verb()` table shared by the three wording mirrors (CLI `effectLine`, web `effectLine`, TUI `effectsLine`). In `internal/cli`, a `paint` helper is the only caller of lipgloss, a `dayWord` helper turns dates into today/yesterday/tomorrow from the cached server clock, and `effectInHeadline` grows two suppression rules. Golden files are regenerated and reviewed.

**Tech Stack:** Go 1.26 (toolchain go1.27.4), `charm.land/lipgloss/v2 v2.0.6` (already a dependency), cobra, the existing `testutil.Golden` harness.

**Spec:** `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J-design; §3 voice, §4 wording tables, §5.1–5.2 progress, §6 palette, §7 empty states, §8 errors). Architecture authority: `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` §1.3 (imports), §6.7 (`textsafe`). D§9.1 (`status --short`) and D§14 (report text) are frozen.

## Global Constraints

- Repo `/home/sean/aix/taskronin`, branch `main`, commit directly on main, never push. Go `1.26.0`, toolchain `go1.27.4`, module `taskronin`, Linux only. No new dependencies.
- Imports (A§1.3, enforced by `internal/archtest`): `cli` may import `client`, `api`, `domain`, `civil`, `datespec`, `textsafe`, `report`, `editor`, `config`, `buildinfo`, lipgloss and cobra; never `service`, `store`, `server`. `web` never imports `cli` or `api`. `tui` imports `client`, `api`, `domain`, `civil`, `datespec`, `textsafe`, `editor`, `config`. Shared wording that all three need lives in `domain`.
- The CLI never calls `time.Now`; "today" is `e.clock().Today` (cached per process, one `GET /clock`).
- A decorative read (day words, the completion tally) never changes a command's exit code or prints an error. The write already succeeded.
- `--json` output is byte-identical to today except that the `reverted` effect's existing `to` field is now populated. `tkr status --short`, `tkr report` text/markdown (D§14), problem JSON, exit codes and flags do not change.
- Color: only through `paint`; `paint` is on exactly when `colorEnabled()` is true (unchanged: `--color`/config `always`, else TTY and `NO_COLOR` unset). Text is sanitized (`textsafe.SanitizeLine`) before it is styled. With color off, output is byte-identical to the uncolored goldens.
- Voice (J§3): sentences, lowercase after the ID mark, no exclamation marks, no new emoji; ID marks `#N`, `bN`, `eN`, `op xxxxxxxx`; the relative-day rule applies to headlines, effect lines and empty states, not to `report`, `show`, `history` or `--json`.
- Golden files: regenerate with `go test ./internal/cli -run '<Test>' -update`, then `git diff internal/cli/testdata/golden` and paste that diff into the task report. A golden that changes for a reason the brief does not name is a finding, not a fixture update.
- `make check` (gofmt, vet, golangci-lint, `go test -race -count=1 -timeout=30m ./...`, govulncheck, mod verify, assets) is green before every commit. On this loaded host, run `go test -race ./internal/cli/... ./internal/service/...` while iterating and the full `make check` once per task; if a package times out alongside the others, re-run it alone with `-timeout=30m` and say so in the report.
- Anchors: grep `AIDEV-` in a file before editing it; never delete an `AIDEV-NOTE`; update the mirror note in `internal/web/flash.go` and the headline-matching note in `internal/cli/effects.go` when their code changes; add anchors at `paint`, `dayWord`, the suppression rules and the tally.
- Commits: conventional (`feat(cli): …`), one per task, message ends with the implementer's trailer (`Co-Authored-By: OpenAI Codex <noreply@openai.com>` for Codex).

## Review Focus

1. **A successful write must never fail on decoration.** The tally read and the clock read for day words run after the mutation; a timeout or a stopped server there prints nothing and exits 0. Tests: Task 2 `TestDayWordWithoutServer`, Task 4 `TestDoneTallyOmittedOnTimeout`.
2. **Color must change bytes only when on.** `--color never` and `NO_COLOR` reproduce the plain goldens byte for byte; `--color always` output, stripped of SGR sequences, equals the plain output. Tests: Task 5 `TestColorNeverMatchesPlain`, `TestColorAlwaysStripsToPlain`.
3. **Suppression must never hide a reason being added.** Only `reason_removed` under a placement headline and `updated` under an `added` headline are suppressed; `reason_added` always prints. Test: Task 4 `TestUndoKeepPrintsNeedsTriage`.
4. **Non-date effect payloads pass through untouched.** `relDate` rewrites only a leading `YYYY-MM-DD`; `task_closed`, `13:00 → 14:00`, titles and empty strings are unchanged. Test: Task 2 `TestRelDate`.
5. **Hostile text stays inert under color.** A title with ESC bytes is sanitized before `paint` wraps it; the only escape sequences in the output are the ones `paint` emits. Test: Task 5 `TestColorSanitizesBeforeStyling`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/domain/op.go`, `op_test.go` | `OpKind.Verb()` human verb table | 1 |
| `internal/service/effects.go`, `effects_test.go`, `op.go`, `history.go`, `idem.go` | `reverted` effect carries the reverted op's kind | 1 |
| `internal/cli/paint.go`, `paint_test.go` | the only lipgloss caller in `cli` | 2 |
| `internal/cli/dates.go`, `dates_test.go` | `formatDate`, `formatMoment` (moved), `dayWord`, `momentWord`, `relDate` | 2 |
| `internal/cli/effects.go`, `capture_test.go`, `testdata/golden/effects.golden` | effect wording, verb use, suppression rules, headline matching | 3, 4 |
| `internal/web/flash.go`, `flash_test.go` | web mirror of effect wording | 3 |
| `internal/tui/banner.go`, `app_test.go` | TUI mirror (`effectsLine`) | 3 |
| `internal/cli/cmd_done.go`, `cmd_add.go`, `cmd_move.go`, `cmd_defer.go`, `cmd_block.go`, `cmd_undo.go`, `cmd_ai.go`, `root.go` | headlines, tally | 4 |
| `internal/cli/tasklist.go`, `cmd_today.go`, `cmd_ls.go`, `cmd_upcoming.go`, `cmd_triage.go`, `cmd_search.go`, `cmd_show.go`, `cmd_status.go`, `cmd_history.go` | color application | 5 |
| same printers + `cmd_report.go` | empty states | 6 |
| `internal/cli/errors.go`, `errors_test.go`, `cmd_add.go`, `cmd_block.go`, `internal/datespec/parse.go`, `internal/service/place.go` | error wording and hints | 7 |
| `docs/quickstart.md` | examples regenerated | 8 |

---

### Task 1: The reverted effect names what it reverted

**Files:**
- Modify: `internal/domain/op.go`, `internal/service/effects.go:136-145`, `internal/service/op.go:295`, `internal/service/history.go:151,202`, `internal/service/idem.go:105`
- Test: `internal/domain/op_test.go` (create if absent), `internal/service/effects_test.go`

**Interfaces:**
- Produces: `func (k OpKind) Verb() string` in `domain` — the table of J§4.2 (`task.add`→`add`, `task.update`→`edit`, `task.due`→`due`, `task.place`→`place`, `task.accept_new`→`accept new`, `task.defer`→`defer`, `task.manual`→`send to triage`, `task.complete`→`complete`, `task.reopen`→`reopen`, `task.drop`→`drop`, `task.delete`→`delete`, `task.undelete`→`undelete`, `block.create`→`block`, `block.reblock`→`reblock`, `block.unblock`→`unblock`, `block.outcome`→`outcome`, `entry.add`→`log`, `entry.update`→`edit entry`, `entry.relink`→`relink entry`, `entry.delete`→`delete entry`, `entry.undelete`→`restore entry`, `focus.set`→`focus`, `op.revert` (the constant `domain.OpRevert`)→`revert`; any other kind → `string(k)`).
- Produces: `Via string` on `service.Effect` and `api.Effect` (`json:"via,omitempty"`; the server's effect conversion copies it); `effectsFromEvents(rec domain.OpRecord, root *domain.OpRecord, evs []domain.Event) []Effect`; when `rec.Via != ""` the first effect is `Effect{Kind: EffReverted, Via: rec.Via, From: root.ID.String()[:8], To: string(root.Kind)}`, and when `root == nil`: `Effect{Kind: EffReverted, Via: rec.Via, From: rec.RevertsOpID.V.String()[:8]}` (`To` empty).
- Produces: `func rootOfReversal(ctx, q store.Q, rec domain.OpRecord) (*domain.OpRecord, error)` in `service`: starting at `rec.RevertsOpID`, `store.GetOp` the target; while the target's `Via != ""` and its `RevertsOpID.Valid`, follow it; stop after 8 hops; return the last op reached that has `Via == ""` (nil if none, or on not-found; any other error is returned). Called by `op.go` (live response), `history.go` and `idem.go` before `effectsFromEvents`. AIDEV-NOTE: the root is decoration for messages; a missing root never fails a response.
- Semantics: `tkr redo` after undoing a completion yields `{Kind: reverted, Via: "redo", From: <original completion op>, To: "task.complete"}`; `tkr undo` of an explicit revert yields `Via: "undo"` with the root user op the revert had reverted.

- [ ] **Step 1: Write the failing tests**

```go
// internal/domain/op_test.go
func TestOpKindVerb(t *testing.T) {
	for kind, want := range map[OpKind]string{
		OpTaskAdd: "add", OpTaskUpdate: "edit", OpTaskDue: "due", OpTaskPlace: "place",
		OpTaskAcceptNew: "accept new", OpTaskDefer: "defer", OpTaskManual: "send to triage",
		OpTaskComplete: "complete", OpTaskReopen: "reopen", OpTaskDrop: "drop",
		OpTaskDelete: "delete", OpTaskUndelete: "undelete", OpBlockCreate: "block",
		OpBlockReblock: "reblock", OpBlockUnblock: "unblock", OpBlockOutcome: "outcome",
		OpEntryAdd: "log", OpEntryUpdate: "edit entry", OpEntryRelink: "relink entry",
		OpEntryDelete: "delete entry", OpEntryUndelete: "restore entry", OpFocusSet: "focus",
		OpRevert: "revert", OpKind("system.import"): "system.import",
	} {
		if got := kind.Verb(); got != want {
			t.Errorf("%s.Verb() = %q, want %q", kind, got, want)
		}
	}
}
```
(Use the constant names as they exist in `op.go`; the strings above are the binding values.)

```go
// internal/service/effects_test.go
func TestEffectsFromEventsRevertedCarriesRoot(t *testing.T) {
	rec := domain.OpRecord{Via: "redo", RevertsOpID: sql.Null[domain.OpID]{V: undoOpID, Valid: true}}
	root := &domain.OpRecord{ID: completeOpID, Kind: domain.OpTaskComplete}
	got := effectsFromEvents(rec, root, []domain.Event{oneEvent})
	// got[0] == Effect{Kind: EffReverted, Via: "redo", From: completeOpID.String()[:8], To: "task.complete"}
	got = effectsFromEvents(rec, nil, []domain.Event{oneEvent})
	// got[0] == Effect{Kind: EffReverted, Via: "redo", From: undoOpID.String()[:8]}
}
// revert_test.go (real ops through the service): complete a task, Undo, Redo:
// the redo's OpResult.Effects[0] has Via "redo", To "task.complete" and From = the completion op's 8-char ID;
// the undo's has Via "undo", same From/To. Then tkr-level: `tkr undo --json` shows "via":"undo".
```
Build the IDs and `oneEvent` with the helpers `effects_test.go` already uses.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/domain -run TestOpKindVerb; go test ./internal/service -run TestEffectsFromEventsRevertedCarriesKind`
Expected: FAIL (method undefined; wrong argument count).

- [ ] **Step 3: Implement**

`Verb()` as a `switch` in `op.go` with a doc comment "Verb is the human verb for messages (J§4.2); the mirrors in cli, web and tui all use it." In `service`: add `Via` to `Effect`, add `rootOfReversal`, add the `root` parameter; `op.go:295`, `history.go` and `idem.go` call `rootOfReversal` when `rec.Via != ""` and pass its result (nil on not-found; any other error is returned as today). In `internal/api/types.go` add `Via string \`json:"via,omitempty"\`` to `Effect` and copy it wherever `service.Effect` is converted to `api.Effect` (grep `Effect{Kind:` in `internal/server`). Add an AIDEV-NOTE at the lookup: the root is decoration; a missing root must not fail history or a replay.

- [ ] **Step 4: Run the package tests**

Run: `go test -race ./internal/domain ./internal/service -run 'Verb|Effects|Revert|Undo|Redo|Idem|History'`
Expected: PASS. Then `make check` once.

- [ ] **Step 5: Commit**

```bash
git add internal/domain internal/service
git commit -m "feat(service): name the reverted op's kind in the reverted effect"
```

---

### Task 2: Relative days and the paint helper

**Files:**
- Create: `internal/cli/paint.go`, `internal/cli/paint_test.go`, `internal/cli/dates.go`, `internal/cli/dates_test.go`
- Modify: `internal/cli/effects.go` (move `formatDate`, `formatMoment` to `dates.go`), `internal/cli/output.go` (`errorLine` styles through `paint`)

**Interfaces:**
- Produces:
```go
type paint struct{ on bool }
func (e *env) paint() paint                 // on == e.colorEnabled()
func (p paint) id(s string) string          // Foreground "6"
func (p paint) ok(s string) string          // "2"
func (p paint) warn(s string) string        // "3"
func (p paint) bad(s string) string         // "1"
func (p paint) dim(s string) string         // "8"
func (p paint) head(s string) string        // Bold
func (p paint) strong(s string) string      // Bold (titles inside headlines)
```
  Every method returns `s` unchanged when `!p.on` or `s == ""`. `paint.go` is the only file in `cli` that imports lipgloss (lint-free, enforced by Task 5's grep step).
```go
func formatDate(civil.Date) string                 // unchanged text: "Mon 2026-11-23"
func formatMoment(*api.Moment) string              // unchanged
func (e *env) dayWord(d civil.Date) string         // "today" | "yesterday" | "tomorrow" | formatDate(d)
func (e *env) momentWord(m *api.Moment) string     // dayWord(m.Date) + " HH:MM" when m.Time != nil
func (e *env) relDate(text string) string          // rewrites a leading YYYY-MM-DD via dayWord; else unchanged
```
  `dayWord` uses `e.clock()`; on error it returns `formatDate(d)`. AIDEV-NOTE: `e.clock()` is cached with a sticky error, so a write command pays at most one extra local read, and a failed read degrades to the absolute date instead of an error.

- [ ] **Step 1: Write the failing tests**

```go
func TestDayWord(t *testing.T) {
	srv, sys := captureServer(t) // clock 2026-10-28 10:00
	e := newTestEnv(t, srv, sys)  // however helpers_test.go builds an env with a client
	for d, want := range map[string]string{"2026-10-28": "today", "2026-10-27": "yesterday", "2026-10-29": "tomorrow", "2026-10-30": "Fri 2026-10-30"} {
		if got := e.dayWord(civil.MustParseDate(d)); got != want { t.Errorf(...) }
	}
}
func TestDayWordWithoutServer(t *testing.T) {
	e := &env{clockLoaded: true, clockErr: errors.New("down")}
	if got := e.dayWord(civil.MustParseDate("2026-10-28")); got != "Wed 2026-10-28" { t.Fatal(got) }
}
func TestRelDate(t *testing.T) {
	// clock 2026-10-28
	cases := map[string]string{
		"2026-10-29 09:00": "tomorrow 09:00", "2026-10-28 13:00-15:00": "today 13:00-15:00",
		"2026-10-30": "Fri 2026-10-30", "task_closed": "task_closed", "13:00 → 14:00": "13:00 → 14:00", "": "",
	}
}
func TestPaint(t *testing.T) {
	off := paint{}
	if off.id("#1") != "#1" || off.ok("") != "" { t.Fatal("off must be identity") }
	on := paint{on: true}
	if got := on.id("#1"); !strings.Contains(got, "\x1b[") || !strings.Contains(got, "#1") { t.Fatal(got) }
	if on.ok("") != "" { t.Fatal("empty stays empty") }
}
```
Use the date constructor `civil` actually exports (grep `func Parse`/`MustParse` in `internal/civil`).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/cli -run 'DayWord|RelDate|Paint'` → FAIL (undefined).

- [ ] **Step 3: Implement** `paint.go`, `dates.go`; move the two format functions; make `errorLine` use `e.paint().bad(line)`.

- [ ] **Step 4: Run** `go test -race ./internal/cli -run 'DayWord|RelDate|Paint|Capture|Usage'` → PASS; `make check`.

- [ ] **Step 5: Commit** — `git commit -m "feat(cli): relative day words and a paint helper"`

---

### Task 3: Effect wording in the three mirrors

**Files:**
- Modify: `internal/cli/effects.go` (`effectLine` becomes `(e *env) effectLine(api.Effect) string`; `printOp`/`effectInHeadline` call it), `internal/cli/capture_test.go` (`TestCaptureEffects*`), `internal/cli/testdata/golden/effects.golden`, `internal/web/flash.go` (`effectLine`), `internal/web/flash_test.go`, `internal/tui/banner.go` (`effectsLine`), `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `domain.OpKind.Verb()` (Task 1), `e.relDate` (Task 2).
- Produces: the wording of J§4 in all three mirrors. Binding strings (CLI and web; `<To>` is the raw payload, `rel(<To>)` the relative-day rewrite — the web has no clock cache in the flash path and prints the raw payload):

| kind | CLI line | web line |
|---|---|---|
| deferred | `#4 deferred to rel(To)` | `#4 deferred to To` |
| undeferred | `#4 defer cleared` | same |
| due_set | `#4 due rel(To)` | `#4 due To` |
| due_cleared | `#4 due date cleared` | same |
| blocked | `b1 scheduled rel(To)` | `b1 scheduled To` |
| block_moved | `b1 moved From → To` | same |
| block_cancelled | `b1 cancelled · To`; `b1 cancelled` when To is empty | same |
| block_resolved | `b1 To` (`b1 worked`, `b1 missed`, `b1 done`); `b1 resolved` when To is empty | same |
| outcome_set | `b1 marked To` | same |
| slipped / unslipped | unchanged | unchanged |
| reason_added | `#3 needs triage (new)` | same |
| reason_removed | `#3 no longer flagged new` | same |
| focus_set / focus_cleared / interrupted / retitled / entry_* / reviewed | unchanged | unchanged |
| reverted | by `Via`: `undid <Verb(To)> (op From)` / `redid <Verb(To)> (op From)` / `reverted <Verb(To)> (op From)`; with `To` empty: `undone (op From)` / `redone (op From)` / `reverted op From`; with `Via` empty treat as `revert` | same |
| created / updated / placed / moved / completed / reopened / dropped / deleted / undeleted / purged | `#N <kind>` (unchanged) | unchanged |
| unknown kind | `#N <kind>` (unchanged) | unchanged |

  TUI `effectsLine` (only the kinds it already shows): `reason_added` → `#3 needs triage (new)`; `reverted` → `undid <verb>` / `redid <verb>` / `reverted <verb>` by `Via` (`undone`/`redone`/`reverted` when `To` is empty); `block_cancelled`, `block_resolved`, `focus_set`, `focus_cleared`, `interrupted`, `completed`, `entry_logged`, `slipped`, `unslipped` unchanged.

- [ ] **Step 1: Update the tests first.** In `TestCaptureEffects` (capture_test.go ~line 560) set the `reverted` fixture to `{Kind: "reverted", Via: "undo", From: "abcdef12", To: "task.complete"}` and add `{Kind: "reverted", Via: "redo", From: "abcdef13", To: "entry.add"}` and `{Kind: "reverted", Via: "revert", From: "abcdef14"}`; set `block_cancelled` to include a second fixture with empty `To`. In `flash_test.go` `TestFlashLinesEveryEffectKind` adjust the expected strings to the web column. In `tui/app_test.go` lines ~253–256 adjust expectations. Run; expect FAIL.

- [ ] **Step 2: Implement the three functions.** Keep the AIDEV-NOTE in `flash.go` ("Mirror effectLine in internal/cli/effects.go") and extend it with "and effectsLine in internal/tui/banner.go; Plan E Task 3 holds the table".

- [ ] **Step 3: Regenerate** `effects.golden` with `-update`; run `git diff internal/cli/testdata/golden/effects.golden` and paste it into the report; check every changed line against the table.

- [ ] **Step 4: Run** `go test -race ./internal/cli ./internal/web ./internal/tui` → PASS; `make check`.

- [ ] **Step 5: Commit** — `git commit -m "feat(cli,web,tui): plain-words effect lines"`

---

### Task 4: Headlines in plain words and the completion tally

**Files:**
- Modify: `internal/cli/cmd_done.go`, `internal/cli/cmd_add.go` (`addHeadline`), `internal/cli/cmd_move.go:59` (keep), `internal/cli/cmd_defer.go:62-69`, `internal/cli/cmd_block.go` (`blockHeadline`), `internal/cli/cmd_undo.go:54`, `internal/cli/cmd_ai.go:146`, `internal/cli/effects.go` (`effectInHeadline`, new `placementHeadline`), `internal/cli/root.go` (env field `tallyTimeout time.Duration`, default `2 * time.Second`)
- Test: `internal/cli/capture_test.go`, goldens `done*.golden`, `add_*.golden`, `undo.golden`, `redo.golden`, `revert.golden`, `drop.golden`, `reopen*.golden`, `delete.golden`, `undelete.golden` as they change

**Interfaces:**
- Consumes: `e.dayWord`, `e.momentWord`, `paint` (not yet applied — Task 5), `domain.OpKind.Verb()`.
- Produces (binding headline strings; `T` is the title):
  - `done`: `✓ #2 'T' done · logged <dayWord>`; after the effect lines, **only when `res.Entry.WorkDate` equals the server's today** (a backdated `--date` prints no tally), the tally line `one done today` or `<n> done today` where n = count of entries in `GET /today` with `Kind == "complete"` and `!Reopened` (`c.Today(ctx, "")` with `ctx, cancel := context.WithTimeout(e.ctx, e.tallyTimeout)`); on any error print nothing. Never under `--json`.
  - `add` with `-P`/`-t`/`-p`: the existing headline + ` · ` + space-joined metadata in the order project, `+tag`…, `prio H`; the `updated` effect for `res.Task.ID` is then suppressed. Example: `#5 'Fix flaky CI job' added to backlog (top) · infra.ci +ci`.
  - `add --defer`: `added, deferred to <momentWord>`; `add --block`: `added, blocked b1 <dayWord> 13:00–15:00`.
  - `keep`: `#3 kept in place`.
  - `defer ID none`: `#4 defer cleared`; `defer ID D`: `#4 deferred to <momentWord>`; `due`: `#4 due <momentWord>` / `#4 due date cleared`.
  - `blockHeadline`: `b1 <dayWord> 09:00–11:00 #1 'T'`.
  - `undo`, `redo`, `revert` (both `tkr revert` and `tkr ai-changes --revert`): the headline **is** `e.effectLine(<the response's reverted effect>)` (Task 3: `undid complete (op a0888c43)`, `redid …`, `reverted …`); if the response has no `reverted` effect, fall back to today's `undone (op <new>)` form and say so in the report. The `reverted` effect is then in-headline and not repeated.
  - `placementHeadline(resp *api.OpResponse, headline string) bool` is true for: any `added` action, `kept in place`, `moved …`, `deferred to …`, `defer cleared`, `blockHeadline`, the reblock headline, the `done` headline, `dropped`, `accepted N new tasks into the backlog`. When true, `reason_removed` and `undeferred` effects whose `TaskID` is `resp.Task.ID` (or any of `resp.Tasks`) are suppressed. `reason_added` and `deferred` are never suppressed.
  - `effectInHeadline` strips a leading `✓ ` before the existing prefix match (update the AIDEV-NOTE at `effects.go:53`).

- [ ] **Step 1: Tests first.** Add to `capture_test.go`:
  - `TestDoneTally`: complete two tasks with the CLI; the second run's stdout ends with `2 done today\n`; the first with `one done today\n`.
  - `TestDoneTallyOmittedOnTimeout`: `e.tallyTimeout = 0` (expose via the test env builder); stdout has no `done today` line; exit 0; stderr empty.
  - `TestDoneBackdatedHasNoTally`: `tkr done N --date yesterday` prints `✓ #N 'T' done · logged yesterday` and no tally line.
  - `TestDoneOnDeferredTaskPrintsNoDeferLine`: a deferred task completed prints the headline (and `focus cleared` if applicable) but no `defer cleared` line.
  - `TestRedoPrintsVerbAndRootOp`: complete, undo, redo: the redo headline is `redid complete (op <8hex of the completion op>)`.
  - `TestKeepPrintsOneLine`: on a `{new}` task, `tkr keep` prints exactly `#3 kept in place\n`.
  - `TestUndoKeepPrintsNeedsTriage`: after `keep`, `tkr undo` prints `undid place (op <8hex>)` then `#3 needs triage (new)`.
  - `TestAddMetadataHeadline`: `tkr add "Fix" --top -t ci -P infra.ci -p H` prints exactly `#N 'Fix' added to backlog (top) · infra.ci +ci prio H\n`.
  Run; expect FAIL.

- [ ] **Step 2: Implement.** Regenerate the affected goldens (`-update`), paste `git diff internal/cli/testdata/golden` into the report, and confirm each changed line maps to a row above.

- [ ] **Step 3: Run** `go test -race ./internal/cli` → PASS; `make check`.

- [ ] **Step 4: Commit** — `git commit -m "feat(cli): headlines in plain words and a completion tally"`

---

### Task 5: Color

**Files:**
- Modify: `internal/cli/tasklist.go` (`taskLine(t, today, p paint)`), `internal/cli/cmd_today.go`, `cmd_ls.go`, `cmd_upcoming.go`, `cmd_triage.go`, `cmd_search.go`, `cmd_show.go`, `cmd_history.go`, `cmd_status.go` (long form only), `effects.go` (`printOp`: IDs and ✓ in headlines and effect lines, title via `strong`)
- Test: `internal/cli/capture_test.go`, `status_test.go`; new goldens `ls_color.golden`, `done_color.golden`, `today_color.golden`

**Interfaces:**
- Consumes: `paint` (Task 2).
- Rules (J§6): `id()` on every `#N`, `bN`, `eN`, `op xxxxxxxx` token at the point it is formatted; `ok()` on `✓`, `done`, `worked`, `◀ now`, the focus line; `warn()` on `[reasons]`/`{reasons}`, `due …`, `reblock?`, `⚠ …`; `bad()` on `overdue`, `slipped`, `missed`; `head()` on section titles (`Triage N`, `Backlog`, `Blocks`, `Log`, `Interruptions`, `Overdue`, `Due soon`, `Deferred`, `Reasons`, `Notes`, `Entries`); `dim()` on the metadata column (project, `+tags`), timestamps in `show`/`history`; `strong()` on the quoted title in headlines (quotes stay outside the styling). Never regex post-processing: style the token where it is built.
- Not colored: `--json`, `status --short`, `report`, the hint lines of Task 7 beyond `bad()`.

- [ ] **Step 1: Tests first.**
  - `TestColorNeverMatchesPlain`: run `ls`, `today`, `done` with `--color never` and with no flag in a non-TTY env; both equal the plain goldens.
  - `TestColorAlwaysStripsToPlain`: run the same with `--color always`; strip `\x1b\[[0-9;]*m` and compare to plain; also pin `ls_color.golden`, `today_color.golden`, `done_color.golden`.
  - `TestColorSanitizesBeforeStyling`: title `Bad\x1b[31mtitle`; with `--color always`, the output contains `Bad�[31mtitle` and every ESC byte in the output is the first byte of an SGR sequence that `paint` emits (parse the output: after stripping SGR sequences no ESC remains).
  - Grep gate (a test, not a script): `TestLipglossOnlyInPaint` walks `internal/cli/*.go` (non-test) and fails if any file other than `paint.go` imports `charm.land/lipgloss/v2`.
  Run; expect FAIL.

- [ ] **Step 2: Implement.** Thread `p := e.paint()` into the printers; keep plain output byte-identical (goldens must not change in this task — if one does, the task is wrong).

- [ ] **Step 3: Run** `go test -race ./internal/cli` → PASS; `make check`.

- [ ] **Step 4: Commit** — `git commit -m "feat(cli): color for IDs, marks, reasons and sections"`

---

### Task 6: Empty states that say what to do next

**Files:**
- Modify: `internal/cli/cmd_ls.go`, `cmd_today.go`, `cmd_upcoming.go`, `cmd_triage.go`, `cmd_report.go` (text format only), `cmd_search.go`, `cmd_show.go`
- Test: `internal/cli/read_test.go` / `status_test.go` / `report_test.go` (wherever those commands are tested) + goldens `ls_empty.golden`, `today_empty.golden`, `upcoming_empty.golden`, `show_minimal.golden`

**Interfaces:** binding strings (J§7):
- `ls`: under `Triage` → `  clear`; under `Backlog` → `  empty — tkr add "…" captures a task`; any other empty `--view` → `  none`.
- `today`: when there are no blocks, triage is 0, no entries and no interruptions: after the focus line print a blank line then `Nothing scheduled, nothing to triage — tkr plan shapes the day` (AI/banner lines still follow if present).
- `upcoming`: `  none` under an empty `Deferred` or `Due soon`.
- `triage` (non-interactive, no flags) empty: `Triage clear`.
- `report` text with no entries in range: header, blank line, `No work logged.`; `--todo`/`--upcoming` sections unchanged after it. Markdown and JSON unchanged.
- `search` with no hits: `No matches for 'q'` (q sanitized).
- `show`: omit the `Reasons`, `Blocks`, `Entries`, `Interruptions` sections when empty; `Tags: none` when no tags; `Notes` empty → the single line `Notes: none` instead of the section.

- [ ] **Step 1: Tests first** pinning each string (one golden per empty screen; exact-string asserts for `triage`, `report`, `search`).
- [ ] **Step 2: Implement.** Section titles go through `head()` (Task 5) and the empty lines through `dim()`.
- [ ] **Step 3: Run** `go test -race ./internal/cli ./internal/report` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(cli): empty states that say what to do next"`

---

### Task 7: Errors with one prefix and a next step

**Files:**
- Modify: `internal/cli/errors.go` (`fail`: hint line), `internal/cli/cmd_add.go:28`, `internal/cli/cmd_block.go` (drop the `block: ` wrapper where datespec already names the field), `internal/datespec/parse.go:98` (`invalid block start: %w` → a message that names the field once and carries no package prefix), `internal/civil/minute.go:38` only if datespec cannot produce the target text otherwise, `internal/service/place.go:77`
- Test: `internal/cli/errors_test.go`, `internal/datespec/parse_test.go`, `internal/service/place_test.go` (or wherever `defer must be in the future` is asserted)

**Interfaces:** binding final stderr texts (exit codes unchanged):
- `tkr block 1 25:00-26:00` → `tkr: block start "25:00": hour out of range` (exit 2)
- `tkr add` → `tkr: add needs a title: tkr add "…"` (exit 2)
- `tkr defer 4 yesterday` → `tkr: defer must be in the future (today is Fri 2026-11-20)` using the server's today (exit 1, as today)
- `tkr done 99` → `tkr: task #99 not found` then `  tkr ls lists open tasks` (exit 5). The hint applies to every `not_found` whose detail starts with `task #`.
- Rule: a user-facing message has at most one `<context>: ` prefix after `tkr: ` and never a Go package name. The report lists the audit: every message reachable from `usagef`, `fmt.Errorf("…: %w")` and `domain.Errorf` in `internal/cli` that was changed, before → after, and every one left alone with the reason.
- Hints: `func hintFor(p api.Problem) string` in `errors.go`; printed by `fail` as `  <hint>` through `errorLine` after the detail and before field/conflict lines; at most one hint; none under `--json`.

- [ ] **Step 1: Tests first** for the four texts (exact stderr, exact exit code) in `errors_test.go` (use `captureServer` for the ones that need a server); run → FAIL.
- [ ] **Step 2: Implement**, then run the audit grep and record it.
- [ ] **Step 3: Run** `go test -race ./internal/cli ./internal/datespec ./internal/service -run 'Error|Usage|Parse|Defer|Place|Block'` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "fix(cli): errors with one prefix and a next step"`

---

### Task 8: Quickstart examples regenerated

**Files:**
- Modify: `docs/quickstart.md`

- [ ] **Step 1:** Build `bin/tkr` (`make build`). Start a scratch instance with `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR` under `$HOME/.cache/tkr-quickstart-<pid>/{c,d,s,r}` (not `/tmp`: init refuses a world-writable parent; keep the path short: the socket path limit is 107 bytes), `tkr init --no-systemd --password-stdin -y --timezone America/Denver`, `tkr serve` in the background.
- [ ] **Step 2:** Run every command shown in `docs/quickstart.md` in order and replace each shown output with the real one, with `--color never` so the file stays plain. Keep the prose; change only the example outputs (and dates, which will differ).
- [ ] **Step 3:** Stop the server, delete the scratch directory. `git diff docs/quickstart.md` goes into the report.
- [ ] **Step 4: Commit** — `git commit -m "docs: refresh the quickstart for the new CLI voice"`
