# Taskronin — Behavior Catalog (executable-spec scenarios)

Status: DRAFT 3, derived from the design spec Draft v6
(`2026-10-25-taskronin-design.md`, cited as D§n) and aligned with the
architecture Draft A1 after its review rounds 1–2 (`2026-10-25-taskronin-architecture.md`,
cited as A§n; change maps A§14.4/14.5, design deltas A§14.2 D36–D37).
Every scenario here is meant to become one or more service-layer tests with an
injected clock (`testutil.Scenario`). Ambiguities found while writing Draft 1
were ruled on; the rulings are in §16 and are applied in the scenarios.
Behaviour where the architecture and this catalog disagree is listed in §17.

## 0. Conventions used by every scenario

**Clock / zone.** Server timezone `America/Denver` (MDT = UTC−6 until
2026-11-01 02:00; MST = UTC−7 until 2027-03-14 02:00; MDT again after).
Unless a scenario says otherwise: **now = Wed 2026-10-28 10:00 MDT
(16:00Z)**, today = 2026-10-28. Dates without a year are 2026. Weekday labels
for the base week: Mon 10-26, Tue 10-27, Wed 10-28, Thu 10-29, Fri 10-30, Sat
10-31, Sun 11-01, Mon 11-02. `…Z` marks a UTC instant; everything else is Denver
wall-clock. Midnight boundaries that matter:

| Local day start | Instant |
|---|---|
| 2026-10-29 00:00 (MDT) | 2026-10-29T06:00Z |
| 2026-11-01 00:00 (MDT) | 2026-11-01T06:00Z |
| 2026-11-02 00:00 (MST) — ends the 25-hour fall-back day | 2026-11-02T07:00Z |
| 2027-03-14 00:00 (MST) | 2027-03-14T07:00Z |
| 2027-03-15 00:00 (MDT) — ends the 23-hour spring-forward day | 2027-03-15T06:00Z |

**Actors.** `owner` = socket owner token (all scopes). `mcp` = `token:ai`
with the default MCP scopes `read+capture+write`. `system` = reconciler.

**Fixtures.** Each scenario starts from an empty DB and builds state in
*Given*. `#n` = task n. `#1{new,due}` = task 1 whose reason rows are `new`
and `due`. `B1(#1, 10-28 13:00-15:00)` = planned block; blocks are created
≥ 15 min before *When* unless marked `fresh` (< 15 min old). `W(#1, D, "t")`
= `work` entry, `C(#1, D)` = `complete` entry. Fresh DB → `#1` has version 1.

**Operations** are the `service` methods of A§3 (op kind in parentheses):
`AddTask(title, flags…)` (`task.add`), `UpdateTask` (`task.update`),
`SetDue(task, moment|nil)` (`task.due`), `Place(task, how, Expect)` with how ∈
keep · top · bottom · before N · after N (`task.place`), `AcceptNew(items)`
(`task.accept_new`), `Defer(task, moment|nil)` (`task.defer`; nil clears the
defer), `MarkManual` (`task.manual`), `Complete(task, flags…)`
(`task.complete`), `Reopen(task[, remove_entry])` (`task.reopen`), `Drop`,
`Delete`, `Undelete` (`task.drop|delete|undelete`), `Block(task, date,
HH:MM-HH:MM)` (`block.create`), `Reblock(block, date, HH:MM-HH:MM)`
(`block.reblock`), `Unblock(block)` (`block.unblock`), `SetOutcome(block,
worked|missed|auto)` (`block.outcome`), `AddEntry(task|null, text, date[, at])`
(`entry.add`), `UpdateEntry`, `RelinkEntry`, `DeleteEntry`, `UndeleteEntry`
(`entry.update|relink|delete|undelete`), `SetFocus(task|null[, interrupt])`
(`focus.set`), `Revert(op)`, `Undo()`, `Redo()` (`op.revert|undo|redo`),
`Reconcile(at T)` (`system.reconcile`). Purge is the **offline** command
`tkr purge` (`purge.task|entry` via `PurgeLive`, A§12.6, §14) — there is no
API/web/MCP purge.
Datespec arguments are written as the CLI would type them; the service receives
the resolved `Moment`/`BlockSpan`/`Date`. Every mutation of an existing record
carries the correct `If-Match`/`Expect` unless the scenario is about it.

**Shorthand for expected state.**
- `PLACED(#n)` ≡ reason rows of #n deleted; `last_placed_at = now`,
  `last_placed_seq = op.seq`;
  `due_ack = phase(#n)` where phase is `overdue` if due moment ≤ now, else
  `due` if `due_date ≤ today`, else `none`; `due_ack_date = today`;
  `version++`; within the op, `slipped`/`continue` additions for #n are
  suppressed (`o.placed[task]`, A§4.4). Placements (A§3 "Pl" column): `Place`
  (keep/top/bottom/before/after), `AcceptNew`, `Defer` (incl. nil), `Block`,
  `Reblock`, `Complete`, `Drop`, and `AddTask` with a placement flag incl.
  `--now` (A§14 D6: `done` and `drop` are placements; `delete` is not).
  A placement on a deferred task also clears the defer (A§14 D5).
- `slip(B)` ≡ `B.slipped = 1`, `B.slipped_at = now`, `B.slipped_seq = op.seq`.
  `unslip(B)` ≡ `B.slipped = 0` (`slipped_at`/`slipped_seq` are left as history).
- `L/S` = slip counts of a task: lifetime / since last triage
  (`slipped = 1 ∧ slipped_seq > COALESCE(last_placed_seq, 0)`).
- **Ordering and acknowledgement use `ops.seq`** (A§4.2, S3): `last_placed_seq`,
  `slipped_seq`, `resolved_seq`, `task_triage.seq`. A slip and a placement in
  the same op share one seq, so `>` is false and the slip counts as
  acknowledged. `now` is read once per transaction; every `*_at` column is
  informational (display, history). In fixtures, timestamps imply seq order
  (a later op has a higher seq); where a scenario writes `slipped_seq >
  last_placed_seq` it means exactly that seq comparison.
- Events recorded by `recompute` (block re-derivations and the `slipped`/
  `continue` changes their hooks make) carry `derived = 1` (A§2.7, R2-1):
  revert never replays them, it re-derives through the normal hooks instead.
- A block row is written `B{status, src, slipped, cancel}` e.g.
  `B1{missed, auto, 1, –}` or `B2{cancelled, –, 0, task_closed}`. "started"
  = start instant ≤ now; "ended" = end instant ≤ now; containment is on
  instants (A§2.1).
- Events are named `entity:action target` with A§2.7 names: `task:insert #1`,
  `task:update #1 (fields)`, `task:move #1`, `task:tombstone`/`untombstone`,
  `triage:add #1 slipped`, `triage:remove #1 continue`, `block:insert B1`,
  `block:update B1` (every block status/slip change, incl. resolution),
  `entry:insert`/`update`/`tombstone`/`untombstone`, `focus:set` (clearing is a
  `focus:set` to null), `interruption:insert`/`tombstone`. "No events" means
  the op recorded nothing and finished as `NoChange` (`op_id = null`, nothing
  pushed on the undo stack, A§4.2 step 12) or the reconcile wrote nothing.
- Reconcile runs inside the op's transaction before the user op (A§4.2 step 7)
  as its own `system.reconcile` op; scenarios that say "`Reconcile(T)` then
  `Op`" may be driven by the op alone with the clock at T.

**Error codes** (A§2.8; HTTP status in parentheses): `validation` (422,
field-level: spans, dates, defer/due rule, past block, DST gap), `malformed`
(400), `not_found` (404), `deleted` (409: mutating a tombstoned entity),
`conflict` (409: illegal status transition, ineligible task for block/focus,
overlap, invariant violation, unblock/outcome on the wrong block state),
`nothing_to_undo` / `nothing_to_redo` (409, empty stack), `revert_conflict`
(409, lists conflicts; also "already reverted by <actor>"), `not_revertible` (409),
`precondition_failed` (412, body carries the current representation),
`precondition_required` (428, missing `If-Match`), `idempotency_key_required`
(428), `idempotency_mismatch` (422), `forbidden` (403: scope, channel),
`peer_rejected` (403: socket guard), `password_required` / `password_invalid`
(403; online only for web login and retention lowering — purge prompts on a
TTY), `unauthenticated` (401), `rate_limited` (429), `busy` (503). An op that
errors changes nothing and writes no events; the reconcile that ran before it
is kept (A§4.3 step 17). The one exception is a `committedError` from
`Undo`/`Redo` (A§4.6): the unusable stack entry is popped and the 409 is
stored for the idempotency key, but no ops row or event is written.

---

## 1. Triage (TRI)

