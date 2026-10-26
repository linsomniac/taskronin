# Taskronin — Design (Draft v6)

Status: DRAFT v6 — five review rounds (consensus: no CRITICAL/HIGH) plus
Sean's answers to question round 1 (§18). **(my call)** = designer proposal
where the description was silent; Sean's decisions are marked **(Sean)**.

---

## 1. Intent and why

Sean tracks work in a plain text file: a dated log of *what I've done* above an
"active point", and a list of *what I need to do* below it. It works, but it
doesn't keep him on task: work arrives continuously and it is easy to get
buried. TaskWarrior had a good CLI/TUI feel but didn't help with focus; what
actually failed was that it was **out of sight** (nothing pulled him back during
the day) and **interrupts won** (new incoming work displaced what he'd planned)
**(Sean)**. Those two failures drive where v1 invests:

- *Out of sight* → the bare `tkr` dashboard, a `status --short` segment for the
  shell prompt/tmux that is always visible, focus, the morning plan / evening
  recap loop, and passed-but-unworked blocks flagged the same day.
- *Interrupts* → capture is one short command that lands in triage *without*
  touching the current plan or focus; if Sean does switch, `tkr add … --now`
  (or `tkr now ID`) records the interruption, so recap shows what displaced
  what, and the displaced block shows up as a slip instead of vanishing.

Taskronin is a single-user task manager built around what the text file does
badly:

1. **Triage** — new, surfaced, slipped and due work lands in a triage list that
   must be consciously cleared (ordered, scheduled, time-blocked, done, or
   dropped) instead of silently piling up.
2. **Daily focus** — time blocks, a "now working on" focus, a morning *plan* and
   evening *recap* loop, a status line for shell/tmux, and missed blocks that
   come back for re-triage, with slips tracked.
3. **A work log linked to tasks** — dated "what I did" entries that roll up into
   a report like the current text file and attach to the task they advanced.

Success: Sean uses it daily instead of the text file; the triage list stays
short; the weekly report is one command; slips are visible, not silent.

## 2. Goals, non-goals, anti-goals

### Goals
- G1. Tasks with title + markdown notes; manual ordering; triage list.
- G2. Log entries (dated; date shown, optional time stored), linked to a task
  or standalone; backdating for recaps.
- G3. One-and-done completion with retitle-at-completion.
- G4. Upcoming (defer-until) date and a separate deadline (due) **(Sean)**.
  Deadlines are rare (a few a year), so due stays out of the way unless set.
- G5. Time blocks; missed blocks return to triage; blocking and slips recorded.
- G6. Focus loop: `plan`, `recap`, focus pointer, `status --short`.
- G7. Work report by day (text/markdown/json), optionally with the todo list.
- G8. CLI, TUI, Web, MCP through one server API. Delivery order: server + CLI +
  MCP → TUI → Web (my call).
- G9. Robustness: no silent data loss; crash-safe; verified backups and
  crash-safe restore; audit history with undo/revert.
- G10. Security: every interface authenticated; server-enforced least
  privilege; hardened web UI; safe terminal output; nothing irrecoverable
  without the human's password.
- G11. Go, single static binary `tkr` (all subcommands, incl. `tkr serve`,
  `tkr mcp`), Linux only; runs on Sean's workstation **(Sean)**.

### Non-goals (v1)
- Multi-user / sharing. Offline-first sync. TaskWarrior compatibility.
- Two-way calendar sync; native mobile apps (web UI is responsive).
- Physical deletion of tasks (tombstones + content purge only).
- Remote access: v1 runs on and serves only Sean's single-user workstation
  (unix socket + loopback TCP) **(Sean)**. Non-loopback binds are refused in v1;
  the TLS rules of §10.2 are kept as the design for a later remote mode.
- Task-to-task links and subtasks ("related work" meant the log entries
  attached to a task, which v1 has) **(Sean)**.
- Desktop notifications, recurring tasks, stale-backlog review, ICS feed
  **(Sean: none in v1)**; importer for the old text file **(Sean: start fresh)**.
- Drag-and-drop in the web UI (move buttons instead) (my call).

### Anti-goals (failure even if it "works")
- A computed urgency score that reorders behind your back.
- Capture slower than appending a line to a text file.
- Anything disappearing or changing without an explicit action, an audit
  record, and a way to see it. Every slip visible.
- An AI client able to irrecoverably destroy data or silently rewrite history.

## 3. Glossary

| Term | Meaning |
|---|---|
| **Task** | Something to do. |
| **Log entry** (`tkr log`) **(Sean)** | Dated record of work done; optional task link. Kinds: `work`, `complete`. |
| **Triage list** | Open tasks with ≥1 pending triage reason. Shown above the backlog. |
| **Backlog** | Open, triaged, not-deferred tasks in manual order. |
| **Upcoming** | Open tasks deferred to a future moment. |
| **Due** | Optional deadline. A task can never be deferred past it. |
| **Time block** | A planned slot on a day for one task. |
| **Slip** | A block commitment not honored (§5.6). |
| **Focus** | The task Sean says he's working on now (cleared at midnight). |
| **Project** | Optional free-form label (one per task, may be dotted like `infra.keycloak`); never required. |
| **Tag** | Free-form labels, many per task. |
| **Day** | A calendar day in the configured timezone; rolls over at midnight **(Sean)**. |
| **Op** | One user command = one transaction = one `op_id`; the unit of undo/revert. |
| **Placement** | A triage-acknowledging action (§5.5). |

## 4. Workflow walkthroughs

### 4.1 Multi-day task
```
$ tkr add "Set up keycloak postgres cluster"     → #42 added to triage (new)
$ tkr plan                                       # TUI opens in Plan mode
   #42 Set up keycloak postgres cluster  [new]   k keep · t top · b bottom · p place · d defer · B block · x done · D drop · s skip
   B 13:00-15:00                                 → block today 13:00–15:00
$ tkr log 42 "Added Ansible role for postgres cluster"   → logged Fri 2026-10-23 on #42
$ tkr now 42                                     # focus
$ tkr log "Created pgk cluster in dev" -e        → #42 (focus); $EDITOR opens for notes
$ tkr recap                                      # TUI Recap mode
  ... Monday ...
$ tkr log 42 "Failover tested in dev"
$ tkr done 42                                    → #42 completed; ✓ entry logged Mon 2026-10-26
```
Report:
```
Fri 2026-10-23
  - Added Ansible role for postgres cluster        [#42 Set up keycloak postgres cluster]
  - Created pgk cluster in dev                     [#42]
Mon 2026-10-26
  - Failover tested in dev                         [#42]
  ✓ Set up keycloak postgres cluster               [#42]
```

### 4.2 One-and-done with retitle
```
$ tkr add "Run ansible against dev qt cluster" --top   → #57 top of backlog (skips triage)
$ tkr done 57 --as "Add svc-cluster weencrypt name to certs"
```
Title changes (old title in history); one ✓ entry; one report line.

### 4.3 Ad-hoc and backdated work
```
$ tkr log -N "Answered Bob's DNS question"            # explicitly no task
$ tkr log 42 "Rebuilt CI runner" --date yesterday     # backdated: ID or -N required
```

### 4.4 A slipped block, and one worked but unfinished
Block for #42 13:00–15:00 Monday; nothing logged by 15:00 → Today marks it
*passed, unworked — reblock?*. If still nothing is logged at midnight, the block
resolves `missed`, and #42 gets the `slipped` reason; Tuesday's plan shows it
first with "slipped ×2 lifetime, ×1 since last triage". A recap after midnight
uses `--date yesterday` (or `recap --date mon`). Logging Wednesday with
`--date mon` flips the block to `worked` (unslip), removes the pending
`slipped` reason if no other unacked slip remains, and adds `continue`. If that
entry is later deleted or reverted, the block slips again and `slipped` comes
back.

Had Sean worked on #42 Monday without finishing, the block resolves `worked`
and #42 comes back to Tuesday's triage as `continue` — not a slip — so a
multi-day task gets re-planned each morning instead of quietly sinking
**(Sean)**.

### 4.5 An interruption
At 10:20, during the #42 block, a page arrives: `tkr add "Restart stuck
pg replica" --now` creates #60, skips triage, sets focus to #60, and records
"interrupted #42". `tkr log "Restarted replica, root cause disk full"` links to
#60 (focus). Back to #42 with `tkr now 42`. Recap lists the interruption; if the
#42 block ends up unworked it is a slip like any other.

## 5. Domain model and rules

### 5.1 Task
| Field | Rule |
|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` — never reused or renumbered. |
| `title` | 1–500 chars, one line (§10.6). |
| `notes` | Markdown, ≤ 64 KiB. |
| `status` | `open` · `done` · `dropped`. |
| `deleted_at` | Tombstone. Deleted tasks are excluded from views, focus, and reconciliation; their log entries remain in reports. |
| `rank` | Every task has one from creation (bottom). Survives triage, defer, reopen. |
| `last_placed_at` | Last placement (§5.5). |
| `defer_date`, `defer_min` | Upcoming moment (date + optional minute, §5.12). |
| `due_date`, `due_min` | Deadline (date + optional minute). |
| `due_ack`, `due_ack_date` | Phase acknowledged for the current due value (`none` · `due` · `overdue`) and the day it was acknowledged (overdue re-fires daily, §5.8). |
| `project` | Optional free-form label (≤ 100 chars, `[A-Za-z0-9._-]`, may be dotted); never required **(Sean)**. |
| `priority` | Optional `H`/`M`/`L` marker; never affects order (my call). |
| `origin` | Actor that created it. |
| `completed_at`, `completed_date` | Current completion; set by `done`, cleared by `reopen`. |
| `created_at`, `updated_at`, `version` | `version` bumps on every user-visible change (incl. triage reasons and user moves). Internal rank renormalization never bumps it. |

**Triage reasons** live in `task_triage(task_id, reason, since)`, one row per
pending reason: `slipped`, `overdue`, `due`, `continue`, `surfaced`,
`reopened`, `manual`, `new`. A task is in triage iff it has ≥1 row.

### 5.2 Log entry
| Field | Rule |
|---|---|
| `id` | AUTOINCREMENT. |
| `task_id` | Optional. `work` entries can be (re)linked; `complete` entries are bound to their task permanently. |
| `kind` | `work` · `complete`. |
| `title` | One sentence, 1–500 chars. |
| `notes` | Markdown, ≤ 64 KiB. |
| `work_date` | Date of the work; default today; never in the future (rejected). |
| `occurred_min` | Optional minute (§5.12), only from `--at`. Display/sort only. |
| `reopened` | On `complete` entries whose task was later reopened; shown "(reopened)". |
| `created_at`, `updated_at`, `version`, `deleted_at` | As tasks. |

- Logging against an open, done, or dropped task is allowed (CLI warns on
  closed). Logging never changes task fields except via the derived block
  effects of §5.6.
- The API always requires an explicit `task_id` (a number or `null`). Implicit
  linking (focus → current block → none) happens **only in interactive
  CLI/TUI**, only when `work_date` is today, and is always echoed. With a
  `--date` other than today the CLI requires an ID or `-N`.
- A `complete` entry cannot be deleted while its task is done (use
  `reopen --remove-entry`); its notes may be edited.

### 5.3 Time block
| Field | Rule |
|---|---|
| `id`, `task_id`, `version`, `deleted_at` | AUTOINCREMENT; versioned; `deleted_at` only set by revert of the creating op (never slips, excluded everywhere). |
| `date`, `start_min`, `end_min` | Date + minutes (§5.12); half-open `[start, end)`; within one day (`0 ≤ start < end ≤ 1440`, no blocks across midnight); nonexistent DST wall times rejected. |
| `status` | `planned` · `done` · `worked` · `missed` · `cancelled`. |
| `outcome_source` | `auto` (derived) · `user` (set in recap). |
| `cancel_reason` | `user` · `task_closed` · `deferred` · `rescheduled`. |
| `slipped`, `slipped_at` | Whether this block currently counts as a slip; when it last became one. |
| `created_at`, `resolved_at` | |

Invariants (checked by every op, including revert):
- Only open, non-deleted tasks can have `planned` blocks.
- **No overlapping planned blocks** (any tasks), so "current block" is unique:
  the planned block whose interval contains now.
- **A task is either deferred or has planned blocks, never both.** Blocking
  clears the defer; deferring cancels the task's unstarted planned blocks.
- Blocking is a placement (§5.5).

### 5.4 Views (computed after reconcile)
- **Triage**: open, not deleted, ≥1 reason row, not deferred into the future.
  Ordered by highest reason (`slipped` > `overdue` > `due` > `continue` >
  `surfaced` > `reopened` > `manual` > `new`), then that reason's `since`.
- **Backlog**: open, not deleted, no reasons, not deferred into the future, by
  `rank`. Overdue/due-soon marked.
- **Upcoming**: open with a future defer, by date; plus "due soon" (due within
  `due_soon_days`, default 3) — shown only when such tasks exist.
- **Today**: today's blocks (current highlighted; ended planned blocks with
  nothing logged marked *reblock?*), focus, triage count, overdue/due-soon
  (only when present), today's log entries, today's interruptions, AI-changes
  and backup-health banners.
- **Log**: entries by `work_date` desc.

### 5.5 Placement (triage) actions
Keys (TUI Triage/Plan modes, web buttons, CLI): `k` keep (current rank) · `t`
top · `b` bottom · `p` place before/after N · `d` defer · `B` block · `x` done ·
`D` drop · `s` skip (not a placement). Bulk: "accept all `new` → bottom".
A placement requires `If-Match` (a stale screen cannot acknowledge a reason it
never showed) and atomically: deletes all reason rows, sets `last_placed_at =
now`, sets `due_ack` to the current due phase and `due_ack_date` to today.
`tkr triage --manual ID` and `undelete` add `manual`.
New tasks get `new` unless `add` has a placement flag (`--top`, `--bottom`,
`--after N`, `--before N`, `--defer D`, `--block T`, `--now`) (my call — the
core answer to "buried"). `--now` = place at top + set focus + record an
`interrupt` event against the previous focus/current block (my call, targets
"interrupts won").

### 5.6 Blocks: outcomes, slips, lifecycle

**Derived outcome** of a block of task T on date D: `done` if a
non-deleted `complete` entry for T has `work_date = D`; else `worked` if a
non-deleted `work` entry for T has `work_date = D`; else `missed`. Any entry that
day counts for every block of T that day; recap can override.

**Resolution.** A `planned` block resolves by derivation
(`outcome_source = auto`) at the end of its day (midnight), or earlier when an
op ends it early (below). User-set outcomes are never re-derived.

**Recompute.** Any op that creates, edits, relinks, deletes, undeletes, or
reverts an entry re-derives every `auto`-resolved block for the **union of the
before- and after-image `(task_id, work_date)` pairs**, in the same
transaction.

**The one slip rule.** Whenever a block transitions to `slipped = 1` — at
resolution, by recompute, by an early end, by a recap outcome set to `missed`,
or by a cancellation in the table below — set `slipped_at = now` and, if the task is open and the same op is not a
placement on that task, add the `slipped` reason. Whenever a block transitions
to `slipped = 0` (unslip), remove the task's `slipped` reason if no other block
of the task has `slipped = 1` and `slipped_at > last_placed_at`.

**The continue rule (Sean).** When a block of an open task resolves (or
re-derives) to `worked` — the task was worked that day but isn't done — add the
`continue` reason, unless the task already has a planned block on a later date
or the same op is a placement on it. It is not a slip. If the block later
re-derives to `missed`, the pending `continue` is replaced by the slip rule; to
`done`, it is removed.

| Situation | `slipped = 1`? |
|---|---|
| Block resolves (or re-derives to) `missed` | yes |
| Unstarted block on **today** cancelled (`unblock`), moved to a later date, or its task deferred | yes |
| …unless the block was created < 15 min ago (fixing a planning mistake) | no (my call) |
| Unstarted block moved to another time the same day | no (row updated in place) |
| Unstarted block on a **future** day moved or cancelled | no |
| Task done / dropped / deleted | no |
| Started block ended early (`unblock`, `reblock`, defer, drop after its start) | resolved by derivation: `missed` ⇒ yes |

(Defer and reblock-to-later-date are placements, so their slip is recorded but
immediately acknowledged; `unblock` is not a placement, so the task goes to
triage.)

`reblock`: same-day unstarted → row updated in place; otherwise the old block
ends (cancelled `rescheduled`, or resolved by derivation if started) and a new
planned block is created; reblocking an already-resolved `missed` block just
creates a new block. Reblock is a placement.

Counts: **lifetime** = blocks of T with `slipped = 1`; **since last triage** =
those with `slipped_at > last_placed_at`.

**Lifecycle effects** (in the op's transaction):
- `done --date D` (D ≤ today): every `planned` block of T dated after D
  (started or not) → `cancelled/task_closed` (no slip); `planned` blocks on D
  that have started (all of them when D < today) resolve by derivation
  (`done`), unstarted ones (only possible when D = today) are cancelled;
  `auto`-resolved `missed` blocks dated after D → `cancelled/task_closed` +
  unslip; `worked`/`done` blocks are left alone; `auto`-resolved blocks on D
  re-derive (→ `done`).
- `drop` / `delete`: started planned blocks resolve by derivation; unstarted →
  `cancelled/task_closed`.
- `defer`: started planned blocks resolve by derivation; unstarted →
  `cancelled/deferred` (slip if today, per table).
- `reopen`: clears `completed_at`/`completed_date`; cancelled blocks stay
  cancelled.

### 5.7 Due and defer moments
- Date-only **defer** moment = 00:00 of that day. Date-only **due** moment = end
  of that day (24:00). Timed values use their minute.
- Defer moment must be **strictly before** the due moment and the defer's
  date ≤ `due_date` (so a task always surfaces on or before its due
  day); setting a due that violates this for an existing defer, or deferring
  past it, is rejected with a message. Deferring an
  already-overdue task is rejected ("change the due date first").
- Changing `due_date`/`due_min` resets `due_ack = none` and removes pending
  `due`/`overdue` reasons.

### 5.8 Reconciliation (authoritative transition spec)
`Reconcile(now)` is idempotent and purely state-based. It runs at startup,
every minute, before every op, and before triage/today/status reads (reads may
trigger these *system* writes; read handlers never perform user ops). It does a
cheap read-only check first and takes the writer only when there is work. One
transaction, actor `system`:
1. **Surface**: open tasks with defer moment ≤ now → clear defer, add
   `surfaced` (step 2 then applies to them normally).
2. **Due**: open, non-deferred tasks with `due_date ≤ today`, `due_ack =
   none`, and no planned block today → add `due`.
3. **Overdue**: open tasks with due moment ≤ now, unless `due_ack = overdue`
   and `due_ack_date = today` → add `overdue`. A placement acknowledges it for
   the day; it **re-enters triage every day while overdue** **(Sean)**.
4. **Resolve** planned blocks on days < today by derivation; apply the slip
   rule and the continue rule.
5. **Focus**: clear focus if its task is ineligible (not open, deleted,
   deferred) or the day changed.
Every change writes an event. Downtime of any length is handled.

### 5.9 Completion, reopen, drop, delete
- `done ID [--as TITLE] [--date D] [--log TEXT] [-m NOTE]`: status done,
  `completed_date` = D (default today, never future); optional retitle (own
  event); always creates one `complete` entry (title = `--log` text, else final
  task title; notes = `-m`); lifecycle effects §5.6; clears reasons and focus.
- `reopen ID [--remove-entry]`: status open, clears completion fields, resets
  `due_ack = none`, adds `reopened`; completion entry marked `reopened`, or
  soft-deleted with `--remove-entry` (needs `destroy`). Blocks cancelled by the
  completion stay cancelled; the CLI hints that `undo` of the `done` op restores
  them exactly.
- `drop ID`. `delete ID` / `undelete ID` (tombstone; CLI confirms or `--yes`).
  `drop` and `delete` clear the task's triage reasons and focus.
- Every op echoes what it did (`#42 'Set up keycloak…' completed`).

### 5.10 Undo and revert
- `op_id` = random 128-bit ID; every event carries it. Actors: `owner` (socket
  owner token and web session, channel recorded separately), `token:<name>`
  (incl. MCP), `system`.
- **Revert** (`POST /api/v1/ops/{op_id}/revert`) is itself a new op that goes
  through normal service validation:
  - refused if any entity it touched has a later event from any other op
    (including `system`), listing the conflicts;
  - field before-images restored; inserted rows tombstoned; triage reasons
    restored as a delta (only the rows that op added/removed); rank restored as
    a move relative to the recorded neighbor;
  - all invariants (§5.3, §5.7) checked on the result; violation → refused;
  - versions advance; block recompute runs.
  - Requires the scopes of the reverted op. Ops touching purged content are
    not revertible.
- **Undo/redo stacks** (classic editor semantics, persisted per actor in
  `undo_stack(actor, stack, seq, op_id)`): a forward op is pushed on the undo
  stack and clears the redo stack. `tkr undo` pops the undo stack, reverts that
  op, and pushes it on the redo stack. `tkr redo` pops the redo stack,
  re-applies the op (by reverting its undo), and pushes it back on the undo
  stack. Undo/redo ops never land on either stack themselves. For conflict
  checks, an op together with the reversal that cancelled it is a net-zero pair
  and is ignored. A per-op revert from AI changes is a forward op of the
  owner. System ops are not revertible.
- Revert restores before-images directly (they do not themselves trigger the
  slip rule); the recompute and reconcile that follow apply it normally. A
  restored `planned` block on a past date resolves at the next reconcile.

### 5.11 Purge
For secrets pasted by mistake. `tkr purge task|entry ID [--with-entries]`
(API op, `destroy` scope **and** the password re-entered — a human-only check,
§10.5; exempt from idempotency storage, §7.5):
- target must be tombstoned first;
- **ledger first**: before scrubbing, append a content-free intent record to
  `<datadir>/purge-ledger` (0600) listing the **exact set** of entity IDs to be
  scrubbed (task, its `complete` entries, and every entry included by
  `--with-entries`), then fsync file and dir. Startup, `restore`, and `import`
  re-apply every ledger record (pending or complete); scrubbing is idempotent,
  so a crash anywhere leaves the purge either finished on restart or re-applied.
  Re-running purge on an already-purged item ensures its ledger record exists;
- in one transaction (SQLite `secure_delete` on; FTS5 tables created with the
  FTS5 `secure-delete` option): scrub title/notes of the target (for a task:
  its `complete` entries, and with `--with-entries` all its linked entries;
  without it the CLI lists linked entry IDs) to `[purged]`/empty; scrub those
  content fields in every event row for those entities (their FTS rows were
  already removed when they were tombstoned);
  mark ops touching them non-revertible; record a content-free `purge` event;
- then `wal_checkpoint(TRUNCATE)`, retried with backoff until it truncates; if
  it cannot within 30 s, report loudly and retry in the background
  (`purge_checkpoint_pending` banner);
- mark the ledger record complete (records are never removed, so purged
  content never silently returns from an older snapshot or export);
- print which backup snapshots (and `*.pre-restore-*` dirs) still contain the
  content; `--scrub-backups` (same password check) first takes a fresh verified
  backup, then rewrites each older snapshot (copy → apply the purge →
  `VACUUM INTO` → integrity-check → atomic replace). Backups are never simply
  deleted by purge. Log lines and exports made earlier are out of reach and the
  CLI says so.

### 5.12 Time and dates
- Server timezone is **explicit config** (IANA). `init` detects it from
  `/etc/localtime`, confirms, writes it; `serve` refuses to start without it.
  Never `time.Local`.
- **Days roll over at midnight** in that zone **(Sean)**; the date of an instant
  is its civil date there. (The earlier `day_starts_at` mechanism was dropped.)
- **Minutes**: minutes since midnight of the stored date, `[0, 1440)` (block
  ends may be 1440). Applies to block times, `--at`, defer/due minutes, and
  "now".
- (date, minute) → instant via the zone. Nonexistent wall times → first valid
  instant after (defer/due) or rejected (blocks). Ambiguous (fall-back) → the
  earlier occurrence, chosen explicitly by offset (not left to `time.Date`).
- Floating wall-clock semantics: changing the timezone keeps wall-clock meaning
  (documented).
- Week starts Monday (configurable).
- Relative-date grammar (`datespec`): `today`, `yesterday`, `tomorrow`, weekday
  names, `+3d`/`-2d`/`+2w`, ISO dates, optional `HH:MM`. Weekday names mean the
  most recent occurrence **including today** for `log`/`done --date`, the next
  occurrence **excluding today** for `defer`/`due`/`block`. Clients resolve
  against `GET /api/v1/clock`; the server validates and echoes resolved dates.

## 6. Architecture

```
            ┌──────────────── taskronin (single Go binary) ─────────────────┐
  CLI ──┐   │ serve: HTTP ─┬─ /api/v1  (bearer token; socket: + peer UID)   │
  TUI ──┼──►│              ├─ /ui      (session cookie + CSRF)              │
  MCP ──┘   │              └─ reconciler, backup scheduler                  │
 (stdio,    │   adapters ──► service layer (rules, authz) ──► store (SQLite)│
  token)    └───────────────────────────────────────────────────────────────┘
  transports (v1): unix socket (CLI/TUI/MCP)  |  TCP 127.0.0.1 (web UI)
```
- Subcommands: `serve`, `init`, `passwd`, `token`, `backup`, `restore`,
  `export`, `import`, `migrate`, CLI verbs, `tui`, `mcp` — all in the one
  `tkr` binary (my call).
- **Service layer** owns every rule and every authorization check (§10.3). HTTP
  API and web handlers are thin adapters; web calls the service in-process
  through the same authz and validation.
- **Shared packages**: `api/` (types), `client/` (Go client for CLI/TUI/MCP),
  `datespec/`, `textsafe/` (validation + terminal sanitizer).
- **Single instance**: `serve` holds an exclusive `flock` on
  `<datadir>/taskronin.lock`. Offline commands (`init`, `migrate`, `import`,
  `restore`) require the lock; `backup`/`export` go through the
  API (admin) while the server runs. Only `init` ever creates a database.
- **Local start**: systemd `--user` service + `.socket` unit (socket
  activation via `LISTEN_FDS`). If unreachable, the CLI prints the user's exact
  input so nothing is lost.
- `plan`, `recap`, `triage -i` launch the TUI in that mode (one interactive
  engine) (my call).

## 7. Storage

### 7.1 SQLite
- `modernc.org/sqlite` (pure Go) (my call).
- All connections: `journal_mode(WAL)`, `synchronous(FULL)`, `foreign_keys(1)`,
  `busy_timeout(5000)` as DSN pragmas. **Writer** `*sql.DB`
  (`SetMaxOpenConns(1)`, `_txlock=immediate`, `secure_delete(1)`); **reader
  pool** (deferred transactions, `query_only(1)`).
- Every op: its changes, events, and idempotency record in one transaction.
- `PRAGMA quick_check` at startup; on failure refuse to start, point to
  `restore`.
- `umask 077`; DB, WAL, backups, exports, drafts 0600; dirs 0700.
- `last_used_at`/`last_seen_at` coalesced in memory, flushed ≤ 1/min.
- `settings` holds `db_generation` (random; new on restore).

### 7.2 Migrations
Embedded, numbered, forward-only; each migration + its `schema_migrations` row
(with checksum) in one transaction; a verified backup must succeed first.
Refuse to open a DB with a newer schema or mismatched checksums. Committed
migration files are never edited.

### 7.3 Backups and restore
- Daily, plus a catch-up at startup if the last success is > 24 h old.
- `VACUUM INTO <dir>/.tmp-<ts>` → open fresh connection → `PRAGMA
  integrity_check` → close → fsync file → rename → fsync dir → rotate (7 daily,
  4 weekly) only after success. Backup files 0600.
- Rotation is the configured retention policy, not a manual prune, so routine
  rotation needs no password. Retention has a floor (≥ 3 daily, ≥ 1 weekly);
  **lowering** retention requires the password and is recorded prominently in
  history and AI changes.
- `last_backup_at`/`last_backup_error` stored; banner in CLI/TUI/web if the last
  backup failed or is > 48 h old. Backup dir configurable; docs give an
  off-host copy example (restic/rsync).
- `tkr restore SNAPSHOT` (offline, holds the lock). Everything is
  prepared on a standalone temp copy before the live DB is touched:
  1. integrity-check the snapshot; require schema ≤ binary;
  2. copy it to `<datadir>/.restore-tmp` (0600); open it in **rollback-journal
     mode** (`journal_mode=DELETE`, so no `-wal` file exists for it); migrate
     it to the binary's schema;
  3. read from the current DB if readable: auth state (password hash, tokens)
     and sequence high-water marks;
  4. in the temp copy: install that auth state; set each sequence to
     max(current, snapshot); new `db_generation`; revoke all web sessions. If
     the current DB is unreadable: revoke all tokens, mint a fresh `owner`
     token (written to `owner.token` at the end of the swap), disable web login
     until `tkr passwd --reset`, set each sequence to max over snapshot and
     all readable backups + 10 000, and say so;
  5. re-apply the purge ledger; `integrity_check`; **close every connection**;
     confirm no `-journal`/`-wal` sidecar remains; fsync the file;
  6. write the restore journal (paths + current step), fsync it and the dir;
     move current DB **and its `-wal`/`-shm`** to `*.pre-restore-<ts>/`; fsync;
     rename temp into place; fsync dir; remove journal; fsync dir.
  `serve` refuses to start while a restore journal exists; `tkr restore
  --resume` completes the recorded swap and `--rollback` moves the
  pre-restore files back, both driven by the journal.
- `export` → versioned JSON (user data + sequence high-water marks; no tokens,
  sessions, or password hash), 0600 via temp+rename. `import` only into an
  empty DB; restores high-water marks.

### 7.4 Events
`events(id, op_id, at, actor, channel, entity, entity_id, task_id, action,
before_json, after_json, revertible)` — field-level before/after; rank changes
record the neighbor. Drives history, undo/revert, AI-changes review.

### 7.5 Idempotency
`idempotency(key, actor, request_hash, op_id, status_code, entity_refs,
created_at)` — kept 7 days. `request_hash` = HMAC-SHA256 with a server-held
key (in `settings`, never exported) over actor | method | path | canonical body.
**No response bodies stored**: a replay returns the current representation of
the referenced entities. **Credential-bearing requests** (web login, `passwd`,
password re-entry for purge/scrub) are exempt from idempotency storage
entirely; a test asserts no stored row or log line depends on a password.

### 7.6 Schema sketch
```sql
tasks(id INTEGER PRIMARY KEY AUTOINCREMENT, title, notes, status, rank INTEGER NOT NULL,
      last_placed_at, defer_date, defer_min, due_date, due_min, due_ack, priority, origin,
      completed_at, completed_date, created_at, updated_at, deleted_at, version)
task_triage(task_id REFERENCES tasks(id), reason, since, PRIMARY KEY(task_id, reason))
log_entries(id INTEGER PRIMARY KEY AUTOINCREMENT,
      task_id INTEGER REFERENCES tasks(id) ON DELETE RESTRICT, kind, title, notes,
      work_date, occurred_min, reopened, created_at, updated_at, deleted_at, version)
time_blocks(id INTEGER PRIMARY KEY AUTOINCREMENT,
      task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT, date, start_min,
      end_min, status, outcome_source, cancel_reason, slipped, slipped_at,
      created_at, resolved_at, deleted_at, version)
tags(id, name UNIQUE COLLATE NOCASE), task_tags(task_id, tag_id)  -- + tasks.project (optional)
settings(key PRIMARY KEY, value)   -- focus_task_id, focus_date, db_generation,
                                   -- last_backup_*, ai_reviewed_at, tz, idem_hmac_key
events(...), idempotency(...), undo_stack(actor, stack, seq, op_id)
api_tokens(id INTEGER PRIMARY KEY AUTOINCREMENT, name UNIQUE, secret_hash BLOB,
      scopes, created_at, expires_at, last_used_at, revoked_at)
web_sessions(id_hash BLOB PRIMARY KEY, csrf_secret, created_at, expires_at, last_seen_at)
tasks_fts, log_entries_fts  -- FTS5 external content, 'secure-delete' on; triggers only
                            -- touch FTS for rows with deleted_at IS NULL (old and new
                            -- image); tombstoning removes the row; purge (target already
                            -- tombstoned) never touches FTS; tests run FTS5 integrity-check
```

## 8. API

- REST + JSON under `/api/v1`; errors RFC 9457 problem+json; no CORS headers.
- Resources: `tasks`, `entries`, `blocks`, `tags`, `events`, `ops`.
- Actions: `POST /tasks/{id}/complete|reopen|drop|delete|undelete|purge|move|defer|place|triage`,
  `POST /entries/{id}/purge`, `POST /blocks/{id}/reblock`, `POST /focus`,
  `POST /ops/{op_id}/revert`, `POST /undo`.
- Aggregates: `GET /today`, `GET /status`, `GET /report`, `GET /clock`,
  `GET /ai-changes`. Aggregate `ETag` = hash of the response body computed after
  reconcile (so wall-clock changes like the current block are reflected).
- **Concurrency**: tasks, entries, blocks → `ETag: "<db_generation>-<version>"`;
  mutations of existing records require `If-Match`; `412` returns the current
  representation. Clients never auto-replay a stale edit. `$EDITOR` drafts are
  kept in `$XDG_STATE_HOME/taskronin/drafts/` (0700/0600) until confirmed.
- **Idempotency**: every non-idempotent POST carries `Idempotency-Key`; same
  key + same hash → replay (current representation); same key + different hash
  → 422. Web forms carry a per-render nonce used as the key.
- Strict JSON (`DisallowUnknownFields`), 1 MiB body limit, service-layer
  validation, cursor pagination. `GET /healthz` unauthenticated, no data.

## 9. Interfaces

### 9.1 CLI (`tkr` **(Sean)**)
```
tkr                         dashboard (= today)
tkr add TITLE [--top|--bottom|--after N|--before N|--defer D|--block T|--now] [--due D] [-t TAG]... [-P PROJECT] [-p H] [-e]
tkr log [ID] TEXT [-N] [--date D] [--at HH:MM] [-e] [-m NOTE]
tkr done ID [--as TITLE] [--date D] [--log TEXT] [-m NOTE]
tkr reopen ID [--remove-entry] | drop ID | delete ID | undelete ID | undo | redo
tkr purge task|entry ID [--with-entries] [--scrub-backups]
tkr plan | recap [--date D] | triage [-i] [--manual ID]
tkr move ID top|bottom|before N|after N | keep ID
tkr defer ID D            tkr due ID D|none
tkr block ID HH:MM-HH:MM [--date D] | reblock BLOCKID HH:MM-HH:MM [--date D] | unblock BLOCKID
tkr now [ID|--clear]      tkr status --short
tkr today | upcoming | ls [-t TAG] [-P PROJECT] | show ID | edit ID | history ID | search QUERY
tkr tag ID +TAG|-TAG...    tkr project ID NAME|none
tkr report [--week|--last-week|--month|--from D --to D] [-t TAG] [-P PROJECT] [--format text|md|json] [--todo] [--upcoming]
tkr ai-changes [--revert OP]
```
`log` arguments: one → text; two with a numeric first → task ID + text; more →
error "quote your text". Implicit linking per §5.2.
`status --short` → e.g. `▶ #42 keycloak (→15:00) · triage 3 · due 1`; local
socket, 300 ms timeout, prints nothing on error.

### 9.2 TUI
Bubble Tea + Lip Gloss + Bubbles. Tabs: Today, Triage, Backlog, Upcoming, Log;
modes Plan (triage → backlog top → block today) and Recap (blocks of a date →
outcome `worked`/`missed` (user-set; flips per §5.6), log entries, `x` = normal
done; then unlinked entries of the day to link). Keys: `j/k`, `J/K` reorder,
`a` add, `l` log, `x` done, `B` block, `d` defer, `f` focus, `/` search, `e`
edit ($EDITOR), `u` undo, triage keys §5.5. Refresh by polling `GET /today`
with `If-None-Match` (my call).

### 9.3 Web
Server-rendered `html/template` + vendored htmx (no CDN, no Node build) (my
call). Views as the TUI; actions: add, log, done, triage keys, move buttons,
block/defer, focus, revert AI changes. Responsive. htmx config
`allowEval:false`, `allowScriptTags:false`, `selfRequestsOnly:true`; no
`hx-on`; GET handlers perform no user ops.

### 9.4 MCP
`tkr mcp` = stdio MCP server (official Go SDK) that **always requires its own
token** and calls the API via `client/`. Default MCP token scopes: `read` +
`capture` + `write` — the AI may also complete, defer, schedule, and edit, but
never drop, delete, purge, or administer **(Sean)**.
Tools: `today`, `list_tasks`, `get_task`, `search`, `work_report` (read);
`add_task` (always lands in triage), `log_work` (capture; explicit `task_id` or
null); `complete_task`, `reopen_task`, `defer_task`, `set_due`, `block_time`,
`update_task` (title/notes/tags/project/priority), `place_task` (write).
Annotations set (`readOnlyHint`, `destructiveHint=false`); user content returned
only as structured JSON fields; per-token mutation cap (default 60/hour).
AI-logged work may un-slip a block (my call); every AI op appears in **AI
changes since last review** (dashboard banner, `ai-changes`, web) with per-op
revert; reverting an AI log re-slips per the one slip rule. Because the AI can
complete and edit, the AI-changes banner is prominent in the dashboard and
`status --short` (e.g. `· ai 3`) until reviewed.

## 10. Security

### 10.1 Threat model
| Threat | In scope? |
|---|---|
| Other local users on client machines | yes |
| Other local users on the **server host** | Sean's workstation is single-user **(Sean)**. They could not reach the socket or DB anyway; the only exposure would be the plain-HTTP loopback web cookie (cookies aren't port-isolated), so plain-HTTP loopback web is an explicit single-user-host setting (§10.2). |
| Malicious web page in Sean's browser (CSRF, DNS rebinding) | yes |
| Network attacker when server is remote (sniffing, brute force, DoS) | yes |
| Prompt injection in task text steering an AI client via MCP | yes |
| Terminal escape / bidi injection via stored text | yes |
| Stolen client token file | yes (scoped, revocable, expiring) |
| Corruption, bad migration, accidental deletion, bad/interrupted restore | yes |
| **Processes running as Sean's own UID — including AI agents with shell access** | **trusted for access**: they can run `tkr` (reading `owner.token`) or read the DB and backups directly, stop the server, or delete files. MCP scopes constrain only MCP-only clients. Within Taskronin's API, protection is recoverability: every op revertible, tombstones, verified backups, and the only irrecoverable ops (purge, backup scrub) require the human's current password. Outside the API (raw file access) nothing in-process can help; off-host backups are the answer. Documented plainly. |

