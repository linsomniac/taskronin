# Taskronin Plan D — Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the loopback web UI (`/ui`): session login, CSRF/Origin/Host defences, strict security headers, server-rendered views and every action of architecture §11.2, calling `service` in-process.

**Architecture:** `internal/markdown` renders notes (goldmark without raw HTML, images as links) through a bluemonday allowlist. `internal/web` is one `http.Handler` built by `web.New`: security headers → Host allowlist → route class → body limit → session → CSRF → `net/http` `ServeMux` routes. Pages are `html/template` with a shared layout; htmx 2.0.11 (vendored, SHA-checked, `hx-boost` on `<body>`) gives in-place navigation. Every mutation is a plain `<form method="post">` → one service call → `303 See Other` to the form's `_return` page; errors render an error page that keeps the submitted input.

**Tech Stack:** Go 1.26 stdlib `net/http`, `html/template`, `embed`; `github.com/yuin/goldmark v1.8.6`; `github.com/microcosm-cc/bluemonday v1.0.27`; htmx 2.0.11; tests parse HTML with `golang.org/x/net/html`.

**Spec:** `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (primary: §1.3, §2.5, §2.6 incl. `BlockView.TaskVersion` (R4-7), §2.8, §3, §6.1–6.2, §6.7 (`textsafe` names), §7.4–7.5, §11.2, §13, §14.6 R3-3), `docs/superpowers/specs/2026-10-25-taskronin-design.md` (D§9.3, D§10.2–10.4), `docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md` (web halves of SCP-04, SCP-05, SCP-07, PRG-18, IDM-04; UND-19).

**Prerequisites:** Plan A (core library; types cited as "Plan A Task N") and Plan B (server, `server.Run` with the `newWebHandler` hook, `testutil.StartServer`/`WithWeb`/`WebURL`) are merged. Plan D is the last delivery step (D§16).

## Global Constraints

- **Execution context:** each implementer receives its task text, these Global Constraints and the "Form contract" section below, with read access to the three specs and every plan file. A citation such as "Plan A Task 32 Interfaces: `service.TodayView`" is binding: open that block for the exact fields. Web handlers and templates use Plan A's `domain`/`service` types (Plan A Tasks 5, 16–35), never the `api` wire types of arch §6.5.
- Go `1.26.0`, toolchain `go1.26.8`, module `taskronin`, Linux only.
- New direct dependencies: `github.com/yuin/goldmark v1.8.6`, `github.com/microcosm-cc/bluemonday v1.0.27`; `golang.org/x/net` at `v0.61.0` (bluemonday requires v0.26.0, which govulncheck flags; v0.61.0 matches the pinned x/crypto v0.58.0 / x/sys v0.49.0 / x/term v0.47.0; tests import `golang.org/x/net/html`). No other dependency changes.
- Allowed imports (arch §1.3; `internal/archtest` checks non-test imports, Plan A Task 1): `markdown` → no internal packages, third-party goldmark + bluemonday. `web` → `service`, `domain`, `civil`, `datespec`, `auth`, `markdown`, `textsafe`, `clock`, `buildinfo` only; never `store`, `api`, `server`, `client`.
- **Test packages (P6):** every `internal/web` test is in package `web_test` and may import `testutil`; internals are reached only through `internal/web/export_test.go` (package `web`, never imports `testutil`). This keeps `web[test] → testutil → server → web` from becoming an import cycle once Task 11 makes `server` import `web`.
- **HTML assertions** parse the response with `golang.org/x/net/html` (test-only import): security properties are checked on parsed elements and attributes (element names, `on*` attribute names, `href`/`src` schemes), never by substring search for words such as `javascript:` that may legitimately appear as escaped text.
- R1: each web handler decodes the request, makes **at most one** service call, then renders or redirects. All rules and authorization stay in `service`. `(*service.Service).Generation()` (cached, no DB, no authz; Plan A Task 16) is not counted.
- R4: no `time.Now`/`time.Local`; use `Options.Clock` and `Options.Zone`. Tests use `httptest.NewRequestWithContext(t.Context(), …)` (noctx).
- `make check` (gofmt, `go vet`, golangci-lint v2.14 incl. gosec — off for `_test.go` per arch §13.4 —, `go test -race -count=1 ./...`, govulncheck v1.9.0, `go mod verify`, `assets-verify`) is green before every commit. The only `//nolint` in Plan D production code is `//nolint:gosec // G203: bluemonday-sanitized output` in `markdown/render.go`; `web` reads no files from variable paths and execs nothing.
- htmx asset: `internal/web/static/htmx-2.0.11.min.js` = npm `htmx.org@2.0.11` `dist/htmx.min.js`, 52 182 bytes, SHA-256 `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`, recorded in `internal/web/static/SHA256SUMS`.
- htmx config, verbatim in the layout `<head>`: `<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"selfRequestsOnly":true,"includeIndicatorStyles":false,"historyCacheSize":0,"refreshOnHistoryMiss":true}'>`.
- Templates: no inline `<script>` bodies, no `<style>`, no `style=` attributes, no `on*=` attributes, no `hx-on`; the only script is `<script src="/static/htmx-2.0.11.min.js"></script>`; confirmations via `hx-confirm`; move buttons, no drag-and-drop.
- Every response from the web handler carries exactly: `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`, `Permissions-Policy: camera=(), geolocation=(), microphone=()`.
- Session cookie (plain loopback, v1): `tr_session=<base64url>; Path=/; HttpOnly; SameSite=Lax` (+ `Expires` = session absolute expiry). Idle 7 d / absolute 30 d are enforced by `service`.
- CSRF token = base64url(HMAC-SHA256(csrf_secret, "tkr-csrf-v1")) — `service.Authenticate` returns it ready-made (`WebAuth.CSRFToken`, Plan A Task 35); every POST except `POST /ui/login` needs a matching token (form `_csrf`, else header `X-CSRF-Token`; `subtle.ConstantTimeCompare`) **and** the Origin rule: `Origin` ∈ {`http://` + Host-allowlist entry} or `Sec-Fetch-Site: same-origin`; reject when both headers are absent or either mismatches. `POST /ui/login` gets the Origin rule only.
- Host allowlist from `web.listen`: the literal `host:port`, plus `localhost:<port>` when the host is `127.0.0.1` or `::1`; anything else → 400.
- Forms follow the Form contract below; every mutating form — including the undo, redo and revert buttons (R3-3) — carries hidden `_csrf`, `_nonce` (fresh 128-bit lowercase hex per render), `_gen` (generation the page was rendered with), `_return`, plus the target's full ETag `domain.FormatETag(gen, version)` as `etag` where the contract says so.
- Web idempotency: `service.Idem{Key: _nonce, Method: "POST", Route: "POST " + r.URL.Path, Body: []byte(form-minus-_csrf-and-_nonce .Encode())}` (`url.Values.Encode` sorts keys); `Meta.Gen = _gen`. `Login` is idempotency-exempt.
- Web actor = `WebAuth.Actor` (`ActorOwner`, `ChanWeb`, all scopes). Token management, settings, password, backup, export and purge have **no** web routes.
- GET handlers perform no user ops (reconcile's system writes are allowed).
- Passwords: never logged, never echoed into a page, never part of an idempotency body or a resubmit form; the web never logs form bodies.
- Only `markdown.Render` output may become `template.HTML`; every other value reaches the page through `html/template` auto-escaping. User text written to logs goes through `textsafe.SanitizeLine` (arch §6.7).
- Anchors: `AIDEV-NOTE:` in `web/csrf.go` (required by arch §1.4), on the Host allowlist, the session middleware, the idempotency-body canonicalization, the htmx error-status rule, the resubmit form and the markdown policy. Grep `AIDEV-` in a package before editing it; never delete an `AIDEV-NOTE`.
- Commits: conventional (`feat(web): …`), each message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never commit secrets; tests use `testutil.Password`.

## Review Focus

1. **Browser textarea line endings** — browsers submit `<textarea>` content with CRLF. Notes added or edited through the web must be stored with LF (D18, `textsafe.ValidateNotes`) and never rejected as control characters. Test: Task 7 `TestAddTaskNotesCRLF` and `TestEditTaskNotesCRLF`.
2. **Double submit / back-button resubmit** — the same rendered form posted twice carries the same `_nonce`; exactly one op must exist and both responses redirect to `_return`. Test: Task 7 `TestAddTaskDoubleSubmitReplays`.
3. **Stale page** — a page rendered before a change made elsewhere (CLI, TUI, AI) and then submitted must show a visible "changed elsewhere — reload" error (412), also for htmx-boosted requests, which do not swap 4xx bodies under the fixed htmx config. Test: Task 3 `TestRenderErrorHTMX`, Task 8 `TestStaleETagShowsReloadError`.
4. **Hostile task text** — titles, notes, tags, entry text and search snippets containing `<script>`, `"><img src=x onerror=…>`, `javascript:` links or quotes in `hx-confirm` attributes stay inert on every page, while the literal text stays visible and editable. Test: Task 1 `FuzzRender`, Task 5 `TestViewsEscapeHostileTitle`, Task 6a `TestDetailEscapesAndRendersMarkdown`.
5. **Open redirect via `_return`** — `https://evil.example/`, `//evil.example/`, `/\evil.example`, `javascript:…` or a non-`/ui/` path fall back to `/ui/`. Test: Task 3 `TestSafeReturn`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/markdown/render.go`, `render_test.go` | `Render`: goldmark (no raw HTML, image→link renderer) → bluemonday policy; tests + `FuzzRender` | 1 |
| `internal/web/static/htmx-2.0.11.min.js`, `static/SHA256SUMS`, `static/app.css` | vendored htmx, its checksum, responsive stylesheet | 2 |
| `internal/web/web.go` | `Options`, `New`, embeds, chain assembly | 2 |
| `internal/web/headers.go` | Host allowlist, security headers, route class, body limit | 2 |
| `internal/web/routes.go` | the route table (`ServeMux` patterns → handlers) | 2 (grown by 4–10) |
| `internal/web/export_test.go` | test seams for package `web_test` | 2 (grown by 3, 7) |
| `internal/web/render.go`, `forms.go` | template set, `page`, FuncMap, `renderPage`, `renderError` (+ resubmit), `statusFor`; form decoding, `safeReturn`, `moreURL` | 3 |
| `internal/web/templates/layout.html`, `partials.html`, `error.html` | base layout, shared partials, error page | 3 |
| `internal/web/helpers_test.go` | `testApp` harness (package `web_test`) | 3 (grown by 4, 5, 7) |
| `internal/web/session.go`, `csrf.go`, `h_auth.go`, `templates/login.html` | session middleware + cookie, CSRF/Origin, login/logout | 4 |
| `internal/web/h_views.go`, `templates/{today,triage,backlog,upcoming,log}.html` | home stub (4); list pages (5) | 4, 5 |
| `internal/web/h_task.go`, `templates/{task,history}.html` | task detail, history | 6a |
| `internal/web/h_recap.go`, `templates/{recap,report}.html` | recap, report | 6b |
| `internal/web/h_search.go`, `templates/{search,aichanges}.html` | search, AI changes | 6c |
| `internal/web/h_tasks.go`, `flash.go` | task POSTs; `mutate` + D§5.9 effect flash | 7, 8 |
| `internal/web/h_entries.go`, `h_blocks.go` | entry and block POSTs | 9 |
| `internal/web/h_ops.go` | focus, undo, redo, revert, AI review | 10 |
| `internal/server/web.go`, `web_integration_test.go` | assigns Plan B's `newWebHandler` hook; TCP end to end | 11 |

## Form contract (Tasks 5–10)

Templates (Tasks 4–6c) render these forms; handlers (Tasks 7–10) decode them. "Hidden" = `{{template "hidden" $}}` (`_csrf`, `_gen`, `_nonce`, `_return`). Every fixed action is its **own** `<form method="post">` carrying its fixed value in an `<input type="hidden">` (no multi-button forms), so a scraped form is complete. `etag`/`task_etag` hold `domain.FormatETag`. Datespecs resolve with `datespec` (A§9.2) against `Options.Clock`/`Options.WeekStart`; **F** = `datespec.Future`, **P** = `datespec.Past`.

| # | Route | Rendered on | Fields (besides hidden) → service call (Plan A signature) |
|---|---|---|---|
| C1 | `POST /ui/tasks` | today, triage | `title`, `notes` (textarea), `due` (F, optional), `tags` (`splitTags`), `project`, `priority` (select `""`/`H`/`M`/`L`; else validation), `place` (select): `""` → `Place nil` (triage); `top`/`bottom`/`now` → `{How}`; `before`/`after` → `{How, Ref: formInt("ref")}`; `defer` → `{How, Defer: formMoment("defer", F)}` (empty → validation `defer`); `block` → `{How, Block: formSpan("span")}`; other → validation `place` → `AddTask(ctx, m, AddTaskIn)` (Plan A Task 16) |
| C2 | `POST /ui/tasks/{id}/edit` | task | `etag`; each of `title`, `notes` (textarea), `project`, `priority` (select) set **only if present** (`form.Has`) → `UpdateTask(ctx, m, UpdateTaskIn)` (Plan A Task 29) |
| C3 | `POST /ui/tasks/{id}/tags` | task | add form: `etag`, `add` (`splitTags`); one remove form per tag: `etag`, hidden `remove` → `UpdateTask{TagsAdd, TagsRemove}` |
| C4 | `POST /ui/tasks/{id}/due` | task, upcoming | `etag`, `due`: `none` → `Due nil`; empty → validation `due`; else F → `SetDue(ctx, m, SetDueIn)` (Plan A Task 20) |
| C5 | `POST /ui/tasks/{id}/place` | triage, backlog, task | one form each for hidden `how` = `keep`/`top`/`bottom`; `before`/`after` forms: hidden `how` + `ref` input (backlog up/down: hidden `ref` = neighbour); `etag` → `Place(ctx, m, PlaceIn)` (Plan A Task 19); bad `how` → validation |
| C6 | `POST /ui/tasks/{id}/defer` | triage, backlog, upcoming, task | `etag`, `until`: `none` → `Until nil` (upcoming "keep" = hidden `until=none`); empty → validation `until`; else F → `Defer(ctx, m, DeferIn)` (Plan A Task 27) |
| C7 | `POST /ui/tasks/{id}/triage` | backlog, task | `etag` → `MarkManual(ctx, m, MarkManualIn)` (Plan A Task 16) |
| C8 | `POST /ui/tasks/{id}/block` | triage, backlog, task | `etag`, `span` (`formSpan`, F) → `Block(ctx, m, BlockIn)` (Plan A Task 22) |
| C9 | `POST /ui/tasks/{id}/complete` | today (focus), triage, backlog, task | `etag`; on task also `as` (non-empty → `As`), `date` (`formDate` P; empty → zero = today), `log`, `notes` (textarea) → `Complete(ctx, m, CompleteIn{…, LogTitle: log, LogNotes: notes})` (Plan A Task 28) |
| C10 | `POST /ui/tasks/{id}/reopen` | task (done/dropped) | `etag`, checkbox `remove_entry` value `1` (`hx-confirm`) → `Reopen(ctx, m, ReopenIn)` (Plan A Task 28) |
| C11 | `POST /ui/tasks/{id}/drop`, `…/delete`, `…/undelete` | drop: triage, backlog, task; delete/undelete: task | `etag` (drop/delete with `hx-confirm`) → `Drop`/`Delete`/`Undelete(ctx, m, TaskRef)` (Plan A Task 29) |
| C12 | `POST /ui/triage/accept-new` | triage | parallel hidden `id` and `etag` lists, one pair per listed task whose reasons are exactly `{new}` (equal length ≥ 1, else malformed) → `AcceptNew(ctx, m, AcceptNewIn{Items []AcceptItem})` (Plan A Task 19) |
| C13 | `POST /ui/entries` | today, log, recap | `task_id` (empty → standalone `Null`, else `formInt`; today: prefilled focus task, else current block's task, else empty), `title`, `notes` (textarea), `date` (`formDate` P; empty → zero = today; recap: prefilled with its date), `at` (`formMinute`) → `AddEntry(ctx, m, AddEntryIn)` (Plan A Task 21) |
| C14 | `POST /ui/entries/{id}/edit` | `entryrow` (log, task, recap) | `etag`; work entries: `title`, `notes` (textarea), `date`, `at`; complete entries: `notes` only. Each set only if present; present-but-empty `at` → clear (`&sql.Null[civil.Minute]{}`), present-but-empty `date` → validation → `UpdateEntry(ctx, m, UpdateEntryIn)` (Plan A Task 25) |
| C15 | `POST /ui/entries/{id}/relink` | `entryrow` (log, recap unlinked) | `etag`, `task_id` (empty → standalone) → `RelinkEntry(ctx, m, RelinkEntryIn)` (Plan A Task 25) |
| C16 | `POST /ui/entries/{id}/delete`, `…/undelete` | `entryrow` | `etag` (delete with `hx-confirm`) → `DeleteEntry`/`UndeleteEntry(ctx, m, EntryRef)` (Plan A Task 25) |
| C17 | `POST /ui/blocks/{id}/reblock` | `blockrow` (today when `NeedsReblock`, recap, task) | `etag` (block), `task_etag` = `FormatETag(gen, BlockView.TaskVersion)`, `span` (`formSpan`) → `Reblock(ctx, m, ReblockIn)` (Plan A Task 24) |
| C18 | `POST /ui/blocks/{id}/unblock` | `blockrow` (planned) | `etag` → `Unblock(ctx, m, BlockRef)` (Plan A Task 24) |
| C19 | `POST /ui/blocks/{id}/outcome` | `blockrow` on recap | one form each for hidden `outcome` = `worked`/`missed`/`auto`; `etag` → `SetOutcome(ctx, m, SetOutcomeIn)` (Plan A Task 24); other value → validation `outcome` |
| C20 | `POST /ui/focus` | today (set: `task_id` input + checkbox `interrupt` value `1`; clear: hidden `task_id=""`), backlog, task (hidden `task_id`) | → `SetFocus(ctx, m, SetFocusIn)` (Plan A Task 26) |
| C21 | `POST /ui/undo`, `POST /ui/redo` | layout nav | — → `Undo(ctx, m)` / `Redo(ctx, m)` (Plan A Task 31) |
| C22 | `POST /ui/ops/{op}/revert` | history, aichanges (`hx-confirm`) | path `op` (`domain.ParseOpID`; bad → 404) → `Revert(ctx, m, RevertIn)` (Plan A Task 30) |
| C23 | `POST /ui/ai-changes/review` | aichanges | hidden `through` (`formInt`) → `MarkAIReviewed(ctx, m, MarkAIReviewedIn)` (Plan A Task 33) |
| C24 | `POST /ui/logout` | layout nav (`hx-boost="false"`) | — → `Logout(ctx, cookie)` (Plan A Task 35) |
| C25 | `POST /ui/login` | login (`hx-boost="false"`, no hidden fields) | `password` → `Login(ctx, LoginIn)` (Plan A Task 35) |

---

### Task 1: Markdown rendering (`internal/markdown`)

**Files:**
- Create: `internal/markdown/render.go`, `internal/markdown/render_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing internal (arch §1.3).
- Produces: `func Render(src string) (template.HTML, error)` — sanitized HTML for notes; the only producer of `template.HTML` from user text. Used by `web` templates as FuncMap `md`.

- [ ] **Step 1: Write the failing tests** in `internal/markdown/render_test.go` (package `markdown`)

```go
func TestRender(t *testing.T) {
	cases := []struct {
		name, in      string
		want, notWant []string
	}{
		{"emphasis", "Hello *world*", []string{"<p>Hello <em>world</em></p>"}, nil},
		{"raw block html omitted", "<script>alert(1)</script>\n\nok", []string{"<p>ok</p>"}, []string{"<script", "alert(1)</script>"}},
		{"raw inline html omitted", `x <img src=x onerror=alert(1)> y`, nil, []string{"<img", "onerror"}},
		{"javascript link", "[c](javascript:alert(1))", nil, []string{"javascript:"}},
		{"data link", "[c](data:text/html,hi)", nil, []string{"data:"}},
		{"https link", "[docs](https://example.com/a)", []string{`href="https://example.com/a"`, `rel="noreferrer noopener"`, `target="_blank"`}, nil},
		{"mailto kept", "[m](mailto:sean@example.com)", []string{`href="mailto:sean@example.com"`}, nil},
		{"relative dropped", "[r](/ui/backlog)", []string{"r"}, []string{`href="/ui/backlog"`}},
		{"image as link", "![diagram](https://example.com/d.png)", []string{`<a href="https://example.com/d.png"`, ">diagram</a>"}, []string{"<img"}},
		{"image javascript", "![x](javascript:alert(1))", nil, []string{"javascript:", "<img"}},
		{"linkify", "see https://example.com/x", []string{`href="https://example.com/x"`}, nil},
		{"strikethrough", "~~old~~", []string{"<del>old</del>"}, nil},
		{"code escaped", "`<b>`", []string{"<code>&lt;b&gt;</code>"}, nil},
		{"list", "1. a\n2. b", []string{"<ol>", "<li>a</li>"}, nil},
		{"harmless text kept", "x onload=y and javascript: as words", []string{"x onload=y and javascript: as words"}, nil},
	}
	// for each: got, err := Render(c.in); err == nil; every want is a substring; no notWant is a substring
}

func FuzzRender(f *testing.F) // seeds: every TestRender input. Property, checked on html.Parse(…) of the output
// (golang.org/x/net/html; walk all nodes): element names ⊆ {html, head, body (parser-added), p, br, hr, h1–h6, blockquote,
// pre, code, em, strong, del, ul, ol, li, a}; attribute names ⊆ {href, rel, target, start}; no attribute name starts with "on";
// every href starts with "http://", "https://" or "mailto:". Text nodes are never inspected (plain words are fine).
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/markdown -run TestRender -race`
Expected: FAIL (`undefined: Render`).

- [ ] **Step 3: Implement `Render(src string) (template.HTML, error)` in `internal/markdown/render.go`**

- One package-level `goldmark.Markdown`: `goldmark.New(goldmark.WithExtensions(extension.Linkify, extension.Strikethrough), goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(imageAsLink{}, 100))))` — **no** `html.WithUnsafe()` (raw HTML becomes `<!-- raw HTML omitted -->`). Priority 100 matters: goldmark registers node renderers from the highest priority value to the lowest, so the lower value registers last and overrides the default HTML renderer (priority 1000) for `ast.KindImage`.
- `imageAsLink` implements `renderer.NodeRenderer`: for `*ast.Image`, entering writes `<a href="` + `util.EscapeHTML(util.URLEscape(n.Destination, true))` + `">` and returns `ast.WalkContinue` (the alt text renders as text); leaving writes `</a>`.
- One package-level bluemonday policy (AIDEV-NOTE: the notes allowlist; D§10.4):
  ```go
  p := bluemonday.NewPolicy()
  p.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote",
  	"pre", "code", "em", "strong", "del", "ul", "ol", "li")
  p.AllowAttrs("start").Matching(bluemonday.Integer).OnElements("ol")
  p.AllowAttrs("href").OnElements("a")
  p.AllowURLSchemes("http", "https", "mailto")
  p.RequireParseableURLs(true)
  p.RequireNoReferrerOnLinks(true)
  p.AddTargetBlankToFullyQualifiedLinks(true) // also adds rel="noopener"
  ```
  Relative URLs stay disallowed (the `<a>` is dropped, its text kept).
- `Render`: convert into a `bytes.Buffer`, `policy.SanitizeBytes`, return `template.HTML(...)` with `//nolint:gosec // G203: bluemonday-sanitized output`.

- [ ] **Step 4: Add the dependencies now that code imports them, then tidy**

Run: `go get github.com/yuin/goldmark@v1.8.6 github.com/microcosm-cc/bluemonday@v1.0.27 golang.org/x/net@v0.61.0 && go mod tidy && grep -E 'goldmark|bluemonday|golang.org/x/net' go.mod`
Expected: `github.com/yuin/goldmark v1.8.6`, `github.com/microcosm-cc/bluemonday v1.0.27` and `golang.org/x/net v0.61.0` remain (x/net is imported by the test); `golang.org/x/crypto`, `x/sys`, `x/term` versions unchanged.

- [ ] **Step 5: Run to verify pass, then fuzz briefly**

Run: `go test ./internal/markdown -race && go test ./internal/markdown -run '^$' -fuzz FuzzRender -fuzztime 30s`
Expected: PASS; fuzz finds no failure.

- [ ] **Step 6: Commit**

```bash
make check
git add go.mod go.sum internal/markdown
git commit -m "feat(markdown): sanitized notes rendering with goldmark and bluemonday

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Web handler skeleton, vendored htmx, outer middleware

**Files:**
- Create: `internal/web/web.go`, `internal/web/headers.go`, `internal/web/routes.go`, `internal/web/export_test.go`, `internal/web/static/htmx-2.0.11.min.js`, `internal/web/static/SHA256SUMS`, `internal/web/static/app.css`, `internal/web/headers_test.go`, `internal/web/static_test.go`

**Interfaces:**
- Consumes: `*service.Service` (stored only); `clock.Clock`; `civil.Zone`; Plan A Task 16 Interfaces: `testutil.Service(t, opts ...SvcOpt) (*service.Service, *clock.Fake)` (clock at `testutil.DefaultNow` = Wed 2026-10-28 10:00 Denver), `testutil.Zone`.
- Produces (Plan B's `server.Run` reaches `New` through the Task 11 hook and wraps it with recoverer/requestID/accessLog, arch §6.2 step 1):
  ```go
  type Options struct {
  	Svc       *service.Service
  	Clock     clock.Clock
  	Zone      civil.Zone
  	WeekStart time.Weekday
  	Listen    string       // bound web listener "host:port" (loopback)
  	Logger    *slog.Logger
  }
  func New(o Options) (http.Handler, error)  // error: Listen not host:port with a numeric port, template parse failure
  func newApp(o Options) (*app, error)        // New = newApp + a.handler()
  func (a *app) handler() http.Handler        // the chain below around a.mux
  const (
  	SessionCookie = "tr_session"
  	HTMXAsset     = "htmx-2.0.11.min.js"
  	HTMXSHA256    = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"
  	HTMXSize      = 52182
  	CSP           = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
  )
  func hostAllowlist(listen string) (map[string]bool, error) // lowercase "host:port" keys
  func bodyLimit(next http.Handler) http.Handler
  // export_test.go (package web): var StaticFS = staticFS; var HostAllowlist = hostAllowlist; var BodyLimit = bodyLimit
  ```
- Chain order inside `handler()` (outermost first): `securityHeaders` → `checkHost` → `routeClass` → `bodyLimit` → (Task 4: `session` → `csrf`) → `a.mux`. (planner decision: security headers wrap the Host check so its 400 carries them too.)

- [ ] **Step 1: Vendor htmx and record its checksum**

```bash
mkdir -p internal/web/static
curl -sSfL https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.11.tgz | tar -xzO package/dist/htmx.min.js > internal/web/static/htmx-2.0.11.min.js
(cd internal/web/static && sha256sum htmx-2.0.11.min.js > SHA256SUMS && cat SHA256SUMS && wc -c htmx-2.0.11.min.js)
```
Expected: `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717  htmx-2.0.11.min.js` and `52182`. Any other value: stop and report. Create `static/app.css` mobile-first: system font stack, max-width content column, nav wrapping at narrow widths, buttons ≥ 2.75rem tall, one `@media (min-width: 48rem)` breakpoint; no `url()`, no `@import`.

- [ ] **Step 2: Write the failing tests** (package `web_test`)

`static_test.go`:
```go
func TestHTMXAssetPinned(t *testing.T)
// b := web.StaticFS.ReadFile("static/" + web.HTMXAsset): len == web.HTMXSize; hex(sha256(b)) == web.HTMXSHA256 (64 hex);
// StaticFS "static/SHA256SUMS" == web.HTMXSHA256 + "  " + web.HTMXAsset + "\n"
func TestStaticServing(t *testing.T) // full handler, Host "127.0.0.1:7878":
// GET /static/htmx-2.0.11.min.js → 200, body == embedded bytes,
//   Cache-Control "public, max-age=31536000, immutable", Content-Type "text/javascript; charset=utf-8"
// GET /static/app.css → 200, Content-Type "text/css; charset=utf-8", same Cache-Control
// GET /static/SHA256SUMS, /static/, /static/nope.js → 404; GET /static/../web.go → not 200 (the mux cleans it to a redirect), body never Go source
```
`headers_test.go` (helper `newHandler(t, listen string) http.Handler` = `web.New(web.Options{Svc, Clock from testutil.Service(t), Zone: testutil.Zone, WeekStart: time.Monday, Listen: listen, Logger: slog.New(slog.DiscardHandler)})`; requests via `httptest.NewRequestWithContext(t.Context(), …)` with `req.Host` set):
```go
func TestHostAllowlist(t *testing.T) // GET /healthz, table (listen, Host, status):
// "127.0.0.1:7878": "127.0.0.1:7878"→200, "localhost:7878"→200, "LOCALHOST:7878"→200,
//   "127.0.0.1:7879"→400, "evil.example:7878"→400, "127.0.0.1"→400, "localhost"→400, ""→400
// "[::1]:7878": "[::1]:7878"→200, "localhost:7878"→200, "127.0.0.1:7878"→400
// "127.0.0.2:7878": "127.0.0.2:7878"→200, "localhost:7878"→400
func TestNewRejectsBadListen(t *testing.T) // "", "7878", "127.0.0.1", "127.0.0.1:http" → error
func TestSecurityHeadersOnEveryResponse(t *testing.T)
// GET /healthz, GET /, GET /static/app.css, GET /nope, GET /api/v1/tasks and a GET with Host "evil.example:7878":
// CSP == web.CSP; X-Content-Type-Options "nosniff"; Referrer-Policy "no-referrer";
// Cross-Origin-Opener-Policy "same-origin"; Permissions-Policy "camera=(), geolocation=(), microphone=()"
func TestSCP04_WebListenerRefusesAPI(t *testing.T) // catalog SCP-04, web half (socket half: Plan B Task 11)
// GET and POST /api/v1/tasks, GET /api/v1/whoami, GET /api/v1, each with "Authorization: Bearer tkr_1_<43 x 'A'>":
// 401, Content-Type "application/problem+json", Cache-Control "no-store", JSON body
// {"type":"urn:taskronin:problem:unauthenticated","title":"Unauthorized","status":401,"code":"unauthenticated",…}
func TestRouteClass(t *testing.T)
// GET / → 303 Location "/ui/"; GET /healthz → 200 body "ok", Content-Type "text/plain; charset=utf-8";
// GET /favicon.ico, /metrics, /uix → 404
func TestBodyLimit(t *testing.T) // web.BodyLimit(stub) where stub writes 204 and records r.PostForm:
// POST form body of 1<<20+1 bytes → 413, stub not called; POST Content-Type "application/json" → 415;
// POST "application/x-www-form-urlencoded; charset=UTF-8" body "a=1" → 204 and stub saw a=1; GET passes untouched
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/web -race`
Expected: FAIL (undefined: `web.New`, `web.StaticFS`, …).

- [ ] **Step 4: Implement**

- `web.go`: `//go:embed static` → `staticFS embed.FS` (Task 3 adds `//go:embed templates` → `templateFS`); `Options`, constants, `newApp` (validates `Listen` via `net.SplitHostPort` + `strconv.ParseUint(port, 10, 16)`, builds the Host and Origin allowlists and `a.mux = a.routes()`), `New`, `handler()`.
- `headers.go`:
  - `hostAllowlist(listen)`: `{lower(listen)}` plus `"localhost:"+port` when the host is `127.0.0.1` or `::1`. `checkHost`: `allowed[strings.ToLower(r.Host)]` else 400 plain text "bad host" (AIDEV-NOTE: DNS-rebinding guard; derived from web.listen, arch §6.2).
  - `securityHeaders`: sets the five headers of the Global Constraints before calling next.
  - `routeClass`: deletes `Authorization`; path `/api/v1` or prefix `/api/v1/` → 401 problem JSON (`detail` "the API is served only on the unix socket", `request_id` = the `X-Request-ID` response header set by the server's requestID middleware, else ""); allowed: `/`, `/healthz`, `/ui`, prefix `/ui/`, prefix `/static/`; anything else 404.
  - `bodyLimit`: POST only: media type (`mime.ParseMediaType`) must be `application/x-www-form-urlencoded` else 415; `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)`; then `r.ParseForm()` eagerly — `*http.MaxBytesError` → 413, other parse error → 400 (plain text); later middleware and handlers read `r.PostForm`.
- `routes.go`: `func (a *app) routes() *http.ServeMux` registering `GET /{$}` (303 `/ui/`), `GET /healthz` (`ok`), `GET /static/{name}` (allowlist: `HTMXAsset` → `text/javascript; charset=utf-8`, `app.css` → `text/css; charset=utf-8`, set explicitly so host `mime.types` files cannot change them; `http.ServeContent` with the embedded bytes, zero modtime, the immutable `Cache-Control`). Later tasks add routes here; page routes use `GET /ui/{$}`-style exact patterns, never the subtree `/ui/`.
- `export_test.go`: the three seams in Interfaces.

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/web -race && make assets-verify`
Expected: PASS; `htmx-2.0.11.min.js: OK`.

- [ ] **Step 6: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): handler skeleton, vendored htmx 2.0.11, host/header/route-class middleware

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Page rendering, error pages, form decoding, test harness

**Files:**
- Create: `internal/web/render.go`, `internal/web/forms.go`, `internal/web/templates/layout.html`, `internal/web/templates/partials.html`, `internal/web/templates/error.html`, `internal/web/helpers_test.go`, `internal/web/render_test.go`, `internal/web/forms_test.go`, `internal/web/templates_test.go`
- Modify: `internal/web/web.go` (`//go:embed templates` → `templateFS`; `newApp` parses templates), `internal/web/export_test.go`

**Interfaces:**
- Consumes: Plan A Task 5 Interfaces: `domain.Error{Code; Detail; Fields; Conflicts; Current; RetryAfter; Err}`, `domain.AsError`, `domain.Errorf`, `domain.FieldError`, `domain.RevertConflict`, `domain.Expect`, `domain.Generation`, `domain.Version`, `domain.Actor`, `domain.Moment`, `domain.BlockSpan`, `domain.OpID`, `domain.FormatETag(g, v) string`, `domain.ParseETag(s) (domain.Expect, error)` (strict; else `*Error` `CodeMalformed`), and the 24 `domain.Code` identifiers of A§2.8: `CodeMalformed`, `CodeUnauthenticated`, `CodeForbidden`, `CodePeerRejected`, `CodePasswordRequired`, `CodePasswordInvalid`, `CodeNotFound`, `CodeDeleted`, `CodeConflict`, `CodeRevertConflict`, `CodeNotRevertible`, `CodePrecondition`, `CodeTooLarge`, `CodeUnsupportedMedia`, `CodeValidation`, `CodeIdemMismatch`, `CodeNothingToUndo`, `CodeNothingToRedo`, `CodePreconditionReq`, `CodeIdemRequired`, `CodeGenerationMismatch`, `CodeRateLimited`, `CodeInternal`, `CodeBusy`. Plan A Task 16: `service.Meta{Actor; Idem; Gen}`, `service.Idem{Key, Method, Route string; Body []byte}`, `(*service.Service).Generation() domain.Generation`, `testutil.Service`, `testutil.Owner()`. Plan A Task 6: `datespec.Parse`, `ParseSpan`, `ParseClock`, `Ref{Today; WeekStart}`, `Past`, `Future`. Task 1 `markdown.Render`; `buildinfo.Version`; `textsafe.SanitizeLine`.
- Produces (used by Tasks 4–10):
  ```go
  type sessionInfo struct{ Actor domain.Actor; CSRF, Cookie string }
  func withSession(ctx context.Context, s sessionInfo) context.Context
  func sessionFrom(ctx context.Context) (sessionInfo, bool)
  type page struct {
  	Title, Tab string            // Tab highlights nav: today|triage|backlog|upcoming|log|report|search|ai
  	CSRF       string            // sessionFrom(...).CSRF; "" when logged out (nav hidden)
  	Gen        domain.Generation // a.o.Svc.Generation(); forms' _gen
  	Return     string            // GET: r.URL.RequestURI(); POST: safeReturn(_return)
  	Today      civil.Date        // Zone.DateOf(Clock.Now())
  	Version    string            // buildinfo.Version (app.css?v= cache-buster)
  	Data       any
  	Err        *domain.Error
  	Resubmit   *resubmit         // error pages for POSTs: the submitted input, editable
  }
  type resubmit struct{ Action string; Keep url.Values; Fields []resubmitField } // Keep → hidden inputs
  type resubmitField struct{ Name, Value string; Long bool }                   // Long → textarea
  func (a *app) newPage(r *http.Request, title, tab string, data any) page
  func (a *app) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, p page)
  func (a *app) renderError(w http.ResponseWriter, r *http.Request, err error)
  func statusFor(c domain.Code) int
  func (a *app) expect(form url.Values, key string) (domain.Expect, error) // "" → CodePreconditionReq; else domain.ParseETag
  func safeReturn(raw string) string
  func idemBody(form url.Values) []byte
  func moreURL(u *url.URL, cursor string) string // u's path + all query params with "cursor" replaced (pagination keeps filters)
  func (a *app) meta(r *http.Request) (service.Meta, error)       // reads r.PostForm (parsed by bodyLimit)
  func formInt(form url.Values, key string) (int64, error)        // "42" or "#42"; else CodeMalformed{Field: key}
  func pathInt(r *http.Request, name string) (int64, error)       // r.PathValue; else CodeNotFound
  func (a *app) ref() datespec.Ref                                // {Zone.DateOf(Clock.Now()), o.WeekStart}
  func (a *app) formMoment(form url.Values, key string, m datespec.Mode) (*domain.Moment, error) // "" → nil
  func (a *app) formSpan(form url.Values, key string) (domain.BlockSpan, error)                  // Future
  func (a *app) formDate(form url.Values, key string, m datespec.Mode) (civil.Date, error)      // "" → zero Date
  func formMinute(form url.Values, key string) (sql.Null[civil.Minute], error)                  // "" → !Valid
  func splitTags(s string) []string // split on commas and whitespace, empties dropped
  ```
  `export_test.go` gains: `type App = app`, `type SessionInfo = sessionInfo`, `var NewApp = newApp`, `TemplateFS = templateFS`, `StatusFor = statusFor`, `SafeReturn = safeReturn`, `IdemBody = idemBody`, `MoreURL = moreURL`, `FormInt = formInt`, `FormMinute = formMinute`, `SplitTags = splitTags`, `WithSession = withSession`, and thin methods `(*App).Handler`, `RenderError`, `Meta`, `Expect`, `FormMoment`, `FormSpan`, `FormDate` delegating to the unexported ones.
  Test harness (`helpers_test.go`, package `web_test`):
  ```go
  const testListen = "127.0.0.1:7878"
  type testApp struct{ t *testing.T; App *web.App; H http.Handler; Svc *service.Service; Clock *clock.Fake; Logs *bytes.Buffer; cookies map[string]string }
  func newTestApp(t *testing.T) *testApp // testutil.Service (clock at DefaultNow); Logs captures slog text output
  func (ta *testApp) get(path string, hdr ...string) *httptest.ResponseRecorder            // Host testListen, stored cookies; hdr = key,value pairs ("" deletes)
  func (ta *testApp) post(path string, form url.Values, hdr ...string) *httptest.ResponseRecorder // + form Content-Type, Origin "http://127.0.0.1:7878" unless overridden; stores Set-Cookie values
  func scrapeForms(t *testing.T, body, action string) []url.Values // golang.org/x/net/html: every <form action=action>, in page order
  func scrapeForm(t *testing.T, body, action string) url.Values     // first of scrapeForms; fails the test if none
  func parseHTML(t *testing.T, body string) *html.Node
  func assertInert(t *testing.T, body string) // parsed page: the only <script> is the htmx one; no attribute name starts with "on";
  	// no href/src/action/formaction value starting (case-insensitive, trimmed) with "javascript:" or "data:"; no <img>, <iframe>, <object>
  ```
- Decode errors are `*domain.Error`: datespec/enum/clock problems → `CodeValidation` with one `FieldError{Field: key}`; ids, list shapes → `CodeMalformed`.
- Template conventions: every mutating form is `<form method="post" action="…">` containing `{{template "hidden" $}}` (emits `_csrf`, `_gen`, `_nonce` via `{{nonce}}`, `_return`) — except the login form and the resubmit form (which emits `{{template "csrfnonce" $}}` plus its `Keep` values); fixed actions are separate forms (Form contract); `<textarea name="…">` is followed by a newline before the value (the HTML parser drops one leading LF).
- FuncMap: `nonce() (string, error)` (16 bytes `crypto/rand`, lowercase hex), `etag` = `domain.FormatETag`, `md` = `markdown.Render`, `day(civil.Date) string` ("Mon 2006-01-02"), `hhmm(any) string` (`civil.Minute`/`civil.EndMinute` → "HH:MM", 1440 → "24:00"), `wall(time.Time) string` ("Mon 2006-01-02 15:04" in `o.Zone`), `short(domain.OpID) string` (first 8 hex), `jsontext(json.RawMessage) string` (`string(b)`, "" for nil — `html/template` would otherwise print a `[]byte` as decimal numbers).

- [ ] **Step 1: Write the failing tests** (package `web_test`)

`forms_test.go`:
```go
func TestExpect(t *testing.T) // the codec itself is Plan A's (domain tests)
// Expect(form{etag: `"0123456789abcdef-3"`}, "etag") → Expect{Gen: "0123456789abcdef", Version: 3};
// form{} and etag "" → CodePreconditionReq; `W/"0123456789abcdef-3"`, `*` → CodeMalformed
func TestSafeReturn(t *testing.T)
// kept: "/ui/", "/ui/backlog", "/ui/log?from=2026-10-20&to=2026-10-28", "/ui/tasks/42#entries"
// → "/ui/": "", "https://evil.example/", "//evil.example/ui/", "/\\evil.example", "javascript:alert(1)",
//   "/api/v1/tasks", "/ui/../api/v1/tasks", "/static/app.css", "/ui/\r\nSet-Cookie: x=1", "ui/backlog"
func TestIdemBody(t *testing.T)
// form{_csrf:"x", _nonce:"n", b:"2", a:"1", tags:["z","y"], _gen:"g"} → "_gen=g&a=1&b=2&tags=z&tags=y"
func TestMoreURL(t *testing.T)
// /ui/log?from=2026-10-20&to=2026-10-21&include_deleted=1&cursor=old, "abc" →
// path /ui/log with query from, to, include_deleted unchanged and cursor=abc; /ui/backlog, "x" → "/ui/backlog?cursor=x"
func TestMeta(t *testing.T) // POST /ui/tasks form {_nonce: 32 hex, _gen: "00000000000000aa", _csrf: "x", _return: "/ui/", title: "a"},
// ctx WithSession(SessionInfo{Actor: webOwner}) → Meta{Actor: webOwner, Gen: "00000000000000aa",
//   Idem{Key: nonce, Method: "POST", Route: "POST /ui/tasks", Body: "_gen=00000000000000aa&_return=%2Fui%2F&title=a"}}
func TestFormDecoders(t *testing.T) // clock Wed 2026-10-28 10:00 Denver, WeekStart Monday
// FormMoment("fri 9:00", Future) → {2026-10-30, 09:00}; "" → nil; "someday" → CodeValidation Fields[0].Field == key
// FormSpan("13:00-15:00") → {2026-10-28, 13:00, 15:00}; FormSpan("fri 23:00-24:00") → end 1440
// FormDate("yesterday", Past) → 2026-10-27; "" → zero; "2026-10-27 09:00" → CodeValidation ("date only")
// FormMinute("9:05") → {545, true}; "" → !Valid; "24:00" → CodeValidation
// FormInt "42" and "#42" → 42; "0", "-1", "4x", "" → CodeMalformed
// SplitTags("a, b  c,,") → ["a","b","c"]
```
`render_test.go`:
```go
func TestStatusForEveryCode(t *testing.T) // A§2.8, by identifier:
// CodeMalformed 400; CodeUnauthenticated 401; CodeForbidden, CodePeerRejected, CodePasswordRequired, CodePasswordInvalid 403;
// CodeNotFound 404; CodeDeleted, CodeConflict, CodeRevertConflict, CodeNotRevertible, CodeNothingToUndo, CodeNothingToRedo 409;
// CodePrecondition, CodeGenerationMismatch 412; CodeTooLarge 413; CodeUnsupportedMedia 415;
// CodeValidation, CodeIdemMismatch 422; CodePreconditionReq, CodeIdemRequired 428;
// CodeRateLimited 429; CodeInternal 500; CodeBusy 503; domain.Code("zzz") → 500
func TestRenderErrorStatus(t *testing.T)
// validation + FieldError{"due","bad date"} on a GET → 422, Content-Type "text/html; charset=utf-8", Cache-Control "no-store",
//   body contains `data-code="validation"` and "bad date"
// internal with Err "secret sauce" → 500; body lacks "secret sauce"; ta.Logs contains "secret sauce"
// rate_limited RetryAfter 2500ms → header Retry-After "3"; a plain error (errors.New) → 500 internal
// revert_conflict with one RevertConflict → body lists its short op id and kind
func TestRenderErrorHTMX(t *testing.T) // same request + "HX-Request: true", precondition_failed:
// status 200; HX-Push-Url "false"; HX-Retarget "body"; HX-Reswap "innerHTML";
// body contains `data-code="precondition_failed"` and "changed elsewhere"; a link to the request's safe _return
func TestRenderErrorKeepsInput(t *testing.T) // POST /ui/tasks with session; form {title: "T", notes: "line1\nline2",
// due: "someday", etag: `"0123456789abcdef-3"`, _gen: "0123456789abcdef", _return: "/ui/triage", _csrf: tok, _nonce: "n…", password: "pw"},
// error validation{due} → 422; scrapeForm(body, "/ui/tasks"): title "T", notes "line1\nline2", due "someday", etag, _gen, _return kept,
// _csrf == tok, _nonce 32 hex ≠ the submitted one, no "password" key; body never contains "pw"
```
`templates_test.go`:
```go
func TestTemplatesLint(t *testing.T) // walks web.TemplateFS (keeps covering templates added later):
// no "<style", no "style=", no match of (?i)\son[a-z]+\s*=, no "hx-on", no "javascript:";
// every "<script" is exactly `<script src="/static/htmx-2.0.11.min.js"></script>` and only in layout.html;
// layout.html contains the htmx-config <meta> verbatim, `hx-boost="true"`, `hx-headers='{"X-CSRF-Token":"{{.CSRF}}"}'`,
// `href="/static/app.css?v={{.Version}}"`; every `<form method="post"` contains `{{template "hidden" $}}`,
// except login.html (Task 4) and the resubmit form in error.html (`{{template "csrfnonce" $}}`)
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race`
Expected: FAIL (undefined: `web.StatusFor`, `web.NewApp`, `newTestApp`, …).

- [ ] **Step 3: Implement**

- `render.go`: templates parsed once in `newApp`: for each `templates/<name>.html` other than `layout.html`/`partials.html`, `template.New(name).Funcs(fm).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")`; pages `{{define "content"}}`; execute `"layout"` into a buffer, then write `Content-Type: text/html; charset=utf-8`, `Cache-Control: no-store` and the status; an execution error is logged and becomes a plain 500.
- `renderError` (AIDEV-NOTE: the fixed htmx config does not swap 4xx/5xx bodies, so `HX-Request: true` gets the error page with status 200 plus `HX-Push-Url: false`, `HX-Retarget: body`, `HX-Reswap: innerHTML`; other requests get `statusFor`): `domain.AsError`; `CodeInternal` → log `textsafe.SanitizeLine(Err.Error())` with the path and `X-Request-ID`, show "internal error"; `RetryAfter > 0` → `Retry-After` = ceil seconds; `CodePrecondition`/`CodeGenerationMismatch` → "This changed elsewhere since the page was loaded — reload" + link to `page.Return`; field errors and revert conflicts listed.
- Resubmit (AIDEV-NOTE: never lose typed text): for a POST under `/ui/` other than `/ui/login` and `/ui/logout`, with any error except `CodeUnauthenticated`/`CodeForbidden`, set `page.Resubmit{Action: r.URL.Path}`: `Keep` = the submitted values of `_gen`, `_return`, `etag`, `task_etag`, `id`, `how`, `remove`, `remove_entry`, `interrupt`, `through`, `outcome` (hidden inputs; the original `_gen`/`etag` keep staleness detection intact); `Fields` = every other submitted key except `_csrf`, `_nonce`, `password`, in sorted key order, `Long` for `notes`. `error.html` renders it as a form (`{{template "csrfnonce" $}}` = current session `_csrf` + fresh `_nonce`; `Keep` as hidden inputs; `Fields` as text inputs/textarea) under "Your input".
- `forms.go`: `expect` delegates to `domain.ParseETag`; `safeReturn` (AIDEV-NOTE: open-redirect guard): reject any CR/LF/backslash, `url.Parse` must give empty scheme and host, raw must start with `/ui/`, and `path.Clean` of the path must still start with `/ui/` (or equal `/ui`); else `/ui/`. `idemBody` (AIDEV-NOTE: canonical web idempotency body, arch §7.5): clone, delete `_csrf`/`_nonce`, `Encode()`. `meta`: actor from `sessionFrom`, `Idem{Key: form "_nonce", Method: "POST", Route: "POST " + r.URL.Path, Body: idemBody(r.PostForm)}`, `Gen: domain.Generation(form "_gen")` — missing/invalid nonce or gen are reported by `service`, never pre-checked here.
- `layout.html`: doctype, `lang="en"`, charset, viewport, the htmx-config meta, `<title>{{.Title}} · Taskronin</title>`, stylesheet link, the htmx script; `<body hx-boost="true" hx-headers='{"X-CSRF-Token":"{{.CSRF}}"}'>`; nav (Today `/ui/`, Triage, Backlog, Upcoming, Log, Report, Search, AI changes, and the C24 logout form) only when `.CSRF` is set; `<main>{{template "content" .}}</main>`. `partials.html`: `hidden`, `csrfnonce`. `error.html`: `<section class="error" data-code="{{.Err.Code}}">` + the resubmit form.
- `helpers_test.go`: `newTestApp` builds `web.NewApp(web.Options{…, Listen: testListen, Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})` and `H = App.Handler()`; `scrapeForms` walks the parsed DOM: for each `form` whose `action` equals `action`, collect `input` name/value (checkbox/radio only when `checked`), `textarea` text, the `selected` option of each `select`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): templates, error pages that keep input, form decoding and test harness

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Sessions, login/logout, CSRF and Origin checks

**Files:**
- Create: `internal/web/session.go`, `internal/web/csrf.go`, `internal/web/h_auth.go`, `internal/web/h_views.go` (home stub), `internal/web/templates/login.html`, `internal/web/templates/today.html` (stub), `internal/web/session_test.go`, `internal/web/csrf_test.go`
- Modify: `internal/web/web.go` (chain gains `session` → `csrf`), `internal/web/routes.go`, `internal/web/helpers_test.go` (add `login`, `submit`)

**Interfaces:**
- Consumes: Plan A Task 35 Interfaces: `(*service.Service).Login(ctx, in service.LoginIn) (service.Session, error)` with `LoginIn{RemoteKey, Password, OldCookie string}` (deletes the `OldCookie` session itself: regeneration) and `Session{CookieValue, CSRFToken string; ExpiresAt time.Time}`; `Authenticate(ctx, cookie string) (service.WebAuth, error)` with `WebAuth{Actor domain.Actor; CSRFToken string}` (idle 7 d / absolute 30 d → `CodeUnauthenticated`); `Logout(ctx, cookie string) error`. Plan A Task 16: `testutil.Password`. Task 3: `withSession`, `renderPage`, `renderError`, harness.
- Produces:
  ```go
  func (a *app) session(next http.Handler) http.Handler // all /ui/* except GET/POST /ui/login
  func (a *app) checkOrigin(r *http.Request) bool       // Origin/Sec-Fetch-Site rule (Global Constraints)
  func (a *app) csrf(next http.Handler) http.Handler    // every POST except POST /ui/login
  func clientIP(r *http.Request) string                 // netip.ParseAddrPort(r.RemoteAddr).Addr().String(); fallback r.RemoteAddr
  func (a *app) today(w http.ResponseWriter, r *http.Request) // stub: renders today.html (just "<h1>Today</h1>"), no service call; Task 5 replaces it
  // harness additions (package web_test)
  func (ta *testApp) login() *testApp // POST /ui/login with testutil.Password; asserts 303; stores the cookie
  func (ta *testApp) submit(pagePath, action string, overrides url.Values) *httptest.ResponseRecorder
  	// GET pagePath (asserts 200), scrapeForm(action), apply overrides (Set; an empty slice deletes the key), POST to action
  ```
- Routes: `GET /ui/login`, `POST /ui/login` (C25), `POST /ui/logout` (C24), `GET /ui/{$}` (stub until Task 5).
- Catalog split: SCP-05's cookie-on-`/api/v1` half is Plan B Task 11; the bearer-on-`/ui` half is here.

- [ ] **Step 1: Write the failing tests** (package `web_test`)

`session_test.go`:
```go
func TestLoginSuccess(t *testing.T)
// GET /ui/login → 200, contains `action="/ui/login"`, `hx-boost="false"`, `type="password"`, no nav
// POST /ui/login {password: testutil.Password}, Origin "http://127.0.0.1:7878" → 303 Location "/ui/";
// Set-Cookie parsed with http.ParseSetCookie: Name "tr_session", Path "/", HttpOnly, SameSite Lax, !Secure,
//   Expires == clock now + 30 d (to the second), Value matches ^[A-Za-z0-9_-]{43}$
// then GET /ui/ with the cookie → 200 with the nav (a "/ui/logout" form)
func TestLoginWrongPassword(t *testing.T) // → 403, `data-code="password_invalid"`, no Set-Cookie, body lacks the submitted password
func TestLoginCrossOrigin(t *testing.T)   // Origin "http://evil.example" → 403, no Set-Cookie; no Origin and no Sec-Fetch-Site → 403
func TestLoginRegeneratesSession(t *testing.T) // login → A; login again sending A → B ≠ A; GET /ui/ with A → 303 /ui/login; with B → 200
func TestSCP05_BearerOnUI(t *testing.T)   // catalog SCP-05, web half
// no cookie, "Authorization: Bearer tkr_1_<43 chars>": GET /ui/ → 303 Location "/ui/login";
// POST /ui/logout → 401, HX-Redirect "/ui/login"
func TestSessionIdleExpiry(t *testing.T)  // login; Clock.Advance(7*24h + time.Minute); GET /ui/ → 303 /ui/login
func TestLogout(t *testing.T)             // submit("/ui/", "/ui/logout", nil) → 303 "/ui/login", Set-Cookie tr_session MaxAge < 0; old cookie → 303
func TestIDM04_LoginNotLogged(t *testing.T) // catalog IDM-04, web half (end-to-end half: Plan B Task 35): after a failed and a successful
// login, ta.Logs contains neither password; neither response body contains them
func TestLoginBodyLimits(t *testing.T)    // POST /ui/login with 1<<20+1 bytes → 413; Content-Type application/json → 415
```
`csrf_test.go`, table over `POST /ui/logout`, each case on a fresh `login()`; base form = `scrapeForm(GET /ui/ body, "/ui/logout")`; a refused case leaves the session valid (a following `GET /ui/` → 200):
```go
func TestCSRF(t *testing.T)
// 1 scraped form, Origin http://127.0.0.1:7878 → 303          2 no _csrf, header X-CSRF-Token valid, Sec-Fetch-Site same-origin, no Origin → 303
// 3 Origin http://localhost:7878 → 303                         4 _csrf deleted → 403
// 5 wrong token → 403                                           6 token of another session → 403
// 7 Origin http://evil.example → 403                            8 Origin https://127.0.0.1:7878 → 403
// 9 Origin null → 403                                           10 neither Origin nor Sec-Fetch-Site → 403
// 11 Sec-Fetch-Site cross-site, no Origin → 403                 12 Origin ok + Sec-Fetch-Site same-site → 403
// 13 empty _csrf + valid header → 303 (form value empty → header used)
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'TestLogin|TestSCP05|TestSession|TestLogout|TestIDM04|TestCSRF'`
Expected: FAIL (404 on `/ui/login`, undefined `login`).

- [ ] **Step 3: Implement**

- `session.go` (AIDEV-NOTE: the only authentication for /ui; cookie never accepted on /api/v1): cookie `tr_session` → `Svc.Authenticate`; ok → `withSession(ctx, sessionInfo{wa.Actor, wa.CSRFToken, cookie})`; `CodeUnauthenticated` or no cookie → GET/HEAD `303 /ui/login`, other methods `401` + `HX-Redirect: /ui/login`; any other error → `renderError`. Cookie writer: `Path=/`, `HttpOnly`, `SameSite=Lax`, `Expires`; logout writes `MaxAge: -1`.
- `csrf.go` (AIDEV-NOTE, required by arch §1.4: both checks, constant-time compare, login exempt from the token only): `checkOrigin` per Global Constraints (`Origin` compared with `"http://" + host` for every Host-allowlist entry); `csrf` reads `r.PostForm` (parsed by `bodyLimit`), takes `_csrf` if non-empty else `X-CSRF-Token`, `subtle.ConstantTimeCompare` with `sessionInfo.CSRF`; failure → `renderError(domain.Errorf(domain.CodeForbidden, "cross-site request refused"))`.
- `h_auth.go`: `GET /ui/login` renders `login.html` (C25: `hx-boost="false"`, `autocomplete="current-password"`, no hidden inputs); `POST /ui/login`: `checkOrigin` else 403; `Svc.Login(ctx, LoginIn{RemoteKey: clientIP(r), Password: r.PostFormValue("password"), OldCookie: <tr_session cookie or "">})`; ok → set cookie, `303 /ui/`; error → re-render `login.html` with the error and its status (`password_invalid` 403, `rate_limited` 429 + `Retry-After`); never echo the password. `POST /ui/logout`: `Svc.Logout(ctx, cookie)` → clear cookie → `303 /ui/login`.
- `h_views.go`: the `today` stub; `routes.go`: `GET /ui/{$}` → `a.today`.
- `web.go`: chain `securityHeaders → checkHost → routeClass → bodyLimit → session → csrf → mux`, `session`/`csrf` applied to `/ui/` paths only, with the login exemptions above.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): session login/logout, CSRF token and Origin checks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: List views — Today, Triage, Backlog, Upcoming, Log