| ID | Given | When | Then |
|---|---|---|---|
| TRI-01 | empty | `AddTask("A")` (owner) | #1 open, rank = bottom, `#1{new}` with `since = now`, `last_placed_at = null`, `due_ack = none`, `origin = owner`. In Triage view, not in Backlog. `task:insert #1`, `triage:add #1 new`. |
| TRI-02 | backlog [#1, #2] (no reasons) | `AddTask("B", --top)` | #3 rank above #1; `#3{}`; `last_placed_at = now`, `due_ack = none`, `due_ack_date = today`. Backlog [#3, #1, #2]. No triage events. |
| TRI-03 | backlog [#1, #2] | `AddTask("B", --bottom)` | #3 rank below #2; `#3{}`; PLACED. Backlog [#1, #2, #3]. |
| TRI-04 | backlog [#1, #2] | `AddTask("B", --after 1)` / `--before 2` | both give [#1, #3, #2]; PLACED. `--after 99` → `not_found`; `--after` a deleted task → `deleted` (409). |
| TRI-05 | empty | `AddTask("B", --defer tomorrow)` | #1 `defer_date = 10-29`, `defer_min = null`, `#1{}`, PLACED. In Upcoming; not in Triage/Backlog. |
| TRI-06 | empty | `AddTask("B", --block 13:00-15:00)` | #1 PLACED; `B1(#1, 10-28 13:00-15:00){planned, –, 0, –}`. Backlog [#1]. `block:insert B1`. |
| TRI-07 | see INT-01 | `AddTask("B", --now)` | #n at rank top, `#n{}`, PLACED, focus = #n, `interrupt` event (details INT-01). |
| TRI-08 | empty | `AddTask("B", --due tomorrow)` | `due_date = 10-29`, `due_min = null`, `due_ack = none`; `#1{new}` — `--due` is **not** a placement flag. |
| TRI-09 | empty | `AddTask("B", --top, --defer tomorrow)` | `validation` (at most one placement flag). Same for any two of `--top --bottom --after --before --defer --block --now`. |
| TRI-10 | `#1{new}` at rank r between #2 and #3 (backlog [#2, #1?, #3] — #1 is excluded from Backlog while it has reasons but keeps its rank) | `Place(#1, keep, If-Match v1)` | PLACED(#1); rank unchanged → Backlog [#2, #1, #3]. `task:update #1 (placement fields)`, `triage:remove #1 new`. |
| TRI-11 | same | `Place(#1, top)` / `Place(#1, bottom)` | PLACED; Backlog [#1, #2, #3] / [#2, #3, #1]. Rank event records the neighbor (before: after #2; after: before #2 / after #3). |
| TRI-12 | same | `Place(#1, before 3)` / `after 3` | PLACED; [#2, #1, #3] / [#2, #3, #1]. `before 99` → `not_found`; `before` a deleted task → `deleted`. |
| TRI-13 | `#1{slipped,new}` | `Defer(#1, "+2d")` | PLACED(#1); `defer_date = 10-30`. In Upcoming. `triage:remove #1 slipped`, `triage:remove #1 new`. |
| TRI-14 | `#1{new}` | `Block(#1, today, 13:00-15:00)` | PLACED(#1); block created. |
| TRI-15 | `#1{new}` | `Complete(#1)` | status done; PLACED(#1) — `Complete` **is** a placement (A§14 D6): `last_placed_at = now`, `due_ack = phase`, `due_ack_date = today`, reasons cleared (see LIF-01). |
| TRI-16 | `#1{new}` | `Drop(#1)` (owner; needs `destroy`) | status dropped; PLACED(#1) (D6). `mcp` → `forbidden`. |
| TRI-17 | `#1{new}` v1 | `Skip(#1)` (TUI `s`) | No request / no change: version 1, reasons `{new}`, no events. |
| TRI-18 | `#1{new}` (since 09:00), `#2{new}` (since 09:30), `#3{new,slipped}`, `#4{surfaced}`; backlog [#9] | `AcceptNew(items = [{#1, etag}, {#2, etag}])` | #1, #2 PLACED as `Place{bottom}` each, in item order → Backlog [#9, #1, #2]. #3, #4 untouched. One op (`task.accept_new`), undone as one; `OpResult.Tasks` = [#1, #2]. Only tasks whose reason set is exactly `{new}` qualify (R1): `AcceptNew([{#3, etag}])` → `conflict`, whole op refused (all-or-nothing); a stale item etag → `precondition_failed`, nothing applied. |
| TRI-19 | `#1{new}` v3 as shown on a screen; then `Reconcile` adds `overdue` → v4 | `Place(#1, keep, If-Match "<gen>-3")` | `precondition_failed` (412), body = #1 at v4 with `{new, overdue}`; nothing changed, no events. Retrying with `If-Match "<gen>-4"` → PLACED (clears both rows). Without `If-Match` → `precondition_required` (428). An `If-Match` with a different `db_generation` (after a restore) → `precondition_failed`. |
| TRI-20 | eight tasks with exactly one reason each, added by ops in this order (ascending `seq`): `#1{new}`, `#2{manual}`, `#3{reopened}`, `#4{surfaced}`, `#5{continue}`, `#6{due}`, `#7{overdue}`, `#8{slipped}`; then `#9{slipped}` whose `slipped` row was added by an op with a **lower** seq than #8's (an earlier reconcile); then `#10{new, overdue}` whose `overdue` row has a **higher** seq than #7's | read Triage | Order: #9, #8, #7, #10, #6, #5, #4, #3, #2, #1 — highest reason first; within a reason ascending `task_triage.seq` (oldest op first, A§3.7/R2), then task id; a multi-reason task sorts by its highest reason's row. `since` is display-only and never used for ordering. |
| TRI-21 | `#1{new,due}` | `Delete(#1)` then `Undelete(#1)` (`write`) | after Delete: `#1{}`, `deleted_at` set, not in any view. After Undelete: `deleted_at = null`, `#1{manual}` (`since = now`, `seq` = this op), in Triage; a future defer, if any, is cleared like `MarkManual` (A§3.2, D4); blocks and focus untouched. `triage:add #1 manual`. |
| TRI-22 | `#1{}` in backlog | `MarkManual(#1)` twice (keys K1, K2) | first: `#1{manual}`, `triage:add #1 manual`, version++. second: `NoChange` (`op_id = null`, 200, no event, version unchanged, nothing pushed on the undo stack; PK `(task_id, reason)`, `since`/`seq` kept) — and an idempotency row **is** stored for K2 with `op_id` NULL (R-j), so a replay of K2 returns 200 `replayed = true` with no effects. |
| TRI-23 | #1 done then deleted | `Undelete(#1)` (`write`, A§3.2/A18) | `deleted_at = null`, status done, **no** `manual` row (A§14 D30: reasons only on open tasks). `mcp` (has `write`) → allowed. |
| TRI-24 | `#1{}` backlog, `due_date = 10-28`, `due_ack = none`, `#1{due}` after reconcile | `Place(#1, keep)` | PLACED with `due_ack = due`, `due_ack_date = 10-28`. (Phase computation per §0.) |
| TRI-25 | `#1{new}` | `Place(#1, keep)` by `mcp` | allowed (`write`); PLACED; op `actor = token:ai`; op listed in AI changes (`unreviewed` 1). |
| TRI-26 | `#1{}` deferred to 11-02 (in Upcoming), rank r | `Place(#1, keep)` (TUI Upcoming `k`) | defer cleared (`defer_date = defer_min = null`), PLACED; #1 in Backlog at rank r (A§14 D5). `Place(#1, top)` → same plus `task:move`. |
| TRI-27 | `#1{}` deferred to 11-02 | `Defer(#1, nil)` (`tkr defer 1 none`, `POST …/defer {"until": null}`) | identical to `Place(#1, keep)`: defer cleared, PLACED, rank kept; op kind `task.defer` (R24, A§3.2). `nil` never touches blocks or focus: on a non-deferred `#1{new}` with B1 planned tomorrow → `#1{}` PLACED, B1 untouched. |
| TRI-28 | `#1{}` deferred to 11-02 | `MarkManual(#1)` | `#1{manual}` **and** defer cleared (A§14 D4) so the task is visible in Triage; not a placement (`last_placed_at` unchanged). |
| TRI-29 | `#1{}` open, `defer_date = 11-02`, then `Delete(#1)` | `Undelete(#1)` | `#1{manual}` and `defer_date = null` (an open undeleted task must be visible in triage, A§3.2); not a placement. |

## 2. Blocks (BLK)

### 2.1 Creation, overlap, validation

| ID | Given | When | Then |
|---|---|---|---|
| BLK-01 | `#1{new}` open | `Block(#1, 10-28, 13:00-15:00)` | `B1{planned, null, 0, –}`, `start_min 780`, `end_min 900`, `resolved_at = null`; PLACED(#1). `block:insert B1`. |
| BLK-02 | B1(#1, 10-28 13:00-15:00) | `Block(#2, 10-28, 15:00-16:00)`; `Block(#2, 10-28, 12:00-13:00)` | both accepted (half-open: `[13:00,15:00)` and `[15:00,16:00)` do not overlap). |
| BLK-03 | B1 as above | each of `Block(#2, 10-28, 14:59-16:00)`, `12:00-13:01`, `13:30-14:00`, `12:00-16:00`, `13:00-15:00`; and `Block(#1, 10-28, 14:00-16:00)` (same task) | `conflict` (overlap) naming B1. No rows written, #1/#2 unchanged (not PLACED). |
| BLK-04 | B1 cancelled by `Unblock` | `Block(#2, 10-28, 13:00-15:00)` | accepted — only `planned` blocks participate in the overlap check. |
| BLK-05 | – | `Block(#1, D, 15:00-13:00)`; `13:00-13:00`; `23:00-01:00`; `24:00-25:00`; `-1`… | `validation` (require `0 ≤ start < end ≤ 1440`, same day). `Block(#1, D, 23:00-24:00)` → accepted, `end_min = 1440`. |
| BLK-06 | now 16:00 | `Block(#1, today, 13:00-15:00)` | `validation` (block entirely in the past). |
| BLK-07 | now 10:00 | `Block(#1, today, 09:00-11:00)` | accepted; it is the current block immediately and counts as *started*. |
| BLK-08 | #1 done; #2 dropped; #3 deleted | `Block(#1,…)`, `Block(#2,…)` → `conflict` (task not open); `Block(#3,…)` → `deleted` (A§3.9); `Block(#999,…)` → `not_found`. |
| BLK-09 | #1 `defer_date = 11-02` | `Block(#1, 10-29, 09:00-10:00)` | `defer_date = defer_min = null`; block planned; PLACED(#1). #1 now in Backlog at its rank, no longer in Upcoming. |
| BLK-10 | `Block(#1, 10-29, 09:00-10:00)` exists; now 10-29 09:30, no entries | `Reblock(B1, 10-29, 09:30-10:30)` | B1 is started → old row resolved by derivation `B1{missed, auto, 1, –}`, `resolved_at = now`; new `B2(#1, 10-29 09:30-10:30){planned}`; PLACED → no `slipped` reason; L/S = 1/0. (Overlap check for B2 excludes the just-resolved B1.) |

### 2.2 DST wall times

| ID | Given | When | Then |
|---|---|---|---|
| BLK-11 | date 2027-03-14 (spring-forward; 02:00–02:59 do not exist) | `Block(#1, 2027-03-14, 02:00-03:00)`; `02:30-04:00`; `01:00-02:30` | `validation` (nonexistent wall time) for each. |
| BLK-12 | same | `Block(#1, 2027-03-14, 01:00-02:00)` | `validation` (end 02:00 is a nonexistent wall time). |
| BLK-13 | same | `Block(#1, 2027-03-14, 01:30-03:30)`; `03:00-04:00` | accepted. 01:30–03:30 spans instants 08:30Z–09:30Z (one real hour). Current at 09:00Z (03:00 MDT), not current at 09:30Z. |
| BLK-14 | date 2026-11-01 (fall-back; 01:00–01:59 occur twice) | `Block(#1, 2026-11-01, 01:30-02:30)` | accepted. Start resolves to the **earlier** occurrence 01:30 MDT = 07:30Z; end 02:30 exists once (MST) = 09:30Z; the block covers two real hours. Current at 07:45Z (01:45 MDT), at 08:15Z (01:15 MST, wall-clock *outside* the range) and at 09:00Z; not current at 09:30Z. |
| BLK-15 | – | `Block(#1, 2026-11-01, 00:00-24:00)` | accepted; 25 real hours (06:00Z 11-01 → 07:00Z 11-02). Current at 2026-11-02T06:30Z (23:30 MST). |
| BLK-16 | – | `Block(#1, 2027-03-14, 13:00-15:00)` | accepted; instants 19:00Z–21:00Z (MDT). The same wall times on 2026-11-01 are 20:00Z–22:00Z (MST). |

### 2.3 Resolution at midnight and derived outcome

| ID | Given | When | Then |
|---|---|---|---|
| BLK-17 | B1(#1, 10-28 13:00-15:00), no entries | `Reconcile(10-28 23:59)` | B1 still `planned` (Today view marks it *reblock?*). No events. |
| BLK-18 | same | `Reconcile(10-29 00:00 = 06:00Z)` | `B1{missed, auto, 1, –}`, `slipped_at = resolved_at = 2026-10-29T06:00Z`, `slipped_seq = resolved_seq =` the reconcile op's seq; `#1{slipped}` with `since` = same instant and `seq` = same op; `#1.version++` (finalized by the reconcile op itself, R-i). `block:update B1`, `triage:add #1 slipped`; actor `system`. L/S = 1/1 (`slipped_seq > COALESCE(last_placed_seq, 0)`). |
| BLK-19 | B1(#1, 2026-11-01 13:00-15:00) | `Reconcile(2026-11-02T06:59Z)` (= 23:59 MST 11-01) | still `planned` — the day is 25 h long; resolving at 06:00Z would be a bug. |
| BLK-20 | same | `Reconcile(2026-11-02T07:00Z)` | resolved `missed`, slipped, `#1{slipped}`. |
| BLK-21 | B1(#1, 2027-03-14 13:00-15:00) | `Reconcile(2027-03-15T05:59Z)` → planned; `Reconcile(2027-03-15T06:00Z)` → resolved (the day is 23 h; 07:00Z would be an hour late). |
| BLK-22 | B1(#1, 10-27 13:00-15:00) and `C(#1, 10-27)` (via `Complete(#1, --date 10-27)` on 10-27) | `Reconcile(10-28 00:00)` | — Complete already resolved B1 (LIF-04); nothing to do. Pure derivation table: `C` on D → `done`; else `W` on D → `worked`; else `missed`. |
| BLK-23 | B1(#1, 10-27 13:00-15:00), `W(#1, 10-27, "x")` | `Reconcile(10-28 00:00)` | `B1{worked, auto, 0, –}`; `#1{continue}` (no later planned block). `block:update B1`, `triage:add #1 continue`. |
| BLK-24 | B1(#1, 10-27), `W(#1, 10-26, "x")` (other day); `W(#2, 10-27, "y")` (other task); `W(#1, 10-27, "z")` **deleted** | `Reconcile(10-28 00:00)` | `B1{missed, auto, 1, –}` — none of those entries count. |
| BLK-25 | B1(#1, 10-27 09:00-10:00), B2(#1, 10-27 14:00-15:00), one `W(#1, 10-27)` | `Reconcile(10-28 00:00)` | both `worked`; one `continue` row. "Any entry that day counts for every block of T that day." |

### 2.4 Slip table, row by row

| ID | Table row | Given | When | Then |
|---|---|---|---|---|
| BLK-26 | resolves `missed` | = BLK-18 | | slip, `slipped` reason. |
| BLK-27 | unstarted **today** cancelled | B1(#1, 10-28 13:00-15:00) created 09:00; `#1{}`; now 10:00 | `Unblock(B1)` | `B1{cancelled, –, 1, user}`, `slipped_at = now`; `#1{slipped}` (unblock is not a placement; `last_placed_at` unchanged). L/S = 1/1. #1 in Triage. |
| BLK-28 | unstarted today moved to later date | same | `Reblock(B1, 10-29, 13:00-15:00)` | `B1{cancelled, –, 1, rescheduled}`; new `B2(#1, 10-29 13:00-15:00){planned}`; PLACED(#1) → `#1{}`; L/S = 1/0. |
| BLK-29 | unstarted today, task deferred | same | `Defer(#1, "+2d")` | `B1{cancelled, –, 1, deferred}`; `defer_date = 10-30`; PLACED(#1); L/S = 1/0. |
| BLK-30 | grace: created < 15 min ago | B1 created 09:50 (`fresh`), now 10:00 | `Unblock(B1)` | `B1{cancelled, –, 0, user}`; `#1{}` unchanged; no slip events. Same `slipped = 0` outcome for `Reblock(B1, 10-29, …)` and `Defer(#1, …)` on a fresh block. |
| BLK-31 | grace boundary | B1 created 09:45:00, now 10:00:00 (exactly 15 min) | `Unblock(B1)` | slipped = 1 (grace is strictly `< 15 min`). At 09:59:59 → slipped = 0. |
| BLK-32 | unstarted moved same day | B1(#1, 10-28 13:00-15:00) | `Reblock(B1, 10-28, 16:00-17:00)` | **same row** B1 updated: `start_min 960`, `end_min 1020`, still `planned`, `slipped 0`, `version++`; no new block; PLACED(#1). `block:update B1`. Also for a future-day block moved within its own day. |
| BLK-33 | unstarted **future** cancelled/moved | B2(#1, 10-29 09:00-10:00) | `Unblock(B2)` → `B2{cancelled, –, 0, user}`, `#1{}` unchanged, L/S 0/0. `Reblock(B2, 10-30, 09:00-10:00)` → `B2{cancelled, –, 0, rescheduled}` + new planned B3 on 10-30; PLACED. |
| BLK-34 | task done/dropped/deleted | B1(#1, 10-28 13:00-15:00) unstarted (≥ 15 min old) | `Drop(#1)` / `Delete(#1)` / `Complete(#1)` | `B1{cancelled, –, 0, task_closed}`; no slip. |
| BLK-35 | started block ended early, nothing logged | B1(#1, 10-28 13:00-15:00); now 14:00 | `Unblock(B1)` | `B1{missed, auto, 1, –}`, `resolved_at = slipped_at = now`; `#1{slipped}`. L/S 1/1. |
| BLK-36 | started block ended early, worked | same + `W(#1, 10-28)` | `Unblock(B1)` | `B1{worked, auto, 0, –}`; `#1{continue}` (continue rule; unblock is not a placement; no later planned block). Not a slip. |
| BLK-37 | started block, later planned block exists | BLK-36 + B2(#1, 10-29) | `Unblock(B1)` | `B1{worked}`; **no** `continue` (later planned block). `#1{}`. |
| BLK-38 | started, reblocked | B1 started 14:00, no entry | `Reblock(B1, 10-29, 09:00-10:00)` | `B1{missed, auto, 1, –}`; B2 planned on 10-29; PLACED → `#1{}`; L/S 1/0. `Reblock(B1, 10-28, 15:00-16:00)` (later today) behaves the same — a started block is never updated in place. |
| BLK-39 | started, fresh | B1(#1, 10-28 09:55-11:00) created 09:55, now 10:00 | `Unblock(B1)` | started row wins over grace: resolved by derivation → `missed`, slipped = 1, `#1{slipped}`. |

### 2.5 Reblock variants and recap outcomes

| ID | Given | When | Then |
|---|---|---|---|
| BLK-40 | B1(#1, 10-27){missed, auto, 1}, `#1{slipped}` | `Reblock(B1, 10-28, 13:00-15:00)` | B1 untouched (still missed, slipped = 1, `slipped_at` unchanged); new B2 planned today; PLACED(#1) → `#1{}`; L/S = 1/0 (acknowledged, `slipped_seq < last_placed_seq`). |
| BLK-41 | B1 `{cancelled}` / `{worked}` / `{done}` | `Reblock(B1, 10-29, …)` | new block created, old untouched, PLACED. |
| BLK-42 | B1(#1, 10-28 13:00-15:00) unstarted; B9(#2, 10-29 09:00-10:00) | `Reblock(B1, 10-29, 09:30-10:30)` | `conflict` (overlap with B9); B1 unchanged (still planned, not cancelled), #1 not PLACED. Atomic. |
| BLK-43 | – | `Unblock(B1{cancelled})`, `Unblock(B1{missed})` → `conflict` (not planned); `Unblock(999)` → `not_found`. |
| BLK-44 | B1(#1, 10-27){missed, auto, 1}, `#1{slipped}`; no other slipped block | `SetOutcome(B1, worked)` on 10-28 | `B1{worked, user, 0, –}`; `#1{continue}` (`slipped` removed by unslip; `continue` added as for a re-derive to worked). |
| BLK-45 | B1(#1, 10-27){worked, auto, 0}, `#1{continue}` | `SetOutcome(B1, missed)` | `B1{missed, user, 1, –}`, `slipped_at = now`; `#1{slipped}` (`continue` removed, `slipped` added — recap outcome is not a placement). L/S 1/1. |
| BLK-46 | after BLK-45 | `AddEntry(#1, "x", 10-27)` | recompute skips user-resolved blocks: B1 stays `{missed, user, 1}`; `#1{slipped}` unchanged. Entry stored normally. |
| BLK-47 | B1(#1, 10-28 13:00-15:00) `planned`, now 10-28 16:00 (ended, still planned) | `SetOutcome(B1, missed)` | `B1{missed, user, 1, –}`, `resolved_at = now`, `resolved_seq = slipped_seq = op.seq`; `#1{slipped}` (R9). At 14:00 (started, not ended) → also allowed (A§3.3/A9). At 12:00 (unstarted) → `conflict` ("use unblock"). On a `cancelled` block → `conflict`. |
| BLK-49 | B1(#1, 10-27){missed, user, 1} (from BLK-45), `#1{slipped}`; `W(#1, 10-27)` exists | `SetOutcome(B1, auto)` | date < today → re-derived: `B1{worked, auto, 0}`; `#1{continue}` (slip/continue hooks apply). |
| BLK-50 | B1(#1, 10-28 13:00-15:00){missed, user, 1} set at 16:00 today (BLK-47) | `SetOutcome(B1, auto)` at 17:00 | date = today → back to `planned` (`outcome_source = null`, `resolved_at = null`, `slipped = 0` → unslip removes `#1{slipped}`); resolves normally at midnight (BLK-18). |
| BLK-48 | B1(#1, 10-28 13:00-15:00) | read at 12:59, 13:00, 14:30, 15:00 | current block: none, B1, B1, none (half-open). `status --short` shows `▶ #1 … (→15:00)` only while current. |

## 3. The one slip rule and the continue rule (SLP)

| ID | Given | When | Then |
|---|---|---|---|
| SLP-01 | B1(#1, Mon 10-26 13:00-15:00){missed, auto, 1, slipped_at Tue 00:00}; `#1{slipped}`; `last_placed_at = Mon 09:00` | `AddEntry(#1, "Did it", date = mon)` on Wed | `B1{worked, auto, 0}` (`slipped_at` unchanged); `#1{continue}`: `triage:remove #1 slipped`, `triage:add #1 continue`, `block:update B1`. L/S 0/0. |
| SLP-02 | B1(#1, Mon){missed, 1}, B2(#1, Tue){missed, 1}; `#1{slipped}` (since Tue 00:00); `last_placed_at` Mon 09:00 | `AddEntry(#1, "x", mon)` | B1 → worked, unslip. B2 still `slipped = 1` with `slipped_at (Wed 00:00) > last_placed_at` → `slipped` reason **stays**; `continue` added → `#1{slipped, continue}`. L/S 1/1. |
| SLP-03 | B1(#1, Mon){missed, 1, slipped_at Tue 00:00}; `Place(#1, keep)` Tue 09:00 (→ `#1{}`, `last_placed_at` Tue 09:00); B2(#1, Tue){missed, 1, slipped_at Wed 00:00}; `#1{slipped}` | `AddEntry(#1, "x", tue)` Wed 10:00 | B2 → worked, unslip. B1 still slipped but `slipped_seq < last_placed_seq` (acknowledged) → `slipped` reason removed. `#1{continue}`. L/S 1/0. |
| SLP-04 | state after SLP-01 (`#1{continue}`, B1 worked, entry E) | `DeleteEntry(E)` Wed 11:00 | recompute (#1, Mon): `B1{missed, auto, 1}`, `slipped_at = Wed 11:00`; `#1{slipped}` — `continue` removed, `slipped` added. L/S 1/1. |
| SLP-05 | SLP-01 performed by `mcp` (AI log) | owner `Revert(op)` | X's non-derived events (the `entry:insert`) are reverted → entry tombstoned; its derived events (`block:update B1`, `−slipped`, `+continue`) are **skipped** and the pair (#1, Mon) is recomputed instead: `B1{missed, auto, 1, slipped_at = now, slipped_seq = R.seq}`, `#1{slipped}` (−continue). Op X shows as reverted in AI changes. Same result if #1 was `Place`d between X and R: the re-slip is unacknowledged (`slipped_seq > last_placed_seq`, S = 1) and the placement's `task:update` on #1 is no conflict (X touched only the entry). |
| SLP-06 | B1(#1, Mon), B2(#1, Tue 09:00-10:00) planned; `W(#1, Mon)` | `Reconcile(Tue 00:00)` | `B1{worked}`; **no** `continue` (planned block on a later date). `#1{}`. Then `W(#1, Tue)`; `Reconcile(Wed 00:00)` → `B2{worked}`, `#1{continue}` (no later block now). |
| SLP-07 | B1(#1, today 13:00-15:00) started (now 14:00); `W(#1, today)` | `Defer(#1, "+2d")` | `B1{worked, auto, 0}`; PLACED; no `continue` (same op is a placement on #1). `#1{}`. Same for `Reblock(B1, …)`. |
| SLP-08 | SLP-01 state | `UpdateEntry(E, work_date → Tue)` | recompute both (#1, Mon) and (#1, Tue): B1 → missed, slip, `continue` replaced by `slipped`. (= "continue replaced by slip on re-derive to missed".) |
| SLP-09 | B1(#1, Mon){worked, auto}, `#1{continue}` | `Complete(#1, --date mon)` on Wed | `C(#1, Mon)` created → B1 re-derives `{done, auto, 0}`; `continue` removed (and all other reasons, by Complete). `triage:remove #1 continue`. |
| SLP-10 | B1(#1, today) started; `W(#1, today)` | `Drop(#1)` at 14:00 | `B1{worked, auto, 0}`; no `continue` (task not open). `#1{}`. |
| SLP-11 | B1(#1, today) started; no entry | `Drop(#1)` at 14:00 | `B1{missed, auto, 1, slipped_at = now}`; **no** `slipped` reason row (task not open); `#1{}`. L = 1 (visible in `show #1`/history). |
| SLP-12 | B1(#1, Mon){missed, 1, slipped_at Tue 00:00}; `Place(#1, keep)` Tue 09:00; B2(#1, Tue){missed, 1, slipped_at Wed 00:00} | read #1 Wed | L/S = 2/1; triage shows "slipped ×2 lifetime, ×1 since last triage". |
| SLP-13 | SLP-03 then `DeleteEntry` Wed 11:00 | | B2 re-slips with `slipped_at = Wed 11:00 > last_placed_at` → `#1{slipped}`; S counts B2 only (B1's `slipped_at` predates the placement): L/S 2/1. |
| SLP-14 | B1(#1, Mon), B2(#1, Tue), server down since Mon | `Reconcile(Wed 09:00)` | both missed, both `slipped_at = Wed 09:00`; **one** `slipped` row (`since = Wed 09:00`); one `+slipped` event. |
| SLP-15 | `#1{continue}` since Tue 00:00 (from Mon worked, not placed); `W(#1, Tue)`; B2(#1, Tue) | `Reconcile(Wed 00:00)` | B2 worked; `continue` row already exists → no new row, `since` unchanged (Tue 00:00), no triage event. |
| SLP-16 | B1(#1, Mon){missed, 1}, `#1{slipped}`; #1 `done` on Tue (reasons cleared, B1 left alone — it is before D) | `AddEntry(#1, "x", mon)` Wed (logging on a done task) | B1 → `{worked, auto, 0}` (unslip); no reasons touched (task not open: no `continue`); `#1{}`. CLI warns "task is done". |

## 4. Recompute over the union of before/after pairs (RCP)

| ID | Given | When | Then |
|---|---|---|---|
| RCP-01 | E = `W(#1, Mon)`; B1(#1, Mon){worked, auto}, `#1{continue}`; B2(#2, Mon){missed, auto, 1}, `#2{slipped}` | `RelinkEntry(E, task → #2)` | one transaction: (#1, Mon): `B1{missed, auto, 1, slipped_at now}`, `#1{slipped}` (−continue). (#2, Mon): `B2{worked, auto, 0}`, `#2{continue}` (−slipped). Events for both blocks and both tasks share one `op_id`. |
| RCP-02 | E = `W(#1, Mon)`; B1(#1, Mon){worked}; B2(#1, Tue){missed, 1}; `#1{slipped, continue}` | `UpdateEntry(E, work_date → Tue)` | B1 → missed (slip), B2 → worked (unslip). End state `#1{slipped, continue}`: `slipped` present (B1 now unacked), `continue` present (B2 is `worked` with `resolved_seq > last_placed_seq`, A§4.4 removal rule). Tests assert the reason set, not the `since` of `continue` (it may be kept or re-added depending on block processing order). L/S 1/1. |
| RCP-03 | SLP-01 state | `DeleteEntry(E)` | = SLP-04. |
| RCP-04 | RCP-03 state | `UndeleteEntry(E)` | B1 → worked, unslip; `#1{continue}`. |
| RCP-05 | RCP-01 state | `Undo()` (reverts the relink) | both pairs recomputed back: `B1{worked}`, `#1{continue}`; `B2{missed, 1, slipped_at = now}`, `#2{slipped}`. |
| RCP-06 | B1(#1, today 13:00-15:00) planned, now 16:00 (Today shows *reblock?*) | `AddEntry(#1, "x", today)` | B1 stays `planned` (recompute touches only `auto`-resolved blocks); *reblock?* marker gone. `Reconcile(tomorrow 00:00)` → `{worked}`, `#1{continue}`. |
| RCP-07 | B1 `{missed, user, 1}` | any entry op on (#1, D) | B1 unchanged (user outcomes never re-derived). |
| RCP-08 | E = `W(#1, Mon)`, B1 worked | `UpdateEntry(E, title/notes only)` | recompute runs over {(#1, Mon)}; outcome unchanged → no block events, block version unchanged. |
| RCP-09 | E = `W(null, Mon)` (standalone); B1(#1, Mon){missed, 1}, `#1{slipped}` | `RelinkEntry(E, task → #1)` (recap "link unlinked entries") | before pair `(null, Mon)` ignored; after pair (#1, Mon): B1 → worked, `#1{continue}`. |

## 5. Lifecycle (LIF)

| ID | Given | When | Then |
|---|---|---|---|
| LIF-01 | `#1{new}` "Set up keycloak", focus = #1 | `Complete(#1)` | status done, `completed_date = 10-28`, `completed_at = now`; `C1(#1, 10-28)` kind complete, title "Set up keycloak", notes ""; `#1{}`; focus null. `task:update #1 (status, completed_*)`, `entry:insert C1`, `triage:remove #1 new`, `focus:set`. |
| LIF-02 | #1 "Run ansible against dev qt cluster" | `Complete(#1, --as "Add svc-cluster weencrypt name to certs")` | title changed, own `task:update #1 (title)` with before/after; C1 title = new title. One ✓ report line. |
| LIF-03 | #1 | `Complete(#1, --log "Failover tested", -m "notes")` | C1 title "Failover tested", notes "notes"; task title unchanged. |
| LIF-04 | B1(#1, 10-28 13:00-15:00) started (now 14:00), B2(#1, 10-28 16:00-17:00), B3(#1, 10-29 09:00-10:00) | `Complete(#1)` | `B1{done, auto, 0}` `resolved_at = now`; `B2{cancelled, –, 0, task_closed}`; `B3{cancelled, –, 0, task_closed}`. No slips. |
| LIF-05 | today Thu 10-29 10:00. #1 blocks: Mon 10-26 `{missed, auto, 1}`; Tue 10-27 `{worked, auto, 0}`; Wed 10-28 `{missed, auto, 1}`; Thu 14:00-16:00 planned (unstarted); Fri planned. `#1{slipped, continue}` | `Complete(#1, --date mon)` | `C1(#1, 10-26)`; `completed_date = 10-26`. Mon → `{done, auto, 0}` (re-derive + unslip). Tue untouched `{worked}`. Wed → `{cancelled, –, 0, task_closed}` (auto-missed after D → cancelled + unslip). Thu, Fri → `{cancelled, –, 0, task_closed}`. `#1{}`. L/S 0/0. |
| LIF-06 | as LIF-05 but Mon block is `{missed, user, 1}` | `Complete(#1, --date mon)` | Mon block untouched (user outcome): still missed, slipped = 1 → L = 1. Rest as LIF-05. |
| LIF-07 | – | `Complete(#1, --date tomorrow)` → `validation`; `Complete(#1)` when done → `conflict`; `Complete(#1)` when dropped → `conflict` (A§14 D10: only open→done, open→dropped, done→open, dropped→open); `Drop(#1)` when done → `conflict`. |
| LIF-08 | `#1{new}` focus = #1; B1 today unstarted; B2 today started (14:00), no entries | `Drop(#1)` | status dropped; `#1{}`; focus null; `B1{cancelled, –, 0, task_closed}`; `B2{missed, auto, 1}` without a `slipped` row; no complete entry; `completed_*` null. |
| LIF-09 | same + `W(#1, 10-27)` exists | `Delete(#1)` | `deleted_at = now`; same block effects as LIF-08; `#1{}`; focus null. #1 absent from Triage/Backlog/Upcoming/Today; its entry still appears in `report`; `GetTask(#1)` returns it with `view = deleted`. `Block(#1,…)`, `SetFocus(#1)`, `AddEntry(#1,…)`, `UpdateTask(#1,…)` → `deleted`. |
| LIF-10 | LIF-09 state | `Undelete(#1)` | `deleted_at = null`; status open; `#1{manual}`; blocks stay cancelled/missed; focus stays null. |
| LIF-11 | `#1{new}`, focus = #1 | `Defer(#1, "+2d")` | PLACED; `defer_date = 10-30`; in Upcoming; after the op (and the reconcile that follows) focus = null. |
| LIF-12 | #1 done (LIF-01 state), `C1` | `Reopen(#1)` | status open; `completed_at/date = null`; `due_ack = none`, `due_ack_date = null`; `#1{reopened}`; `C1.reopened = 1` (not deleted); `last_placed_at`/`due_ack_date` keep the values set by `Complete` (a placement, D6), so any block slipped before completion counts as acknowledged: L/S = L/0 until a new slip. C1 still counts as `complete` for derivation. `task:update #1 (status, completed_*, due_ack)`, `entry:update C1 (reopened)`. |
| LIF-13 | LIF-04 state (B2, B3 cancelled task_closed) | `Reopen(#1)` | B2, B3 remain `cancelled/task_closed`; B1 remains `{done}`. CLI prints the hint that `undo` of the `done` op would restore them. |
| LIF-20 | LIF-05 state after `Complete(#1, --date mon)` (Mon block `done`, Wed block cancelled+unslipped; `last_placed_at = now_complete`); then `Reopen(#1)` on 10-30 | `Reconcile`; read #1 | `#1{reopened}` only; L/S = 0/0 (the Wed slip was unslipped by `Complete`; Mon is `done`). Then `Unblock` of a fresh today block at 10-30 (≥ 15 min old) → L/S = 1/1 and `#1{reopened, slipped}` — only slips after the completion placement count as "since last triage". |
| LIF-14 | LIF-04 state; actor owner | `Reopen(#1, remove_entry)` | C1 `deleted_at = now`; recompute (#1, 10-28): `B1{missed, auto, 1, slipped_at = now}` (no other entry that day); `#1{reopened, slipped}` (Reopen is not a placement, so the new slip is unacknowledged: `slipped_seq > last_placed_seq` set by `Complete`; L/S = 1/1). Same by `mcp` → `forbidden` (needs `destroy`). |
| LIF-15 | #1 done, C1 | `DeleteEntry(C1)` → `conflict` ("use reopen --remove-entry"); `UpdateEntry(C1, notes)` → allowed (`destroy`); `UpdateEntry(C1, title)` / `work_date` → `validation` (A§14 D32: only notes of a `complete` entry are editable). After `Reopen(#1)`: `DeleteEntry(C1)` → allowed (destroy). |
| LIF-16 | #1 dropped | `Reopen(#1)` | status open; `#1{reopened}`; `due_ack = none`. |
| LIF-17 | LIF-12 state | `Complete(#1)` again on 10-29 | second entry `C2(#1, 10-29)`; C1 keeps `reopened = 1`. Report: 10-28 `✓ … (reopened)`, 10-29 `✓ …`. |
| LIF-18 | #1 `due_date = 10-26` (overdue, `due_ack = overdue`), done | `Reopen(#1)` then `Reconcile` | `due_ack = none` → reconcile adds `overdue` (and `due`, R11): `#1{overdue, due, reopened}`, ordered under `overdue`. |
| LIF-19 | #1 open, `C1` from an earlier done+reopen (`reopened = 1`) | `RelinkEntry(C1, → #2)` | `validation` — `complete` entries are bound to their task permanently. |

## 6. Due and defer (DUE)

| ID | Given | When | Then |
|---|---|---|---|
| DUE-01 | `#1{}` `due_date = 10-28` (date-only → moment 10-29 00:00 = 06:00Z) | `Reconcile(10-28 00:00)` → `#1{due}`; `Reconcile(10-28 23:59)` → still `{due}`, no `overdue`; `Reconcile(10-29 00:00)` → `#1{due, overdue}`. |
| DUE-02 | `due = 10-28 17:00` | `Reconcile(10-28 00:00)` → `{due}`; `16:59` → unchanged; `17:00` → `+overdue`. |
| DUE-03 | `due = 2026-11-01 01:30` (ambiguous; earlier occurrence = MDT = 07:30Z) | `Reconcile(2026-11-01T07:29Z)` → not overdue; `Reconcile(07:30Z)` → `overdue`. (08:30Z — the MST occurrence — would be wrong.) |
| DUE-04 | `defer = 2027-03-14 02:30` (nonexistent → first valid instant 03:00 MDT = 09:00Z) | `Reconcile(2027-03-14T08:59Z)` → still deferred; `Reconcile(09:00Z)` → surfaced: defer cleared, `#1{surfaced}`. |
| DUE-05 | `defer = 10-29` (date-only → 00:00) | `Reconcile(10-28 23:59)` → deferred, in Upcoming; `Reconcile(10-29 00:00)` → `defer_date = null`, `#1{surfaced}`, version++, `task:update #1 (defer_date, defer_min)`, `triage:add #1 surfaced`. |
| DUE-06 | `due = 10-30` (date-only) | `Defer(#1, "10-30")` ok; `Defer(#1, "10-30 23:59")` ok; `Defer(#1, "10-31")` → `validation` (defer date > due date). |
| DUE-07 | `due = 10-30 10:00` | `Defer(#1, "10-30 10:00")` → `validation` (not strictly before); `"10-30 09:59"` ok; `"10-30"` (00:00) ok. |
| DUE-08 | `defer = 11-02` | `SetDue(#1, "10-30")` → `validation` (due before defer); `SetDue(#1, "11-02")` ok (00:00 < 24:00); `SetDue(#1, "11-02 00:00")` → `validation`; `SetDue(#1, "11-02 00:01")` ok. |
| DUE-09 | `due = 10-28 09:00`, now 10:00 (overdue, `#1{overdue}`) | `Defer(#1, "10-28 15:00")` | `validation` ("change the due date first"). |
| DUE-10 | `due = 10-26`, `#1{overdue, due, new}`, `due_ack = overdue` | `SetDue(#1, "11-05")` | `due_ack = none`, `due_ack_date = null`; `#1{new}` (only `due`/`overdue` removed). `SetDue(#1, none)` → same, `due_date = null`. Following `Reconcile` adds nothing until 11-05. |
| DUE-11 | – | `SetDue(#1, "yesterday")` | `validation` (due in the past). `SetDue(#1, "today")` → ok (moment 24:00 is future). |
| DUE-12 | `defer = 10-30`, `due = 10-30` | `Reconcile(10-30 00:00)` | one reconcile: surfaced **and** due (step 2 sees the just-surfaced task): `#1{surfaced, due}`, both `since = 10-30 00:00`. |
| DUE-13 | `due = 10-28`, B1(#1, 10-28 13:00-15:00) created 10-27 | `Reconcile(10-28 00:00)` → no `due` (planned block today); `Reconcile(10-28 16:00)` → still none (block ended but still `planned`); `Reconcile(10-29 00:00)` → B1 missed/slip; `#1{slipped, overdue, due}` (`due`: due_date ≤ today, ack none, no planned block today anymore). Order under `slipped`. |
| DUE-14 | `due = 10-26` | `Reconcile(10-27 00:00)` → `{due, overdue}`; `Place(#1, keep)` 10-27 09:00 → `#1{}`, `due_ack = overdue`, `due_ack_date = 10-27`; `Reconcile(10-27 23:59)` → `{}`; `Reconcile(10-28 00:00)` → `{overdue}` (since 10-28 00:00; no `due`: ack ≠ none); no placement on 10-28; `Reconcile(10-29 00:00)` → still one `overdue` row, `since` 10-28 00:00. `Place` 10-29 → `{}`; `Reconcile(10-30 00:00)` → `{overdue}`. |
| DUE-15 | `due = 10-28 17:00`, `#1{due}` | `Place(#1, keep)` at 10:00 → `due_ack = due`, `due_ack_date = 10-28`, `#1{}`; `Reconcile(17:00)` → `#1{overdue}` (ack ≠ overdue). |
| DUE-16 | `due = 11-05`, `#1{new}` | `Place(#1, keep)` | `due_ack = none` (phase none), `due_ack_date = 10-28`. |
| DUE-17 | `defer = 10-30`, `due = 11-02` | 10-28: in Upcoming, not due-soon (5 days). `Reconcile(10-30 00:00)` → `{surfaced}`. `Place(keep)` 10-30 09:00 → `{}`, `due_ack = none`. `Reconcile(11-02 00:00)` → `{due}`. `Reconcile(11-03 00:00)` → `{due, overdue}` (R11). `Place` 11-03 → ack `overdue`/11-03. `Reconcile(11-04 00:00)` → `{overdue}`. |
| DUE-18 | `due_soon_days = 3`; `#1 due = 10-31`, `#2 due = 11-01`, `#3` no due | read Upcoming on 10-28 | due-soon lists #1 only (`due_date − today ≤ 3`); section hidden when no task qualifies and nothing is deferred. |
| DUE-19 | `#1 defer = 10-30`, `#2 defer = 10-29 09:00`, `#3 defer = 10-29` | read Upcoming | order by defer moment: #3 (10-29 00:00), #2 (10-29 09:00), #1. |
| DUE-20 | `#1{}` `due = 10-28`, B1(#1, 10-28) planned, `Unblock(B1)` at 10:00 (slip) | `Reconcile(10:00)` | `#1{slipped, due}` — with no planned block today the `due` reason now fires. |
| DUE-21 | `#1` deferred to 10-30, `due = 10-29` | `SetDue(#1, "10-29")` is rejected before this state exists (DUE-08); conversely `Defer(#1, "10-30")` with `due = 10-29` → `validation`. (Invariant: a task always surfaces on or before its due day.) |

## 7. Reconcile (REC)

| ID | Given | When | Then |
|---|---|---|---|
| REC-01 | pending work at T (e.g. BLK-18 fixture) | `Reconcile(T)` then `Reconcile(T)` again | first: N events in one `system` transaction. Second: zero events, no writer transaction, `updated_at`/`version` unchanged everywhere. |
| REC-02 | #1: B1(10-26), B2(10-27), no entries; #2 `defer = 10-27`; #3 `due = 10-26`; focus = #1 set 10-25; server down 10-25 12:00 → 10-28 09:00 | `Reconcile(10-28 09:00)` | one transaction, actor `system`, one `op_id`: `B1{missed,1}`, `B2{missed,1}` both `slipped_at = 10-28 09:00`; `#1{slipped}` (one row); #2 defer cleared, `#2{surfaced}`; `#3{due, overdue}`; focus null (day changed). All `since` = 10-28 09:00. |
| REC-03 | focus = #1 set 10-28 22:00 (`focus_date = 10-28`) | `Reconcile(10-28 23:59)` → kept; `Reconcile(10-29 00:00)` → focus null, `focus:set`. |
| REC-04 | focus set 2026-11-01 23:30 MST (= 11-02T06:30Z) | `Reconcile(11-02T06:59Z)` → kept; `Reconcile(11-02T07:00Z)` → cleared. |
| REC-05 | focus = #1 | after `Defer(#1,…)` / `Complete(#1)` / `Drop(#1)` / `Delete(#1)` the next read shows focus = null; `status --short` has no `▶`. |
| REC-06 | nothing pending | `GET /today` twice at the same instant | read-only pre-check finds nothing: no write transaction, no events, identical `ETag`. |
| REC-07 | B1(#1, 10-28 13:00-15:00), no entries; now 10-29 00:30 | `AddEntry(#1, "x", yesterday)` | reconcile runs first (system op: B1 missed, slip, `#1{slipped}`), then the user op recomputes (#1, 10-28): `B1{worked, auto, 0}`, `#1{continue}`. Two `op_id`s; history shows both. |
| REC-08 | B1(#1, 10-31), B2(#1, 2026-11-01), server down since 10-31 12:00 | `Reconcile(2026-11-02T06:30Z)` (23:30 MST, still 11-01) → only B1 resolved; `Reconcile(2026-11-02T07:00Z)` → B2 resolved. |
| REC-09 | B1(#1, 2027-03-13), B2(#1, 2027-03-14), down since 03-13 | `Reconcile(2027-03-15T05:59Z)` → only B1; `Reconcile(06:00Z)` → B2 too. |
| REC-10 | UND-16 state: a `planned` block restored by undo on a past date | next `Reconcile` | resolves it by derivation (`missed` → slip + `slipped` reason, actor system). Undo itself did not apply the slip rule. |
| REC-11 | #1 deferred to 10-29 09:00 with focus = #1? (impossible: defer clears focus) — instead: focus = #1, #1 `Complete` by `mcp` | `Reconcile` | focus null (task not open). |
| REC-12 | reconcile inside a read handler | `GET /tasks?view=triage` at 10-29 00:00 with pending midnight work | the read triggers the `system` write (events, actor system), then returns the post-reconcile view; no user op is performed by the GET. |

## 8. Focus and implicit log linking (FOC)

| ID | Given | When | Then |
|---|---|---|---|
| FOC-01 | #1 open | `SetFocus(#1)` | `focus_task_id = 1`, `focus_date = 10-28`; `focus:set`; `status --short` starts with `▶ #1`. |
| FOC-02 | focus = #1 | `SetFocus(null)` | both settings null; `focus:set`. |
| FOC-03 | #1 done / #2 deleted / #3 deferred | `SetFocus(#1)`, `SetFocus(#3)` → `conflict`; `SetFocus(#2)` → `deleted`; `SetFocus(#999)` → `not_found`. `SetFocus` needs no `If-Match` (A§14 D3) but does need an `Idempotency-Key`. |
| FOC-04 | focus = #1; current block B(#2) at 13:30 | CLI `tkr log "text"` | entry `task_id = 1`; CLI echoes "→ #1 (focus)". |
| FOC-05 | no focus; current block B(#2) | `tkr log "text"` | `task_id = 2`; echo "→ #2 (current block)". `status --short` shows `▷ #2 … (→15:00)` (hollow marker: current block without focus, A§9.5). |
| FOC-06 | no focus; no current block | `tkr log "text"` | `task_id = null`; echo "no task". |
| FOC-07 | focus = #1 | `tkr log "text" --date yesterday` | CLI error (no request sent): "--date other than today requires an ID or -N". `tkr log -N "text" --date yesterday` → `task_id = null`. `tkr log 2 "text" --date yesterday` → `task_id = 2`. |
| FOC-08 | focus = #1 | `tkr log 2 "text"` / `tkr log -N "text"` | explicit wins: `task_id = 2` / null. |
| FOC-09 | – | `POST /entries` body without `task_id` → `validation` (field required; `null` allowed). API never infers. |
| FOC-10 | – | `tkr log 42 Fixed it` (three args) → usage error "quote your text" (exit 2); `tkr log "42 things done"` → text, no ID; `tkr log 42 "Fixed it"` → ID 42; `tkr log 42` without `-e` → usage error (A§14 D20); `tkr log -N 42 "x"` → usage "-N conflicts with a task id". |
| FOC-12 | stdin is not a TTY (script) | `tkr log "text"` (no ID, no `-N`) | usage error (exit 2): implicit linking is interactive-only (A§14 issue 18); no request sent. With `-N` or an ID → works. |
| FOC-11 | focus = #1 set 10-28 | `tkr log "x"` at 10-29 00:10 | reconcile cleared focus at midnight → falls through to current block / none. |

## 9. Undo, redo, revert (UND)

| ID | Given | When | Then |
|---|---|---|---|
| UND-01 | owner `AddTask("A")` = op A | `Undo()` | #1 `deleted_at` set (inserted rows tombstoned), reason rows removed; revert op R1 has its own `op_id`; undo stack(owner) = [], redo = [A]. `#1.version` advanced. |
| UND-02 | UND-01 | `Redo()` | reverts R1: #1 `deleted_at = null`, `#1{new}` restored; undo = [A], redo = []. |
| UND-03 | UND-02 | `Undo()` | #1 tombstoned again; undo = [], redo = [A]. (undo/redo/undo.) |
| UND-04 | op A; `Undo()`; then `AddTask("B")` = op B | `Redo()` | `nothing_to_redo` (409; the forward op cleared the redo stack). undo = [B]. |
| UND-05 | owner op A (`AddTask`), then `mcp` op B (`Log`) | `mcp Undo()` → reverts B (A untouched); `owner Undo()` → reverts A. Stacks are per actor; web session and CLI share the `owner` stack. |
| UND-06 | op A `Place(#1, top)`; op B `UpdateTask(#1)` | `Undo()` on A (e.g. via `Revert(A)`) | `revert_conflict` listing (task #1, op B). Nothing changed. `Undo()` pops B first (stack order), so this needs `Revert(A)` or an interleaved actor. Stack unchanged on refusal. |
| UND-07 | Mon: op A `Block(#1, Mon 13:00-15:00)`; `Reconcile(Tue 00:00)` resolved it | `Undo()` Tue | `revert_conflict` (block B1 has a later `system` event). |
| UND-08 | A `AddTask` → #1; B `Place(#1, top)`; `Undo()` (reverts B, net-zero pair B/R_B) | `Undo()` again | reverts A: allowed — the B/R_B pair is ignored in the conflict check. #1 tombstoned. |
| UND-09 | B1(#1, Thu 09:00-10:00) planned. A `Unblock(B1)` (future → no slip); B `Block(#2, Thu, 09:00-10:00)` | `Revert(A)` | `conflict` (invariant: restored B1 would overlap B2). No conflict-refusal (A touched B1 only, B touched B2 and #2). State unchanged. |
| UND-10 | B1(#1, Thu) planned. A `Unblock(B1)`; B `Defer(#1, "+5d")` (allowed: no planned blocks) | `Revert(A)` | `conflict` (invariant: deferred task with a planned block). |
| UND-11 | `mcp` op X = `AddEntry(#1, …, mon)` (unslipped B1, `#1{continue}`; those are X's derived events) | owner `Revert(X)` via `ai-changes --revert` | forward owner op R (`op.revert`, `reverts_op_id = X`): `entry:tombstone` by actor owner; recompute re-derives `B1{missed, 1, slipped_seq = R.seq}`, `#1{slipped}`; R pushed on the owner undo stack, owner redo cleared; X.state = reverted; AI changes shows X as reverted. |
| UND-12 | owner op D = `Drop(#1)` (destroy); owner op P = `Place(#2, top)` (write) | `mcp Revert(D)` → `forbidden` (scope). `mcp Revert(P)` → `forbidden` too: a non-owner actor may revert only its own ops (A§14 D15). `mcp Revert(own AddEntry op)` → allowed (`capture`). Owner may revert any revertible op of any actor. |
| UND-13 | op A `AddEntry(#1, …)` → E; `DeleteEntry(E)`; offline `tkr purge entry E` (§14) | `Revert(A)` → `not_revertible` (`ops.purged = 1`); `Undo()` with A on top → `committedError{not_revertible}`: A popped from the stack, the 409 stored for the idempotency key, no ops row, no event (UND-26). |
| UND-14 | backlog [#X, #T, #Y]; op A `Place(#T, top)` → [#T, #X, #Y]; `AddTask("Z", --bottom)` → [#T, #X, #Y, #Z]; rank renormalization may run | `Revert(A)` | #T re-inserted relative to its recorded neighbor (after #X) → [#X, #T, #Y, #Z]. No conflict (Z is another entity; renormalization writes no events). |
| UND-15 | `#1{new}`; A `MarkManual(#1)` → `{new, manual}`; B `Complete(#1)` → `{}`; `Undo()` (R_B) → `{new, manual}` restored as delta, status open | `Undo()` (reverts A; B/R_B net-zero) | delta: only `manual` removed → `#1{new}`. |
| UND-16 | BLK-27 state (A = `Unblock(B1)`: cancelled, slipped, `#1{slipped}`) | `Undo()` | before-images restored directly: `B1{planned, null, 0, –}`, `slipped_at = null`; `#1{}` via delta (−slipped); no slip-rule events. If the undo happens the next day, B1 is a `planned` block on a past date → REC-10. |
| UND-17 | LIF-04 state (op D = Complete) | `Undo()` | status open, `completed_*` null, C1 tombstoned, `B1{planned}` again (started), B2/B3 `planned`; reasons restored as delta (`+new` if Complete removed it, original `since`); `last_placed_at`/`due_ack*` back to their before-images (Complete is a placement); recompute re-derives nothing (B1 was re-planned, not auto-resolved). Focus (R15): same-day undo → `focus:set` back to the before-image (#1, today) because invariant I6 still holds; the midnight `system.reconcile` focus event is ignored in the conflict check, so an undo on 10-29 is **not** refused — it leaves focus null (before-image `date = 10-28` fails I6), no error. |
| UND-18 | empty stacks | `Undo()` → `nothing_to_undo` (409); `Redo()` → `nothing_to_redo`. Both roll back and store nothing (not committed pops). |
| UND-19 | owner op A via CLI | web `undo` | reverts A (same actor `owner`, `channel` differs on the event). |
| UND-20 | UND-11 state (X reverted by owner); mcp undo stack = [Y (older, active), X (top)] | `mcp Undo()` key K | X is `reverted` → `committedError{revert_conflict "already reverted by owner"}` (A§4.6/A16): X is popped and discarded, the 409 (code + detail) is stored for K, no ops row, no event, Y untouched. `mcp Undo()` key K again → replays the same 409 without touching Y. `mcp Undo()` key K2 → reverts Y. `Revert(X)` explicitly → `revert_conflict` ("already reverted by owner"). |
| UND-23 | op A `Block(#1, 10-29, 09:00-10:00)` | `Revert(A)` | block tombstoned (`block:update B1 (deleted_at)` via `setBlock{Restore}`), never slips, excluded from overlap checks; `last_placed_at`/`due_ack*` of #1 restored; reasons re-added as delta with original `since`. A later `Block(#2, 10-29, 09:00-10:00)` → accepted. |
| UND-24 | `mcp` op X = `AddEntry(#1, "a", mon)` (B1 Mon missed→worked, `#1{continue}`, derived); then owner op Y = `AddEntry(#1, "b", mon)` (no block change, no derived events) | owner `Revert(X)` | no conflict (Y touched entry E_b, X's non-derived events touch only E_a); E_a tombstoned; recompute of (#1, Mon) finds E_b → B1 stays `{worked}`; **no** block or triage events are written (R2-1 second-entry case); `#1{continue}` unchanged. |
| UND-25 | op F = `SetFocus(#1)` at 10-28 22:00 with before-image focus = #2 (date 10-28); midnight reconcile cleared focus | `Undo()` on 10-29 | the before-image (#2, date 10-28) fails I6 → focus left null; the current focus is already null → **no domain events**, yet the undo succeeds (R2-4): ops row `op.undo` kept with `op_id` returned (not `NoChange`), F.state = reverted, F moved to the redo stack, effects `[]`. |
| UND-26 | owner undo stack = [B (active), A (top, `purged = 1`)] | `Undo()` key K | `committedError{not_revertible}`: A's stack row deleted, idempotency row for K stored with status 409 + code/detail, `COMMIT`; no ops row, no event. Retry `Undo()` key K → the same 409 is replayed and B is **not** touched (R2-5). `Undo()` key K2 → reverts B. |
| UND-27 | T0 = `AddTask("A")`; R = `Revert(T0)`; U = `Undo()` (pops R); D = `Redo()` (re-applies R by reverting U) | read `ops` states after each step | after R: T0 reverted, R active (undo stack [R]). After U: R reverted, T0 **active** (chain walked, R2-11), redo [R]. After D: U reverted, R active, T0 reverted; undo stack [R], redo []. `Redo()` again → `nothing_to_redo`. |
| UND-21 | `system` reconcile op S | `Revert(S)` → `not_revertible`. |
| UND-22 | A `Defer(#1, "+2d")` with B1(today) cancelled/deferred+slipped | `Undo()` | `defer_date` back to null, `B1{planned, 0}`, `last_placed_at` and `due_ack*` restored to before-images, reasons restored as delta. |

## 10. Idempotency (IDM)

| ID | Given | When | Then |
|---|---|---|---|
| IDM-01 | – | `POST /tasks {title:"A"}` key K → 201 #1; `UpdateTask(#1, "A2")`; replay `POST /tasks {title:"A"}` key K | 201 with current representation of #1 (title "A2", current version/ETag); no second task; no new op. |
| IDM-02 | after IDM-01 | `POST /tasks {title:"B"}` key K | `idempotency_mismatch` (422); no task created. |
| IDM-03 | owner used key K | `mcp POST /tasks {title:"C"}` key K | independent: 201 #2 (hash includes actor). |
| IDM-04 | – | `Login`, `ChangePassword`, `UpdateSettings`, `CreateToken`, `RunBackup` (A§4.9 exempt list; purge and backup scrubbing are offline and never see idempotency) | no `idempotency` row written; `Idem.Body`/`request_hash` never computed over a password; test asserts no idempotency row, ops/event JSON, pending-request file, or log line contains the test password. |
| IDM-05 | key K stored at T | replay at T + 7 d + 1 s | treated as new (row expired): creates #2. |
| IDM-06 | – | non-exempt `POST`/`PATCH` without `Idempotency-Key`, or with a key outside `[A-Za-z0-9_-]{16,128}` | `idempotency_key_required` (428) / `malformed` (400); no op. |
| IDM-08 | `Place(#1, keep)` key K succeeded; then `Place(#1, keep)` key K again but with a stale `If-Match` | replay | `Replayed = true`, 200, current representation — the stored op wins; `If-Match` is not re-evaluated on a replay. |
| IDM-09 | CLI sends `Place(#1, top)` key K; the socket connection drops after the request was written, no response after retries (`client.ErrOutcomeUnknown`) | – | CLI saves `<state>/pending/K.json` (0600: method, path, If-Match, body, created_at), prints "outcome unknown — saved as pending request K; run `tkr pending retry`", exit 3. `tkr pending retry` resends with key K → replay (200, `replayed = true`) or the original result; the file is deleted on any definitive answer. A pending file whose age reaches 7 days − 1 hour (`client.PendingResendWindow`, a safety margin inside the server's 7-day idempotency retention) → `tkr pending retry` refuses it and tells the user to check (`tkr show`, `tkr entries`) then `tkr pending drop K` (R2-10). Password-bearing requests are never saved. |
| IDM-10 | `MarkManual(#1)` key K2 when `#1{manual}` already (no-op) | replay K2 | first call: `NoChange`, 200, idempotency row `{op_id: NULL, status 200, refs: [task #1]}` (R-j). Replay: 200, `replayed = true`, effects `[]`, current representation of #1. Same key with a different body → `idempotency_mismatch`. |
| IDM-07 | `POST /tasks/1/complete` key K → 200; `Reopen(#1)`; replay key K | 200 with current representation (#1 open) — no response body is stored; replay never re-completes. |

## 11. Scopes matrix (SCP)

| Operation | read | capture | write | destroy | admin |
|---|---|---|---|---|---|
| GET views, report, search, history, clock | ✓ | | | | |
| `AddTask` without placement flag (→ triage) | | ✓ | | | |
| `AddTask` with placement flag (`--top` … `--now`) | | ✓ | + ✓ **owner only** (any `ActorToken` → `forbidden`, R-b) | | |
| `AddEntry` (new `work` entry; explicit task or null) | | ✓ | | | |
| `Place`, `AcceptNew`, `MarkManual`, `Defer` (incl. nil), `SetDue`, `Block`, `Reblock`, `Unblock`, `SetOutcome`, `SetFocus`, `Complete`, `Reopen` (no `remove_entry`), `Undelete` (task, A18), `UpdateTask` (title/notes/tags/project/priority), `RelinkEntry`; `MarkAIReviewed` (**owner only**, R-a) | | | ✓ | | |
| `Drop`, `Delete`, `UpdateEntry`/`DeleteEntry`/`UndeleteEntry` of existing entries, `Reopen --remove-entry` | | | | ✓ | |
| `tkr purge`, `tkr scrub-backups` | offline: instance lock + password on a TTY; no token, no scope, no endpoint (A§14 D37) | | | | |
| `CreateToken`/`RevokeToken`/`ListTokens` (never via web), `UpdateSettings` (+ password when lowering retention), `ChangePassword` (owner), `RunBackup`, `BackupStatus`, `Export` | | | | | ✓ |
| `Revert`/`Undo`/`Redo` | scopes of the reverted op; a non-owner actor only for its own ops (A§14 D15) | | | | |


| ID | Given | When | Then |
|---|---|---|---|
| SCP-01 | token `ro` = read | `AddTask`, `Log`, `Place` → `forbidden`; `GET /today` → 200. |
| SCP-02 | `mcp` (read+capture+write) | `Drop`, `Delete`, `DeleteEntry`, `UpdateEntry(existing)`, `Reopen --remove-entry` → `forbidden`; `Complete`, `Defer`, `Block`, `Place`, `UpdateTask`, `Undelete`, `AddEntry`, `AddTask` (no placement) → allowed. There is no purge endpoint to call (PRG-18). |
| SCP-03 | token `t` with `capture+write` (also `mcp`) | `AddTask("x", place = top)` → `forbidden` (R-b: token-created tasks always land in triage, regardless of scopes); `AddTask("x")` → 201 `{new}`; the same `place` by owner → allowed. The MCP `add_task` tool never offers a placement field. |
| SCP-04 | owner token | over the TCP web listener any `/api/v1/*` request → 401 `unauthenticated` (v1 API is socket-only, A§14 D13/16). `AuthenticateToken(owner, verifiedSocket = false)` → `unauthenticated` (unit test of the socket-only rule). Same token over the socket with matching peer UID → 200. Socket connection whose peer UID ≠ server UID → 403 `peer_rejected` before authn. |
| SCP-05 | web session cookie | `POST /api/v1/tasks` on the socket with cookie only → `unauthenticated` (the API chain strips `Cookie`); bearer token on `/ui/*` → 303 to `/ui/login` (GET) / 401 (POST), `Authorization` stripped. |
| SCP-06 | socket request carrying `Forwarded` / `X-Forwarded-For` / `Via` | any `/api/v1` call → 403 `peer_rejected`, even with a valid owner token. |
| SCP-07 | web session (owner) | `CreateToken`/`RevokeToken`/`ListTokens` → `forbidden` (channel `web`); everything else as owner; retention lowering (`PATCH /settings`) still requires the password; purge and backup scrubbing are not available from the web (offline commands). |
| SCP-13 | owner token on the socket with `X-Taskronin-Channel: mcp` | any request → `forbidden` (A§14 D33); `tkr mcp` itself refuses to start when `GET /whoami` says `owner: true`. |
| SCP-08 | token `w` = write only (no read) | `Place(#1, keep)` → allowed; `GET /tasks/1` → `forbidden`. |
| SCP-09 | `mcp` | `Undo()` of its own `AddEntry` → allowed (`capture`); `Revert(owner Drop)` → `forbidden` (scope and ownership, UND-12). |
| SCP-10 | `mcp`, `mutations_per_hour = 60` | 61st mutation within a sliding hour → `rate_limited` with `RetryAfter = oldest op in window + 1 h − now` (counted from `ops` rows of the actor, A§4.2 step 9; `NoChange` ops are rolled back and do not count); reads unaffected; owner has no cap. |
| SCP-11 | `mcp` | `AddEntry(#1, …)` → op `actor = token:ai`; listed in `GET /ai-changes` (`unreviewed = 1`); `status --short` shows `· ai 1` until the **owner** runs `MarkAIReviewed(through_seq ≥ op.seq)` (`POST /ai-changes/review`, `tkr ai-changes --ack`), which sets `settings.ai_reviewed_through`. `mcp MarkAIReviewed` → `forbidden` (owner only, R-a); owner with `through_seq` > latest committed `ops.seq` → `validation`. The review op is not revertible. |
| SCP-12 | expired token / revoked token / token file mode 0644 | request → `unauthenticated`; client refuses to send a token from a non-0600 file. |

## 12. Dates and datespec (DAT)

now = Wed 2026-10-28 10:00 MDT. "past-inclusive" rules apply to `log --date`
and `done --date`; "future-exclusive" rules to `defer`, `due`, `block --date`.

| ID | Input | For | Resolves to | Notes |
|---|---|---|---|---|
| DAT-01 | `today` / `yesterday` / `-2d` / `2026-10-01` | log | 10-28 / 10-27 / 10-26 / 10-01 | |
| DAT-02 | `wed` / `tue` / `thu` / `monday` / `Mon` / `tues` / `weds` / `thur` / `thurs` | log | 10-28 (today included) / 10-27 / 10-22 / 10-26 / 10-26 / 10-27 / 10-28 / 10-22 / 10-22 | case-insensitive; forms per the A§9.2 grammar; `+0d` = today |
| DAT-03 | `tomorrow` / `+1d` / `+3d` / `2026-10-29` | log, done | `validation` (future `work_date` / `completed_date`) | server validates even if a client sends it |
| DAT-04 | `tomorrow` / `thu` / `wed` / `mon` / `+3d` / `+2w` | defer | 10-29 00:00 / 10-29 / 11-04 (today excluded) / 11-02 / 10-31 / 11-11 | |
| DAT-05 | `today` / `yesterday` / `-1d` / `today 09:00` | defer | `validation` (moment ≤ now) | `today 15:00` → ok (15:00 > now) |
| DAT-06 | `15:00` (bare time) | defer, due | today 15:00 | |
| DAT-07 | `mon 09:00` / `2026-11-03 14:00` / `+3d 09:00` | defer | 11-02 09:00 / 11-03 14:00 / 10-31 09:00 | `defer_min = 540 / 840 / 540` |
| DAT-08 | `today` / `fri` / `wed` / `2027-03-14` | due | 10-28 (moment 24:00) / 10-30 / 11-04 / 2027-03-14 | `yesterday` → see DUE-11 |
| DAT-09 | (none) / `fri` / `wed` / `yesterday` | block `--date` | 10-28 / 10-30 / 11-04 / `validation` | |
| DAT-10 | `--at 14:30` / `--at 24:00` / `--at 9:5` | log | `occurred_min = 870` / `validation` / `validation` | |
| DAT-11 | `--at 02:30` with `--date 2027-03-14` | log | accepted, `occurred_min = 150` (display/sort only) | |
| DAT-12 | `2026-13-01` / `2026-2-3` / `nextweek` / `+3x` | any | `validation` | |
| DAT-13 | `--week` / `--last-week` / `--month` / `--today` | report | 10-26..11-01 (full current week from `week_start`, A§14 D12 as revised in round 1) / 10-19..10-25 (previous full week) / 10-01..10-31 (full calendar month) / 10-28 | inclusive; `--from 10-30 --to 10-28` → usage error (CLI) / `validation` (API); `to − from > 366 d` → `validation` |
| DAT-14 | `GET /clock` | – | `{"now":"2026-10-28T10:00:00.000-06:00","today":"2026-10-28","timezone":"America/Denver","week_start":"monday","due_soon_days":3,"server_version":…}`; after fall-back the offset is `-07:00`. Clients resolve datespecs against `today`; the server validates and echoes resolved values in `effects`. |
| DAT-17 | `--at 10:30` with `--date today` (now 10:00) | log | `validation` (A§14 D9: a time on today cannot be in the future); `--at 09:59` → ok; `--at 10:30 --date yesterday` → ok. |
| DAT-15 | `defer 2026-11-01 01:30` | defer | `defer_min = 90`; moment = earlier occurrence 07:30Z | compare DUE-03 |
| DAT-16 | day of instant | – | 2026-11-02T06:30Z → civil date 2026-11-01; 2027-03-15T06:00Z → 2027-03-15 | "today" is the civil date in the zone |

## 13. Report (RPT)

Fixture (all 2026): #42 "Set up keycloak postgres cluster" (tag `infra`,
project `infra.keycloak`); #57 "Run ansible against dev qt cluster" (tag
`infra`); #7 "Write report" (project `docs`). Entries:
- `W(#42, Fri 10-23, "Added Ansible role for postgres cluster")` created 09:00
- `W(#42, Fri 10-23, "Created pgk cluster in dev")` created 15:00
- `W(null, Fri 10-23, "Answered Bob's DNS question", at 11:00)`
- `W(#7, Mon 10-26, "Drafted outline")`
- `W(#42, Mon 10-26, "Failover tested in dev")`
- `Complete(#42)` Mon 10-26 → `C(#42, 10-26)`; `Complete(#57, --as "Add svc-cluster weencrypt name to certs")` Mon 10-26
- `Reopen(#42)` Tue → C marked reopened

| ID | When | Then |
|---|---|---|
| RPT-01 | `report --from 10-23 --to 10-26 --format text` | Days ascending; within a day timed entries first (by `occurred_min`), then untimed by `created_at`: `Fri 2026-10-23` → `Answered Bob's DNS question` (11:00, no annotation) · `Added Ansible role… [#42 Set up keycloak postgres cluster]` · `Created pgk cluster in dev [#42]`; `Mon 2026-10-26` → `Drafted outline [#7 Write report]` · `Failover tested in dev [#42]` · `✓ Set up keycloak postgres cluster [#42] (reopened)` · `✓ Add svc-cluster weencrypt name to certs [#57 Add svc-cluster weencrypt name to certs]`. |
| RPT-02 | `✓` prefix | exactly `complete` entries; `(reopened)` suffix exactly those with `reopened = 1`. |
| RPT-03 | first appearance | full title on a task's first line **in this report**, `[#ID]` after — across days. `report --from 10-26` alone shows `[#42 Set up keycloak postgres cluster]` on "Failover tested" (first appearance in that report). |
| RPT-04 | standalone entries | no annotation at all. |
| RPT-05 | `--todo` | appends "Triage" (view order, with reasons) then "Backlog" (rank order) sections; `--upcoming` appends "Upcoming" (defer order, due-soon). Both: todo then upcoming. |
| RPT-06 | `-t infra` | only entries of tasks tagged `infra` (#42, #57); standalone and #7 entries excluded (A§14 D12). `-P docs` → only #7's. `-P infra` → #42's entries too: a project filter matches the project itself and any dotted descendant (`infra` matches `infra.keycloak`, prefix on a `.` boundary; `inf` or `infra.key` match nothing) (R21). `-t Infra` → same as `-t infra` (tags are case-insensitive). |
| RPT-07 | #7 deleted (task tombstoned, its entries live); #42 purged with `tkr purge task 42` (task + `C101` tombstoned and scrubbed; `W100`/`W102` left alone) | `report` | #7's entry is still listed with #7's current title (a deleted *task* does not hide its entries, D§5.1). Reports list only non-deleted entries: `C101` (tombstoned by the purge) is **gone** — no `✓ [purged]` line; `W100`/`W102` remain with their own titles, annotated `[#42 [purged]]` then `[#42]` (the task's scrubbed title). After `tkr purge task 42 --with-entries` no #42 line remains at all. `ListEntries(include_deleted = true)` and `GetTask(42).entries`-style reads that include deleted rows show them as `[purged]`. |
| RPT-08 | retitle | #57's earlier entries (if any) show the **current** title. |
| RPT-09 | `--format json` | `api.Report`: `{from, to, tag, project, days:[{date, lines:[{entry_id, kind, title, notes, task_id, time, reopened}]}], tasks:{"42":{title, status, deleted}, …}, todo?, upcoming?}`; days ascending, days without entries omitted. `md`: `## Mon 2026-10-26` headings, `- ` / `- ✓ ` bullets, annotation `(#42 Title)` then `(#42)`, user text markdown-escaped. Text: title column padded to the widest line (max 60 cols). |
| RPT-10 | range with no entries | `days = []`, `tasks = {}`; text output prints the header only (plus any `--todo`/`--upcoming` sections); exit 0. |

## 14. Purge (PRG) — offline command

Purge is an **offline** command (A§14 D37, A§12.6, D§5.11 as amended):
`tkr purge task|entry ID [--with-entries] [-y]` needs the instance lock (server
stopped) and the password on a TTY, then runs: ledger intent → live scrub →
WAL checkpoint → scrub of every backup set via journaled swaps → ledger
`complete`. There is no API, web or MCP purge. Fixture: server stopped; #42
with `C101(#42, 10-26)`, `W100(#42, 10-23)`, `W102(#42, 10-26)`; #42 is `done`
then `Delete`d (tombstoned); password `pw`; the backup dir holds snapshot S1
(taken while #42 was still open, with `#42{new}` and a planned block B9 on
10-27) and a `*.pre-restore-*` set P1; ledger at `<data>/purge-ledger` (0600).

| ID | Given | When | Then |
|---|---|---|---|
| PRG-01 | server running (instance lock held) | `tkr purge task 42` | refused before any prompt: "the server is running — `systemctl --user stop taskronin.socket taskronin.service` first"; nothing changed. With `<data>/swap.journal` or `<backupdir>/scrub.journal` present → refused ("resume it first"). |
| PRG-02 | stdin is not a TTY | `tkr purge task 42 -y` | refused: the password can only be prompted on a TTY (`-y` skips the confirmation, never the password); exit 4; nothing changed. |
| PRG-03 | TTY, wrong password three times | `tkr purge task 42` | 1 s delay after each failure; after the third → exit 4. No ledger line, no DB change, no ops row. The password never appears in logs, ledger or any file. |
| PRG-04 | #42 `done` but **not** tombstoned | `tkr purge task 42` (pw) | `conflict` "delete it first" (exit 6) from `PurgeLive` **before** the ledger append: no ledger line, nothing scrubbed. `tkr purge entry 100` while e100 is not tombstoned → same. Missing ID → `not_found`. |
| PRG-05 | fixture | `tkr purge task 42` (pw) | Order (A§12.6): lock → `CleanupTemps` → password → `OpenLedger` (not damaged) → `ReplayLedger(live)` (no earlier intent → true no-op, no ops row) → **one writer transaction** `purge.task` (actor `owner`, channel `cli`, not revertible, no idempotency) via `run`: validate the target, select the exact set {task 42; entries [101]} (the `complete` entry only), `Append` the `pending` line + fsync file and dir **while holding the writer**, `ScrubEntities`, content-free `purge` event `{"ids":[42,101]}`, commit → `PRAGMA wal_checkpoint(TRUNCATE)` (succeeds: no other connection; WAL truncated) → scrub S1 and P1 (PRG-10) → `Append` the `complete` line → print scrubbed IDs, "linked entries not purged: e100, e102", per-set results, and that earlier exports/log lines are out of reach. `PurgeLiveResult{TaskIDs:[42], EntryIDs:[101], LinkedNotIncluded:[100,102], Changed:true}`. |
| PRG-06 | fixture | `tkr purge task 42 --with-entries` | set = {42; 100, 101, 102}; all four scrubbed; `LinkedNotIncluded = []`. `DeleteEntry(100)` then `tkr purge entry 100` → set = {entries [100]} only; #42 untouched. |
| PRG-07 | after PRG-06 | inspect the live DB | `ScrubEntities` (A§12.0): per member **one** `UPDATE … SET deleted_at = COALESCE(deleted_at, now), purged_at = now, title = '[purged]', notes = ''` — e100–e102 were live, so the FTS trigger sees their old content and removes their rows with `secure-delete` (R-p); task `project = NULL`, its `task_tags` deleted, orphaned `tags` deleted; `before_json`/`after_json` of every `task`/`entry`/`task_tags` event of the set rewritten (`title → "[purged]"`, `notes → ""`, `project → null`, `tags → []`, other fields intact); every op with an event on the set has `purged = 1, revertible = 0`. FTS content `integrity-check` passes; `CheckAll` passes (I4 accepts a done task whose only completion entry is purged). |
| PRG-08 | after PRG-05 | read the ledger | two JSON lines: `{"v":1,"rec":"<32 hex>","at":<ms>,"state":"pending","tasks":[42],"entries":[101],"sum":"<64 hex>"}` then the same `rec` with `"state":"complete"`; `sum` = SHA-256 of the record encoded with `sum = ""`. Lines are never removed or rewritten except by `--repair-ledger`. |
| PRG-09 | crash injected inside the writer tx **after** the pending `Append` + fsync, before `COMMIT` | `tkr serve` | the tx rolled back (DB unchanged) but the `pending` line exists → startup `ReapplyLedger` (`ReplayLedger(live)`, op `system.ledger_replay`, events) scrubs the set, then `wal_checkpoint(TRUNCATE)` before the listeners open. Startup never writes `complete`: dashboard banner `purge_incomplete`, `status --short` shows `⚠ purge` (`purge_warning`), until `tkr scrub-backups` finishes every backup set and appends `complete`. |
| PRG-10 | backup scrub (purge step 7 = `tkr scrub-backups` steps 4–6) | sets S1, P1 | per set: `Stage` (copy DB + `-wal` into `<data>/staging/`, **normal** open so the WAL is recovered, then `journal_mode=DELETE`; never `immutable=1`) → `ReplayLedger(historical)` (`noReconcile`, `structuralOnly`): in S1 #42 is open → delete semantics first: `B9{cancelled, –, 0, task_closed}` (no derivation, no slip), triage rows deleted, focus cleared if on #42, then tombstone + scrub; **no reconcile** — S1's other past `planned` blocks stay `planned` (point in time preserved, R2-2) → `CheckStructural`, `integrity_check`, FTS check → `VACUUM INTO` a `.tkr-tmp-*` file next to the target → `Verify` → `Swap{Kind: scrub, DeleteAside: true}` (old set moved to a `.tkr-tmp-` quarantine dir, new file renamed into place, old set unlinked, journal removed). Snapshot names and count unchanged. A set without any member → `clean`, discarded, untouched. |
| PRG-11 | crash after PRG-05's commit, before the checkpoint | `tkr serve` | `ReapplyLedger` finds nothing to change (`ScrubEntities` is a true no-op: no ops row, no events, R2-12), checkpoints the WAL; record still `pending` → `purge_incomplete` banner until `tkr scrub-backups`. |
| PRG-12 | crash during step 7 leaving `<backupdir>/scrub.journal` at step `moved-old` | `tkr serve` → refuses to start ("resume the swap"). `tkr scrub-backups` (or `--resume`) → `ResumeSwap` finishes that swap first (install `New`, unlink the aside set, remove the journal), then continues with the remaining sets, then appends `complete` for every `pending` record. A later failure that leaves a journal stops the batch (R2-7); a failure without a journal is reported and the batch continues. |
| PRG-13 | crash before the pending line's fsync completed (file ends mid-line, no newline) | next `OpenLedger` (startup or purge) | torn tail → truncated back to the last complete line and fsynced before any append; `Damaged = false`; nothing was scrubbed (the append was inside the uncommitted tx); the user simply re-runs purge. |
| PRG-14 | ledger with a newline-terminated line whose `sum` is wrong (or that does not parse) | `tkr serve`; `tkr purge task 42` | startup: `Damaged = true`, the bad line is kept on disk and skipped, all valid records replay (R2-8); banner `purge_ledger_damaged`, `⚠ purge`. `tkr purge task 42` → refused "run `tkr purge --repair-ledger`" (no new intents while damaged, no prompt side effects). `tkr scrub-backups` is still allowed and applies the valid records. |
| PRG-15 | PRG-14 | `tkr purge --repair-ledger` (lock + pw) | `Repair()` rewrites the file from the valid records (temp + fsync + rename + dir fsync) and prints the dropped line numbers; then `ReplayLedger(live)` + checkpoint; `Damaged = false`; the banner is gone at the next start. |
| PRG-16 | already purged (PRG-05 done) | `tkr purge task 42` again (pw) | content unchanged; `ReplayLedger` and `PurgeLive` write no events (`Changed = false`, no ops row); a ledger record covering {42, 101} exists; exit 0. |
| PRG-17 | ops A (`AddTask` → #42) and L (`AddEntry` → e100), both `purged = 1` after PRG-06 | `Revert(A)`, `Revert(L)` → `not_revertible`; `Undo()` with A on top → `committedError{not_revertible}` (UND-26). `GET /tasks/42/history` still lists the events, with scrubbed payloads. |
| PRG-18 | – | `POST /api/v1/tasks/42/purge`, `POST /api/v1/entries/100/purge`, a web purge form, an MCP purge tool | none exist: 404 (route not registered) / no form / no tool. No scope grants purge; `destroy` covers only drop/delete/entry edits (A§7.3). |
| PRG-19 | after PRG-06 (`--with-entries`) | `report`, `ListEntries`, `GetTask(42)`, `search "keycloak"` | `report` and default `ListEntries` show **no** #42 lines (every set member is tombstoned; reports exclude tombstoned entries, RPT-07); `ListEntries(include_deleted = true)` lists e100–e102 with `title = "[purged]"`, `notes = ""`, `deleted_at`/`purged_at` set; `GetTask(42)` returns `title = "[purged]"`, `purged_at` set, `view = deleted`; search finds nothing. |
| PRG-20 | an older snapshot S0 or an old export still holds #42's text | `tkr restore S0` / `tkr import FILE` | the staged copy gets `ReplayLedger(live)` → `Reconcile` → `CheckAll` before the swap, so the installed DB already shows `[purged]`; the pre-restore/pre-import set left aside is scrubbed by the next `tkr scrub-backups` (PRG-10) and reported by purge step 9 until then. |
| PRG-21 | interrupted staging left a copy under `<data>/staging/` and a `.tkr-tmp-*` file in the backup dir, none referenced by a journal | next `tkr serve` or any offline command | `CleanupTemps` deletes them under the lock (they may hold unscrubbed content, R2-6); paths referenced by an active swap journal are kept for `--resume`. |
| PRG-22 | – | `tkr scrub-backups --check` | reports `contains` / `clean` per set, rewrites nothing, takes no `prescrub` snapshot, appends no `complete` line; exits 8 if any set reports `contains`, else 0. Without `--check`: `ReplayLedger(live)` + checkpoint first, a fresh verified `prescrub` snapshot, then PRG-10 per set, then `complete` lines for all `pending` records. |

## 15. Interruptions (INT)

| ID | Given | When | Then |
|---|---|---|---|
| INT-01 | focus = #42; current block B1(#42, 09:00-11:00); now 10:20 | `AddTask("Restart stuck pg replica", --now)` → #60 | #60 open, rank top, `#60{}`, PLACED; `focus:set` → #60 (`focus.date` today); `interruption:insert` row `{at: 10:20, date: 10-28, from_task_id: 42, from_block_id: B1, to_task_id: 60, op_id}` (A§14 D11). B1 unchanged (still planned). Effects include `interrupted`. |
| INT-02 | no focus; current block B1(#42) | `AddTask("x", --now)` | interrupt target = #42 (via block). |
| INT-03 | focus = #42; no current block | `AddTask("x", --now)` | interrupt target = #42 (via focus), `block_id = null`. |
| INT-04 | no focus, no current block | `AddTask("x", --now)` | focus set; **no** interruption row. |
| INT-05 | INT-01 state (focus #60, B1(#42) current) | `SetFocus(#42)` (back to the block's task) | focus = #42; **no** interruption row (switching to the current block's own task never records one). |
| INT-10 | INT-01 state | `SetFocus(#61)` (a third task, while B1(#42) is current) | interruption `{from_task_id: 42, from_block_id: B1, to_task_id: 61}` — a different task's block is current (A§15 Q2). |
| INT-11 | focus = #42, **no** current block | `SetFocus(#61)` | focus = #61; no interruption. `SetFocus(#61, interrupt = true)` (`tkr now 61 --interrupt`) → interruption `{from_task_id: 42, from_block_id: null, to_task_id: 61}`. |
| INT-12 | focus = #7; current block B1(#42) | `AddTask("x", --now)` → #62 | interruption `{from_task_id: 42, from_block_id: B1, to_task_id: 62}` — the current block's task wins over the previous focus. |
| INT-13 | no focus, no current block | `SetFocus(#61, interrupt = true)` | focus set; no interruption (nothing to interrupt); no error. |
| INT-06 | INT-01 state | `tkr log "Restarted replica"` | links to #60 (focus wins over current block). |
| INT-07 | INT-01 state, nothing logged for #42 | `Reconcile(next 00:00)` | `B1{missed, auto, 1}`, `#42{slipped}` — an interrupted block slips like any other. With `W(#42, today)` → worked, `#42{continue}`. |
| INT-08 | INT-01 state | `recap` / Today view for today | lists "10:20 #60 'Restart stuck pg replica' interrupted #42 (block 09:00–11:00)". |
| INT-09 | INT-01 | `Undo()` | #60 tombstoned (`task:tombstone`); interruption row tombstoned (`interruption:tombstone`) and no longer listed in Today/recap; `last_placed_at`/`due_ack*` before-images restored. Focus (R15): same-day undo → restored to #42 (before-image, I6 holds); next-day undo → focus null, no error; the system's midnight focus clear never causes a `revert_conflict`. |

## 16. Resolved ambiguities

Rulings applied throughout the catalog (R-numbers are referenced from the
scenarios). "A§" = architecture Draft A1.

| # | Where | Question | Ruling |
|---|---|---|---|
| R1 | TRI-18 | Bulk "accept all `new`" with tasks that have `new` plus other reasons. | Only tasks whose reason set is exactly `{new}` qualify; an item that does not → `conflict`, all-or-nothing; per-item etags (A§3.2 `AcceptNew`). |
| R2 | TRI-20 | Direction of the `since` tiebreak. | Ascending (oldest pending first). |
| R3 | TRI-23 | `undelete` of a non-open task adds `manual`? | No (A§14 D30). |
| R4 | BLK-06, BLK-07 | Blocks in the past. | Reject a block whose end instant ≤ now (`validation`); a block whose start has passed but whose end is in the future is allowed (A§14 D8). |
| R5 | BLK-12 | Block end exactly at the spring-forward gap (`01:00-02:00` on 2027-03-14). | Reject (`validation`), like any nonexistent wall time; message suggests `-03:00`. |
| R6 | BLK-14 | "Current block" in the repeated fall-back hour. | Instant semantics (A§2.1: containment computed on instants, never on minutes). |
| R7 | BLK-41 | `reblock` of a `cancelled`/`worked`/`done` block. | Allowed; same as reblocking a resolved `missed` block: the old block is untouched, a new planned block is created, the task is placed. |
| R8 | BLK-44 | Does a user outcome `worked` add `continue`? | Yes (A§3.3 `SetOutcome`: slip/continue rules apply). |
| R9 | BLK-47 | Recap outcome on today's still-planned block. | Allowed on resolved blocks and on today's `planned` blocks once **started** (A§3.3/A9, round 1 wording); unstarted planned or cancelled → `conflict` ("use unblock"). Resolves now with `outcome_source = user`. |
| R10 | LIF-07 | `done` on a `dropped` task. | `conflict`; transitions limited to A§14 D10. |
| R11 | DUE-01, DUE-13, DUE-17, LIF-18 | Reconcile steps 2 and 3 both fire → `{due, overdue}` on one task. | Keep: harmless (ordering uses the highest reason, placement clears both); A§4.3 implements the steps literally. |
| R12 | DUE-11 | Setting a due moment in the past. | Reject (`validation`). `due today` (date-only, moment 24:00) is allowed. |
| R13 | DUE-18 | Due-soon bound. | `due_date ≤ today + due_soon_days` (A§2.6 `TaskView.DueSoon`). |
| R14 | UND-06 | After a `revert_conflict`, does the op stay on the undo stack? | Stays; a failed undo leaves both stacks unchanged (A§4.5). |
| R15 | UND-17, INT-09 | Is focus restored by undo/revert? | `focus` events are ignored in revert conflict checks (the midnight `system.reconcile` clear must not block next-day undo). The focus before-image is restored only if it still satisfies invariant I6 (task open, not deleted, not deferred, `before.date = today`); otherwise focus is left unchanged (null after midnight), no error. Ruled §17 C1; architecture A§4.5 to be updated. |
| R16 | UND-20 | Actor A's `undo` whose top op was already reverted by actor B. | A§4.6 (round 1–2): `committedError{revert_conflict "already reverted by <actor>"}` — the entry is popped, the 409 is stored for the idempotency key, no ops row/event; other stack entries untouched. An explicit `Revert` of a reverted op → `revert_conflict`. |
| R17 | IDM-06 | Missing `Idempotency-Key`. | `idempotency_key_required` (428); malformed key → `malformed` (400) (A§2.8, A§4.2 step 2). |
| R18 | SCP matrix | Scope of `AddTask` with a placement flag, and of `Undelete`. | `AddTask` with `place` needs `capture + write` **and the owner** (tokens → `forbidden`, R-b); task `Undelete` is `write` (A§3.2/A18, final); `UndeleteEntry` is `destroy` (A§3.4). |
| R19 | DAT-06 | Bare `HH:MM` as a datespec. | Today at that time (A§9.2 grammar: "clock alone → today"); future-direction commands reject it when ≤ now. |
| R20 | DAT-11 | `--at` with a nonexistent DST wall time. | Accept — `occurred_min` is display/sort only; it is never converted to an instant. |
| R21 | RPT-06 | Report filters. | `-t`/`-P` exclude standalone (task-less) entries (A§14 D12). `-P X` matches project `X` and any dotted descendant `X.*` (prefix on a `.` boundary), not a plain string prefix. |
| R22 | INT-01 … INT-13 | When is an interruption recorded? | A§15 Q2: `add --now` always records one — against the current block's task if a block of another task is current, else against the previous focus if any and different. `tkr now ID` records one only when a different task's block is current, or with `--interrupt` (then against the current block, else the previous focus). Switching to the current block's own task never records one. |
| R23 | LIF-14 | `reopen --remove-entry` re-derives the completion day's `done` block to `missed` → slip + `slipped` reason beside `reopened`. | Keep the literal reading: the commitment is no longer honored once the completion is withdrawn. |
| R24 | TRI-27 | No way to un-defer a task without blocking it or waiting for it to surface. | `tkr defer ID none` = `Defer(task, nil)` = `Place{keep}`: clears the defer, is a placement, keeps the rank (A§14 D5). `Place` and `MarkManual` on a deferred task also clear the defer (D5, D4). |

## 17. Conflicts with architecture

Behaviour where this catalog and architecture Draft A1 disagreed and the design
did not settle it. **All six are ruled** (team lead, 2026-10-25) in favour of
the recommendation column; the catalog already asserts the ruled behaviour and
the architecture is being updated to match.

| # | Catalog | Architecture (Draft A1) | Recommendation → ruling |
|---|---|---|---|
| C1 | R15 / UND-17, INT-09: focus is **not** restored by revert, and `focus` events do not participate in the revert conflict check. | A§4.5 step 3: "focus set → `Before`" (the before-image `{task_id, date}` is written back), and `focus` events are not excluded from the conflict check (A§4.5 step 2). | **Ruled** — Change the architecture: (a) skip `focus` events in the conflict check — otherwise the midnight `system.reconcile` focus clear makes every undo of a `Complete`/`Drop`/`Defer`/`add --now` that touched focus fail the next day; (b) restore the before-image only when it still satisfies invariant I6 (task open, not deleted, not deferred, `before.date = today`), else leave focus unchanged. With (b) the catalog's "not restored" expectation becomes "restored only on the same day" — UND-17/INT-09 should then assert: same-day undo → focus back to the before-image; next-day undo → focus null, no error. |
| C2 | BLK-08, FOC-03: `Block`/`SetFocus` on a done/dropped/deferred task → `conflict`. | A§3.9 lists "planned block on ineligible task, focus on ineligible task" in the row "`conflict` (overlap) / `validation` (field-level)" without saying which applies. | **Ruled** — Architecture should state it explicitly; recommended `conflict` (it is a state problem, not a field problem). Field-level problems (span, past block, DST gap, defer/due rule) stay `validation`. |
| C3 | DAT-05, DUE-11: `Defer` to a moment ≤ now and `SetDue` to a moment ≤ now are rejected (`validation`). | A§3.2 `Defer`/`SetDue` list only the D§5.7 rules and the overdue-task rule; nothing about past moments. | **Ruled** — Add both rejections to A§3.2 (otherwise a past defer silently surfaces at the next reconcile with a misleading `surfaced` reason, and a past due is immediately `overdue`). |
| C4 | BLK-41 (R7): `Reblock` of a `cancelled`/`worked`/`done` block creates a new block. | A§3.3 `Reblock` covers planned (in place or ended) and resolved `missed` blocks only. | **Ruled** — Add the R7 wording to A§3.3. |
| C5 | RPT-06 (R21): `-P X` matches `X` and `X.*`. | A§3.7 `ListTasks`/`Report` take `Project` with no matching rule; A§14 D12 covers only standalone exclusion. | **Ruled** — Add the dotted-descendant rule to A§3.7 and A§9.7 (one `LIKE`/`GLOB` on `project = ? OR project GLOB ?||'.*'`). |
| C6 | TRI-18: an `AcceptNew` item whose reason set is not exactly `{new}` → `conflict`, whole op refused. | A§3.2: "all-or-nothing; only tasks whose only reason is `new`" — refuse or skip is not stated. | **Ruled** — Architecture should state "refuse with `conflict` naming the item" (the TUI/web only ever send qualifying tasks, so a mismatch means a stale screen). |

### 17.1 Alignment with architecture review rounds 1–2 (A§14.4, A§14.5)

Applied in this catalog (Draft 3), superseding earlier catalog text where noted:

| Ref | Change adopted | Catalog |
|---|---|---|
| S3 | Ordering/acknowledgement by `ops.seq` (`last_placed_seq`, `slipped_seq`, `resolved_seq`, `task_triage.seq`); `*_at` informational | §0, TRI-20, BLK-18, SLP-*, L/S everywhere |
| S4 / D37 | Purge is offline (`tkr purge`), no endpoint, password on a TTY, ledger with checksums, torn-tail/damaged handling, `--repair-ledger`, backup scrub via journaled swaps | §14 rewritten (PRG-01…22), SCP matrix, SCP-02/03, IDM-04, UND-13 |
| R2-1 | `events.derived`; revert skips derived events and re-derives | §0, SLP-05, UND-11, UND-24 |
| R2-4 | Eventless undo/redo still finalizes state and stack | UND-25 |
| R2-5 / R-h | Committed pops store a 409 for the idempotency key; lookup before target selection | UND-13, UND-20, UND-26, PRG-17 |
| R2-11 | `ops.state` chain walked transitively | UND-27 |
| R-j | No-ops store an idempotency row (`op_id` NULL) | TRI-22, IDM-10 |
| R-m / R2-10 | `tkr pending` with the 7-day limit | IDM-09 |
| R-a | `MarkAIReviewed` owner only; `through_seq` bound | SCP-11, matrix |
| R-b | Tokens cannot place at `AddTask` | SCP-03, matrix |
| C6 | `AcceptNew` all-or-nothing | TRI-18 (unchanged) |
| A9 (round-1 wording) | Recap outcome allowed on today's **started** planned blocks | BLK-47, R9 |
| A16 (round-1 wording) | Undo of an already-reverted op → `committedError{revert_conflict}`, popped | UND-20, R16 |
| A18 (final) | Task `Undelete` is `write`; clears a future defer on an open task | TRI-21, TRI-23, TRI-29, R18 |
| D12 (revised) | `--week`/`--month` are full calendar periods again | DAT-13 |
| A§2.8 | `nothing_to_undo` / `nothing_to_redo` are codes of their own | §0, UND-04, UND-18 |

**Remaining conflicts after this pass: none.** Two notes for the architect,
not conflicts: (1) A§3.9 and A§6.4 still say `Undo`/`Redo` on an empty stack
→ `nothing_to_undo`/`nothing_to_redo` while A§4.3 step 7 says the same — the
catalog follows them; (2) A§12.6 step 5 appends the ledger line *inside* the
writer transaction, so a crash before `COMMIT` leaves a `pending` record for
content that was never scrubbed — harmless (replay scrubs it at the next start,
PRG-09) but worth a sentence in A§12.6 so implementers do not "fix" it.
