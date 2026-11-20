# Taskronin Plan G — Web face

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the loopback web UI look cared for: a real stylesheet with dark mode, task/block/entry cards with chips, one primary action row per card with the rest behind `More`, a visible success flash, a centered login, and empty states in the shared voice.

**Architecture:** Presentation only. `internal/web/static/app.css` is rewritten (still one file, still served under the existing CSP and content hash). Templates change structure and classes, never routes, handler logic, form field names, `data-*` attributes or `aria-label`s that tests and screen readers rely on. No JavaScript beyond the vendored htmx; `<details>` provides the only disclosure behavior.

**Tech Stack:** Go 1.26 (toolchain go1.27.4) `html/template`, plain CSS (custom properties, `prefers-color-scheme`), htmx 2.0.11 unchanged, tests parse with `golang.org/x/net/html`.

**Spec:** `docs/superpowers/specs/2026-11-20-joy-pass-design.md` (J§3 voice, J§4.3 flash, J§5.2 triage clear, J§7 empty states, J§10 web face). Plan D's Global Constraints and rulings (`docs/superpowers/plans/2026-10-25-taskronin-plan-d-web.md`, `docs/superpowers/notes/2026-11-16-plan-d-execution-rulings.md`) remain binding for everything not restated here.

## Global Constraints

- Repo `/home/sean/aix/taskronin`, branch `main`, commit directly on main, never push. Go `1.26.0`, toolchain `go1.27.4`, module `taskronin`, Linux only. No new dependencies; no Node, no build step.
- Security posture is frozen: the CSP header text, the htmx `<meta name="htmx-config">`, `hx-boost` on `<body>`, the single `<script src="/static/htmx-2.0.11.min.js">`, no `<style>`, no `style=`, no `on*=`, no `hx-on`, no `javascript:` (enforced by `templates_test.go`). `app.css` contains no `@import`, no `url(` and no external reference (new test, Task 1).
- Every mutating form keeps its hidden fields (`_csrf`, `_nonce`, `_gen`, `_return`, `etag`/`task_etag` where present), its `method`, `action`, field names and `hx-confirm` text. Routes and handlers are unchanged (R1 holds: one service call per handler). `data-task`, `data-entry`, `data-block`, `data-banner`, `data-search-*`, `data-snippet`, `data-code`, `aria-label` and `aria-current` attributes are unchanged in name and value.
- The page `Version` (DF23) is the embedded CSS content hash; it changes automatically with the stylesheet. `assets-verify` checks only the htmx file.
- `html/template` auto-escaping is the only escaping; flash lines and titles are never marked safe.
- Voice (J§3) for all copy; no exclamation marks; `✓` is the only mark added.
- `make check` green before every commit; iterate with `go test -race ./internal/web`, full `make check` once per task; re-run a timed-out package alone with `-timeout=30m` and say so.
- Anchors: grep `AIDEV-` before editing; never delete an `AIDEV-NOTE`; keep the `rowCtx` note in `partials.html` and `render.go` accurate; add anchors at the primary/`More` split and the stylesheet's theme tokens.
- Commits: conventional (`feat(web): …`), one per task, Codex trailer `Co-Authored-By: OpenAI Codex <noreply@openai.com>`.

## Review Focus