**Files:**
- Create: `internal/web/templates/{triage,backlog,upcoming,log}.html`, `internal/web/views_test.go`
- Modify: `internal/web/h_views.go` (real `today` + four handlers), `internal/web/templates/today.html`, `internal/web/routes.go`, `internal/web/templates/partials.html` (`taskrow`, `entryrow`, `blockrow`, `addtask`, `logentry`), `internal/web/helpers_test.go` (seeding helpers)

**Interfaces:**
- Consumes: Plan A Task 32 Interfaces: `Today(ctx, a) (service.TodayView, error)`, `Upcoming(ctx, a) (service.UpcomingView, error)`, `ListTasks(ctx, a, service.ListTasksIn) (service.Page[domain.TaskView], error)`, `ListEntries(ctx, a, service.ListEntriesIn) (service.Page[domain.EntryView], error)`, `service.Page[T]{Items []T; NextCursor string}` (`""` = last page), `TodayView`/`Banner`/`BackupHealth` fields. Plan A Task 5 Interfaces / A§2.6: `domain.TaskView` (`Reasons []TriageRow`, `Tags`, `View`, `Deferred`, `DuePhase`, `DueSoon`, `SlipsLifetime`, `SlipsSinceTriage`; optional fields are `sql.Null[T]` — templates test `.DueDate.Valid` and print `.DueDate.V`), `domain.EntryView` (`TaskTitle`), `domain.BlockView` (`Span.Start`, `Span.End`, `Current`, `Started`, `Ended`, `NeedsReblock`, `TaskTitle`, `TaskVersion`), `domain.Interruption`. Plan A Task 33: `ListOps(ctx, a, service.ListOpsIn) (service.Page[service.OpSummary], error)` (newest first; tests). Seeding (each signature in that Plan A task's Interfaces): `AddTask` (16), `Place` (19), `SetDue` (20), `AddEntry` (21), `Block` (22), `DeleteEntry` (25), `SetFocus` (26), `Defer` (27), `Complete` (28). Plan A Task 16: `testutil.Meta(a domain.Actor, gen domain.Generation) service.Meta`, `testutil.Owner()`, `testutil.AI()`, `testutil.Local`. Tasks 3–4 helpers.
- Produces: routes `GET /ui/{$}` (replacing the stub), `GET /ui/triage`, `GET /ui/backlog`, `GET /ui/upcoming`, `GET /ui/log`; partials `taskrow` (title link `/ui/tasks/{id}`, `#id`, reasons, slips `L/S`, due/defer, tags, project, priority, `data-task="{id}"`), `entryrow` (`data-entry="{id}"`; C14–C16 forms), `blockrow` (`data-block="{id}"`; C17/C18 forms), `addtask` (C1), `logentry` (C13); harness `func (ta *testApp) meta() service.Meta` (`testutil.Meta(testutil.Owner(), ta.Svc.Generation())`), `addTask(in service.AddTaskIn) domain.TaskID`, `task(id domain.TaskID) domain.TaskDetail`, `opKinds() []string` (all ops oldest first: pages through `ListOps` and reverses).
- Page content (D§5.4, arch §11.2); each list with `NextCursor != ""` renders a "More" link `moreURL(r.URL, NextCursor)` (Limit 100; filters survive):
  - **Today** (`Today`): each `Banners` entry as `<div class="banner" data-banner="{{.Kind}}">`; an `ai_changes` banner linking `/ui/ai-changes` (synthesized when `AIChanges > 0` and `Banners` has none); backup warning when unhealthy; focus task (C9 complete, C20 clear); current block highlighted; today's blocks (`blockrow`; `NeedsReblock` shows "reblock?"); triage count + top 5 linking `/ui/triage`; overdue and due-soon only when non-empty; today's entries; interruptions; `addtask` (C1), `logentry` (C13, `task_id` prefilled), C20 set-focus form.
  - **Triage** (`ListTasks{View: triage}`): `addtask` (C1); per task C5 (keep/top/bottom/before/after), C6, C8, C9, C11 drop (`hx-confirm`), detail link; C12 accept-new form (only if ≥ 1 `{new}`-only task).
  - **Backlog** (`ListTasks{View: backlog}`): rank order; "up" = C5 `before` the previous row, "down" = C5 `after` the next row (absent on the first/last row of the page), top, bottom, C7, C9, C6, C8, C20 (hidden `task_id`), C11 drop (`hx-confirm`).
  - **Upcoming** (`Upcoming`): deferred (defer moment via `day`/`hhmm`; C6 change and C6 "keep" = hidden `until=none`), due soon (C4).
  - **Log** (`ListEntries`): `from`/`to` query params (P datespecs; default `today−13 … today`), `include_deleted=1`; grouped by `work_date` desc; each `entryrow`: time, title, task link or "standalone", notes via `md`, C14/C15/C16 (deleted → undelete); `logentry` (C13).

- [ ] **Step 1: Write the failing tests** in `views_test.go` (clock at `testutil.DefaultNow`; seed through `ta.Svc` with `ta.meta()`)

```go
func TestTodayPage(t *testing.T)
// seed #1 "Write report" (place top) + SetFocus(#1) + Block(#1, today 13:00-15:00) + AddEntry(#1, "Drafted intro");
//   #2 "Triage me" (no placement); testutil.AI() AddEntry(standalone)
// GET /ui/ → 200; contains `data-task="1"`, `data-block="1"`, "13:00", "15:00", "Drafted intro", triage count 1,
//   `data-banner="ai_changes"`; scrapeForm(body, "/ui/entries")["task_id"] == ["1"];
//   forms present: "/ui/tasks", "/ui/focus", "/ui/tasks/1/complete", "/ui/blocks/1/unblock"
func TestTodayReblockFormCarriesTaskETag(t *testing.T) // R4-7: ta.Clock.Set(Local("2026-10-28 07:00")) before any op; Block(#1, 08:00-09:00);
// Set 10:00 (ended, nothing logged → NeedsReblock): /ui/ shows "reblock?"; scrapeForm("/ui/blocks/1/reblock"):
// etag == domain.FormatETag(gen, block version), task_etag == domain.FormatETag(gen, task(1).Version)
func TestTriagePage(t *testing.T)
// #1 new, #2 placed bottom, #3 new → GET /ui/triage: `data-task="1"` before `data-task="3"`, no `data-task="2"`;
//   a "/ui/tasks" form with fields title, notes, due, tags, project, priority, place, ref, defer, span;
//   scrapeForms("/ui/tasks/1/place") has how values keep, top, bottom, before, after; forms "/ui/tasks/1/defer", "/ui/tasks/1/block",
//   "/ui/tasks/1/complete", "/ui/tasks/1/drop" (with `hx-confirm`); scrapeForm("/ui/triage/accept-new"):
//   id ["1","3"], etag [domain.FormatETag(gen, v1), domain.FormatETag(gen, v3)]
func TestBacklogPage(t *testing.T)
// #1,#2,#3 placed bottom → rows in order; scrapeForms("/ui/tasks/2/place") includes {how: before, ref: 1} and {how: after, ref: 3};
//   no "/ui/tasks/1/place" form has how=before, no "/ui/tasks/3/place" form has how=after
func TestUpcomingPage(t *testing.T) // #1 Defer("fri 9:00") → "Fri 2026-10-30" and "09:00", scrapeForms("/ui/tasks/1/defer") includes until=none;
// #2 SetDue(tomorrow) → under due soon with form "/ui/tasks/2/due"
func TestLogPage(t *testing.T)
// entries 10-28 (#1), 10-27 (#1), 10-27 standalone → default /ui/log shows "Wed 2026-10-28" before "Tue 2026-10-27";
// ?from=2026-10-27&to=2026-10-27 → only 10-27 entries; ?from=someday → 422 `data-code="validation"` naming "from";
// forms "/ui/entries/1/edit", "/ui/entries/1/relink", "/ui/entries/1/delete", "/ui/entries"
func TestListPaging(t *testing.T) // pagination keeps filters: 101 entries dated 10-20 on #1, one more on 10-20 then DeleteEntry'd, one on 10-28;
// /ui/log?from=2026-10-20&to=2026-10-20&include_deleted=1 → 100 entry rows; its More link has from, to, include_deleted unchanged + cursor;
// following it → the remaining 2 rows (one with an "undelete" form); no page shows the 10-28 entry
func TestViewsEscapeHostileTitle(t *testing.T) // Review Focus 4
// title `<script>alert(1)</script>"' onmouseover=x` on a triage task, a backlog task, a deferred task, and as an entry title:
// each of /ui/, /ui/triage, /ui/backlog, /ui/upcoming, /ui/log passes assertInert and its text content contains the literal title
func TestGETsPerformNoUserOps(t *testing.T)
// seed; before := ta.opKinds(); GET all five pages twice; every op after `before` has kind prefix "system."
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'Page|Paging|Hostile|NoUserOps|ReblockForm'`
Expected: FAIL (stub Today, 404 on the other pages).

- [ ] **Step 3: Implement** the five handlers in `h_views.go` (each: parse query → one service call with the session actor → `renderPage(…, 200, name, a.newPage(r, title, tab, view))`; decode errors → `renderError`), the templates, partials and seeding helpers.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS (including `TestTemplatesLint` over the new templates).

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): today, triage, backlog, upcoming and log views

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6a: Task detail and history views

**Files:**
- Create: `internal/web/h_task.go`, `internal/web/templates/{task,history}.html`, `internal/web/task_detail_test.go`
- Modify: `internal/web/routes.go`

**Interfaces:**
- Consumes: Plan A Task 16: `GetTask(ctx, a, id domain.TaskID) (domain.TaskDetail, error)` (`TaskDetail{TaskView; Entries []EntryView; Blocks []BlockView; Interruptions []Interruption}`; tombstoned tasks come back with `View == domain.ViewDeleted`). Plan A Task 33: `History(ctx, a, service.HistoryIn{TaskID; Limit; Cursor}) (service.Page[service.EventView], error)` (newest first) with `EventView{ID; Op OpSummary; Entity; EntityID; Action; Before, After json.RawMessage}`. Seeding: Plan A Tasks 16, 22, 28, 29 (`UpdateTask`, `Delete`). Tasks 3–5 helpers and partials.
- Produces: routes `GET /ui/tasks/{id}`, `GET /ui/tasks/{id}/history`.
  - **Task detail**: fields, reasons, slips, due/defer, project, priority, notes via `md`, tags with C3 forms; C2 edit form (title, notes textarea, project, priority select), C4, C6, C5, C8, C7, C9 (with `as`, `date`, `log`, `notes`) when open, C10 when done/dropped, C11 drop/delete when not deleted, C11 undelete when deleted (then no other mutating forms), C20; blocks (`blockrow`), entries (`entryrow`), interruptions, history link.
  - **History**: events newest first: `wall` time, op kind, actor, channel, entity/action, `Before`/`After` printed with `jsontext` inside `<pre>`; a C22 revert form (`hx-confirm`) on each op that is revertible and active.

- [ ] **Step 1: Write the failing tests** in `task_detail_test.go`

```go
func TestTaskDetailOpen(t *testing.T) // #1 notes "**bold** [x](https://example.com)", tags [db infra], project "infra.kc", due fri, one block, one entry:
// contains "<strong>bold</strong>" and an <a> with rel "noreferrer noopener"; forms "/ui/tasks/1/edit", "/ui/tasks/1/tags" (one add, one remove per tag),
// due, defer, place, block, triage, complete, drop, delete; no "/ui/tasks/1/reopen" or "/ui/tasks/1/undelete" form;
// scrapeForm("/ui/tasks/1/edit")["notes"] == [the raw notes]
func TestTaskDetailDoneAndDeleted(t *testing.T) // done → reopen form (remove_entry checkbox, hx-confirm), no complete form;
// deleted → 200 with a "deleted" marker and only the undelete form
func TestTaskDetailNotFound(t *testing.T)  // /ui/tasks/999 → 404 `data-code="not_found"`; /ui/tasks/abc → 404
func TestDetailEscapesAndRendersMarkdown(t *testing.T) // Review Focus 4: title `"><img src=x onerror=alert(1)>`,
// notes "<script>x</script> [c](javascript:alert(1)) **b**":
// /ui/tasks/1 passes assertInert; contains "<strong>b</strong>"; scrapeForm("/ui/tasks/1/edit") returns the raw title and the raw notes
// (the literal "javascript:alert(1)" is preserved as text); /ui/tasks/1/history passes assertInert and its <pre> text contains "onerror=alert(1)"
func TestHistoryPage(t *testing.T) // add → UpdateTask(title) → Complete: kinds "task.complete", "task.update", "task.add" in that order;
// a revert form "/ui/ops/<op_id>/revert" for the complete op; event JSON shown as text (e.g. `"title"`), never as a list of byte numbers
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'TaskDetail|DetailEscapes|HistoryPage'`
Expected: FAIL (404).

- [ ] **Step 3: Implement** both handlers in `h_task.go` (one service call each) and the templates.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): task detail and history views

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6b: Recap and report views

**Files:**
- Create: `internal/web/h_recap.go`, `internal/web/templates/{recap,report}.html`, `internal/web/recap_report_test.go`
- Modify: `internal/web/routes.go`

**Interfaces:**
- Consumes: Plan A Task 32: `Day(ctx, a, d civil.Date) (service.DayView, error)` (`DayView{Date; Blocks []domain.BlockView; Entries, Unlinked []domain.EntryView; Interruptions}`). Plan A Task 34: `Report(ctx, a, service.ReportIn{From, To civil.Date; Tag, Project string; Todo, Upcoming bool}) (service.Report, error)` with `Report{From, To; Tag, Project; Days []ReportDay{Date; Lines []ReportLine}; Tasks map[domain.TaskID]ReportTask{Title; Status; Deleted}; Todo *ReportTodo{Triage, Backlog []domain.TaskView}; Upcoming []domain.TaskView}` and `ReportLine{EntryID; Kind; Title; Notes; TaskID sql.Null[domain.TaskID]; Time sql.Null[civil.Minute]; Reopened}`; `From > To` → validation. Seeding: Plan A Tasks 16, 21, 22, 28. Tasks 3–5 helpers and partials.
- Produces: routes `GET /ui/recap`, `GET /ui/report`.
  - **Recap** (`date` P datespec, default today): `blockrow` with C19 outcome forms, C17/C18 for planned blocks; entries; unlinked entries each with C15; interruptions; `logentry` (C13) with `date` prefilled to the recap date.
  - **Report** (query `from`, `to` P datespecs, `tag`, `project`, `todo=1`, `upcoming=1`; default = the full current week from `Options.WeekStart`): a GET filter form; days ascending with `day` headings; `✓` for `complete` lines, " (reopened)" suffix; a task's first appearance `#ID Title`, later `#ID`; line notes via `md`; Triage/Backlog sections with `todo`, Upcoming with `upcoming`.

- [ ] **Step 1: Write the failing tests** in `recap_report_test.go`

```go
func TestRecapPage(t *testing.T) // ta.Clock.Set(Local("2026-10-27 10:00")) before any op; Block(#1, 13:00-15:00);
// Set 2026-10-28 10:00 (reconcile resolves it missed); standalone entry dated 10-27:
// /ui/recap?date=yesterday → scrapeForms("/ui/blocks/1/outcome") outcome values {worked, missed, auto}; form "/ui/entries/1/relink";
// scrapeForm("/ui/entries")["date"] == ["2026-10-27"]; /ui/recap → today's date heading; ?date=garbage → 422
func TestReportPage(t *testing.T) // entries Mon 10-26 and Tue 10-27 on #1, Complete(#1) today:
// /ui/report → "Mon 2026-10-26" before "Tue 2026-10-27", "✓", first "#1 <title>" then bare "#1";
// default range == ?from=2026-10-26&to=2026-11-01; todo=1 adds "Triage" and "Backlog"; from=2026-10-28&to=2026-10-26 → 422
func TestReportEscapes(t *testing.T) // entry title `<script>x</script>` and notes "[c](javascript:alert(1))" → /ui/report passes assertInert;
// text contains "<script>x</script>" as text
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'RecapPage|Report'`
Expected: FAIL (404).

- [ ] **Step 3: Implement** both handlers in `h_recap.go` (one service call each) and the templates.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): recap and report views

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6c: Search and AI-changes views

