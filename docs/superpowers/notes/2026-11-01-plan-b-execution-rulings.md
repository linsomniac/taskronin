# Plan B execution — rulings and parked findings

Plan B (server, client, CLI, offline operations, MCP) was executed 2026-10-28..11-01 with subagent-driven development on `main`: Codex implementers and reviewers for Tasks 1–25b (automatic fix rounds), then — after Codex's weekly limit — Claude Sonnet implementers and Sonnet/Opus task reviewers for Tasks 26–35, and a two-pass Claude Opus final whole-branch review followed by one Opus fix wave and an Opus scoped re-review. Range e1c08c4..HEAD. Every ruling below was made by the controller on the owner's behalf unless marked as an owner decision; each lists what it costs if wrong.

## Pre-flight rulings (Codex pre-flight scan, all accepted; BF33–BF35 added during execution)

- BF01: Tasks 1/2/8/13 leave XDG-derived Paths and server.toml overrides as competing authorities; normalize DB, lock, ledger, staging, socket and backup paths once before server/offline use, and test a non-default data_dir. (A§8.1–8.4, §4.10.)
- BF02: Tasks 7/31 explicitly omit backup.Inventory although A§12.1 declares it; approve removing that unused contract and retain offline.scrubSets(check=true), rather than introducing forbidden backup→service/offline imports. (A§1.3, §12.1.)
- BF03: Task 9's --units-only conflicts with Task 8's mandatory begin/CleanupTemps preamble; give units-only an early config-and-units branch that neither opens the DB nor cleans/chmods data or state. (A§8.5.)
- BF04: Task 11's decodeBody always json.Marshal-canonicalizes requests, including Task 17's passwords; separate strict decoding from keyed-request canonicalization so password bodies never become canonical Idem.Body bytes. (A§6.5, §7.4.)
- BF05: Task 11's blanket authentication-failure 401 conflicts with AuthenticateToken's owner+MCP forbidden result and Task 33's expected 403; preserve domain codes and add WWW-Authenticate only to 401 responses. (A§6.2, §7.2; SCP-13.)
- BF06: Task 11 places X-Taskronin-Version in middleware skipped by early errors, while the protocol requires it always; set mandatory response headers before guards or in every responder, and test 401/403/415/500 paths. (A§6.3.)
- BF07: Task 11's catch-all recoverer conflicts with Task 17's panic(http.ErrAbortHandler); re-panic that sentinel, make response wrappers expose Unwrap, and interrupt blocked export writes on request cancellation as well as shutdown. (A§4.10–4.11, §6.1–6.2.)
- BF08: Task 13 assumes every scheduler call participates in Service.Drain, but FlushUsage deliberately bypasses admission/accounting; cancel and join scheduler goroutines before the final flush/checkpoint/close. (A§4.10.)
- BF09: Tasks 14 and 17 both claim GET /settings; keep its handler/client read/route in Task 14 and let Task 17 add only PATCH plus admin routes, avoiding duplicate ServeMux registration. (A§6.4.)
- BF10: Task 15 maps cleared Priority to *string, but service.UpdateTaskIn.Priority is *domain.Priority; convert to domain.PriorityNone and preserve absent/null/value semantics. (A§3.2, §6.5.)
- BF11: Tasks 4/11/16 need Entry.task_title, but OpResult.Entry and entry precondition errors contain only domain.LogEntry; enrich service result/error loading with EntryView before wiring adapters, preserving the one-service-call rule. (A§1.3 R1, §6.5–6.6.)
- BF12: Task 15 expects AcceptNew tasks in request-item order, but the service returns new-reason sequence order; adopt the architecture/service ordering and test reversed input plus replay. (A§3.2 overrides catalog TRI-18's item-order wording.)
- BF13: Task 14 extends task=none semantics to blocks, whose service input and client query can express only a concrete task or no filter; allow none only for entries and reject it for blocks. (A§6.4, §6.7.)
- BF14: Task 17 calls RunBackup(ctx, actor, kind), which cannot validate the generation required on POST /backups; explicitly validate the transport generation before that call without changing its scheduler/offline signature. (A§6.3, §6.7.)
- BF15: Task 17's scope matrix assumes token Undo/Redo can select owner history and that admin alone can mint arbitrary scopes; test actor-stack isolation separately, use Revert for foreign-owner denial, and mint only the caller's scopes. (A§4.6, §7.3; Plan A least-privilege ruling.)
- BF16: Tasks 17/27 expose the parked retention-lowering password check while it holds the writer; assign its service fix to Task 17: verify before writer admission, then recheck credentials and the lowering decision inside the transaction. (A§4.1, §4.11, §7.4; Plan A parked finding.)
- BF17: Tasks 13/17 inherit ChangePassword verification before lifecycle admission and generation checks; include its password work in shutdown accounting and reject stale/shutdown requests before expensive verification. (A§3.1, §4.10–4.11; Plan A parked finding.)
- BF18: Tasks 12/17 save keyed token revocations, but Task 18 Resend returns only *api.OpResponse while revocation returns TokenInfo; define a typed resend result covering both and update Task 27's retry rendering. (A§6.4, §6.7, §9.6.)
- BF19: Tasks 19/22 use first-line ParseTaskBuffer for log -e, while the log contract requires the first non-empty editor line; add a log-specific parser and keep edit's first-line format. (A§9.4; D§9.1.)
- BF20: Task 20 mandates header-only output whenever Days is empty, which loses requested Todo/Upcoming sections; apply header-only only when those sections are absent and test empty days with populated optional sections. (A§9.7; RPT-10.)
- BF21: Task 23's reopen fixture creates an already-ended 09:00–09:30 block at 10:00, which service.Block rejects; create it before 09:00, then advance to 10:00 before complete/reopen. (A§3.3; BLK-06, LIF-14.)
- BF22: Task 25a requires exactly one GET /status, but Task 12 retries every read; add an explicit no-retry call policy for status --short and enforce its total status_timeout across configuration/client setup and the request. (A§9.5–9.6.)
- BF23: Task 29 classifies invalid snapshots only at Verify, but Task 7's Stage already opens SQLite and fails first on non-SQLite/corrupt input; map validation failures from both phases to the promised snapshot error while preserving filesystem/I/O causes. (A§12.0, §12.2; Review Focus 5.)
- BF24: Task 29's unreadable-current test demands previous live max+10001 although the prescribed recovery can inspect only readable snapshots; assert max(recoverable sequence marks)+10001 and state that inaccessible live high-water marks cannot be recovered. (A§12.2 step 4.)
- BF25: Task 30 deliberately splits ImportData and RecordSystemOp into two staged transactions although A§12.5 requires one for steps 4a–d; approve the unpublished-staging exception explicitly, keeping publication conditional on both commits and all checks. (A§12.5.)
- BF26: Tasks 8/31/32 lack a pre-password store-opening path and open separate cached Ledgers in openLive and the caller; split store opening from service construction and pass one Ledger instance through replay, PurgeLive, scrub and completion. (A§12.6–12.7.)
- BF27: Task 32 promises no ledger write before confirmation but calls OpenLedger first, which truncates torn tails; confirm from non-mutating reads before opening the ledger, and enumerate all linked entries including tombstones and all pages for an accurate purge set. (A§9.1, §12.6; planner decision 23.)
- BF28: Tasks 33/34 name schema goldens mcp_*.json, but Task 5 Golden always reads/writes <name>.golden; use mcp_*_input.golden and mcp_*_output.golden consistently in manifests and tests. (A§13.1–13.2; §10 filename inconsistency.)
- BF29: Task 35 depends on a client _test.go helper that server_test cannot import; implement its dropped-response fixture locally or add an import-cycle-safe normal testutil helper, and include that file in the task's scope. (A§13.1–13.2; Plan B test-package constraint.)
- BF30: Tasks 4/12/28/35 prescribe fixed password/token fixtures beyond testutil.Password; generate valid alternative credentials at runtime and retain fixed strings only as explicitly invalid inputs. (A§1.4, §13.1; Plan B Secrets constraint.)
- BF31: Tasks 11/14 never assign the 10-second read/aggregate deadline, and implemented service.read uses only its caller context; add the deadline in the HTTP adapter, with long export routes using their separate budget. (A§4.11.)
- BF32: Task 34 needs Task 33's New registration and toolError implementation to add write tools and 412 current data, but omits server.go from its Files; make Task 33 read-only at completion and explicitly extend server.go in Task 34. (A§10; per-task green-check constraint.)
- BF33 (Task 11): Ruling: Task 11 — authorize the `server → textsafe` import edge (add `textsafe` to the `server` row of A§1.3 and to `internal/archtest/deps_test.go` in the same commit as Task 11) — the Global Constraint "all user text printed to terminals or logs goes through textsafe" is the security requirement and A§1.3 simply omitted the edge; textsafe is a no-I/O leaf (R5, only x/ansi) so the edge cannot create a cycle or leak I/O — if wrong: one removable import edge, logs would rely on slog escaping instead.
- BF34: Ruling: bump toolchain go1.26.8 → go1.26.9 (commit 1ef83da: go.mod, A line 23, Plans B/C/D Global Constraints; workspace global-constraints.md updated) — govulncheck now reports six stdlib vulnerabilities in 1.26.8 fixed in 1.26.9, and `make check` must stay green; a patch-level toolchain bump is the boring fix — if wrong: revert one commit; civil/zone.go AIDEV-NOTEs referencing 1.26.8 tzdata left untouched (historical brute-force claim).
- BF35: Ruling: bump toolchain go1.26.9 → go1.26.10 (commit HEAD after 99ba55f; same files as BF34) — govulncheck now reports GO-2026-6809 in 1.26.9; standing rule from here: a stdlib-only govulncheck failure with a patch-level fix is handled by a separate controller toolchain-bump commit, never by the task — if wrong: revert one commit.

## Rulings during execution

- Ruling: BF16/BF17 also close Plan A's parked password-path items inside Plan B Task 17.
- Ruling: BF02 drops the unused backup.Inventory contract; BF25 approves import's two staged transactions with publication conditional on both.
- Ruling: Task 11 — authorize the `server → textsafe` import edge (add `textsafe` to the `server` row of A§1.3 and to `internal/archtest/deps_test.go` in the same commit as Task 11) — the Global Constraint "all user text printed to terminals or logs goes through textsafe" is the security requirement and A§1.3 simply omitted the edge; textsafe is a no-I/O leaf (R5, only x/ansi) so the edge cannot create a cycle or leak I/O — if wrong: one removable import edge, logs would rely on slog escaping instead.
- Ruling: bump toolchain go1.26.8 → go1.26.9 (commit 1ef83da: go.mod, A line 23, Plans B/C/D Global Constraints; workspace global-constraints.md updated) — govulncheck now reports six stdlib vulnerabilities in 1.26.8 fixed in 1.26.9, and `make check` must stay green; a patch-level toolchain bump is the boring fix — if wrong: revert one commit; civil/zone.go AIDEV-NOTEs referencing 1.26.8 tzdata left untouched (historical brute-force claim).
- Ruling: bump toolchain go1.26.9 → go1.26.10 (commit HEAD after 99ba55f; same files as BF34) — govulncheck now reports GO-2026-6809 in 1.26.9; standing rule from here: a stdlib-only govulncheck failure with a patch-level fix is handled by a separate controller toolchain-bump commit, never by the task — if wrong: revert one commit.
- Ruling: Codex usage limit reached (resets 2026-11-04 05:00) and agy quota still exhausted — Tasks 26-review onward use Claude subagents: Sonnet implementers, Sonnet task reviewers (Opus for the security/crash-critical Tasks 28-32), Opus final review; controller pauses at ≥90% Claude session/weekly usage — Codex/agy are unavailable for days and the user authorized opus/fable sub-agents — if wrong: higher Claude usage, mitigated by the 90% pause.
- Ruling: Tasks 28-32 task reviews use Sonnet with an explicit security brief instead of Opus — weekly usage is 86% against the 90% pause line and Sonnet already caught a real token-shadowing bug in Task 27; Opus is kept for the final whole-branch review — if wrong: a subtle bug survives to the final review, which is scoped to these security-critical areas.
- Ruling: Task 30 import size cap (no io.LimitReader) parked for the final review — the spec sets no limit and a number would be an invented business rule; exports of a single-user task DB are small — if wrong: a huge or hostile file costs memory during an offline command the owner runs deliberately.
- Ruling: Task 31 — accept the defense-in-depth fix: a set judged clean whose original -wal or -shm is non-empty is rewritten (reported contains under --check), with a test — purge guarantees must not rest on invariants owned by other tasks; the cost is one extra rewrite in a case that should never occur — if wrong: a few wasted rewrites.
- Ruling: Task 31 --check exits 0 even when it reports contains — kept as the brief/tests specify; noted as a question for Sean at the checkpoint (a non-zero exit would help scripts but would be an invented contract) — if wrong: a one-line exit-code change later.
- Ruling (owner decision 2026-10-29): scrub-backups --check exits non-zero when any set contains purged content — implemented by the controller as a dedicated exit 8 (A§9.6 new row, §12.7 step 7, catalog PRG-22), commit 01bcad0; exit 8 rather than 1 so scripts can distinguish 'content found' from 'command failed' — if wrong: change one constant.
- Ruling: add -timeout=30m to the Makefile test target and A§13.5 (controller commit 1fec980) — the race suite's slowest package nears go test's 10-minute default under load, making make check fail spuriously; a longer ceiling changes no test — if wrong: a truly hung test takes 30 min to fail instead of 10.
- Ruling: Task 33 I1 — amend A§10: MCP error results carry the problem JSON ONLY as TextContent with isError=true and no StructuredContent; success results keep StructuredContent + the TextContent mirror — interop with the most common MCP client (Claude Code itself uses the TS SDK) outweighs the plan's structured-error mandate — if wrong: a host that wanted machine-readable error codes parses the text JSON instead.
- Ruling: Task 33 fold-ins — M2 owner-token refusal exits 4 per A§9.6; M1 work_report rejects a time in from/to like tkr report; M4 strengthen the trivial default limit/view test. Search limit 1–200 default 50 kept (mirrors list_tasks); listed for the owner at the checkpoint.
- Ruling: Task 35 — add a `make install` target (build, then install -m 0755 bin/tkr to $(PREFIX)/bin with PREFIX ?= $(HOME)/.local) and make the README use it — the owner chose 'make install to ~/.local/bin' in design §19 (line 863); Plan B's 'make build + copy' text contradicts that decision and the design wins — if wrong: one Makefile target to drop.
- Ruling: controller amended the unpushed HEAD 8f2c52c → bfe3ba1: git rm --cached the four .tkr-* dirs, deleted them from disk, and added .tkr-*/ .t-*/ .init-*/ .paths-*/ to .gitignore — the files were throwaway test artifacts, but test-generated tokens should never sit in history and amending an unpushed tip is the boring fix; code content is otherwise identical to 8f2c52c (the final reviewers' range still resolves) — if wrong: nothing lost, the dirs were regenerated test scratch.
- Ruling: final fix wave = F1–F17 in final-fix-wave.md, one Opus implementer, 3–4 commits; parked for the owner: B-M6, B-M8, B-M10, B-M11, instance-ID ledger binding; parked 3/5/8a accepted as-is — C1/I1–I3 and pass A's retry/pending/socket items affect data safety or recoverability; the rest are cheap wording/test fixes — if wrong: extra scope in one fix wave.
- Ruling: accept the F5 interpretation (409/422 on the retry after a lost first attempt → outcome-unknown + pending) — conservative: pending retry then records the real answer, and nothing is ever double-applied or silently dropped — if wrong: a confusing 'outcome unknown' message for a request that was in fact refused.
- Ruling: keep the Plan B SDD workspace until after the owner's checkpoint (the skill would delete it now) — the owner may request Plan B changes after trying the CLI/MCP, and the ledger, rulings, briefs and templates are the recovery map for that; the committed notes doc preserves the rulings either way — if wrong: a git-ignored directory lingers a little longer.

## Parked findings (deferred minors; see the final reviews for verdicts)

- Task 1: minor (deferred): see task-1-review.md Minor section
- Task 2: minor (deferred): see task-2-review.md Minor section
- Task 3: minor (deferred): see task-3-review.md Minor section
- Task 4: minor (deferred): see task-4-review.md Minor section
- Task 5: minor (deferred): see task-5-review.md Minor section
- Task 6: minor (deferred): see task-6-review.md Minor section
- Task 7: minor (deferred): see task-7-review.md Minor section
- Task 8: minor (deferred): see task-8-review.md Minor section
- Task 9: minor (deferred): see task-9-review.md Minor section
- Task 10: minor (deferred): see task-10-review.md Minor section
- Task 11: minor (deferred): see task-11-review.md Minor section
- Task 12: minor (deferred): see task-12-review.md Minor section
- Task 13: minor (deferred): see task-13-review.md Minor section
- Task 14: minor (deferred): see task-14-review.md Minor section
- Task 15: minor (deferred): see task-15-review.md Minor section
- Task 16: minor (deferred): see task-16-review.md Minor section
- Task 17: minor (deferred): see task-17-review.md Minor section
- Task 18: minor (deferred): see task-18-review.md Minor section
- Task 19: minor (deferred): see task-19-review.md Minor section
- Task 20: minor (deferred): see task-20-review.md Minor section
- Task 21: minor (deferred): see task-21-review.md Minor section
- Task 22: minor (deferred): see task-22-review.md Minor section
- Task 23: minor (deferred): see task-23-review.md Minor section
- Task 24a: minor (deferred): see task-24a-review.md Minor section
- Task 24b: minor (deferred): see task-24b-review.md Minor section
- Task 24c: minor (deferred): see task-24c-review.md Minor section
- Task 25a: minor (deferred): see task-25a-review.md Minor section
- Task 25b: minor (deferred): see task-25b-review.md Minor section
- Task 26: minor (deferred): see task-26-review.md Minor section
- Task 28: minor (deferred): see task-28-review.md Minor section
- Task 27: minor (deferred): see task-27-review.md Minor section (write-file race, pending-retry message)
- Task 29: minor (deferred → final review focus): restore.go:170 any readCurrent error takes the unreadable-DB path; restore.go:506 owner-token check I/O error lacks hint; untested rollback hint/same-second guard/onInstalled failure
- Task 30: minor (deferred): import size cap; dangling-FK test doesn't assert error text; event payloads/ops.actor not textsafe-validated (display sanitizes)
- Task 31: minor (deferred): pending/ and drafts/ subdirectories not cleaned; test walk misses an out-of-tree backup.dir; --check replays ledger on live DB (per spec)
- Task 32: minor (deferred → final review focus): purge.go:348-371 RepairLedger prints dropped lines only after replay succeeds (spec: right after Repair); purge.go:133-137 no immediate replay after a PurgeLive failure post-append; main fixture byte search uses whole phrases (FTS token residue only covered by hunter2 test); purge_test.go:429 weak; declined purge vs torn tail untested
- Task 34: minor (deferred → final review focus): MCP input schemas lack maxLength/min/enum constraints (server validates); no test that the fetched etag is sent as If-Match or that retries reuse the key; TestPlaceTask weak
- Task 33: minor (deferred): remaining M3/M5–M9 in task-33-review.md

## Final whole-branch review (two Opus passes) and fix wave

- Pass A (network/auth surface): no Critical/Important. Pass B (offline + CLI): one Critical, three Important. Full text in the git-ignored SDD workspace (`final-review-A.md`, `final-review-B.md`); the fixes and their regression tests are in commits 464cf8c, c9dfb21, 5f14968, ac3ed59.
- Fixed (F1–F17): `tkr init` refuses over a leftover purge ledger / journal / `-wal`/`-shm` (the old ledger was being applied to a fresh DB's reused IDs); offline failures show their sanitized cause instead of "internal"; restore takes the destructive "unreadable live DB" path only for a missing or genuinely corrupt DB; `init --mcp-token` validates its expiry first with the shared `domain.ParseTokenExpiry` grammar; after a lost first attempt any non-success on the retry is outcome-unknown + pending; pending resend window is 7 d − 1 h; `tkr serve` never deletes an in-use socket; `authn` enforces the verified socket itself; `backup.dir` may not be the data dir or inside its staging dir; plus recovery hints, `--repair-ledger` output order, damaged-ledger warnings on restore/import, purge ID forms, mutation-only "nothing was saved", and extra purge/scrub tests.
- Scoped Opus re-review: all F1–F17 addressed, no new Critical/Important.

## Residual minors (parked)

- `internal/config/server.go:94-97`: a `backup.dir` named `.tkr-tmp-*` directly inside the data dir still passes validation, and `CleanupTemps` would delete it (requires a deliberately odd name).
- `internal/server/listeners.go:57→60`: a tiny window between the in-use probe and unlinking a stale socket.
- `internal/store/open.go:29`: a dangling symlink at the DB path counts as "missing", so restore takes the unreadable-DB path.
- `internal/cli/cmd_pending.go:144-147`: a resend that ends outcome-unknown is stored with a misleading `last_error` (advice shown is correct).
- From final review B, deliberately not changed: a backup set that can never be scrubbed blocks purge completion forever (B-M6); exit 8 under `--json` comes with an `internal`/500 problem object (B-M8); a declined `tkr delete` exits 0 silently (B-M10); `passwd --reset` checkpoints without replaying the ledger (B-M11).

## Owner decisions at the checkpoint (2026-11-01)

- Q1 (delegated): Ruling (owner delegated Q1 to controller judgment, 2026-11-01): do NOT bind purge-ledger records to a database instance ID — `tkr init` now refuses over a leftover ledger, which closes the realistic path; the remaining path (restoring/importing data from a different instance into this data dir) is documented in the README instead. An instance ID would need a new migration, a ledger record-format change and new replay/import/restore semantics in the most security-critical code, for a single-user, single-instance tool — the boring choice is the guard plus documentation — if wrong: restoring another instance's snapshot here could scrub same-numbered tasks/entries; the cost then is adding the ID later (new migration + ledger field).
- Q2: Owner decision (Q2, 2026-11-01): MCP limit 1–200 default 50 confirmed; the tools must say so — list_tasks/search descriptions now state the per-call limit, the default and next_cursor, and search gained the cursor input list_tasks already had (A§10 row updated) so a client can page past the default.
- Q3: Owner decision (Q3, 2026-11-01): keep B-M6, B-M8, B-M10 and B-M11 as they are.
