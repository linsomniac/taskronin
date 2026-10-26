# Taskronin Plan D — Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the loopback web UI (`/ui`): session login, CSRF/Origin/Host defences, strict security headers, server-rendered views and every action of architecture §11.2, calling `service` in-process.

**Architecture:** `internal/markdown` renders notes (goldmark without raw HTML, images as links) through a bluemonday allowlist. `internal/web` is one `http.Handler` built by `web.New`: Host allowlist → security headers → route class → body limit → session → CSRF → `net/http` `ServeMux` routes. Pages are `html/template` with a shared layout; htmx 2.0.11 (vendored, SHA-checked, `hx-boost` on `<body>`) gives in-place navigation. Every mutation is a plain `<form method="post">` → one service call → `303 See Other` to the form's `_return` page; errors render an error page.

**Tech Stack:** Go 1.26 stdlib `net/http`, `html/template`, `embed`; `github.com/yuin/goldmark v1.8.6`; `github.com/microcosm-cc/bluemonday v1.0.27`; htmx 2.0.11.

**Spec:** `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (primary: §1.3, §2.5, §2.8, §3, §6.1–6.2, §7.4–7.5, §11.2, §13), `docs/superpowers/specs/2026-10-25-taskronin-design.md` (D§9.3, D§10.2–10.4), `docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md` (SCP-04/05/07, PRG-18, UND-19, IDM-04 web parts).

**Prerequisites:** Plan A (core library: `domain`, `civil`, `clock`, `datespec`, `auth`, `service`, `testutil`) and Plan B (server wiring, `server.Run` mounting `web.New`, `testutil.StartServer`) are merged. Plan D is the last delivery step (D§16).

## Global Constraints

- Go `1.26.0`, toolchain `go1.26.8`, module `taskronin`, Linux only.
- New direct dependencies: `github.com/yuin/goldmark v1.8.6`, `github.com/microcosm-cc/bluemonday v1.0.27`; raise `golang.org/x/net` to `v0.61.0` (bluemonday requires v0.26.0, which govulncheck flags; v0.61.0 matches the pinned x/crypto v0.58.0 / x/sys v0.49.0 / x/term v0.47.0). No other dependency changes.
- Allowed imports (arch §1.3, enforced by `internal/archtest`): `markdown` → no internal packages, third-party goldmark + bluemonday only. `web` → `service`, `domain`, `civil`, `datespec`, `auth`, `markdown`, `textsafe`, `clock`, `buildinfo` only; never `store`, `api`, `server`, `client`. Tests reach other packages only through `testutil`.
- R1: each web handler decodes the request, makes **at most one** service call, then renders or redirects. All rules and authorization stay in `service`; web never re-implements a rule.
- R4: no `time.Now`/`time.Local`; use `Options.Clock` and `Options.Zone`. Tests use `httptest.NewRequestWithContext(t.Context(), …)` (noctx).
- `make check` (gofmt, `go vet`, golangci-lint v2.14 incl. gosec, `go test -race -count=1 ./...`, govulncheck v1.9.0, `go mod verify`, `assets-verify`) is green before every commit.
- htmx asset: `internal/web/static/htmx-2.0.11.min.js` = npm `htmx.org@2.0.11` `dist/htmx.min.js`, 52 182 bytes, SHA-256 `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`, recorded in `internal/web/static/SHA256SUMS`.
- htmx config, verbatim in the layout `<head>`: `<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"selfRequestsOnly":true,"includeIndicatorStyles":false,"historyCacheSize":0,"refreshOnHistoryMiss":true}'>`.
- Templates: no inline `<script>` bodies, no `<style>`, no `style=` attributes, no `on*=` attributes, no `hx-on`; the only script is `<script src="/static/htmx-2.0.11.min.js"></script>`; confirmations via `hx-confirm`; move buttons, no drag-and-drop.
- Every response from the web handler carries exactly: `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`, `Permissions-Policy: camera=(), geolocation=(), microphone=()`.
- Session cookie (plain loopback, v1): `tr_session=<base64url>; Path=/; HttpOnly; SameSite=Lax` (+ `Expires` = session absolute expiry). Idle 7 d / absolute 30 d are enforced by `service`.
- CSRF token = base64url(HMAC-SHA256(csrf_secret, "tkr-csrf-v1")); every POST except `POST /ui/login` needs a matching token (form `_csrf`, else header `X-CSRF-Token`; constant-time compare) **and** the Origin rule: `Origin` ∈ {`http://` + Host-allowlist entry} or `Sec-Fetch-Site: same-origin`; reject when both headers are absent or either mismatches. `POST /ui/login` gets the Origin rule only.
- Host allowlist from `web.listen`: the literal `host:port`, plus `localhost:<port>` when the host is `127.0.0.1` or `::1`; anything else → 400.
- Every mutating form carries hidden `_csrf`, `_nonce` (fresh 128-bit lowercase hex per render), `_gen` (generation the page was rendered with), `_return`, and the target's full ETag `"<gen>-<version>"` as `etag` (+ `task_etag` for reblock; parallel `id`/`etag` lists for accept-new).
- Web idempotency: `service.Idem{Key: _nonce, Method: "POST", Route: "POST " + r.URL.Path, Body: []byte(form-minus-_csrf-and-_nonce .Encode())}` (`url.Values.Encode` sorts keys); `Meta.Gen = _gen`. `Login` is idempotency-exempt.
- Web actor = what `service.Authenticate` returns (`ActorOwner`, `ChanWeb`, all scopes). Token management, settings, password, backup, export and purge have **no** web routes.
- GET handlers perform no user ops (reconcile's system writes are allowed).
- Passwords: never logged, never echoed into a page, never part of an idempotency body; the web never logs form bodies.
- Only `markdown.Render` output may become `template.HTML`; every other value reaches the page through `html/template` auto-escaping.
- Anchors: `AIDEV-NOTE:` in `web/csrf.go` (required by arch §1.4), on the Host allowlist, the session middleware, the idempotency-body canonicalization, the htmx error-status rule and the markdown policy. Grep `AIDEV-` in a package before editing it; never delete an `AIDEV-NOTE`.
- Commits: conventional (`feat(web): …`), each message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never commit secrets; tests use the `testutil` password fixture.

## Review Focus

1. **Browser textarea line endings** — browsers submit `<textarea>` content with CRLF. Notes added or edited through the web must be stored with LF (D18) and never rejected as control characters. Test: Task 7 `TestAddTaskNotesCRLF`, Task 8 `TestEditTaskNotesCRLF`.
2. **Double submit / back-button resubmit** — the same rendered form posted twice carries the same `_nonce`; exactly one op must exist and both responses redirect to `_return`. Test: Task 7 `TestAddTaskDoubleSubmitReplays`.
3. **Stale page** — a page rendered before a change made elsewhere (CLI, TUI, AI) and then submitted must show a visible "changed elsewhere — reload" error (412), also for htmx-boosted requests, which do not swap 4xx bodies under the fixed htmx config. Test: Task 3 `TestRenderErrorHTMX`, Task 8 `TestStaleETagShowsReloadError`.
4. **Hostile task text** — titles, notes, tags, entry text and search snippets containing `<script>`, `"><img src=x onerror=…>`, `javascript:` links or quotes in `hx-confirm` attributes stay inert on every page. Test: Task 5 `TestViewsEscapeHostileTitle`, Task 6 `TestDetailEscapesAndRendersMarkdown`, Task 1 `FuzzRender`.
5. **Open redirect via `_return`** — `https://evil.example/`, `//evil.example/`, `/\evil.example`, `javascript:…` or a non-`/ui/` path fall back to `/ui/`. Test: Task 3 `TestSafeReturn`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/markdown/render.go` | `Render`: goldmark (no raw HTML, image→link renderer) → bluemonday policy | 1 |
| `internal/markdown/render_test.go` | table tests + `FuzzRender` | 1 |
| `internal/web/static/htmx-2.0.11.min.js`, `static/SHA256SUMS`, `static/app.css` | vendored htmx, its checksum, responsive stylesheet | 2 |
| `internal/web/web.go` | `Options`, `New`, embeds, chain assembly, static + healthz + root handlers | 2 |
| `internal/web/headers.go` | Host allowlist, security headers, route class, body limit | 2 |
| `internal/web/render.go` | template set, `page`, FuncMap, `renderPage`, `renderError`, `statusFor` | 3 |
| `internal/web/forms.go` | form decoding: `meta`, `parseETag`, `formatETag`, `safeReturn`, id/date/span parsing | 3 |
| `internal/web/templates/layout.html`, `error.html` | base layout (nav, banners slot, htmx meta/script), error page | 3 |
| `internal/web/session.go`, `csrf.go`, `h_auth.go`, `templates/login.html` | session middleware + cookie, CSRF/Origin, login/logout | 4 |
| `internal/web/routes.go` | the route table (`ServeMux` patterns → handlers) | 2 (grown by 4–10) |
| `internal/web/h_views.go`, `templates/{today,triage,backlog,upcoming,log}.html` | list pages | 5 |
| `internal/web/h_detail.go`, `templates/{task,history,recap,report,search,aichanges}.html` | detail, history, recap, report, search, AI changes | 6 |
| `internal/web/h_tasks.go` | task POSTs | 7, 8 |
| `internal/web/h_entries.go`, `h_blocks.go` | entry and block POSTs | 9 |
| `internal/web/h_ops.go` | focus, undo, redo, revert, AI review | 10 |
| `internal/web/helpers_test.go` | `testApp` harness (recorder-based, cookie + form scraping) | 3, 4 |
| `internal/server/web_integration_test.go` | real TCP listener end to end via `testutil.StartServer` | 11 |

---
### Task 1: Markdown rendering (`internal/markdown`)

**Files:**
- Create: `internal/markdown/render.go`, `internal/markdown/render_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing internal (arch §1.3: `markdown` imports no internal package).
- Produces: `func Render(src string) (template.HTML, error)` — sanitized HTML for notes; the only producer of `template.HTML` from user text. Used by `web` templates as FuncMap `md`.

- [ ] **Step 1: Add the dependencies**

Run: `go get github.com/yuin/goldmark@v1.8.6 github.com/microcosm-cc/bluemonday@v1.0.27 golang.org/x/net@v0.61.0 && go mod tidy`
Expected: `go.mod` lists goldmark v1.8.6 and bluemonday v1.0.27 as direct requirements and `golang.org/x/net v0.61.0` (indirect); `golang.org/x/crypto`, `x/sys`, `x/term` versions unchanged.

- [ ] **Step 2: Write the failing tests** in `internal/markdown/render_test.go`

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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Render(c.in)
			if err != nil { t.Fatal(err) }
			for _, w := range c.want { if !strings.Contains(string(got), w) { t.Errorf("missing %q in %q", w, got) } }
			for _, n := range c.notWant { if strings.Contains(string(got), n) { t.Errorf("unexpected %q in %q", n, got) } }
		})
	}
}