### 10.2 Transports
- **Unix socket** `$XDG_RUNTIME_DIR/taskronin/sock` (dir 0700, socket 0600;
  fallback `$XDG_STATE_HOME/taskronin/` with ownership check). Stale socket
  unlinked only while holding the instance lock.
  **Socket auth = owner token + peer UID**: `init` creates an `owner` token
  (all scopes) and writes it to `$XDG_STATE_HOME/taskronin/owner.token`
  (0600; docs: never sync or commit it); the local CLI/TUI send it
  automatically. **The owner token is accepted only on socket connections that
  pass the peer-UID check** — never over TCP — so a leaked copy is useless
  off-host. Socket connections must
  *also* pass the `SO_PEERCRED` UID check (read in `http.Server.ConnContext`).
  Peer credentials alone never grant anything, so forwarders (`ssh -L`,
  `socat`, a proxy) cannot impersonate the owner. Requests over the socket
  carrying `Forwarded`/`X-Forwarded-*`/`Via` are additionally rejected. Docs
  say proxies use TCP loopback.
- **TCP** (web UI) binds `127.0.0.1` only. **v1 refuses non-loopback binds**
  (local-only deployment **(Sean)**). Kept as the design for a later remote
  mode: non-loopback requires TLS (cert + key, SIGHUP reload, TLS ≥ 1.2), a
  same-host proxy uses TCP loopback with `public_origin`, recommended over
  Tailscale/WireGuard with `tailscale cert`; clients trust system roots or
  `ca_file`, refuse `http://` to non-loopback hosts, never `InsecureSkipVerify`.