**Files:**
- Create: `internal/web/h_search.go`, `internal/web/templates/{search,aichanges}.html`, `internal/web/search_ai_test.go`
- Modify: `internal/web/routes.go`

**Interfaces:**
- Consumes: Plan A Task 34: `Search(ctx, a, service.SearchIn{Query, Kind string; Limit int; Cursor string}) (service.Page[service.SearchHit], error)` (`Kind` `""` = all; no terms → validation) with `SearchHit{Kind string; ID int64; TaskID sql.Null[domain.TaskID]; Title, Snippet string; Status domain.TaskStatus; WorkDate sql.Null[civil.Date]}`. Plan A Task 33: `ListAIChanges(ctx, a, service.ListAIChangesIn{IncludeReviewed bool; Limit int; Cursor string}) (service.AIChanges, error)` with `AIChanges{ReviewedThrough domain.OpSeq; Unreviewed int; Ops []OpSummary; NextCursor string}`, `OpSummary{Seq; OpID; Kind; Actor; Channel; At; State; Revertible; Via; TaskIDs; Effects}`; `MarkAIReviewed` (seeding). Plan A Task 16: `testutil.AI()`. Tasks 3–5 helpers.
- Produces: routes `GET /ui/search`, `GET /ui/ai-changes`.
  - **Search** (`q`, `kind` ∈ task|entry|all, default all): empty `q` → form only, **no service call**; hits link to `/ui/tasks/{id}` (entries: their task, or `/ui/log?from=D&to=D` when standalone); snippets as escaped text; More link via `moreURL`.
  - **AI changes** (`all=1` → `IncludeReviewed`): ops (seq, `wall` time, kind, actor, state, effects, task links), C22 per revertible active op, More link via `moreURL`, and when `Unreviewed > 0` the C23 form with `through` = the highest seq on this page.

