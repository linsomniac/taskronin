# Plan C execution — rulings and parked findings

Plan C (the Bubble Tea TUI, `internal/tui`, launched by `tkr tui`, `plan`, `recap` and `triage -i`) was executed 2026-11-14..15 with subagent-driven development on `main`.

- **Implementation:** Codex implemented all 12 tasks, the final fix wave and a regression fix.
- **Task reviews:** Claude Sonnet reviewed Tasks 1, 4, 6–10; Claude Opus reviewed Tasks 2, 3, 5, 11 and 12. Every Important finding went through a fix round and a scoped re-review.
- **Whole-branch review:** a Claude Opus whole-branch review, one fix wave, an Opus scoped re-review, and a targeted fix for the regressions that wave introduced, re-reviewed by Sonnet.
- **Range:** 41f052b..8605f1c.

Every ruling below was made by the controller on the owner's behalf; each lists what it costs if wrong.

## Pre-flight rulings (Opus pre-flight scan, CF01–CF14, all accepted)

- **CF01:** `tkr tui` passes the underlying `*os.File` from the CLI's `terminalInput` to Bubble Tea, which enters raw mode only for a `term.File` (bubbletea v2.1.0 `tty_unix.go:17`). Verified by a tmux PTY smoke test (`-icanon -echo`).
- **CF02:** the Today ETag hashes `clock.now`, so every 5 s poll is a 200. A poll reloads only when the payload differs once `Clock` is zeroed, and never auto-reloads overlays.
- **CF03/CF04:** errors are classified in this order: outcome-unknown, then not-sent, then problem. Loads use their own read-error policy, not the mutation policy ("nothing was saved" only for mutations).
- **CF05:** the TUI retry window uses `client.PendingResendWindow` and `>=`.
- **CF06:** the API includes `RefreshGeneration`; the retry path compares the generation against a fresh value.
- **CF07:** `NewApp` defaults `Editor.Getenv`, and tests stub the editor. Production uses the real environment lookup and `paths.DraftsDir`.
- **CF08/CF09:** use `exec.CommandContext`, and drop the unused G304 directive.
- **CF10:** Upcoming "keep" is bound to `K`, not `k`, because `k` navigates every list (A§14 issue 12). A§11.1 and TRI-26 were amended. *Owner may prefer another key.* Cost if wrong: one rebinding and one spec row.
- **CF11:** the revert-prefix change landed before Task 1.
- **CF12:** the "interrupted" effect line uses `From`, mirroring the CLI.
- **CF13:** editor file I/O runs in `tea.Cmd`s, so Update never blocks.
- **CF14:** indirect module bumps pulled in by the pinned charm modules are expected.

## Rulings during execution

- **Task 1 / controller:** the catalog coverage scanner skips directories the go tool ignores (`.tkr-*`, `testdata`, `_*`). Commit dfd0c3f landed while `make check` was failing only on host-load timeouts that passed in isolation. Cost if wrong: a real failure hidden by noise. Mitigated by isolated reruns.
- **Task 3 I1:** a form keeps its outcome-unknown snapshot until Enter. Unchanged text resends under the original key; changed text sends a new request. A live banner says which will happen. Cost if wrong: small form-state rework.
- **Task 12:** `internal/testutil/golden.go` registers `-update` only if absent, because teatest's `x/exp/golden` also defines it. Cost if wrong: a dependency init-order change panics loudly at test start.
- **Final fix wave scope:** I1–I6, Task 8 M1, and minors M1–M4, M6, M8 and M9 went into one dispatch. **M7** (confirm before marking AI changes reviewed, plus an "n of N" header) is parked for the owner as a design change.
- **S1′ (service change, outside the TUI):** when an ordinary op's own 15 s OpBudget expires after writer admission (deadline or SQLITE_INTERRUPT, before or after commit), it maps to **busy** (503 + Retry-After), but **only for idempotency-keyed ops**.
  - A same-key retry either re-executes (nothing committed) or replays the stored result (the idempotency record commits in the same transaction).
  - Exempt ops (settings, token create, password, backup) keep their previous mapping. The client busy-retries them without a key, and they have no replay record, so a post-commit busy could duplicate a token or a settings change. Codex found this counterexample and stopped instead of applying the original blanket rule.
  - Cost if wrong: exempt admin ops still return 500 under extreme load.
- **Regression fix beyond the single fix wave:** the fix wave introduced two Important regressions where a key could act on a task the user could not see:
  - a failed reload hid the current card while `x` still completed it;
  - scrolling Today hid the selection while `x` acted on it.
  
  One targeted commit (8605f1c) fixed both, plus `q` being untypable in forms during an op, plus content blanking on refresh. It also makes a TUI form keep its keyed retry snapshot on busy, so Enter resends under the original key and S1′ cannot create a duplicate. Cost if wrong: one extra commit and review.