func FuzzRender(f *testing.F) // seeds: every TestRender input. Property for any input:
// no "<script", no "<img", no "<iframe", no match of (?i)\son[a-z]+\s*=,
// every href="…" value starts with "http://", "https://" or "mailto:".
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/markdown -run 'TestRender' -race`
Expected: FAIL (`undefined: Render`).

- [ ] **Step 4: Implement `Render(src string) (template.HTML, error)` in `internal/markdown/render.go`**

- One package-level `goldmark.Markdown` built with `goldmark.New(goldmark.WithExtensions(extension.Linkify, extension.Strikethrough), goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(imageAsLink{}, 100))))` — **no** `html.WithUnsafe()` (raw HTML becomes `<!-- raw HTML omitted -->`). Priority 100 matters: goldmark registers renderers from highest to lowest priority value, so the lower value is registered last and overrides the default HTML renderer (priority 1000) for `ast.KindImage`.
- `imageAsLink` implements `renderer.NodeRenderer`: for `*ast.Image`, entering writes `<a href="` + `util.EscapeHTML(util.URLEscape(n.Destination, true))` + `">` and returns `ast.WalkContinue` (the alt text children render as text); leaving writes `</a>`.
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
- Create: `internal/web/web.go`, `internal/web/headers.go`, `internal/web/routes.go`, `internal/web/static/htmx-2.0.11.min.js`, `internal/web/static/SHA256SUMS`, `internal/web/static/app.css`, `internal/web/headers_test.go`, `internal/web/static_test.go`

**Interfaces:**
- Consumes: `*service.Service` (stored only); `clock.Clock`; `civil.Zone`; `testutil.Service(t testing.TB, opts ...testutil.SvcOpt) (*service.Service, *clock.Fake)`, `testutil.Zone`.
- Produces (Plan B's `server.Run` calls `New` and wraps it with its recoverer/requestID/accessLog, arch §6.2 step 1):
  ```go
  type Options struct {
  	Svc       *service.Service
  	Clock     clock.Clock
  	Zone      civil.Zone
  	WeekStart time.Weekday
  	Listen    string       // config web.listen, already validated loopback "host:port"
  	Logger    *slog.Logger
  }
  func New(o Options) (http.Handler, error)  // error: Listen not host:port with numeric port, template parse failure
  func newApp(o Options) (*app, error)        // New = newApp + a.handler(); tests use newApp for internals
  func (a *app) handler() http.Handler        // the chain below around a.mux
  const (
  	SessionCookie = "tr_session"
  	HTMXAsset     = "htmx-2.0.11.min.js"
  	HTMXSHA256    = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"
  	HTMXSize      = 52182
  	CSP           = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
  )
  func hostAllowlist(listen string) (map[string]bool, error) // lowercase "host:port" keys
  ```
- Chain order inside `handler()` (outermost first): `securityHeaders` → `checkHost` → `routeClass` → `bodyLimit` → (Task 4: `session` → `csrf`) → `a.mux`. (planner decision: security headers wrap the Host check so the 400 carries them too.)

- [ ] **Step 1: Vendor htmx and record its checksum**

```bash
mkdir -p internal/web/static
curl -sSfL https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.11.tgz | tar -xzO package/dist/htmx.min.js > internal/web/static/htmx-2.0.11.min.js
(cd internal/web/static && sha256sum htmx-2.0.11.min.js > SHA256SUMS && cat SHA256SUMS && wc -c htmx-2.0.11.min.js)
```
Expected: `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717  htmx-2.0.11.min.js` and `52182`. Any other value: stop and report (do not continue with a different file). Create `static/app.css` with a mobile-first layout: system font stack, max-width content column, nav wrapping at narrow widths, buttons ≥ 2.75rem tall, one `@media (min-width: 48rem)` breakpoint; no `url()`, no `@import`.

- [ ] **Step 2: Write the failing tests**

`internal/web/static_test.go`:
```go
func TestHTMXAssetPinned(t *testing.T) {
	b, err := staticFS.ReadFile("static/" + HTMXAsset)
	// err nil; len(b) == HTMXSize; hex(sha256(b)) == HTMXSHA256; len(HTMXSHA256) == 64
	sums, _ := staticFS.ReadFile("static/SHA256SUMS")
	// string(sums) == HTMXSHA256 + "  " + HTMXAsset + "\n"
}
func TestStaticServing(t *testing.T) // via the full handler, Host "127.0.0.1:7878":
// GET /static/htmx-2.0.11.min.js → 200, body == embedded bytes,
//   Cache-Control "public, max-age=31536000, immutable", Content-Type "text/javascript; charset=utf-8"
// GET /static/app.css → 200, Content-Type "text/css; charset=utf-8", same Cache-Control
// GET /static/SHA256SUMS, /static/, /static/../web.go, /static/nope.js → 404
```
`internal/web/headers_test.go` (helper `newHandler(t, listen string) http.Handler` = `New(Options{Svc, Clock from testutil.Service(t), Zone: testutil.Zone, WeekStart: time.Monday, Listen: listen, Logger: slog.New(slog.DiscardHandler)})`; requests via `httptest.NewRequestWithContext(t.Context(), …)` with `req.Host` set):
```go
func TestHostAllowlist(t *testing.T) // GET /healthz, table (listen, Host, status):
// "127.0.0.1:7878": "127.0.0.1:7878"→200, "localhost:7878"→200, "LOCALHOST:7878"→200,
//   "127.0.0.1:7879"→400, "evil.example:7878"→400, "127.0.0.1"→400, "localhost"→400, ""→400
// "[::1]:7878": "[::1]:7878"→200, "localhost:7878"→200, "127.0.0.1:7878"→400
// "127.0.0.2:7878": "127.0.0.2:7878"→200, "localhost:7878"→400
func TestNewRejectsBadListen(t *testing.T) // "", "7878", "127.0.0.1", "127.0.0.1:http" → error
func TestSecurityHeadersOnEveryResponse(t *testing.T)
// for GET /healthz, GET /, GET /static/app.css, GET /nope, GET /api/v1/tasks and a GET with Host "evil.example:7878":
// header CSP == CSP const; X-Content-Type-Options "nosniff"; Referrer-Policy "no-referrer";
// Cross-Origin-Opener-Policy "same-origin"; Permissions-Policy "camera=(), geolocation=(), microphone=()"
func TestSCP04_WebListenerRefusesAPI(t *testing.T) // catalog SCP-04, web part (socket part: Plan B)
// GET and POST /api/v1/tasks, GET /api/v1/whoami, GET /api/v1, each with "Authorization: Bearer tkr_1_<43 x 'A'>":
// 401, Content-Type "application/problem+json", Cache-Control "no-store", JSON body
// {"type":"urn:taskronin:problem:unauthenticated","title":"Unauthorized","status":401,"code":"unauthenticated",…}
func TestRouteClass(t *testing.T)
// GET / → 303 Location "/ui/"; GET /healthz → 200 body "ok" Content-Type "text/plain; charset=utf-8";
// GET /favicon.ico, /metrics, /uix → 404
func TestBodyLimit(t *testing.T) // bodyLimit(stub) where stub calls r.ParseForm and writes 204 or the error status:
// POST form body of 1<<20+1 bytes → 413; POST Content-Type "application/json" → 415;
// POST "application/x-www-form-urlencoded; charset=UTF-8" small → 204; GET passes untouched
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/web -race`
Expected: FAIL (undefined: `New`, `staticFS`, …).

- [ ] **Step 4: Implement**

- `web.go`: `//go:embed static` into `staticFS embed.FS` (Task 3 adds a separate `//go:embed templates` `templateFS`); `Options`, constants, `newApp` (validates `Listen` via `net.SplitHostPort` + `strconv.ParseUint(port, 10, 16)`, builds the Host and Origin allowlists, the mux via `a.routes()`), `New`, `handler()`.
- `headers.go`:
  - `hostAllowlist(listen)`: `{lower(listen)}` plus `"localhost:"+port` when the host is `127.0.0.1` or `::1`. `checkHost`: `allowed[strings.ToLower(r.Host)]` else 400 plain text "bad host" (AIDEV-NOTE: DNS-rebinding guard; derived from web.listen, arch §6.2).
  - `securityHeaders`: sets the five headers of the Global Constraints before calling next.
  - `routeClass`: deletes `Authorization`; path `/api/v1` or prefix `/api/v1/` → 401 problem JSON (`detail` "the API is served only on the unix socket", `request_id` = the `X-Request-ID` response header already set by the server's requestID middleware, else ""); allowed: `/`, `/healthz`, `/ui`, prefix `/ui/`, prefix `/static/`; anything else 404.
  - `bodyLimit`: POST only: `Content-Type` media type (`mime.ParseMediaType`) must be `application/x-www-form-urlencoded` else 415; `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)`; a `*http.MaxBytesError` from `ParseForm` → 413 (handlers map it via `renderError` with `domain.CodeTooLarge` in Task 3).
- `routes.go`: `func (a *app) routes() *http.ServeMux` registering `GET /{$}` (303 `/ui/`), `GET /healthz` (`ok`), `GET /static/{name}` (allowlist: `HTMXAsset`, `app.css`; `http.ServeContent` with the embedded bytes, a fixed zero modtime, the immutable `Cache-Control`). Later tasks add their routes here.
- Note in `README.md` nothing yet (Task 11).

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
- Modify: `internal/web/web.go` (add `//go:embed templates` → `templateFS`; `newApp` loads templates)

**Interfaces:**
- Consumes: `domain.Error{Code; Detail; Fields []FieldError; Conflicts []RevertConflict; Current any; RetryAfter time.Duration; Err error}`, `domain.AsError(err) (*domain.Error, bool)`, `domain.Errorf(code, format, args...) *domain.Error`, every `domain.Code` constant (arch §2.8), `domain.Expect{Gen; Version}`, `domain.Generation`, `domain.Version`, `domain.Actor`, `domain.Moment`, `domain.BlockSpan`; `service.Meta{Actor; Idem; Gen}`, `service.Idem{Key, Method, Route string; Body []byte}`, `(*service.Service).Generation() domain.Generation` (cached `db_generation`); `datespec.Parse(s, m, r) (domain.Moment, error)`, `datespec.ParseSpan(s, m, r) (domain.BlockSpan, error)`, `datespec.ParseClock(s, allow2400) (civil.Minute, error)`, `datespec.Ref{Today; WeekStart}`, `datespec.Past`, `datespec.Future`; `markdown.Render`; `buildinfo.Version`; `testutil.Service`, `testutil.Owner()`.
- Produces (used by Tasks 4–10):
  ```go
  type sessionInfo struct{ Actor domain.Actor; CSRF, Cookie string }
  func withSession(ctx context.Context, s sessionInfo) context.Context
  func sessionFrom(ctx context.Context) (sessionInfo, bool)
  type page struct {
  	Title, Tab string            // Tab highlights nav: today|triage|backlog|upcoming|log|report|search|ai
  	CSRF       string            // from sessionFrom; "" when logged out (nav hidden)
  	Gen        domain.Generation // a.o.Svc.Generation(); forms' _gen
  	Return     string            // GET: r.URL.RequestURI(); POST: safeReturn(_return); forms' _return
  	Today      civil.Date        // Zone.DateOf(Clock.Now())
  	Version    string            // buildinfo.Version (app.css?v= cache-buster)
  	Data       any
  	Err        *domain.Error
  }
  func (a *app) newPage(r *http.Request, title, tab string, data any) page
  func (a *app) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, p page)
  func (a *app) renderError(w http.ResponseWriter, r *http.Request, err error)
  func statusFor(c domain.Code) int
  func formatETag(gen domain.Generation, v domain.Version) string // `"<gen>-<v>"`
  func parseETag(s string) (domain.Expect, error)                 // malformed → CodeMalformed
  func (a *app) expect(form url.Values, key string) (domain.Expect, error) // "" → CodePreconditionReq
  func safeReturn(raw string) string
  func idemBody(form url.Values) []byte
  func (a *app) meta(r *http.Request) (service.Meta, error)       // after r.ParseForm
  func formInt(form url.Values, key string) (int64, error)        // "42" or "#42"; else CodeMalformed{Field: key}
  func pathInt(r *http.Request, name string) (int64, error)       // r.PathValue; else CodeNotFound
  func (a *app) ref() datespec.Ref                                // {Zone.DateOf(Clock.Now()), o.WeekStart}
  func (a *app) formMoment(form url.Values, key string, m datespec.Mode) (*domain.Moment, error) // "" → nil
  func (a *app) formSpan(form url.Values, key string) (domain.BlockSpan, error)                  // Future
  func (a *app) formDate(form url.Values, key string, m datespec.Mode) (civil.Date, error)      // "" → zero Date
  func formMinute(form url.Values, key string) (sql.Null[civil.Minute], error)                  // "" → !Valid
  func splitTags(s string) []string // split on commas and whitespace, empties dropped
  // test harness (helpers_test.go)
  const testListen = "127.0.0.1:7878"
  type testApp struct{ t *testing.T; app *app; h http.Handler; Svc *service.Service; Clock *clock.Fake; Logs *bytes.Buffer; cookie string }
  func newTestApp(t *testing.T) *testApp // testutil.Service; clock set to Wed 2026-10-28 10:00 America/Denver; Logs captures slog text
  func (ta *testApp) get(path string, hdr ...string) *httptest.ResponseRecorder            // Host testListen, cookie if set; hdr = key,value pairs
  func (ta *testApp) post(path string, form url.Values, hdr ...string) *httptest.ResponseRecorder // + form Content-Type, Origin "http://127.0.0.1:7878" unless overridden
  func scrapeForm(t *testing.T, body, action string) url.Values
  ```
- Decode errors are `*domain.Error`: datespec/enum/clock problems → `CodeValidation` with one `FieldError{Field: key}`; ids, ETags and list mismatches → `CodeMalformed`.
- Template conventions (the scraper relies on them): every mutating form is `<form method="post" action="…">` and contains `{{template "hidden" $}}`, which emits exactly `<input type="hidden" name="_csrf" value="…">`, `_gen`, `_nonce` (`{{nonce}}`), `_return`; other inputs are written `<input type="…" name="…" value="…">` in that attribute order; `<textarea name="…">…</textarea>`; `<select name="…">` with `<option value="…" selected>`.
- FuncMap: `nonce() (string, error)` (16 bytes `crypto/rand`, lowercase hex), `etag(domain.Generation, domain.Version) string`, `md(string) (template.HTML, error)` = `markdown.Render`, `day(civil.Date) string` ("Mon 2006-01-02"), `hhmm(any) string` (`civil.Minute`/`civil.EndMinute` → "HH:MM", 1440 → "24:00"), `wall(time.Time) string` ("Mon 2006-01-02 15:04" in `o.Zone`), `short(domain.OpID) string` (first 8 hex).

- [ ] **Step 1: Write the failing tests**

`forms_test.go`:
```go
func TestParseETag(t *testing.T)
// ok: `"0123456789abcdef-3"` → Expect{Gen: "0123456789abcdef", Version: 3}; formatETag round-trips.
// malformed (CodeMalformed): `0123456789abcdef-3`, `W/"0123456789abcdef-3"`, `"0123456789ABCDEF-3"`,
//   `"0123456789abcdef-0"`, `"0123456789abcdef-03"`, `"*"`, `"abc-1"`, `"0123456789abcdef-3" `
// expect(form{}, "etag") → CodePreconditionReq
func TestSafeReturn(t *testing.T)
// kept: "/ui/", "/ui/backlog", "/ui/log?from=2026-10-20&to=2026-10-28", "/ui/tasks/42#entries"
// → "/ui/": "", "https://evil.example/", "//evil.example/ui/", "/\\evil.example", "javascript:alert(1)",
//   "/api/v1/tasks", "/ui/../api/v1/tasks", "/static/app.css", "/ui/\r\nSet-Cookie: x=1", "ui/backlog"
func TestIdemBody(t *testing.T)
// form{_csrf:"x", _nonce:"n", b:"2", a:"1", tags:["z","y"], _gen:"g"} → "_gen=g&a=1&b=2&tags=z&tags=y"
func TestMeta(t *testing.T) // POST /ui/tasks form {_nonce: 32 hex, _gen: "00000000000000aa", _csrf: "x", _return: "/ui/", title: "a"},
// ctx withSession(sessionInfo{Actor: webOwner}) → Meta{Actor: webOwner, Gen: "00000000000000aa",
//   Idem{Key: nonce, Method: "POST", Route: "POST /ui/tasks", Body: "_gen=00000000000000aa&_return=%2Fui%2F&title=a"}}
func TestFormDecoders(t *testing.T) // clock Wed 2026-10-28 10:00 Denver, WeekStart Monday
// formMoment("fri 9:00", Future) → {2026-10-30, 09:00}; "" → nil; "someday" → CodeValidation Fields[0].Field == key
// formSpan("13:00-15:00") → {2026-10-28, 13:00, 15:00}; formSpan("fri 23:00-24:00") → end 1440
// formDate("yesterday", Past) → 2026-10-27; "" → zero; "2026-10-27 09:00" → CodeValidation ("date only")
// formMinute("9:05") → {545, true}; "" → !Valid; "24:00" → CodeValidation
// formInt "42" and "#42" → 42; "0", "-1", "4x", "" → CodeMalformed
// splitTags("a, b  c,,") → ["a","b","c"]
```
`render_test.go`:
```go
func TestStatusForEveryCode(t *testing.T) // arch §2.8 table, all 24 codes:
// malformed 400; unauthenticated 401; forbidden, peer_rejected, password_required, password_invalid 403;
// not_found 404; deleted, conflict, revert_conflict, not_revertible, nothing_to_undo, nothing_to_redo 409;
// precondition_failed, generation_mismatch 412; body_too_large 413; unsupported_media_type 415;
// validation, idempotency_mismatch 422; precondition_required, idempotency_key_required 428;
// rate_limited 429; internal 500; busy 503; unknown Code("zzz") → 500
func TestRenderErrorStatus(t *testing.T)
// validation + FieldError{"due","bad date"} → 422, Content-Type "text/html; charset=utf-8", Cache-Control "no-store",
//   body contains `data-code="validation"` and "bad date"
// internal with Err "secret sauce" → 500; body lacks "secret sauce"; ta.Logs contains "secret sauce"
// rate_limited RetryAfter 2500ms → header Retry-After "3"; a plain error (errors.New) → 500 internal
// revert_conflict with one RevertConflict → body lists its short op id and kind
func TestRenderErrorHTMX(t *testing.T) // same request + "HX-Request: true", precondition_failed:
// status 200; HX-Push-Url "false"; HX-Retarget "body"; HX-Reswap "innerHTML";
// body contains `data-code="precondition_failed"` and "changed elsewhere"; a link to the request's safe _return
```
`templates_test.go`:
```go
func TestTemplatesLint(t *testing.T) // walks templateFS (keeps covering templates added by later tasks):
// no "<style", no "style=", no match of (?i)\son[a-z]+\s*=, no "hx-on", no "javascript:";
// every "<script" is exactly `<script src="/static/htmx-2.0.11.min.js"></script>` and only in layout.html;
// layout.html contains the htmx-config <meta> verbatim (Global Constraints), `hx-boost="true"`,
// `hx-headers='{"X-CSRF-Token":"{{.CSRF}}"}'`, `href="/static/app.css?v={{.Version}}"`;
// every `<form method="post"` block contains `{{template "hidden" $}}`
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race`
Expected: FAIL (undefined: `statusFor`, `parseETag`, `newTestApp`, …).

- [ ] **Step 3: Implement**

- `render.go`: templates parsed once in `newApp`: for each `templates/<name>.html` other than `layout.html`/`partials.html`, `template.New(name).Funcs(fm).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")`; pages `{{define "content"}}`; execute `"layout"` into a buffer, then write headers (`Content-Type: text/html; charset=utf-8`, `Cache-Control: no-store`) and status; an execution error is logged and becomes a plain 500.
- `renderError` (AIDEV-NOTE: the fixed htmx config does not swap 4xx/5xx bodies, so htmx requests get their error page with status 200 and `HX-Push-Url: false`; non-htmx requests get the real status): `domain.AsError`; `CodeInternal` → log `Err` with the path and `X-Request-ID`, show "internal error"; `RetryAfter > 0` → `Retry-After` = ceil seconds; `precondition_failed`/`generation_mismatch` → "This changed elsewhere since the page was loaded — reload" + link to `page.Return`; field errors and revert conflicts listed.
- `forms.go`: ETag regexp `^"([0-9a-f]{16})-([1-9][0-9]{0,18})"$`; `safeReturn` (AIDEV-NOTE: open-redirect guard): reject any CR/LF/backslash, `url.Parse` must give empty scheme and host, raw must start with `/ui/`, and `path.Clean` of the path must still start with `/ui/` (or equal `/ui`); else `/ui/`. `idemBody` (AIDEV-NOTE: canonical web idempotency body, arch §7.5): clone, delete `_csrf`/`_nonce`, `Encode()`. `meta`: actor from `sessionFrom`, `Idem{Key: form "_nonce", Method: "POST", Route: "POST " + r.URL.Path, Body: idemBody(r.PostForm)}`, `Gen: domain.Generation(form "_gen")` — missing/invalid nonce or gen are reported by `service` (`idempotency_key_required`, `malformed`, `precondition_required`, `generation_mismatch`), never pre-checked here.
- `layout.html`: doctype, `lang="en"`, charset, viewport, the htmx-config meta, `<title>{{.Title}} · Taskronin</title>`, stylesheet link, the htmx script; `<body hx-boost="true" hx-headers='{"X-CSRF-Token":"{{.CSRF}}"}'>`; nav (Today `/ui/`, Triage, Backlog, Upcoming, Log, Report, Search, AI changes, and a `POST /ui/logout` form with `hx-boost="false"`) only when `.CSRF` is set; `<main>{{template "content" .}}</main>`. `partials.html`: `{{define "hidden"}}` per the conventions above. `error.html`: `<section class="error" data-code="{{.Err.Code}}">`.
- `helpers_test.go`: `newTestApp` builds `newApp(Options{…, Listen: testListen, Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})` and `h = app.handler()`; `scrapeForm` finds the `<form` whose `action="…"` equals `action` (HTML-unescaped), up to `</form>`, and collects `input` name/value pairs (checkbox/radio only when `checked`), `textarea` contents, and the `selected` option of each `select`, all HTML-unescaped; it fails the test if the form is absent.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/web -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make check
git add internal/web
git commit -m "feat(web): templates, error pages, form decoding and test harness

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Sessions, login/logout, CSRF and Origin checks

**Files:**
- Create: `internal/web/session.go`, `internal/web/csrf.go`, `internal/web/h_auth.go`, `internal/web/templates/login.html`, `internal/web/session_test.go`, `internal/web/csrf_test.go`
- Modify: `internal/web/web.go` (chain gains `session` → `csrf`), `internal/web/routes.go`, `internal/web/helpers_test.go` (add `login`, `submit`)

**Interfaces:**
- Consumes: `(*service.Service).Login(ctx context.Context, in service.LoginIn) (service.Session, error)` with `LoginIn{RemoteKey, Password, OldCookie string}` and `Session{CookieValue, CSRFToken string; ExpiresAt time.Time}` (service deletes the `OldCookie` session: regeneration); `(*service.Service).Authenticate(ctx context.Context, cookie string) (domain.Actor, [32]byte, error)` (actor = `ActorOwner`, `ChanWeb`, `AllScopes`; second value = CSRF secret; idle 7 d / absolute 30 d → `unauthenticated`); `(*service.Service).Logout(ctx context.Context, cookie string) error`; `auth.CSRFToken(secret [32]byte) string`; `testutil.Password` (the fixture password seeded by `testutil.Service`); Task 3's `sessionInfo`, `withSession`, `renderPage`, `renderError`, harness.
- Produces:
  ```go
  func (a *app) session(next http.Handler) http.Handler // all /ui/* except GET/POST /ui/login
  func (a *app) checkOrigin(r *http.Request) bool       // Origin/Sec-Fetch-Site rule (Global Constraints)
  func (a *app) csrf(next http.Handler) http.Handler    // every POST except POST /ui/login
  func clientIP(r *http.Request) string                 // netip.ParseAddrPort(r.RemoteAddr).Addr().String(); fallback r.RemoteAddr
  // harness additions
  func (ta *testApp) login() *testApp // POST /ui/login with testutil.Password; asserts 303; stores the cookie
  func (ta *testApp) submit(pagePath, action string, overrides url.Values) *httptest.ResponseRecorder
  	// GET pagePath (asserts 200), scrapeForm(action), apply overrides (Set; empty slice deletes), POST to action
  ```
- Routes: `GET /ui/login` (form), `POST /ui/login`, `POST /ui/logout`.

- [ ] **Step 1: Write the failing tests**

`session_test.go` (no Today page exists yet: an authenticated `GET /ui/nope` reaches the mux and is 404, an unauthenticated one is 303):
```go
func TestLoginSuccess(t *testing.T)
// GET /ui/login → 200, contains `action="/ui/login"`, `hx-boost="false"`, `type="password"`, no nav
// POST /ui/login {password: testutil.Password}, Origin "http://127.0.0.1:7878" → 303 Location "/ui/";
// Set-Cookie parsed with http.ParseSetCookie: Name "tr_session", Path "/", HttpOnly, SameSite Lax, !Secure,
//   Expires == clock now + 30 d (to the second), Value matches ^[A-Za-z0-9_-]{43}$
// then GET /ui/nope with the cookie → 404 (authenticated, no such page)
func TestLoginWrongPassword(t *testing.T) // → 403, `data-code="password_invalid"`, no Set-Cookie, body lacks the submitted password
func TestLoginCrossOrigin(t *testing.T)   // Origin "http://evil.example" → 403, no Set-Cookie; no Origin and no Sec-Fetch-Site → 403
func TestLoginRegeneratesSession(t *testing.T) // login → A; login again sending A → B ≠ A; A → 303 /ui/login; B → 404
func TestSCP05_BearerOnUI(t *testing.T)   // catalog SCP-05, web part (socket part: Plan B)
// no cookie, "Authorization: Bearer tkr_1_<43 chars>": GET /ui/nope → 303 Location "/ui/login";
// POST /ui/logout → 401, HX-Redirect "/ui/login"
func TestSessionIdleExpiry(t *testing.T)  // login; Clock.Advance(7*24h + time.Minute); GET /ui/nope → 303 /ui/login
func TestLogout(t *testing.T)             // POST /ui/logout via submit → 303 "/ui/login", Set-Cookie tr_session MaxAge < 0; old cookie → 303
func TestIDM04_LoginNotLogged(t *testing.T) // catalog IDM-04, web part: after a failed and a successful login
// ta.Logs contains neither the wrong nor the right password; neither response body contains them
func TestLoginBodyLimits(t *testing.T)    // POST /ui/login with 1<<20+1 bytes → 413; Content-Type application/json → 415
```
`csrf_test.go`, table over `POST /ui/logout`, each case on a fresh `login()`; form = scraped `_csrf` (or as stated); a refused case leaves the session valid (follow-up `GET /ui/nope` → 404):
```go
func TestCSRF(t *testing.T)
// 1 valid _csrf, Origin http://127.0.0.1:7878 → 303          2 no _csrf, header X-CSRF-Token valid, Sec-Fetch-Site same-origin → 303
// 3 Origin http://localhost:7878 → 303                        4 missing token → 403
// 5 wrong token → 403                                          6 token of another session → 403
// 7 Origin http://evil.example → 403                           8 Origin https://127.0.0.1:7878 → 403
// 9 Origin null → 403                                          10 neither Origin nor Sec-Fetch-Site → 403
// 11 Sec-Fetch-Site cross-site, no Origin → 403                12 Origin ok + Sec-Fetch-Site same-site → 403
// 13 empty _csrf + valid header → 303 (form value empty → header used)
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web -race -run 'TestLogin|TestSCP05|TestSession|TestLogout|TestIDM04|TestCSRF'`
Expected: FAIL (404 on `/ui/login`, undefined `login`).

- [ ] **Step 3: Implement**

- `session.go` (AIDEV-NOTE: the only authentication for /ui; cookie never accepted on /api/v1): cookie `tr_session` → `Svc.Authenticate`; ok → `withSession(ctx, sessionInfo{Actor, auth.CSRFToken(secret), cookie})`; `unauthenticated` or no cookie → GET/HEAD `303 /ui/login`, other methods `401` + `HX-Redirect: /ui/login`; any other error → `renderError`. Cookie writer sets `Path=/`, `HttpOnly`, `SameSite=Lax`, `Expires`; logout writes `MaxAge: -1`.
- `csrf.go` (AIDEV-NOTE, required by arch §1.4: both checks, constant-time compare, login exempt from the token only): `checkOrigin` per Global Constraints (Origin compared against `"http://" + host` for every Host-allowlist entry); `csrf` parses the form, takes `_csrf` if non-empty else `X-CSRF-Token`, `subtle.ConstantTimeCompare` with `sessionInfo.CSRF`; failure → `renderError(domain.Errorf(domain.CodeForbidden, "cross-site request refused"))`.
- `h_auth.go`: `GET /ui/login` renders `login.html` (form with `hx-boost="false"`, `autocomplete="current-password"`, no hidden inputs); `POST /ui/login`: `checkOrigin` else 403; `Svc.Login(ctx, LoginIn{RemoteKey: clientIP(r), Password: r.PostFormValue("password"), OldCookie: <tr_session cookie or "">})`; ok → set cookie, `303 /ui/`; error → re-render `login.html` with the error and its status (`password_invalid` 403, `rate_limited` 429 + `Retry-After`); never echo the password. `POST /ui/logout`: `Svc.Logout(ctx, cookie)` → clear cookie → `303 /ui/login`.
- `web.go`: chain becomes `securityHeaders → checkHost → routeClass → bodyLimit → session → csrf → mux` with `session`/`csrf` applied only to `/ui/` paths as described.

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

