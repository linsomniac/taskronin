# Plan A execution — rulings and parked findings

Plan A (core library) was executed 2026-10-26..27 with subagent-driven development: Claude for Tasks 1–3, then Codex implementers + agy/Codex reviewers (to conserve the weekly Claude limit), Claude Opus for the final whole-branch review. Range 37cfa7c..1df740d on main. Every ruling below was made by the controller on the user's behalf; each lists what it costs if wrong.

## Pre-flight rulings (before Task 1)

- F1 (Tasks 17, 18, 35b, 36): testutil.Service cleanup: if svc.Stopping() is closed, skip Reconcile and run CheckStructural only; otherwise Reconcile + CheckAll. (A§4.10 step 1, A§13.1)
- F2 (Task 31): Scenario.Stack already exists from Task 16a — do NOT redeclare it; Task 31 only uses it.
- F3 (Task 36, TestIDM04): set Meta.Idem only for ChangePassword, UpdateSettings, CreateToken; call Login(ctx, LoginIn) and RunBackup(ctx, a, kind) with their real signatures; assert zero idempotency rows and no password anywhere. (catalog IDM-04, A§4.9)
- F4 (Tasks 35a, 36, 37; helper may be introduced in 35a): add package-internal `func (s *Service) systemMeta() Meta` returning Meta{Actor: domain.SystemActor(), Gen: s.Generation()}; system opSpecs use scopes 0, idem false (ResetPasswordOffline, RecordSystemOp, ReplayLedger, ReapplyLedger). (A§3.1, A§2.5, A§4.3)
- F5 (Tasks 19, 32): TRI-18's #3{new,slipped} and #4{surfaced} fixtures are inserted with TestOpRaw + store.AddReason (explicitly allowed for TRI-18); Task 32's "the one place" SQL-fixture wording becomes "one of two places (TRI-18, TRI-20)".
- F6 (Task 30): revert applying triage/task_tags events calls markTaskDirty(task_id); interruption tombstones bump nothing (no version column). (A§4.3, A§5.2)
- F7 (Task 16b): AddTask and MarkManual opSpecs are idem: true, revertible: true. (A§3.2, A§3.10)
- F8 (Tasks 19, 23, 28, 29, 30, 32–34): the owning task asserts each scenario's Then via state readable at that point (store rows, GetTask, ops/events); view-only parts (Today/report/history/AI-changes) are added as assertions for those same IDs in the view tasks: LIF-09 → Tasks 32 & 34; REC-07, SLP-11 → Task 33; LIF-02, LIF-17 → Task 34; UND-11, SLP-05 → Task 33; TRI-18 "undone as one" → Task 31.
- F9 (Task 16a effects): companion columns (last_placed_at, last_placed_seq, due_ack, due_ack_date, completed_at, completed_date, defer_min, due_min, version, updated_at) are absorbed by their primary effect; `updated` is emitted only for keys no effect row claims (title, notes, project, priority, tags). A placement yields [placed]; Complete yields [completed].
- F10 (Tasks 22, 24): Task 22 splits createBlock into `o.checkSpan(span, exclude ...domain.BlockID) error` (start<end, date ≥ today, DST gap → validation, end instant > now, overlap excluding `exclude`) + insert; Task 24's Reblock in-place branch calls checkSpan.
- F11 (Task 16a, or Task 36 adding internal/testutil/service.go to its Files): testutil.Service defaults Options.Backups to &backup.Manager{Dir: t.TempDir(), Clock: clk, Snap: st.Snap} once backup.Manager exists (Task 15 precedes 16a, so do it in 16a).
- F12 (Tasks 26, 32, 33, 34): Task 26 adds `currentBlockAt(ctx, q, z, now)` (the op method wraps it); Task 32 names `decodeKeyset(view, cursor)` / `decodeOffset(cursor)` reused by 33/34 — no duplicated logic.
- F13 (Tasks 16a, 37): test snippets use `if err := s.Store.Reader.QueryRowContext(s.Ctx(), …).Scan(&v); err != nil { t.Fatal(err) }` (tests are linted: noctx, errcheck).
- F14 (all remaining tasks; general rule, added during execution): when a catalog scenario's Given state is unreachable through the real service ops available at that task (e.g. a placement would clear the reasons the Given needs), the implementer may build that Given with a raw store-layer fixture inside testutil's TestOpRaw (or equivalent helper), clearly commented "fixture shortcut: <why unreachable>" and listed in the report. The When and Then must still use the real service ops/reconcile. This does not permit changing service semantics to make a Given reachable.

## Rulings during execution

