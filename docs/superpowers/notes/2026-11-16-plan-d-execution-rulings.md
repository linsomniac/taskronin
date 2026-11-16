# Plan D execution — rulings and parked findings

Plan D (the loopback web UI: `internal/markdown`, `internal/web`, and the server mount in `internal/server/web.go`) was executed 2026-11-15..16 with subagent-driven development on `main`.

Process:
- **Implementation:** Codex implemented all 13 tasks (1, 2, 3, 4, 5, 6a, 6b, 6c, 7, 8, 9, 10, 11) and the final fix wave.
- **Task reviews:** Claude Opus reviewed Tasks 2, 3, 4, 7, 10 and 11, the security-heavy ones. Claude Sonnet reviewed the rest.
- **Fix rounds:** every Important finding went through a fix round and a scoped re-review.
- **Final gate:** an Opus whole-branch review, one fix wave, and an Opus scoped re-review.

Range: 9e078c1..b87e1b7. Two unrelated commits sit inside that range: 8605f1c (a Plan C TUI fix) and 0867c31 (the Plan C notes).

The controller made every ruling below on the owner's behalf. Each one lists what it costs if wrong.

## Pre-flight rulings (Opus pre-flight scan, DF01–DF22, all accepted)

- **DF01 — login and logout broke in real browsers.** Every page sends `Referrer-Policy: no-referrer`, so browsers send `Origin: null` on the plain (non-htmx) login and logout forms. The planned rule rejected that. New rule:
  - When `Sec-Fetch-Site` is present it decides: `same-origin` passes, anything else gets 403.
  - When it is absent, `Origin` must match the Host allowlist, and an absent or `null` Origin gets 403.
  - A present Origin that is not `null` and not on the allowlist always gets 403.

  A§7.5 was reworded to match. The tests and the TCP end-to-end test log in with browser-shaped headers.
- **DF02 — markdown links.** `http`/`https` are allowed only with a host and `mailto` only with an address. The plan's version failed its own fuzz property and left an `http:foo` same-origin link hole.
- **DF03 — row partials.** Row partials take a `rowCtx{P page; V row}` through the `row` template function. Inside a partial, `$` is the row, so the plan's `{{template "hidden" $}}` could not work there.
- **DF04 — dependency pin.** `golang.org/x/net` is pinned at v0.62.0, not v0.61.0, which has GO-2026-6860 and GO-2026-6866.
- **DF05–DF07 — tests and lint the plan could not pass:**
  - DF05: a fixture fix.
  - DF06: a fresh test app per CSRF case, because the PasswordGate bucket never refills on a fake clock.
  - DF07: one `setCookie` helper carries a G124 nolint (plain-HTTP loopback cannot set `Secure`). One `redirectReturn` helper carries a G710 nolint (the target is `safeReturn`-allowlisted).
- **DF08 — GET deadline.** GET/HEAD requests get a 10 s read deadline (A§4.11, Plan B BF31).
- **DF09 — 412 resubmit page.** On a 412 or `generation_mismatch`, the resubmit shows the typed input read-only, with no submit button.
- **DF10 — htmx 4xx.** Under htmx, body-limit 400/413/415 errors render through the HTML error page.
- **DF11 — expired session on POST.** It returns 401 plus `HX-Redirect`, with a minimal HTML body.
- **DF12–DF22 — minor items:**
  - an anchor update in `lifecycle.go`;
  - session and CSRF path scoping;
  - drift notes on the copies of `statusFor` and the problem JSON;
  - the middleware order (A§6.2 amended);
  - indirect dependencies;
  - history paging and one revert form per op;
  - test precision;
  - signature slips;
  - form picking and the C17 rule;
  - Today renders `Banners` only;
  - error-page field names;
  - flash wording mirrors the CLI;
  - a correction to the file table.

## Rulings during execution