1. **No restructuring may drop a hidden field.** Test: Task 3 `TestEveryFormKeepsHiddenFields` walks every `<form method="post">` on every GET page and asserts `_csrf`, `_nonce`, `_gen`, `_return` are present (and `etag` on every form whose action matches `/ui/tasks/\d+/`, `/ui/entries/\d+/`, `/ui/blocks/\d+/`).
2. **Danger actions keep their confirmations** after moving into `More`. Test: Task 3 `TestDangerFormsKeepConfirm` (Drop, Delete, Delete entry, Reopen).
3. **The stylesheet is self-contained** and the templates still carry no inline style or script. Tests: Task 1 `TestCSSSelfContained`, existing `TestTemplates*`.
4. **Flash and error copy is escaped.** A retitle with `<b>` in the title shows literally in the flash banner. Test: Task 4 `TestFlashEscapesHTML`.
5. **Empty states appear only when the list is empty**, never alongside items (a template `{{else}}` mistake would show both). Test: Task 4 `TestEmptyStatesExclusive`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/web/static/app.css` | the stylesheet: tokens, dark mode, frame, cards, chips, buttons, forms, flash, login | 1 |
| `internal/web/templates/layout.html`, `login.html`, `error.html` | frame (`<header class="topbar">`), flash banner, login card, error card | 1 |
| `internal/web/templates/partials.html` (`taskrow`, `blockrow`, `entryrow`) | cards and chips | 2 |
| `internal/web/templates/partials.html` (`taskactions`, `addtask`), `triage.html`, `backlog.html`, `upcoming.html`, `task.html`, `today.html` | primary row + `More`, button classes, add-task placement group | 3 |
| `internal/web/templates/today.html`, `triage.html`, `backlog.html`, `upcoming.html`, `log.html`, `search.html`, `report.html`, `recap.html`, `task.html` | empty-state copy; flash `✓` | 4 |
| `internal/web/*_test.go` | the tests named per task | 1–4 |

---

### Task 1: Stylesheet, frame, flash, login

**Files:**
- Modify: `internal/web/static/app.css`, `internal/web/templates/layout.html`, `internal/web/templates/login.html`, `internal/web/templates/error.html`
- Test: `internal/web/static_test.go` (`TestCSSSelfContained`), `internal/web/templates_test.go` or `views_test.go` (`TestLayoutFrame`)

**Interfaces (binding):**
- `layout.html`: `<header class="topbar">` containing `<a class="brand" href="/ui/">Taskronin</a>`, the existing `<nav aria-label="Main">` (links unchanged, current page keeps `aria-current="page"`), and a `<div class="topbar-actions">` holding the Undo, Redo and Log out forms (unchanged forms, buttons get `class="btn small"`). Flash: `<section class="flash" role="status" aria-label="Action result">` keeps its attributes and gains a leading `<span class="flash-mark">✓</span>`; lines stay `<ul><li>`. `<main class="page">`.
- `login.html`: `<section class="login-card">` with `<h1>Taskronin</h1>`, `<p class="tagline">Your tasks, your focus, your work log.</p>`, the existing form (unchanged names/attributes), the error paragraph unchanged (`class="error" data-code`).
- `error.html`: wrap in `<section class="error card" data-code="…">` — `data-code` and all text unchanged.
- `app.css` (rewrite; keep it under ~400 lines):
  - `:root` tokens: `--bg`, `--fg`, `--muted`, `--card`, `--border`, `--accent` (blue), `--ok` (green), `--warn` (amber), `--bad` (red), `--radius: 0.5rem`; redefined under `@media (prefers-color-scheme: dark)`.
  - Frame: `body { max-width: 56rem; margin: 0 auto; padding: 1rem; background: var(--bg); color: var(--fg); }`; `.topbar` flex, wraps on phones; nav links as pills, `[aria-current="page"]` filled with `--accent`.
  - `.card` (border, radius, padding, background `--card`, margin-block); `.card.focus` left border `--ok`; `.card.current` left border `--accent`.
  - `.chip` (inline pill, small text), `.chip.warn`, `.chip.bad`, `.chip.tag`; `.meta` muted small line.
  - `.btn` base, `.btn.primary` (filled accent), `.btn.secondary` (outlined), `.btn.danger` (red outline), `.btn.small`; `.actions` flex row with gap; `details.more > summary` styled as a secondary button.
  - Forms: labels block, inputs full width up to 24rem, textarea full width; `:focus-visible` ring kept.
  - `.flash` green-tinted banner with `--ok` border; `.error.card` red border.
  - `.login-card` centered, max-width 22rem, margin-top 15vh.
  - Keep `.notes-plain`, `.muted`, `.notes` (markdown output: readable line-height, code blocks scroll horizontally).
  - No `@import`, no `url(`, no external fonts (system font stack as today).

- [ ] **Step 1: Tests first**: `TestCSSSelfContained` (read `static/app.css` via `web.StaticFS`; fail on `@import`, `url(`, `http:`, `https:`, `expression(`); `TestLayoutFrame` (GET `/ui/` logged in; parse; exactly one `header.topbar`; it contains `nav[aria-label=Main]` and three forms with actions `/ui/undo`, `/ui/redo`, `/ui/logout`; `main.page` exists; GET `/ui/login` has `section.login-card` with the tagline text). Run → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test -race ./internal/web` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(web): stylesheet with dark mode, top bar, flash and login card"`

---

### Task 2: Cards and chips

**Files:**
- Modify: `internal/web/templates/partials.html` (`taskrow`, `blockrow`, `entryrow`), `internal/web/templates/task.html` (slips line)
- Test: `internal/web/views_test.go`, `task_detail_test.go`, `entries_blocks_test.go` (update assertions on removed text; add `TestTaskCardChips`)

**Interfaces (binding):**
- `taskrow` → `<article class="card task" data-task="{{.V.ID}}">` with `<h3 class="card-title"><a href="/ui/tasks/ID"><span class="id">#ID</span> Title</a></h3>` and `<p class="meta">` containing, in order: reasons as `<span class="chip warn reason">new</span>` (keep class `reason`), `<span class="chip bad" title="Slips: lifetime / since triage">slipped ×L</span>` **only when `SlipsLifetime > 0`** (L = lifetime; the `Slips L/S: a/b` text leaves rows and stays on the task page as a plain line), due as `<span class="chip warn">due <day></span>` or `<span class="chip bad">overdue</span>` when `DuePhase == "overdue"` (and `due soon` chip when `.V.DueSoon`), defer as `<span class="chip">deferred until <day> [HH:MM]</span>`, tags as `<span class="chip tag">name</span>` (keep class `tag`), project as `<span class="muted">project</span>`, priority as `<span class="chip">prio H</span>`.
- `blockrow` → `<article class="card block{{if .V.Current}} current{{end}}" data-block=… {{aria-current}}>`; `<p class="card-title">` with time span and task link, `<p class="meta">` with status chip (`planned` plain, `worked`/`done` `.ok`, `missed` `.bad`, `cancelled` muted) and `reblock?` as `<strong class="chip warn">reblock?</strong>` (keep the `<mark>Current block</mark>` element).
- `entryrow` → `<article class="card entry" data-entry=…>`; `✓` for completions as `<span class="ok">✓</span>`; task link line as `.meta`; `(reopened)`/`(deleted)` text unchanged.
- Today's Focus block: wrap in `<div class="card focus">` with `▶` before the title (text `▶ ` inside `.card-title`).
- Add `.ok` and `.id` classes to the stylesheet if Task 1 did not.

- [ ] **Step 1: Tests first**: `TestTaskCardChips` (a task with reasons, 2 lifetime slips, due tomorrow, tags, project: parse the Triage page; the `article[data-task]` contains `.chip.reason` with `new`, `.chip.bad` with `slipped ×2`, `.chip.tag` ×2; a task with 0 slips has no `slipped` text). Update assertions that looked for `Slips L/S` in rows. Run → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test -race ./internal/web` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(web): task, block and entry cards with chips"`

---

### Task 3: One primary action row, the rest behind More

**Files:**
- Modify: `internal/web/templates/partials.html` (`taskactions`, `addtask`), `triage.html`, `backlog.html`, `upcoming.html`, `task.html`, `today.html`
- Test: `internal/web/forms_test.go` or `views_test.go` (`TestEveryFormKeepsHiddenFields`, `TestDangerFormsKeepConfirm`, `TestPrimaryActionRows`)

**Interfaces (binding):**
- Inside each task card on list pages: `<div class="actions">` (primary) then `<details class="more"><summary>More</summary><div class="actions">…</div></details>`.
  - Triage primary: Keep, Top, Bottom (`btn secondary`), Complete (`btn primary`). More: Place before/after (with their ID inputs), Defer, Block, Drop (`btn danger`).
  - Backlog primary: Up, Down, Focus (`btn secondary`), Complete (`btn primary`). More: Top, Bottom, Send to triage, Defer, Block, Drop.
  - Upcoming (Deferred): primary Keep (`btn secondary`) and Change defer (`btn primary`); nothing in More. Due soon: Set due (`btn primary`).
  - Task page: primary Complete (`btn primary`, the full complete form with its optional fields moves into a `<details class="more"><summary>Complete with options</summary>` **only for the optional fields**: the plain Complete button posts the same form with empty optional fields — implement as one form whose optional inputs sit inside the details and whose submit button sits outside), Focus, Keep (`btn secondary`); More: Top, Bottom, Place before/after, Defer, Block, Send to triage, Drop (`danger`), Delete (`danger`). Done/dropped tasks: Reopen primary, Delete in More. Deleted: Undelete primary.
  - Today: Focus card primary Complete + Clear focus; Set focus form unchanged.
- `addtask`: fields Title, Notes, Due, Tags, Project, Priority stay visible; Placement, Reference task ID, Defer until, Block span move into `<details class="more"><summary>Placement</summary>`; submit `btn primary`. It stays on Today and Triage only.
- Every moved form keeps its hidden inputs, `etag`, `hx-confirm`, names and action.

- [ ] **Step 1: Tests first**: `TestEveryFormKeepsHiddenFields` (GET each of `/ui/`, `/ui/triage`, `/ui/backlog`, `/ui/upcoming`, `/ui/log`, `/ui/recap`, `/ui/search?q=x`, `/ui/tasks/{id}` for an open, a done and a deleted task, with fixtures that make every form render; for every `form[method=post]` assert `_csrf`, `_nonce`, `_gen`, `_return` inputs exist and `etag` exists when the action matches the entity routes), `TestDangerFormsKeepConfirm` (every form whose action ends with `/drop`, `/delete`, `/reopen` has a non-empty `hx-confirm`), `TestPrimaryActionRows` (Triage card: the first `.actions` contains buttons Keep, Top, Bottom, Complete and no Drop; Drop is inside `details.more`; Backlog: Up/Down/Focus/Complete). Run → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test -race ./internal/web` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(web): primary action rows with the rest behind More"`

---

### Task 4: Empty states and the success flash

**Files:**
- Modify: `today.html`, `triage.html`, `backlog.html`, `upcoming.html`, `log.html`, `search.html`, `report.html`, `recap.html`, `task.html`, `layout.html` (flash mark if not done in Task 1)
- Test: `internal/web/views_test.go` (`TestEmptyStatesExclusive`, `TestEmptyStateCopy`), `flash_test.go` (`TestFlashEscapesHTML`)

**Interfaces (binding copy; each as `<p class="empty">…</p>`):**
- Today Focus: `No focus. Pick a task below, or set one by ID.`; Blocks: `No blocks today.` followed by `<span class="muted">Schedule one from a task's actions.</span>`; Triage: `Triage clear.`; Entries: `Nothing logged yet today.`; Interruptions: `No interruptions today.` (unchanged).
- Triage page: `Triage clear. Nothing needs a decision.`
- Backlog: `Backlog empty. New tasks start in Triage.` (no add form is added to this page)
- Upcoming Deferred: `Nothing deferred.`; Due soon section stays omitted when empty (as today).
- Log: `No entries in this range. Log one below.`
- Search (searched, no hits): `No matches for “<q>”.` (curly quotes; `q` auto-escaped).
- Report (no entries): `No work logged in this range.`
- Recap: Blocks `No blocks on this date.` (unchanged), Entries `No linked entries on this date.` (unchanged), Unlinked `No unlinked entries.`; when all three are empty and no interruptions, one line above them: `Nothing to recap for <day>.`
- Task page: `No blocks.`, `No entries.`, `No interruptions.` (unchanged).
- Flash: lines render inside the `.flash` banner with the `✓` mark (Task 1); the wording is Plan E's web `effectLine` (unchanged here).

- [ ] **Step 1: Tests first**: `TestEmptyStateCopy` (each page empty → exact text present), `TestEmptyStatesExclusive` (each page with one item → its empty text absent), `TestFlashEscapesHTML` (retitle a task to `<b>x</b>`; the next page's `.flash li` text contains `<b>x</b>` literally and the parsed node has no `b` element). Run → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test -race ./internal/web` → PASS; `make check`.
- [ ] **Step 4: Commit** — `git commit -m "feat(web): empty states in the shared voice"`