- [ ] **Step 1: Write the failing tests** in `search_ai_test.go`

```go
func TestSearchPage(t *testing.T) // #1 "Set up keycloak" → /ui/search?q=keycloak links "/ui/tasks/1"; q="" → 200 with the search form and no results list;
// q="*" → 422 (no search terms); a task titled `<b onclick=x>keycloak</b>` → page passes assertInert
func TestAIChangesPage(t *testing.T) // testutil.AI() AddEntry(#1) → lists "token:ai", a revert form for it, scrapeForm("/ui/ai-changes/review")
// through == that op's seq; after MarkAIReviewed via the service: no review form; ?all=1 still lists the op
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'SearchPage|AIChangesPage'`
Expected: FAIL (404).

- [ ] **Step 3: Implement** both handlers in `h_search.go` (search with empty `q` makes no call; otherwise one) and the templates.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): search and AI-changes views

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Task capture and edit actions (C1–C4) and the effect flash

**Files:**
- Create: `internal/web/h_tasks.go`, `internal/web/flash.go`, `internal/web/tasks_capture_test.go`
- Modify: `internal/web/routes.go`, `internal/web/render.go` (`page.Flash []string`), `internal/web/templates/layout.html` (flash box), `internal/web/helpers_test.go` (keep `tr_flash` with the session cookie)