- **Plain-HTTP web UI** only on loopback and only with
  `web.allow_plain_loopback = true`; `init` sets it after confirming the host is
  single-user (true for Sean's workstation), and `serve` warns at startup if it
  later detects other human accounts.
- `http.Server` timeouts: `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s,
  `WriteTimeout` 60 s, `IdleTimeout` 120 s; `MaxHeaderBytes` 64 KiB.

### 10.3 Authentication and authorization
- **Two route classes, never mixed** (middleware invariant + test):
  `/api/v1/*` accepts only `Authorization: Bearer` (+ peer UID on socket), ignores
  cookies; `/ui/*` accepts only the session cookie. Cookie-only POST to
  `/api/v1` → 401.
- Every API request carries a bearer token (over the socket, the owner token
  file is used automatically); a token gets exactly its scopes.
- **Scopes**, enforced per operation in the service layer:
  `read` · `capture` (add task → triage; add `work` entry) · `write` (move,
  place, defer, block, focus, complete, reopen, update, relink, revert of own
  ops) · `destroy` (drop, delete, edit/delete existing entries,
  `reopen --remove-entry`, purge*) · `admin` (tokens, settings, backup,
  export). Revert requires the scopes of the reverted op. *Purge also requires
  the password (§10.5).
- **Web session** = owner with all scopes, except that token management is
  refused for web-session auth (CLI only).
- **Tokens**: `tkr_<id>_<base64url 32 bytes>`; lookup by id; SHA-256 of the
  secret compared with `subtle.ConstantTimeCompare`; shown once; named; scoped;
  optional expiry (prompted at creation); revocable; managed only via CLI with
  `admin`. Client token file must be 0600 or the client refuses.
- **Password**: always set by `init`; argon2id (t=3, m=64 MiB, p=1);
  verification (login and re-entry) behind a semaphore of 1 with per-IP +
  global token-bucket delays (no lockout). Online `passwd` requires the current
  password; a forgotten password is reset only offline (`tkr passwd
  --reset`, holds the lock), which also revokes all tokens and sessions and
  writes a fresh `owner.token`.
- **Web login**: session ID 32 random bytes stored hashed, regenerated on
  login; idle 7 d / absolute 30 d; `passwd` revokes all sessions.
- **Cookie**: over TLS `__Host-tr_session; Secure; HttpOnly; Path=/;
  SameSite=Lax`; plain-HTTP loopback (opt-in) `tr_session; HttpOnly; Path=/;
  SameSite=Lax`.

### 10.4 Web hardening
- CSRF: per-session token on every state-changing request **and**
  `Origin`/`Sec-Fetch-Site: same-origin` check (reject when both absent).
- `Host` header allowlist (DNS rebinding).
- CSP `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self';
  connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors
  'none'`; `X-Content-Type-Options: nosniff`; `Referrer-Policy: no-referrer`;
  `Cross-Origin-Opener-Policy: same-origin`; minimal `Permissions-Policy`.
- Markdown: goldmark without raw HTML → bluemonday (schemes `http`, `https`,
  `mailto`; `rel="noopener noreferrer"`; remote images rendered as links).

### 10.5 MCP and AI agents
Server-enforced scopes; MCP token always required; mutation cap; AI-created
tasks always land in triage; every op audited and listed in AI changes with
revert. Tool descriptions and structured outputs keep user text as data, but
that is not relied on as a control. **Password re-entry** is required (CLI
prompt on a TTY, or web form; sent in the request body field `password`,
verified through the rate-limited path, never stored or logged) for purge and
backup scrub — the only ops that cannot be reverted — so an agent acting as Sean
cannot do irrecoverable damage through Taskronin's API.

### 10.6 Text safety
- **Input**: valid UTF-8; strip BOM; titles: no C0, C1, or newline; notes: C0
  only `\n`/`\t`; reject C1, bidi controls (U+202A–U+202E, U+2066–U+2069),
  U+2028/U+2029; length limits.
- **Output to terminals and logs**: allowlist — any non-printable rune except
  structural `\n`/`\t` → U+FFFD; width via `uniseg`/`x/ansi`.
- `$EDITOR` invoked via shell-word splitting, never through a shell; temp files
  0600 in the drafts dir.

### 10.7 Other
Parameterized SQL only. Secrets never logged (`slog` redaction). Minimal
dependencies; `go mod verify`; `govulncheck`; vendored htmx with recorded
SHA-256. Hardened systemd units (`NoNewPrivileges`, `ProtectSystem=strict`,
`PrivateTmp`, `ProtectHome=read-only` + `ReadWritePaths=` data/state dirs).
Destructive CLI commands require confirmation or `--yes`.

## 11. Robustness
- WAL + `synchronous=FULL`; ops transactional with events and idempotency.
- Idempotent, state-based reconciliation; idempotent client retries.
- Crash-safe backup and restore (journaled swap).
- Graceful shutdown: stop accepting, drain, finish transaction, checkpoint WAL,
  release lock.
- Injected clock everywhere.
- Actionable client errors; capture text echoed on failure.
- Structured `slog` logs with request and op IDs.

## 12. Testing strategy
- Service tests with a fake clock: triage reasons and ordering; reconcile
  (downtime, DST, midnight rollover); derived outcomes and
  recompute over before/after pairs; the slip rule and table row by row
  (incl. AI-log revert re-slip); lifecycle effects; defer/block exclusivity;
  due/overdue phases incl. timed due; rank; revert of every op type incl.
  invariant refusals; undo/redo/undo sequences; idempotency; purge incl.
  crash between ledger write and scrub.
- Store tests on temp SQLite files; migrations from every prior version; backup
  → restore round-trip incl. WAL sidecars, sequence and auth carry-over,
  interrupted-restore journal; crash-during-backup simulation.
- API tests: route-class invariant, socket+`Forwarded` refusal, scopes matrix,
  ETag/generation conflicts, idempotency replay/mismatch, validation.
- Web tests: CSRF, Origin/Sec-Fetch-Site, Host allowlist, headers, escaping,
  markdown sanitization, plain-loopback opt-in.
- CLI golden tests; TUI model tests (`teatest`); MCP tool tests.
- Fuzz: `datespec`, `log` arg parsing, text validators, terminal sanitizer,
  markdown pipeline.
- `go test -race`, `golangci-lint` (incl. `gosec`), `govulncheck`.

## 13. Tech stack (my calls)
Go 1.26; stdlib `net/http`; `modernc.org/sqlite`; `spf13/cobra`; Bubble Tea /
Lip Gloss / Bubbles (v2, `charm.land` paths); `modelcontextprotocol/go-sdk`;
`golang.org/x/crypto/argon2`; `golang.org/x/sys/unix`; `yuin/goldmark` +
`microcosm-cc/bluemonday`; `BurntSushi/toml`; htmx (vendored). XDG paths.

## 14. Report format
Grouped by `work_date`; within a day by `occurred_min` (untimed after timed),
then `created_at`. `complete` entries prefixed `✓`; reopened ones suffixed
"(reopened)". Task annotation: current title in full on first appearance per
report, `[#ID]` after. `--todo` appends triage + backlog; `--upcoming` appends
upcoming. `-t`/`-P` filter by tag/project. No importer; the format follows
§4.1 **(Sean: start fresh)**.

## 15. Left to the builder
Package layout; column types; rank gaps/renormalization (atomic,
deterministic, never bumps `version`); TUI layout; polling interval; log format;
message wording.

## 16. Calls made by the designer (for review)
Stable integer IDs; new tasks → triage; priority as marker only; web via htmx;
`modernc` SQLite; `plan`/`recap` as TUI modes; 15-min slip grace for new
blocks; focus clears at day end and wins over the current block for implicit
linking (interactive CLI/TUI, today only); AI-logged work may un-slip; any
slip (incl. `unblock` of today's block) sends the task to triage; purge needs
the password; delivery order server+CLI+MCP → TUI → Web; no drag-and-drop in v1
web; single binary named `tkr`; `add --now` for interruptions; `continue`
ranks between `due` and `surfaced` in triage order.

## 17. Review history
Round 1: 4 reviewers (Opus, Fable, Codex, agy) — ~25 CRITICAL/HIGH.
Round 2: 3 reviewers — 16/16 round-1 items mostly resolved; 4–10 new HIGH.
Round 3: 3 reviewers — 2–6 HIGH (purge, restore ordering, re-slip, socket
proxy, loopback cookies, revert invariants). Addressed in v3.
Round 4: agy — no HIGH; Opus — 2 HIGH (peer-cred-only socket auth via header-less
forwarders; password-derived hashes in idempotency rows); Codex — 1 HIGH
(restore temp copy's WAL). Addressed in v4 with FTS5 secure-delete, undo stack,
strict defer<due, purge ledger, offline password reset.
Round 5: agy and Opus — NO CRITICAL/HIGH; Codex — 1 HIGH (crash between purge
commit and ledger append). Addressed in v5 (ledger-first purge with exact ID
sets; explicit undo/redo stacks; owner token socket-only under
`$XDG_STATE_HOME`; retention floor + password to lower; FTS trigger rules).

## 18. Question round 1 — Sean's answers (2026-10-25)
1. TaskWarrior failed because it was **out of sight** and **interrupts won**.
2. Dates: upcoming **and** due; due dates are rare (a couple a year).
3. Overdue: re-enter triage **every day** while overdue.
4. Day rollover: **midnight**.
5. Worked-but-unfinished block: **back to triage as `continue`** (not a slip).
6. Deployment: **this workstation only**; single-user host.
7. MCP: read + add + log **and** complete/defer/edit (no deletes).
8. Organizing: **tags**, plus an **optional** project that is never enforced.
9. "Related work" = the log entries attached to a task (already designed).
10. v1 extras: **none**.
11. Importer: **no**, start fresh.
12. Records are **log entries** (`tkr log`).
13. Command: **`tkr`**.
14. Platforms: Linux (implied by 6).
15. Git: commit on a local branch.

## 19. Open questions (round 2)
Collected during detailed design/planning; see the plan document.