- Ruling: commit trailers name the actual authoring model (from each agent's attribution reminder), overriding the plan's fixed Opus trailer — accurate attribution — cost if wrong: cosmetic.
- Task 1: parked — commit 00b915e trailer names Opus though authored by Sonnet — Ruling: accept; the trailer rule was changed after that commit was made, and amending would rewrite history for a cosmetic issue — cost: one misattributed trailer.
- Task 3: Ruling: G115 (integer-conversion overflow) on provably range-limited values (calendar fields, validated minutes) may be silenced with a specific `//nolint:gosec // G115: <why in range>` directive — G115 false positives are common and the plan's suppression list predates them; A§13.4's policy allows explicit per-site directives — cost if wrong: a few justified suppressions (Task 2's three and Task 3's two sites stand).
- Task 4: Ruling: strip exactly one leading BOM; any other U+FEFF is rejected — keeps validation idempotent; U+FEFF is invisible — cost if wrong: rare legit pasted text with stray U+FEFF is rejected.
- Task 7: Ruling: read-only DELETE fixture asserts Ping fails (driver rejects before hook); hook mismatch covered by a separate fixture that reaches the real verification code — pinned driver behavior makes the brief's exact fixture impossible — cost if wrong: none (both paths still tested).
- Task 10: Ruling: single-row Get* return tombstoned rows (DeletedAt exposed); lists/aggregates/derivations exclude them — A§2.8 GET semantics vs brief; consistent with Task 9 GetTask — cost if wrong: callers must check DeletedAt.
- Task 16a: Ruling: update event images carry changed columns plus companions (due_date+due_min, defer_date+defer_min, block outcome_source+resolved_at on status change) — effects must be reconstructible from the op's own events; extends A§2.7 'changed only' — cost if wrong: slightly larger event payloads.
- Task 20: Ruling: DUE-10 Given state may use a raw store fixture (like F5) — unreachable via real ops at this point — cost: lower fixture realism for one scenario.
- Task 23: Ruling: SLP-15 Given built with raw fixture; generalized as F14 for all remaining tasks — unreachable Givens — cost: lower fixture realism for a few scenarios.
- Final review (Opus, risk-scoped; Codex full-branch review failed — Codex quota exhausted until 2026-11-02): 0 Critical, 2 Important, 5 Minor. Ruling: non-owner token minting limited to own scopes — least privilege; spec silent — cost if wrong: admin tokens can't delegate wider scopes.
- Final: parked — settings.go:56 retention-lowering still verifies the password while holding the writer (same class as I1) — Ruling: owner-only, rare CLI path; fix in Plan B when wiring settings or a later cleanup — cost: up to ~15 s writer stall when lowering retention with a wrong password burst.
- Final: parked — Login/ChangePassword now verify before lifecycle admission (shutdown → internal instead of busy for a request racing Drain); ChangePassword checks password before generation/idempotency key; raw HashPassword error not mapped; writerAdmissionError untested — Ruling: minor ordering/mapping nits; revisit in Plan B integration — cost: odd error codes in rare shutdown/stale-generation races.

## Parked and deferred findings

- Task 1: parked — commit 00b915e trailer names Opus though authored by Sonnet — Ruling: accept; the trailer rule was changed after that commit was made, and amending would rewrite history for a cosmetic issue — cost: one misattributed trailer.
- Task 1: complete (commits 37cfa7c..00b915e, review clean except parked trailer)
- Task 3: note for Plan B: LoadZone rejects slash-less IANA links (Japan, GB…); init must canonicalize /etc/localtime target to an Area/City name.
- Task 3: note for Tasks 20, 27: never pass arithmetic-built Minutes to DeferMoment/DueMoment (they panic outside 0..1440); only validated values.
- Task 3: note (plan-wide): tests rely on GOROOT/lib/time/zoneinfo.zip (ships with every Go toolchain) — accepted, no test-only tzdata import.
- Task 33: deferred (out-of-scope, Important per re-reviewer): done/dropped/deleted task-view cursors share one numeric schema and are interchangeable (views.go:776) — triage at final review.
- Final: parked — settings.go:56 retention-lowering still verifies the password while holding the writer (same class as I1) — Ruling: owner-only, rare CLI path; fix in Plan B when wiring settings or a later cleanup — cost: up to ~15 s writer stall when lowering retention with a wrong password burst.
- Final: parked — Login/ChangePassword now verify before lifecycle admission (shutdown → internal instead of busy for a request racing Drain); ChangePassword checks password before generation/idempotency key; raw HashPassword error not mapped; writerAdmissionError untested — Ruling: minor ordering/mapping nits; revisit in Plan B integration — cost: odd error codes in rare shutdown/stale-generation races.

Per-task minor findings were deferred to the final review (it triaged them; the cheap robustness ones were fixed in 1df740d).