**Interfaces:**
- Consumes: Form contract rows C1–C4 and the service calls they name (Plan A Tasks 16, 20, 29); Plan A Task 16: `service.OpResult{OpID; Created; Replayed; NoChange; …; Effects []Effect; Warnings []string}`, `service.Effect{Kind EffectKind; TaskID; BlockID; EntryID; Reason; From, To string}`; Task 3 decoders, `meta`, `renderError`; Task 4 `submit`; Task 5 `addTask`, `task`, `opKinds`, `meta`.
- Produces:
  ```go
  // mutate: a.meta(r) → res, err := fn(m, r.PostForm) → on success setFlash(w, r, flashLines(res)) and 303 to safeReturn(_return)
  // (also for Replayed and NoChange); renderError on any error. Every POST handler in Tasks 7–10 is a mutate closure making one service call.
  func (a *app) mutate(w http.ResponseWriter, r *http.Request, fn func(m service.Meta, form url.Values) (*service.OpResult, error))
  // Flash = D§5.9 "every op echoes what it did" across the 303: cookie "tr_flash", Path=/ui/, HttpOnly, SameSite=Lax, MaxAge 60,
  // value base64url(text) + "." + base64url(HMAC-SHA256(key = session CSRF token, text)); ≤ 2 KiB (lines truncated).
  // renderPage reads + verifies + clears it into page.Flash (rendered escaped); a bad MAC is ignored.
  func flashLines(res *service.OpResult) []string // one line per effect: "#42 completed", "b7 slipped", "e9 entry logged",
  	// "#42 reason slipped added", "#42 retitled: <From> → <To>"; then each warning; NoChange → "no change"; Replayed → "already applied"
  func (a *app) setFlash(w http.ResponseWriter, r *http.Request, lines []string)
  ```
  Routes: C1 `POST /ui/tasks`, C2, C3, C4.