- **DF23 — stylesheet caching.** The layout links `/static/app.css?v=<content hash>`. Otherwise `immutable` caching would pin a stale stylesheet for a year. Cost if wrong: one template helper.
- **DF24 — `OPTIONS *`.** The web `http.Server` sets `DisableGeneralOptionsHandler`, so `OPTIONS *` goes through the security headers and the Host check.
- **DF25 — retry after a server error.** After an error of 500 or above, the error page's "Try again" reuses the submitted `_nonce`. The service can commit and then return busy or internal, and the same key either replays the result or runs exactly once. 4xx errors keep a fresh nonce. Cost if wrong: an edited retry after a 5xx needs one extra round trip.
- **DF26 — undo/redo errors.** Undo and redo error pages offer no "Try again" on 4xx. A fresh-nonce retry would act on the *next* op on the stack, and the nav already has the buttons. Cost if wrong: one extra click.
- **DF27 — web port bind failure.** If the web listener fails to bind (port in use, `[::1]` unavailable) or its handler fails to build, the server logs a warning and keeps serving the unix-socket API without the web UI. This matches how the consent gate degrades. The spec is silent; failing the whole server would take the CLI, TUI and MCP down because port 7878 is busy. **The owner may prefer fail-fast.**
- **DF28 — expensive notes.** `markdown.Render` runs a linear pre-scan first. Notes over the complexity bounds render as escaped plain text with "Shown as plain text — too complex to format." The bounds are nesting above 16, more than 2048 inline delimiters, or output above 512 KiB.
  - Why: goldmark's cost is quadratic in nesting, and template rendering ignores the GET deadline. Three AI-written 64 KiB notes made `/ui/` take 15 s; after the fix, 18 such notes take 1.8 s.
  - Cost if wrong: an unusual but legitimate note shows unformatted. **The owner may prefer another presentation.**
- **DF29 — stale tab after logging in again.** A same-origin CSRF token mismatch shows "Your session changed (you logged in again) — reload, then paste", with the typed input read-only. Cross-site failures keep the terse refusal and echo nothing.

## Parked findings (deferred minors)

- **Task 2:**
  - Extra header and Host table rows. The behaviour was verified by probe.
  - A fourth test seam, `NewWithProbe`.
- **Task 4:** more than 4 stray `tr_session` cookies ahead of a valid one force a client-side logout. This is bounded, and a loopback attacker can already force a logout.
- **Task 5:** the Overdue, Due soon and Deferred sections are unbounded, because the service views have no cursor. Log page headings repeat across pages.
- **Task 6a:**
  - History of a nonexistent task returns 200 "No history". Checking existence would break R1.
  - Revert deduplication is per page.
- **Task 6b:**
  - The >366-day range rule is not pinned in a web test.
  - Deleted tasks are not marked in the report.
  - There are no empty-state tests.
- **Task 6c:** no web-side cap on search query length.
- **Task 7:** the flash wording table does not fail when a new EffectKind is added.
- **Task 8:**
  - Accept-new malformed cases do not pin the field.
  - The 428/412 checks are pinned only for place and complete; the other actions share `taskRef`.
  - `until=none` is an exact match.
  - The number of accept-new pairs is bounded only by the 1 MiB body limit.
- **Task 9:**
  - The empty-date message is hand-rolled.
  - A whitespace-only `task_id` gets a 400.
- **Task 11:** the end-to-end "appears" check searches the whole document.
- **Final re-review:**
  - The `reviewed` effect is now omitted when a review changes nothing. This drifts from the Plan A effect table.
  - The A§6.2 middleware list does not mention `readDeadline` or `recoverHTML`.
  - There is no per-page markdown budget.
  - A runtime web `Serve` error still stops the whole server (pre-existing).
  - Notes with more than 2048 delimiters, code fences included, render as plain text. This is per DF28.

## Owner questions (no action taken)

1. **DF27:** keep serving the API when the web port is busy, or refuse to start?
2. **DF28:** how should over-complex notes be presented? The default is escaped plain text with a one-line explanation.
3. **DF17 vs the TUI:** in the web UI you can mark AI changes reviewed only from the oldest unreviewed page. The TUI instead loads all pages first.