## Parked findings (deferred minors)

- **Task 1:** shallow copies in the test fake, `AIChanges` aliasing, `entriesNext` only on the first page, and a redundant `TestFakeImplementsAPI`. All test-only.
- **Task 3:** a parse failure after changed text drops the snapshot. Every form with a parse step sends If-Match, so a duplicate can only get a 412. The fix is a 3-line move: drop the snapshot after `submit` succeeds.
- **Task 4:**
  - A non-poll `fetchToday` (from `openLog`) can mask the next poll's reload.
  - A midnight rollover alone does not reload the other tabs.
  - No test switches tabs during an in-flight poll.
- **Task 6:** an `m.up` nil guard is missing; the truncation banner is never explicitly cleared.
- **Task 7:** the "Card i/n" denominator includes bulk-accepted cards that were never shown.
- **Task 8:**
  - Free-slot text can be clipped at about 80 columns.
  - `from` uses `clock.Now`, which only advances on Today fetches; an AIDEV-NOTE should say so.
  - The step-2 reload is not asserted.
- **Task 9:** loop-variable addresses (fine under Go 1.22+); `recap_test.go` is large.
- **Task 10:** the paging comment, the plan-mandated day prefix on each row, and a nil-page guard.
- **Task 11:**
  - After a payload-changing poll, closing an upper overlay reloads a revealed AI overlay, so the user presses `A` again. It never reviews unseen ops.
  - First-load hint text.
- **Task 12:**
  - The `golden.go` note should name the init-order assumption.
  - A bad `--date` message does not name `--date`.
  - One cmd_tui test assertion is weak; exit code 2 is still asserted.
- **Fix-wave re-review:**
  - **N4:** read-error banners from untargeted reads, or from a closed overlay or exited mode, are never cleared.
  - **N6:** if the budget expires just before Commit, the result is `sql.ErrTxDone`, which stays 500. This is safe because nothing committed.
- **Regression re-review:**
  - Today `k` skips the last row once when the content fits.
  - `x` can complete the focus task while its line is scrolled off; A§11.1 allows this.
  - `acceptNew` acts on queued or skipped new-only cards that are not on screen (deliberate, per its code comment).
- **Test robustness (cross-plan):** fixed timeouts (StartServer 10 s, client 20 s, op 15 s) make `make check` flaky when the host is loaded by other projects' test suites. Plan C's own ceilings were raised to 15 s.

## Owner questions (no action taken)

1. **Upcoming keep key:** `K` (CF10). Change it if you prefer another key.
2. **Today `x`/`B` with a selected row:** they act on the selected row, including block and triage rows, rather than only on the focus task (A§11.1 says "done focus task"). Rows start unselected, so `x` with no selection still completes the focus task.
3. **AI review in the TUI (M7):** `A` marks every loaded row reviewed, including rows the user never scrolled to. It never covers unloaded ops. A confirmation prompt plus an "n of N" header would make this explicit.

## Parked minors completed (2026-11-18)

Codex fixed every parked TUI finding above except the three No-change items, in a21bf8c, e8d5910 and 5038d7f. A Sonnet review approved them with no Critical or Important findings.

- **Polling:**
  - A separate poll baseline means a non-poll Today fetch can no longer hide the next poll's reload.
  - A midnight rollover alone reloads the visible tab.
  - A poll reply that arrives after a tab switch is discarded.
  - Polls never mark overlays dirty (CF02).
- **Banners:** read-error banners now have an owner. A later successful read of the same target, closing the overlay or exiting the mode clears them.
- **Lists and cards:**
  - Upcoming has a nil guard.
  - The truncation banner clears after a complete reload.
  - The card denominator excludes cards that were bulk-accepted but never shown.
  - Free-slot text wraps.
  - Today `k` lands on the last row.
- **Retry snapshot:** a parse error after a text edit keeps the original retry snapshot. Restoring the text resends under the original key.
- **CLI and tests:**
  - A bad `--date` names the flag.
  - golden.go checks that the reused `update` flag is a bool.
  - The test fake deep-copies its responses.
  - The recap tests are split by feature.
- **No change, as ruled:**
  - the Log row's per-row day prefix;
  - `x` on a focus task that has scrolled off screen;
  - accept-new on off-screen new-only cards.
- **Follow-up (3004963):** the new exact-message `--date` test failed whenever `NO_COLOR` was unset, because a TTY stdout adds ANSI color. The test now sets `NO_COLOR`.
- **Residual minors, not fixed:**
  - a duplicate Today read when a refresh reply discovers midnight;
  - the banner owner is an optional argument;
  - only esc clears a form-owned banner.