- [ ] **Step 1: Write the failing tests** in `tasks_capture_test.go` (all via `ta.login().submit(page, action, overrides)`)

```go
func TestAddTaskDefaultTriage(t *testing.T) // submit("/ui/triage", "/ui/tasks", {title: "Write report"}) → 303 Location "/ui/triage";
// task(1).Reasons == [new]; last opKind "task.add" with channel "web";
// the next GET /ui/triage (with tr_flash) shows "#1 created"; a second GET does not (cookie cleared)
func TestFlashTamperIgnored(t *testing.T) // tr_flash with a valid-looking value but a wrong MAC → page renders without a flash box
func TestAddTaskPlacements(t *testing.T) // table on a DB with #1 placed bottom; submit("/ui/", "/ui/tasks", {title, place, …}):
// top → View backlog, rank < #1's; bottom → rank > #1's; before ref "#1" → rank < #1's; after ref 1 → rank > #1's;
// now → Today focus is the new task; defer + defer "fri 9:00" → View upcoming, DeferDate 2026-10-30, DeferMin 540;
// block + span "13:00-15:00" → one planned block 2026-10-28 13:00–15:00, no reasons
func TestAddTaskFields(t *testing.T) // due "fri", tags "db, infra", project "infra.kc", priority "H" → DueDate 2026-10-30, Tags [db infra], Project, PriorityH
func TestAddTaskNotesCRLF(t *testing.T) // Review Focus 1: notes "line1\r\nline2\r\n" → stored Notes "line1\nline2\n" (D18)
func TestAddTaskDoubleSubmitReplays(t *testing.T) // Review Focus 2: scrape /ui/ once; POST the identical values twice →
// both 303 to "/ui/"; exactly one task exists; opKinds grew by exactly one "task.add"
func TestAddTaskValidation(t *testing.T) // title "" → 422 `data-code="validation"`; due "someday" → 422 naming "due";
// place "sideways" → 422 naming "place"; place "defer" with empty defer → 422 naming "defer"; priority "Z" → 422; nothing created
func TestValidationKeepsInput(t *testing.T) // submit("/ui/triage", "/ui/tasks", {title: "Long one", notes: "para 1\n\npara 2", due: "someday"}) → 422;
// f := scrapeForm(errorBody, "/ui/tasks") has the title, notes and due as typed; f.Set("due", "fri"); POST f → 303 "/ui/triage";
// the task exists with Notes "para 1\n\npara 2" and DueDate 2026-10-30
func TestEditTask(t *testing.T) // from /ui/tasks/1: title "New", project "", priority "" → Title "New", Project "", Priority none; version bumped
func TestEditTaskNotesCRLF(t *testing.T) // Review Focus 1: notes "a\r\nb" via the edit form → Notes "a\nb"
func TestEditTaskPartialForm(t *testing.T) // POST edit with only etag + title (+ hidden fields) → notes, project, priority unchanged
func TestTags(t *testing.T) // add "x y" → Tags include x, y; the remove form for "x" → x gone, y kept
func TestDue(t *testing.T)  // due "fri" → DueDate 2026-10-30; due "none" → cleared; due "yesterday" → 422 (service: due moment ≤ now)
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'TestAddTask|TestFlash|TestValidationKeepsInput|TestEditTask|TestTags|TestDue'`
Expected: FAIL (404/405 on the POST routes).

- [ ] **Step 3: Implement** `mutate`, `flash.go` and the four handlers in `h_tasks.go`; register routes.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): add, edit, tag and due actions; effect flash

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Placement and lifecycle actions (C5–C12)

**Files:**
- Create: `internal/web/tasks_lifecycle_test.go`
- Modify: `internal/web/h_tasks.go`, `internal/web/routes.go`

**Interfaces:**
- Consumes: Form contract rows C5–C12 and the service calls they name (Plan A Tasks 16, 19, 22, 27, 28, 29: `PlaceIn`, `DeferIn`, `MarkManualIn`, `BlockIn`, `CompleteIn`, `ReopenIn`, `TaskRef`, `AcceptNewIn{Items []AcceptItem}`); Task 7 `mutate`; seeding as in Task 5 plus `UpdateTask` (Plan A Task 29).
- Produces: routes C5–C12.

- [ ] **Step 1: Write the failing tests** in `tasks_lifecycle_test.go`

```go
func TestTriageActions(t *testing.T) // table; each case seeds #1 {new} (+ #2 placed bottom for before/after) and submits from "/ui/triage",
// picking the scraped form by its hidden how/until where several share an action:
// keep → View backlog, no reasons; top → rank < #2; before ref 2 → rank < #2; after ref 2 → rank > #2;
// defer until "fri 9:00" → View upcoming; block span "13:00-15:00" → planned block, no reasons;
// complete → status done, one complete entry dated 2026-10-28; drop → status dropped; each → 303 "/ui/triage"
func TestDeferNone(t *testing.T) // #1 deferred to fri → the /ui/upcoming defer form with until=none → View backlog, Rank unchanged
func TestManualTriage(t *testing.T) // backlog #1 → submit("/ui/tasks/1", "/ui/tasks/1/triage", nil) → Reasons [manual]
func TestCompleteFields(t *testing.T) // from /ui/tasks/1: as "Renamed", date "yesterday", log "Did it", notes "n" → Title "Renamed",
// CompletedDate 2026-10-27, complete entry Title "Did it", Notes "n"; date "2026-10-27 09:00" → 422 naming "date"
func TestReopen(t *testing.T) // done #1 → reopen → open, Reasons [reopened]; done again → reopen with remove_entry "1" → that complete entry deleted
func TestDeleteUndelete(t *testing.T) // delete from /ui/tasks/1 (form has hx-confirm) → task(1).DeletedAt valid; undelete → open, Reasons [manual]
func TestAcceptNew(t *testing.T) // #1 {new}, #2 {new}, #3 {new, due} (due today) → the form lists ids 1,2 only; submit → both backlog, #1 rank < #2;
// #3 still in triage; POST with id ["1","2"] and one etag → 400 `data-code="malformed"`
func TestStaleETagShowsReloadError(t *testing.T) // Review Focus 3: render /ui/triage; then UpdateTask(#1 title) with testutil.Owner() (channel cli);
// submit the scraped keep form → 412, `data-code="precondition_failed"`, "changed elsewhere", link to "/ui/triage";
// the same with header "HX-Request: true" → 200, HX-Push-Url "false", same marker; #1 still in triage
func TestGenerationAndNonceErrors(t *testing.T) // keep form with _gen "ffffffffffffffff" → 412 `data-code="generation_mismatch"`;
// _nonce "" → 428 `data-code="idempotency_key_required"`; _nonce "bad nonce!" → 400 `data-code="malformed"`; etag "" → 428 `data-code="precondition_required"`
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'TestTriageActions|TestDeferNone|TestManualTriage|TestComplete|TestReopen|TestDeleteUndelete|TestAcceptNew|TestStaleETag|TestGenerationAndNonce'`
Expected: FAIL (405/404).

- [ ] **Step 3: Implement** the handlers as `mutate` closures in `h_tasks.go`; register routes.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): placement and lifecycle actions for tasks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Entry and block actions (C13–C19)

**Files:**
- Create: `internal/web/h_entries.go`, `internal/web/h_blocks.go`, `internal/web/entries_blocks_test.go`
- Modify: `internal/web/routes.go`

**Interfaces:**
- Consumes: Form contract rows C13–C19 and the service calls they name (Plan A Tasks 21, 24, 25: `AddEntryIn`, `UpdateEntryIn`, `RelinkEntryIn`, `EntryRef`, `ReblockIn{BlockID; Expect, TaskExpect; Span}`, `BlockRef`, `SetOutcomeIn`); Plan A Task 21 `GetEntry(ctx, a, id domain.EntryID) (domain.EntryView, error)`, Task 22 `GetBlock(ctx, a, id domain.BlockID) (domain.BlockView, error)` (tests); Task 7 `mutate`.
- Produces: routes C13–C19.

- [ ] **Step 1: Write the failing tests** in `entries_blocks_test.go`

```go
func TestAddEntryFromToday(t *testing.T) // focus #1 → submit("/ui/", "/ui/entries", {title: "Fixed replica"}) → entry TaskID 1, WorkDate 2026-10-28;
// task_id "" → standalone; date "yesterday" + task_id 1 → WorkDate 2026-10-27; at "9:30" → OccurredMin 570;
// at "11:00" (now 10:00) → 422 (service); at "25:00" → 422 naming "at"
func TestAddEntryWarningFlash(t *testing.T) // #1 done; log against #1 → 303; the next page's flash contains the OpResult warning text
func TestEditEntry(t *testing.T) // work entry: title/notes/date "yesterday"/at "8:15" via the edit form → stored; at "" → cleared
func TestEditCompleteEntryNotesOnly(t *testing.T) // a complete entry's edit form has a notes field and no title/date/at fields;
// submitting it changes Notes only → 303
func TestRelinkEntry(t *testing.T) // standalone → task_id 1 → linked; then task_id "" → standalone
func TestDeleteUndeleteEntry(t *testing.T) // delete (form has hx-confirm) → DeletedAt valid; /ui/log?include_deleted=1 shows undelete; undelete → restored
func TestReblockInPlace(t *testing.T) // block today 13:00-15:00 (unstarted) → reblock span "16:00-17:00" from /ui/tasks/1 → same block id, 16:00–17:00;
// task_etag overridden with a stale value → 412 and the block unchanged
func TestUnblock(t *testing.T) // unstarted planned block → unblock → status cancelled, CancelReason user
func TestOutcome(t *testing.T) // seed as Task 6b TestRecapPage (block created with the clock at 10-27 10:00, missed once the clock is at 10-28 10:00);
// from /ui/recap?date=yesterday: the worked form → status worked, source user, Reasons include continue;
// the auto form → re-derived (missed); outcome overridden to "maybe" → 422 naming "outcome"
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'Entry|Relink|Reblock|Unblock|Outcome'`
Expected: FAIL.

- [ ] **Step 3: Implement** the seven handlers as `mutate` closures.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): entry and block actions

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Focus, undo/redo, revert, AI review (C20–C23); absent admin and purge routes

**Files:**
- Create: `internal/web/h_ops.go`, `internal/web/ops_test.go`
- Modify: `internal/web/routes.go`, `internal/web/templates/layout.html` (C21 undo and redo forms in the nav)

**Interfaces:**
- Consumes: Form contract rows C20–C23 and the service calls they name (Plan A Tasks 26, 30, 31, 33: `SetFocusIn`, `Undo(ctx, m)`, `Redo(ctx, m)`, `RevertIn{OpID}`, `MarkAIReviewedIn{Through}`); Plan A Task 5 `domain.ParseOpID`; Plan A Task 33 `ListAIChanges` (tests); Task 7 `mutate`.
- Produces: routes C20–C23. No route exists for tokens, settings, password, backups, export or purge (arch §7.3, §11.2, D37).

- [ ] **Step 1: Write the failing tests** in `ops_test.go`

```go
func TestFocusSetAndClear(t *testing.T) // the /ui/backlog focus form for #1 → Today focus #1; Today's clear form → no focus;
// focus #2 with interrupt "1" while #1 is focused → one interruption from #1 to #2
func TestUND19_WebUndoRevertsCLIOp(t *testing.T) // catalog UND-19: AddTask with ta.meta() (testutil.Owner(), channel cli);
// submit("/ui/", "/ui/undo", nil) → 303; the task is tombstoned; the newest op has via "undo" and channel "web";
// submit("/ui/", "/ui/redo", nil) → the task is restored
func TestUndoEmptyStack(t *testing.T) // fresh DB → undo → 409 `data-code="nothing_to_undo"`; redo → `nothing_to_redo`
func TestUndoStaleGeneration(t *testing.T) // R3-3: an owner op exists; the nav undo form with _gen "ffffffffffffffff"
// → 412 `data-code="generation_mismatch"`; nothing reverted; a correct undo afterwards succeeds
func TestRevertFromAIChanges(t *testing.T) // testutil.AI() AddEntry(#1) → the /ui/ai-changes revert form → entry deleted;
// re-posting that scraped form with a fresh _nonce → 409 `data-code="revert_conflict"` ("already reverted")
func TestMarkAIReviewed(t *testing.T) // review form → ListAIChanges.Unreviewed == 0; /ui/ no longer contains `data-banner="ai_changes"`;
// through overridden to the latest seq + 10 → 422
func TestSCP07_NoAdminRoutes(t *testing.T) // catalog SCP-07, web half (`forbidden` for ChanWeb token ops: Plan A):
// logged in, valid CSRF: GET and POST /ui/tokens, /ui/tokens/1/revoke, /ui/settings, /ui/password, /ui/backups, /ui/export → 404 or 405, never 2xx/3xx;
// no rendered page (/ui/, /ui/tasks/1, /ui/ai-changes) links or posts to /ui/tokens or /ui/settings
func TestPRG18_NoWebPurge(t *testing.T) // catalog PRG-18, web half (API half: Plan B Task 15): POST /ui/tasks/1/purge, /ui/entries/1/purge, /ui/purge → 404/405;
// no form on /ui/tasks/1 or /ui/log has an action containing "purge"
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'Focus|UND19|Undo|Revert|AIReviewed|SCP07|PRG18'`
Expected: FAIL for the new routes (the SCP-07/PRG-18 negatives may already pass — keep them as regression guards).

- [ ] **Step 3: Implement** the four handlers as `mutate` closures; add the C21 forms to the nav.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): focus, undo/redo, revert and AI-review actions

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Mount the web handler in the server; end to end over TCP

**Files:**
- Create: `internal/server/web.go`, `internal/server/web_integration_test.go`
- Modify: `README.md` (short "Web UI" section: `web.enabled`, `http://127.0.0.1:7878/ui/`, password login, single-user plain-loopback opt-in, no remote access in v1)

**Interfaces:**
- Consumes: Plan B Task 13 Interfaces: `var newWebHandler func(svc *service.Service, clk clock.Clock, zone civil.Zone, weekStart time.Weekday, listen string, log *slog.Logger) (http.Handler, error)` in `internal/server/lifecycle.go` (nil → no web listener; `Run` passes the bound address `ln.Addr().String()`, wraps the handler in recoverer/requestID/accessLog, serves it with the D§10.2 timeouts and `ConnContext`; the `allow_plain_loopback` gate and `TestWebListenerGate` are Plan B's), `testutil.StartServer`, `testutil.WithWeb()` (web enabled, `allow_plain_loopback = true`, listen `127.0.0.1:0`), `Server.WebURL` (= `"http://" + Startup.WebAddr`, the bound address reported through `server.Deps.Started func(Startup)`; `""` when no web listener runs), `Server.Client`. Plan A Task 16 `testutil.Password`. Task 2 `web.New`, `web.Options`.
- Produces: `internal/server/web.go` (AIDEV-NOTE: the only place `server` learns about `web`): `func init() { newWebHandler = func(svc *service.Service, clk clock.Clock, zone civil.Zone, weekStart time.Weekday, listen string, log *slog.Logger) (http.Handler, error) { return web.New(web.Options{Svc: svc, Clock: clk, Zone: zone, WeekStart: weekStart, Listen: listen, Logger: log}) } }`.
- Cross-plan contract (confirmed by planner-b): Plan B's `TestWebListenerGate` installs `nil` explicitly through `server.SetWebHandler(nil)` (+ restore) for its "no handler wired" case, so this `init` does not disturb it.

- [ ] **Step 1: Write the failing test** in `internal/server/web_integration_test.go` (package `server_test`, like Plan B's server tests: it imports `testutil`, which imports `server`)

```go
func TestWebUIOverTCP(t *testing.T)
// srv := testutil.StartServer(t, testutil.WithWeb()); c := &http.Client{Jar: cookiejar.New(nil)}, requests via
// http.NewRequestWithContext(t.Context(), …) with header Origin = srv.WebURL:
// GET srv.WebURL+"/" → redirects end at "/ui/login" 200 carrying the CSP header;
// POST /ui/login password=testutil.Password → ends on "/ui/" 200 (cookie in the jar);
// parse "/ui/" with golang.org/x/net/html, take the "/ui/tasks" form's hidden fields, POST title "From the browser" → ends 200 on "/ui/";
// srv.Client lists triage over the socket and finds "From the browser";
// GET srv.WebURL+"/api/v1/whoami" with the owner bearer token → 401;
// a GET with req.Host = "evil.example" → 400; GET srv.WebURL+"/healthz" → 200 "ok"
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server -race -run TestWebUIOverTCP`
Expected: FAIL (`srv.WebURL` is "": `newWebHandler` is nil, so no web listener).

- [ ] **Step 3: Implement** `internal/server/web.go` and the README section.

- [ ] **Step 4: Run the full suite**

Run: `make check`
Expected: all green, including Plan B's `TestWebListenerGate`.

- [ ] **Step 5: Commit**

```bash
git add internal/server/web.go internal/server/web_integration_test.go README.md
git commit -m "feat(server): mount the web UI; end-to-end test over TCP

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Planner decisions & open items

### Engineering decisions (made here, marked "(planner decision)" or stated in the task)

1. `golang.org/x/net` raised to `v0.61.0`: bluemonday v1.0.27 requires v0.26.0, which govulncheck reports for `x/net/html`; v0.61.0 is ≥ 14 days old and matches the pinned x/crypto/x/sys/x/term; tests also import `x/net/html` for parsed-HTML assertions (Task 1).
2. Markdown policy: an explicit bluemonday allowlist (no images, no tables, no classes); relative links dropped; external links get `rel="noreferrer noopener"` + `target="_blank"`; goldmark extensions Linkify + Strikethrough only (Task 1).
3. Security headers wrap the Host check so even its 400 carries them; "minimal Permissions-Policy" = `camera=(), geolocation=(), microphone=()`; `/ui` responses add `Cache-Control: no-store`; Host matching is case-insensitive (Task 2).
4. POST bodies must be `application/x-www-form-urlencoded` (415 otherwise); `bodyLimit` parses the form once for everything downstream (Task 2).
5. Interaction model: `hx-boost="true"` on `<body>`; every mutation is a plain form → one service call → `303` to `_return`. Because the fixed htmx config does not swap 4xx/5xx, htmx requests receive error pages with status 200 + `HX-Push-Url: false` (`data-code` carries the real code); non-htmx requests get the real status. Rejected alternative: adding `responseHandling` to the architecture's verbatim htmx config (Task 3).
6. `_return` hidden field instead of `Referer` (absent under `Referrer-Policy: no-referrer`), restricted to `/ui/` paths (Task 3).
7. `web` keeps its own `statusFor` (copy of arch §2.8; `web` cannot import `server`), pinned by a test over every `Code`; the ETag codec is Plan A's `domain.FormatETag`/`ParseETag` (Task 3).
8. Decode error classes: bad ids/list shapes → 400 `malformed`; unknown path id → 404; datespec/enum/clock → 422 `validation` naming the field; empty `etag` → 428 `precondition_required`; nonce and generation problems are left to `service` (Task 3).
9. Web inputs are datespec text resolved with the injected clock and `Options.WeekStart` (Future: add/defer/due/block/reblock; Past: complete/log dates, recap, report); `formInt` accepts `#42` (Task 3).
10. `app.css?v=<buildinfo.Version>` cache-buster, since `/static/app.css` is served `immutable` (Task 3).
11. Error pages for POSTs carry a "Your input" resubmit form with every submitted value except `_csrf`, `_nonce`, `password`; the original `_gen`/`etag` stay hidden so staleness is still detected (Task 3).
12. Pagination: "More" links are built by `moreURL`, which keeps every current query parameter and replaces only `cursor`; lists page at 100 (Tasks 3, 5, 6c).
13. Form contract section: one table shared by templates and handlers; every fixed action is its own form with a hidden value, so scraped forms are complete (Tasks 5–10).
14. Tests are package `web_test` with `export_test.go` seams (P6); HTML security assertions parse the DOM with `x/net/html` (Tasks 1–11).
15. Task 4 adds an authenticated `GET /ui/` stub so logout/CSRF tests have a real page to scrape before Task 5 replaces it (P2).
16. Task 6 split into 6a (task detail + history), 6b (recap + report), 6c (search + AI changes) (P3).
17. Login cookie carries `Expires` = the session's absolute expiry; the presented old cookie goes to `service` as `LoginIn.OldCookie`, which deletes it (regeneration) (Task 4).
18. Log defaults to `today−13…today`, Recap to today, Report to the full current week; empty search makes no service call (Tasks 5, 6b, 6c).
19. Edit forms set only the fields present; `complete` entries get a notes-only form (Tasks 7, 9).
20. D§5.9 echo on the web: a 60-second HMAC'd flash cookie (`tr_flash`, Path `/ui/`) carries the op's effects and warnings across the 303 (Task 7).
21. `web.New(Options{Svc, Clock, Zone, WeekStart, Listen, Logger}) (http.Handler, error)`; Plan B's `newWebHandler` hook (agreed signature; no `web` types, so Plan B compiles first) is assigned in `internal/server/web.go` (Tasks 2, 11).
22. `(*service.Service).Generation()` (cached `db_generation`, no DB/authz) stamps `_gen`; not counted as the handler's one service call under R1 (Task 3).

### Cross-plan contracts and open items (none need the user)

- Confirmed against the final architecture: `domain.BlockView.TaskVersion` (R4-7, reblock `task_etag`), `Meta.Gen` / `_gen` on every mutation incl. undo/redo/revert (R3-3), `textsafe.SanitizeLine` (§6.7), gosec off in `_test.go` (§13.4).
- Confirmed against Plan A (planner-a's reply and `2026-10-25-taskronin-plan-a-core.md`): every service signature cited in the Form contract and Interfaces blocks; `Authenticate` → `WebAuth{Actor, CSRFToken}`; `Login` deletes the old session; `Generation()`; `domain.FormatETag`/`ParseETag`; `testutil.Service` seeding `auth_state` with `testutil.Password` plus `db_generation`; `testutil.Meta(a, gen)`, `testutil.AI()`, `testutil.DefaultNow`.
- Confirmed with planner-b: `newWebHandler` exactly as in Task 11 (nil → no web listener, bound address passed); Plan B owns the `allow_plain_loopback` gate and its test and provides `testutil.WithWeb()` and `Server.WebURL`. Plan B's `TestWebListenerGate` "no handler wired" case sets `server.SetWebHandler(nil)` explicitly (confirmed), because Task 11's `init` assigns the hook. `Server.WebURL` comes from Plan B's `Startup.WebAddr` callback; Plan D reads only `WebURL`.
- Catalog split: Plan D owns the web halves of SCP-04, SCP-05, SCP-07, PRG-18 and IDM-04, and UND-19; Plan B owns the API/socket halves (SCP-04/05 in its Task 11, PRG-18 in Task 15, IDM-04 end to end in Task 35); SCP-07's service-level `forbidden` is Plan A's.

### Needs the user

- None.
