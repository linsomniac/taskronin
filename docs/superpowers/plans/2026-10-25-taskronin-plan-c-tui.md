# Taskronin Plan C — TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/tui` — the Bubble Tea app behind `tkr tui`, `tkr plan`, `tkr recap` and `tkr triage -i` — with five tabs, Triage-card / Plan / Recap modes, overlays, `$EDITOR` editing and 5-second ETag polling, all through `internal/client`.

**Architecture:** One `tea.Model` (`*App`) owns a tab bar, an optional mode screen, a stack of overlays and an optional one-line form. Every API call is a `tea.Cmd` returning a typed msg; `Update` never blocks. The TUI calls the server through `API`, an exact subset of `*client.Client`'s method set (A§6.7), so tests swap in an in-package fake. Tests drive `*App` synchronously; a few `teatest` tests run the real program; Task 12 runs end-to-end against `testutil.StartServer`.

**Tech Stack:** Go 1.26, `charm.land/bubbletea/v2` v2.1.0, `charm.land/bubbles/v2` v2.2.1 (`key`, `help`, `textinput`, `viewport` only), `charm.land/lipgloss/v2` v2.0.6, test-only `github.com/charmbracelet/x/exp/teatest/v2`.

**Spec:** `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (A§11.1 TUI inventory; A§6.7 client/editor/textsafe/config API; A§1.3 layering; A§6.4–6.6 wire types; A§9.1–9.2 client rules and datespec; A§13.4 lint), `docs/superpowers/specs/2026-10-25-taskronin-design.md` (D§5.2, D§5.5, D§9.2), `docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md`.

## Global Constraints

- Execution context: each implementer gets its task text, these Global Constraints, and read access to the three specs and every plan file. A task may therefore cite a signature by precise location ("A§6.7 `client.Mut`", "Plan C Task 7 Interfaces: `cardDeck`") instead of repeating it; the cited symbol is authoritative.
- Go `1.26.0`, toolchain `go1.26.9`, module `taskronin` (never hard-code `taskronin/` outside imports), Linux only.
- Pinned (A§1.1): `charm.land/bubbletea/v2 v2.1.0`, `charm.land/bubbles/v2 v2.2.1`, `charm.land/lipgloss/v2 v2.0.6`, `github.com/charmbracelet/x/ansi v0.11.9`, `golang.org/x/sys v0.49.0`; test-only `github.com/charmbracelet/x/exp/teatest/v2 v2.0.0-20260311145557-c83711a11ffa` (P1). A dependency is added (`go get <module>@<pin>`, then `go mod tidy`) in the task that first imports it — charm modules in Task 2, teatest in Task 4 — and `go list -m` must then print exactly these versions (verified 2026-10-25 in a scratch module: no bumps).
- Layering (A§1.3): `internal/tui` imports only `client`, `api`, `domain`, `civil`, `datespec`, `textsafe`, `editor`, `config` (+ bubbletea, bubbles, lipgloss); no `clock` (`editor.Drafts` with a nil clock defaults to `clock.Real{}`). `_test.go` files may also import `testutil` and teatest (archtest checks non-test imports only). In `internal/cli`, only `cmd_tui.go` imports `tui`.
- R4: no `time.Now`, no `time.Local` (forbidigo). The TUI shows server time from `api.Clock`; timers only via `tea.Tick`.
- No `fmt.Print*`/`os.Exit` (forbidigo); output only through the Bubble Tea program.
- Lint (A§13.4): gosec is off for `_test.go`; in production code the only gosec suppressions are the two in Task 5, written exactly as given there.
- Every piece of user text (titles, notes, tags, projects, details, snippets, actor names, event JSON) goes through `textsafe.SanitizeLine` (one line) or `textsafe.Sanitize` (notes) before styling; columns are cut with `textsafe.Truncate(s, w, "…")`. Every rendered line is at most the terminal width.
- Every API call is a `tea.Cmd` that returns a typed msg; commands are combined with `tea.Batch` only (never `tea.Sequence` — the synchronous test driver relies on this).
- Mutations of existing records send the record's `etag` as `client.Mut{IfMatch: …}`. On 412: banner `changed elsewhere — refreshed`, refetch, never retry (A§11.1). On `generation_mismatch`: refetch, never resend (the client refreshes its cached generation, A§6.7).
- The TUI's client is `client.NewFromConfig(cfg, "tui", nil)` — channel `tui`, no pending store (A§6.7).
- AIDEV anchors: grep `AIDEV-` in `internal/tui` and `internal/cli` before editing; never delete an `AIDEV-NOTE`. Required here: request-sequence staleness guard and in-flight guard (`app.go`), error/412/generation policy (`banner.go`), ETag poll loop (`poll.go`), draft lifetime (`editor.go`), retry snapshot (`forms.go`), AI-review watermark (`overlay_ai.go`).
- `make check` (fmt-check, vet, golangci-lint incl. gosec, `go test -race ./...`, govulncheck, `go mod verify`) green before every commit. Commits are conventional and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never edit committed migration files; never commit secrets (tests mint tokens at runtime).
- Catalog tests (ruling P5): a test that owns a catalog ID is named `Test<PFX><NN>_<What>`; Plan C owns TRI-17 (Plan A Task 38 `notPlanA`), and Task 12 removes it from `notPlanA`. Other IDs appear only in comments of TUI tests (Plan A owns their service tests).

## Review Focus

1. **The list changes under the cursor** (a poll or own-op refresh removes or reorders rows): the selection follows the task ID; the action sends the selected task's own ETag; a vanished task clamps the cursor and nothing fires against a different task. → Task 6 `TestListSelectionFollowsID`, `TestBacklogActionAfterReorderTargetsSelected`.
2. **Double keypress while a mutation is in flight** (`x x`, `k k` on a card, `enter` twice in a form): exactly one request; later presses are ignored with `busy…`. → Task 2 `TestMutateIgnoresWhileInflight`, Task 7 `TestCardDoubleKeySendsOnce`.
3. **Capture text when the server is down or the outcome is unknown**: the form stays open with the typed text; "not sent" says nothing was saved; "outcome unknown" keeps an immutable snapshot (resolved body, key, generation) and resends exactly that, refusing after a restore or the 7-day window. → Task 3 `TestFormKeepsTextOnFailure`, `TestFormRetrySendsSnapshot`, `TestFormRetryRefusedWhenStale`.
4. **Hostile, long or wide text and tiny terminals**: no ESC/C1/bidi control reaches the screen, no line exceeds the width, no panic at 20×5. → Task 2 `TestRenderSanitizesAndClips`, Task 4 `TestTodayTinyTerminal`.
5. **`$EDITOR` trouble** (unparsable setting, draft not creatable, failing command, unchanged buffer, emptied title, server refusal): no PATCH unless something valid changed; the draft is discarded only after the server confirms or when nothing changed, otherwise its path is shown. → Task 5 `TestEditorFailuresKeepDraft`.

## Consumed contract

All from the architecture or agreed with Plan B: `client` (A§6.7: `Client`, `Mut`, `PageQuery`, `TaskQuery`, `EntryQuery`, `BlockQuery`, `SearchQuery`, the methods listed in Task 1, `ProblemError`, `ErrNotSent`, `ErrOutcomeUnknown`, `ErrGenerationMismatch`, `OutcomeUnknownError`, `NewFromConfig`); `editor` (A§6.7: `Command`, `Drafts{Dir; Clock}`, `(Drafts).New(initial string) (*Draft, error)`, `Draft{Path}`, `(*Draft).Discard() error`; plus Plan B Task 19 `FormatTaskBuffer(title, notes string) string`, `ParseTaskBuffer(s string) (title, notes string, err error)`); `textsafe` (A§6.7: `Sanitize`, `SanitizeLine`, `Width`, `Truncate(s string, width int, tail string) string`); `config` (A§6.7: `Client{Editor, …}`, `Paths{DraftsDir, …}`); `datespec` (A§9.2: `Parse`, `ParseSpan`, `Past`, `Future`, `Ref`); `api` (A§6.5–6.6, incl. `Block.TaskETag`, R4-7); `civil`/`domain` (A§2.2, A§2.4); `testutil.StartServer` (A§13.1); Plan B Task 21 `cli`: `TUIRequest{Mode, Tab, RecapDate string}`, `var launchTUI func(cmd *cobra.Command, req TUIRequest) error`, `envFrom(cmd) *env`, `(*env).clientConfig() (*config.Client, error)`, `(*env).colorEnabled() bool`, `usagef(format string, a ...any) error` (exit 2), env fields `e.paths`, `e.in`, `e.out`, `e.sys.StdinTTY`, `e.sys.StdoutTTY`, `e.sys.Getenv`; Plan A Task 38 `notPlanA` in `internal/service/catalog_coverage_test.go`. Not used: pending requests (`PendingStore`, `Resend`, `Actor`, `ErrActorMismatch`).

## File structure (`internal/tui`)

| File | Responsibility |
|---|---|
| `api.go` | `API` (subset of `*client.Client`) + compile-time assertion |
| `screen.go` | `screen` interface, `Tab`, `Mode`, `ParseTab` |
| `app.go` | `Config`, `App`, `NewApp`, `Init/Update/View`, key routing, overlay stack, request sequencing, in-flight guard, `mutate`, `/today` store |
| `keys.go`, `styles.go`, `banner.go`, `render.go` | bindings + help; plain/colour styles; error policy and effects line; row/clip helpers |
| `forms.go`, `dates.go`, `link.go` | one-line prompts, y/n confirms, retry snapshot; datespec glue; implicit log linking |
| `poll.go`, `tab_today.go` | ETag polling; Today tab |
| `editor.go`, `overlay_detail.go`, `overlay_history.go` | `$EDITOR` flows; detail; history |
| `list.go`, `tab_triage.go`, `tab_backlog.go`, `tab_upcoming.go`, `tab_log.go` | selectable list; list tabs; Log tab |
| `mode_card.go`, `mode_plan.go`, `mode_recap.go` | Triage-card deck; Plan; Recap |
| `overlay_search.go`, `overlay_ai.go` | search; AI changes |
| `run.go` | `RunOptions`, `Run` (pre-flight + program) |
| `fake_test.go`, `helpers_test.go` | fake `API`; synchronous driver |

---

### Task 1: `API` interface and fake

**Files:**
- Create: `internal/tui/doc.go`, `internal/tui/api.go`
- Test: `internal/tui/fake_test.go`, `internal/tui/api_test.go`, `internal/tui/helpers_test.go` (only `eq` for now)

**Interfaces:**
- Consumes: `client` (A§6.7), `api` (A§6.5).
- Produces (`api.go`; signatures copied from A§6.7):
```go
type API interface {
	Clock(ctx context.Context) (*api.Clock, error)
	Generation(ctx context.Context) (string, error)
	Today(ctx context.Context, ifNoneMatch string) (t *api.Today, etag string, notModified bool, err error)
	Day(ctx context.Context, d civil.Date) (*api.Day, error)
	Upcoming(ctx context.Context) (*api.Upcoming, error)
	Search(ctx context.Context, q client.SearchQuery) (*api.Page[api.SearchHit], error)
	ListTasks(ctx context.Context, q client.TaskQuery) (*api.Page[api.Task], error)
	GetTask(ctx context.Context, id int64) (*api.TaskDetail, error)
	TaskHistory(ctx context.Context, id int64, p client.PageQuery) (*api.Page[api.EventView], error)
	ListEntries(ctx context.Context, q client.EntryQuery) (*api.Page[api.Entry], error)
	ListBlocks(ctx context.Context, q client.BlockQuery) ([]api.Block, error)
	AIChanges(ctx context.Context, all bool, p client.PageQuery) (*api.AIChanges, error)
	AddTask(ctx context.Context, m client.Mut, r api.AddTaskRequest) (*api.OpResponse, error)
	UpdateTask(ctx context.Context, m client.Mut, id int64, r api.UpdateTaskRequest) (*api.OpResponse, error)
	PlaceTask(ctx context.Context, m client.Mut, id int64, r api.PlaceRequest) (*api.OpResponse, error)
	DeferTask(ctx context.Context, m client.Mut, id int64, r api.DeferRequest) (*api.OpResponse, error)
	BlockTask(ctx context.Context, m client.Mut, id int64, r api.BlockRequest) (*api.OpResponse, error)
	CompleteTask(ctx context.Context, m client.Mut, id int64, r api.CompleteRequest) (*api.OpResponse, error)
	DropTask(ctx context.Context, m client.Mut, id int64) (*api.OpResponse, error)
	AcceptNew(ctx context.Context, m client.Mut, r api.AcceptNewRequest) (*api.OpResponse, error)
	AddEntry(ctx context.Context, m client.Mut, r api.AddEntryRequest) (*api.OpResponse, error)
	UpdateEntry(ctx context.Context, m client.Mut, id int64, r api.UpdateEntryRequest) (*api.OpResponse, error)
	RelinkEntry(ctx context.Context, m client.Mut, id int64, r api.RelinkRequest) (*api.OpResponse, error)
	DeleteEntry(ctx context.Context, m client.Mut, id int64) (*api.OpResponse, error)
	Reblock(ctx context.Context, m client.Mut, blockID int64, r api.ReblockRequest) (*api.OpResponse, error)
	Unblock(ctx context.Context, m client.Mut, blockID int64) (*api.OpResponse, error)
	SetOutcome(ctx context.Context, m client.Mut, blockID int64, r api.OutcomeRequest) (*api.OpResponse, error)
	SetFocus(ctx context.Context, m client.Mut, r api.FocusRequest) (*api.OpResponse, error)
	Revert(ctx context.Context, m client.Mut, opID string) (*api.OpResponse, error)
	Undo(ctx context.Context, m client.Mut) (*api.OpResponse, error)
	Redo(ctx context.Context, m client.Mut) (*api.OpResponse, error)
	ReviewAIChanges(ctx context.Context, m client.Mut, r api.ReviewRequest) (*api.OpResponse, error)
}
var _ API = (*client.Client)(nil)
```
- Test-side (`fake_test.go`, used by every later task):
```go
type call struct {
	Method string
	ID     int64      // task/entry/block id when the method takes one
	Mut    client.Mut // mutations
	INM    string     // Today's ifNoneMatch
	Q      any        // query struct / civil.Date / opID / all-flag for reads and Revert
	Req    any        // request body
}
type fakeAPI struct {
	mu        sync.Mutex
	calls     []call
	clock     api.Clock
	gen       string                // returned by Generation; tests change it to simulate a restore
	today     api.Today; todayETag string
	tasks     map[string][]api.Task // by view
	upcoming  api.Upcoming
	entries   []api.Entry; entriesNext *string
	days      map[civil.Date]api.Day
	blocks    []api.Block
	details   map[int64]api.TaskDetail
	history   map[int64][]api.EventView
	hits      []api.SearchHit
	aiPages   []api.AIChanges       // page i is returned for the i-th distinct cursor ("" first)
	resp      api.OpResponse        // returned (copied) by every mutation
	errs      map[string]error      // Method → error, one-shot
}
func newFake() *fakeAPI                                // clock = testClock(); gen = clock.Generation; resp.OpID non-nil; empty maps
func (f *fakeAPI) count(method string) int
func (f *fakeAPI) mustLast(t *testing.T, method string) call
func (f *fakeAPI) setToday(td api.Today, etag string)  // under f.mu (teatest tests change it concurrently)
func testClock() api.Clock // Now 2026-10-28T10:00:00.000-06:00, Today 2026-10-28, "America/Denver", "monday", DueSoonDays 3, Generation "0123456789abcdef"
func task(id int64, title string, reasons ...string) api.Task // Status "open", View "triage" if reasons else "backlog", ETag "g-<id>"
func problem(code string, status int, detail string) error    // &client.ProblemError{Status: status, Problem: api.Problem{Code: code, Status: status, Detail: detail}}
func notSent() error                                          // fmt.Errorf("dial: %w", client.ErrNotSent)
func outcomeUnknown(key string) error                         // &client.OutcomeUnknownError{Key: key, Err: client.ErrOutcomeUnknown}
```
`Today` returns `notModified=true` when `ifNoneMatch == f.todayETag && ifNoneMatch != ""`. Paged reads return one page (`NextCursor nil`) except `ListEntries` (returns `entriesNext` on the first page) and `AIChanges` (`aiPages`). Mutations never change fake data; tests set post-op data themselves.

- [ ] **Step 1: Write the failing tests**

```go
func TestFakeImplementsAPI(t *testing.T) { var _ API = newFake() }
func TestFakeErrorsClassify(t *testing.T) {
	var pe *client.ProblemError
	if !errors.As(problem("precondition_failed", 412, "x"), &pe) || pe.Problem.Code != "precondition_failed" { t.Fatal("problem") }
	if !errors.Is(notSent(), client.ErrNotSent) || !errors.Is(outcomeUnknown("K"), client.ErrOutcomeUnknown) { t.Fatal("sentinels") }
}
func TestFakeTodayETag(t *testing.T) {
	f := newFake(); f.todayETag = `"a-1"`
	_, _, nm, _ := f.Today(t.Context(), `"a-1"`); if !nm { t.Fatal("want 304") }
	_, et, nm, _ := f.Today(t.Context(), ""); if nm || et != `"a-1"` { t.Fatal(et) }
	eq(t, f.mustLast(t, "Today").INM, "")
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestFake' -race -count=1` → FAIL (`undefined: API`).

- [ ] **Step 3: Implement `api.go`** (interface and assertion above; `doc.go` holds the package comment). No new module is needed: Task 1 imports only internal packages. If `*client.Client` fails the assertion, the client deviates from A§6.7: stop and report BLOCKED with the compiler output — do not change the interface.

- [ ] **Step 4: Run to see them pass** — `go test ./internal/tui ./internal/archtest -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): the TUI's client API subset and its test fake` (+ trailer).

---

### Task 2: App shell — tabs, overlays, keys, styles, error policy, request guards

**Files:**
- Create: `internal/tui/screen.go`, `app.go`, `keys.go`, `styles.go`, `banner.go`, `render.go`
- Modify: `go.mod`, `go.sum`, `internal/tui/helpers_test.go` (driver below)
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: Task 1 `API`, fake.
- Produces:
```go
type Tab int;  const (TabToday Tab = iota; TabTriage; TabBacklog; TabUpcoming; TabLog)
type Mode int; const (ModeTabs Mode = iota; ModeTriage; ModePlan; ModeRecap)
func ParseTab(s string) (Tab, error) // today|triage|backlog|upcoming|log
const DefaultPollInterval = 5 * time.Second // A§14 D25

type screen interface {
	load(a *App) tea.Cmd                              // (re)fetch; nil if nothing to fetch
	handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) // true = key consumed
	update(a *App, msg tea.Msg) tea.Cmd               // data msgs (screens ignore msgs they do not own)
	view(a *App, w, h int) string
	selection() (taskID int64, title string, ok bool) // for `l` linking
}
type stubScreen struct{ name string } // placeholder for screens not built yet: renders name, loads nothing
type EditorEnv struct {
	Setting   string              // config.Client.Editor
	Getenv    func(string) string
	DraftsDir string              // config.Paths.DraftsDir
	Exec      func(*exec.Cmd, tea.ExecCallback) tea.Cmd // nil → tea.ExecProcess
}
type Config struct {
	API          API
	Ctx          context.Context
	Clock        api.Clock     // pre-flight value; replaced by every /today
	StartTab     Tab
	StartMode    Mode
	RecapDate    civil.Date    // ModeRecap; zero → Clock.Today
	PollInterval time.Duration // 0 → DefaultPollInterval; < 0 → polling off (tests)
	Color        bool
	Editor       EditorEnv
}
type banner struct{ kind bannerKind; text string } // kind: bannerInfo | bannerError
type App struct { /* unexported: cfg, api, ctx, width, height, clock api.Clock, today *api.Today, todayETag string,
	tab Tab, tabs [5]screen, mode Mode, modeScreen screen, exitWithMode bool,
	overlays []screen /* stack; top is current */, form screen /* open prompt (Plan C Task 3 *form); nil when closed */,
	banner banner, inflight, quitting bool, reqSeq map[string]int, styles Styles, keys keyMap, help help.Model */ }
func NewApp(cfg Config) *App          // stubScreen per tab; later tasks install real screens and modes here
func (a *App) Init() tea.Cmd          // tea.Batch(fetchToday(false), current().load(a))
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (a *App) View() tea.View         // AltScreen = true; layout below
func (a *App) current() screen        // top overlay > modeScreen > tabs[tab] (never the form)
func (a *App) nextReq(target string) int
func (a *App) latest(target string, req int) bool
type todayMsg struct{ req int; poll bool; today *api.Today; etag string; notModified bool; err error }
func (a *App) fetchToday(poll bool) tea.Cmd // If-None-Match = a.todayETag when poll, "" otherwise
func (a *App) refresh() tea.Cmd       // tea.Batch(fetchToday(false), current().load(a))
func (a *App) mutate(label string, call func(ctx context.Context, c API) (*api.OpResponse, error),
	after func(a *App, r *api.OpResponse) tea.Cmd, onErr func(a *App, err error)) tea.Cmd
type opMsg struct{ label string; resp *api.OpResponse; err error; after func(*App, *api.OpResponse) tea.Cmd; onErr func(*App, error) }
func (a *App) setBanner(k bannerKind, text string)
func (a *App) appendBanner(s string)
func (a *App) handleErr(err error) tea.Cmd
func effectsLine(effs []api.Effect) string
func (a *App) line(s string, w int) string // SanitizeLine + Truncate(w, "…")
func clip(lines []string, w, h int) string // ≤ h lines, each Truncate(w, "…")
func (a *App) setTabScreen(t Tab, s screen)
func (a *App) enterMode(m Mode, s screen) tea.Cmd; func (a *App) exitMode() tea.Cmd // exitMode quits when exitWithMode
func (a *App) openOverlay(s screen) tea.Cmd;     func (a *App) closeOverlay()       // push / pop
```
- Test driver (`helpers_test.go`):
```go
func newTestApp(t *testing.T, f *fakeAPI, opts ...func(*Config)) *App // Ctx t.Context(), Clock f.clock, PollInterval -1, Color false; WindowSizeMsg 100×30; drains Init
func key(s string) tea.KeyPressMsg // "enter"→{Code: tea.KeyEnter}, "esc"→KeyEscape, "tab"→KeyTab, "ctrl+r"/"ctrl+c"→{Code: 'r'/'c', Mod: tea.ModCtrl}, one rune→{Code: r, Text: s}
func press(t *testing.T, a *App, keys ...string) // per key: a.Update(key(k)), then drain the cmd
func typeText(t *testing.T, a *App, s string)
func drain(t *testing.T, a *App, cmd tea.Cmd)   // run cmd synchronously (t.Fatal if > 2 s), feed msg to Update; expand tea.BatchMsg; stop at tea.QuitMsg; ≤ 1000 msgs
func viewText(a *App) string                    // a.View().Content
func contains(s string) func([]byte) bool       // for teatest.WaitFor
func ptr[T any](v T) *T
type recScreen struct{ stubScreen; loads, keys int } // counts load() calls and keys offered; consumes none
```
Verified on the pinned versions: `KeyPressMsg.String()` is `"J"` for `{Code: 'J', Text: "J"}`, and `"ctrl+r"`, `"enter"`, `"esc"`, `"tab"` for those keys; `key.Matches` compares against `String()`.

- [ ] **Step 1: Write the failing tests** (`app_test.go`)

```go
func TestTabKeys(t *testing.T) {
	a := newTestApp(t, newFake())
	for _, c := range []struct{ k string; want Tab }{{"2", TabTriage}, {"3", TabBacklog}, {"4", TabUpcoming}, {"5", TabLog}, {"1", TabToday}, {"tab", TabTriage}} {
		press(t, a, c.k); eq(t, a.tab, c.want)
	}
}
func TestQuitKeys(t *testing.T) { /* "q" in ModeTabs → a.quitting; fresh app, "ctrl+c" → a.quitting */ }
func TestHelpToggle(t *testing.T) { /* "?" → viewText contains "ctrl+r redo"; "?" again → it does not */ }
func TestGlobalsInModes(t *testing.T) {
	f := newFake(); a := newTestApp(t, f); m := &recScreen{}
	drain(t, a, a.enterMode(ModeTriage, m))
	press(t, a, "?"); if !strings.Contains(viewText(a), "ctrl+r redo") { t.Fatal("help") } // global fallback when the mode does not consume the key (u/ctrl+r/"/" are tested in Task 11)
	press(t, a, "2"); eq(t, a.mode, ModeTriage)     // tab keys only in ModeTabs
	press(t, a, "q"); eq(t, a.mode, ModeTabs)       // mode entered from tabs: q returns to tabs
}
func TestOverlayStack(t *testing.T) {
	a := newTestApp(t, newFake()); s1, s2 := &recScreen{}, &recScreen{}
	drain(t, a, a.openOverlay(s1)); drain(t, a, a.openOverlay(s2))
	if a.current() != screen(s2) { t.Fatal("top") } // identity, not DeepEqual (both are zero recScreens)
	press(t, a, "esc"); if a.current() != screen(s1) { t.Fatal("pop") }
	press(t, a, "esc"); if a.current() != a.tabs[TabToday] { t.Fatal("tab") }
}
func TestMutateIgnoresWhileInflight(t *testing.T) { // Review Focus 2
	f := newFake(); a := newTestApp(t, f)
	undo := func(ctx context.Context, c API) (*api.OpResponse, error) { return c.Undo(ctx, client.Mut{}) }
	cmd := a.mutate("undo", undo, nil, nil)
	if a.mutate("undo", undo, nil, nil) != nil { t.Fatal("second mutate must return nil") }
	if !strings.Contains(viewText(a), "busy") { t.Fatal("no busy banner") }
	drain(t, a, cmd); eq(t, f.count("Undo"), 1); eq(t, a.inflight, false)
}
func TestHandleErrPolicy(t *testing.T) {
	cases := []struct{ err error; banner string; refreshes bool }{
		{problem("precondition_failed", 412, "x"), "changed elsewhere — refreshed", true},
		{problem("generation_mismatch", 412, "x"), "database was restored — refreshed", true},
		{problem("nothing_to_undo", 409, "x"), "nothing to undo", false},
		{problem("nothing_to_redo", 409, "x"), "nothing to redo", false},
		{problem("rate_limited", 429, "x"), "server busy — try again", false},
		{problem("busy", 503, "x"), "server busy — try again", false},
		{notSent(), "cannot reach the Taskronin server — nothing was saved", false},
		{outcomeUnknown("K1"), "outcome unknown — refreshed; check before repeating", true},
		{&client.ProblemError{Status: 422, Problem: api.Problem{Code: "validation", Detail: "bad", Errors: []api.FieldError{{Field: "span", Message: "past"}}}}, "bad (span: past)", false},
	}
	for _, c := range cases {
		f := newFake(); a := newTestApp(t, f); n := f.count("Today")
		drain(t, a, a.handleErr(c.err))
		eq(t, a.banner.text, c.banner); eq(t, f.count("Today") > n, c.refreshes)
	}
}
func TestEffectsLine(t *testing.T) {
	eq(t, effectsLine([]api.Effect{{Kind: "block_cancelled", BlockID: 7}, {Kind: "slipped", BlockID: 7},
		{Kind: "reason_added", TaskID: 42, Reason: "slipped"}, {Kind: "updated", TaskID: 42}}), "b7 cancelled · b7 slipped · #42 +slipped")
}
func TestRenderSanitizesAndClips(t *testing.T) { // Review Focus 4
	a := newTestApp(t, newFake())
	out := a.line("evil\x1b[31mred\u202e"+strings.Repeat("宽", 200), 40)
	if strings.ContainsAny(out, "\x1b\u202e") || lipgloss.Width(out) > 40 { t.Fatalf("%q", out) }
	a.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	for _, l := range strings.Split(viewText(a), "\n") { if lipgloss.Width(l) > 20 { t.Fatal(l) } }
	if n := strings.Count(viewText(a), "\n"); n > 4 { t.Fatal(n) }
}
func TestTodayMsgStoresAggregate(t *testing.T) {
	f := newFake(); f.today = api.Today{Clock: f.clock, AIChanges: 2, Banners: []api.Banner{{Kind: "backup_failed"}}}; f.todayETag = `"a-1"`
	a := newTestApp(t, f) // Init fetched /today
	eq(t, a.todayETag, `"a-1"`); if !strings.Contains(viewText(a), "⚠ backup") || !strings.Contains(viewText(a), "ai 2") { t.Fatal(viewText(a)) }
	r1 := a.nextReq("today"); r2 := a.nextReq("today")
	a.Update(todayMsg{req: r2, today: &api.Today{Clock: f.clock}, etag: `"a-3"`}); a.Update(todayMsg{req: r1, today: &api.Today{Clock: f.clock}, etag: `"a-2"`})
	eq(t, a.todayETag, `"a-3"`) // the older response is dropped
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestTabKeys|TestQuit|TestHelp|TestGlobals|TestOverlay|TestMutate|TestHandleErr|TestEffects|TestRender|TestTodayMsg' -race -count=1` → FAIL (`undefined: NewApp`).

- [ ] **Step 3: Implement**, then add the modules this code imports: `go get charm.land/bubbletea/v2@v2.1.0 charm.land/bubbles/v2@v2.2.1 charm.land/lipgloss/v2@v2.0.6 && go mod tidy`, and check `go list -m charm.land/bubbletea/v2 charm.land/bubbles/v2 charm.land/lipgloss/v2 github.com/charmbracelet/x/ansi golang.org/x/sys` prints v2.1.0, v2.2.1, v2.0.6, v0.11.9, v0.49.0.
  - `todayMsg` (AIDEV-NOTE: request-sequence staleness guard): drop when `!latest("today", req)`; error → `handleErr`; `notModified` → nothing; else store `*today`, `todayETag`, `clock = today.Clock`.
  - Key routing in `Update`, in order: `ctrl+c` quits; an open `form` gets every key; the top overlay (`esc` → `closeOverlay`); the mode screen (`q`/`esc` → `exitMode`); in `ModeTabs` the tab screen's `handle`. Keys nobody consumed fall back to globals: `a`, `l`, `/`, `u`, `ctrl+r`, `?`, `esc` (clears banner) everywhere; `1`–`5`, `tab`/`shift+tab`, `A` (AI overlay) and `q` (quit) only in `ModeTabs` with no overlay. `up`/`down` alias `k`/`j` in every list. Globals whose task has not landed yet are no-ops.
  - `View()`: header line; `current().view(a, w, h-reserved)`; when `a.form != nil`, `a.form.view(a, w, 3)` in the lines just above the bottom line; bottom line = banner, else short help. Everything goes through `clip(…, width, height)`.
  - `mutate` (AIDEV-NOTE: in-flight guard): if `a.inflight` → banner `busy…`, return nil; else set `inflight` and return a cmd running `call(a.ctx, a.api)` → `opMsg`. On `opMsg`: clear `inflight`; error → `handleErr`, then `onErr` if set; success → banner `label` + `" · " + effectsLine` (when non-empty) + `" · ⚠ " + SanitizeLine(w)` per warning, then `a.refresh()`, batched with `after(a, resp)` only when `after != nil`.
  - `handleErr` (`banner.go`, AIDEV-NOTE: error policy) implements the test table: `errors.As` `*client.ProblemError` → by `Problem.Code`; `errors.Is(err, client.ErrNotSent)`; `errors.As` `*client.OutcomeUnknownError`; 412, `generation_mismatch` and outcome-unknown return `a.refresh()`; any other problem → sanitized `Detail` + ` (field: message; …)` when `Errors` is set; anything else → sanitized `err.Error()`. Never re-sends.
  - `effectsLine` (others omitted; joined ` · `): `slipped`→`b<B> slipped`, `unslipped`→`b<B> unslipped`, `reason_added`→`#<T> +<reason>`, `interrupted`→`interrupted #<T>`, `focus_set`→`focus #<T>`, `focus_cleared`→`focus cleared`, `block_cancelled`→`b<B> cancelled`, `block_resolved`→`b<B> <To, or "resolved">`, `completed`→`#<T> completed`, `entry_logged`→`e<E> logged`, `reverted`→`reverted`.
  - `styles.go`: `type Styles struct{ Header, TabOn, TabOff, Selected, Dim, Warn, Err, Info, Current lipgloss.Style }`; `newStyles(color bool)`: when `!color` every field is a bare `lipgloss.NewStyle()` and help uses `help.Styles{}`, so plain mode emits no escape codes. Selected rows start with `› ` (others `  `) in both modes.
  - Header: `Taskronin  <Wed 2026-10-28 10:00 from clock.Now>` + `[1 Today] 2 Triage 3 Backlog 4 Upcoming 5 Log` + warnings from the last `/today`: `⚠ backup` (banner kinds `backup_failed`/`backup_stale`), `⚠ purge` (`purge_incomplete`/`purge_ledger_damaged`), `ai N` (`AIChanges > 0`).

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): app shell with tabs, overlays, error policy and request guards` (+ trailer; `go.mod`/`go.sum` included).

---

### Task 3: Forms with retry snapshots, datespec glue, `a` add and `l` log

**Files:**
- Create: `internal/tui/forms.go`, `internal/tui/dates.go`, `internal/tui/link.go`
- Modify: `internal/tui/app.go` (globals `a`, `l`)
- Test: `internal/tui/forms_test.go`

**Interfaces:**
- Consumes: Plan C Task 2 Interfaces (`mutate`, `handleErr`, `setBanner`, `fetchToday`, `screen.selection`); `datespec` (A§9.2).
- Produces:
```go
// sendFunc performs one mutation whose request body was resolved when the form was submitted;
// key "" lets the client mint an Idempotency-Key.
type sendFunc func(ctx context.Context, c API, key string) (*api.OpResponse, error)
type formRetry struct {      // immutable snapshot kept after an outcome-unknown reply
	value, key, gen string   // input text, the client's Idempotency-Key, Generation() at send time
	at   time.Time           // a.clock.Now read in Update when the FIRST attempt is submitted (before the cmd runs); never updated later
	send sendFunc            // the same resolved request
}
type form struct {           // implements screen; stored in App.form
	title, hint string       // e.g. "Add task", "Log → #42 (focus)"
	input   textinput.Model  // Styles().Cursor.Blink = false (no blink cmds)
	errText string           // local parse error under the input; nothing sent
	waiting bool             // submitted; the form closes only on success
	retry   *formRetry
	submit  func(a *App, value string) (label string, send sendFunc, err error) // parse + resolve once; err → errText
	after   func(a *App, r *api.OpResponse) tea.Cmd                             // success hook for modes; may be nil
}
func (a *App) openForm(title, hint, initial string,
	submit func(a *App, value string) (string, sendFunc, error), after func(*App, *api.OpResponse) tea.Cmd) tea.Cmd
var errRetryStale = errors.New("retry snapshot is stale")
func (a *App) confirm(question string, yes func(*App) tea.Cmd) tea.Cmd // "y" runs yes; any other key cancels
func (a *App) ref() datespec.Ref        // {a.clock.Today, weekdayOf(a.clock.WeekStart)}
func weekdayOf(s string) time.Weekday   // "monday".."sunday"; unknown → Monday
func (a *App) parseMoment(s string, m datespec.Mode) (api.Moment, error)
func (a *App) parseSpan(s string) (api.BlockRequest, error)               // datespec.Future
func (a *App) parseDate(s string, m datespec.Mode) (civil.Date, error)    // rejects a time part
func parseTaskRef(s string) (id int64, none bool, err error)              // "42" | "#42" | "-" (none)
type link struct{ TaskID int64; Has bool; Why string } // Why: "selected" | "focus" | "current block" | "no task"
func (a *App) logLink() link
func (l link) label() string // "→ #42 (focus)", "→ #2 (current block)", "→ #7 (selected)", "→ no task"
func (a *App) openLog(date civil.Date, l link) tea.Cmd // AddEntry form; WorkDate set only when date ≠ clock.Today
func (a *App) openAdd() tea.Cmd
```
`enter` in a form (AIDEV-NOTE: retry snapshot): if `retry != nil` and the input equals `retry.value` → run one `mutate` whose call first reads fresh server time with `c.Clock(ctx)` (`Now − retry.at > 7 × 24h` → return `errRetryStale`, the idempotency window has passed), then compares `c.Generation(ctx)` with `retry.gen` (different → `errRetryStale`), and only then calls `retry.send(ctx, c, retry.key)`. Stale (either way) → `retry = nil`, banner `the database may have changed since the first attempt (restore or 7-day limit) — check, then enter sends a new request`. Otherwise (no snapshot, or the text changed) → `retry = nil`, `submit`, `at := a.clock.Now.Time` taken right here in `Update` (the last server time, never later than the real send, so the window can only end early), and `mutate` whose call records `gen := c.Generation(ctx)` before `send(ctx, c, "")`. Success → `a.form = nil` (untyped nil), then `after`. Error → `waiting = false`, text kept; on `*client.OutcomeUnknownError` store `retry = &formRetry{value, err.Key, gen, at, send}` (the `at` captured before sending, not the time the failure arrived) for a first attempt, but leave an existing snapshot untouched when a resend of it ends outcome-unknown again and banner `outcome unknown — enter resends the same request`. (`gen` is written inside the cmd and read in `onErr` after the `opMsg` arrives; the program's message channel orders the two.)

- [ ] **Step 1: Write the failing tests**

```go
func TestAddTask(t *testing.T) {
	f := newFake(); a := newTestApp(t, f)
	press(t, a, "a"); typeText(t, a, "Restart stuck pg replica")
	if !strings.Contains(viewText(a), "Restart stuck pg replica") { t.Fatal("form not rendered by App.View") }
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddTask").Req, any(api.AddTaskRequest{Title: "Restart stuck pg replica"})) // never a placement (D§5.5)
	if a.form != nil { t.Fatal("form must close on success") }
}
func TestFormKeepsTextOnFailure(t *testing.T) { // Review Focus 3
	f := newFake(); f.errs["AddTask"] = notSent()
	a := newTestApp(t, f); press(t, a, "a"); typeText(t, a, "Call Bob"); press(t, a, "enter")
	fm := a.form.(*form); eq(t, fm.input.Value(), "Call Bob"); eq(t, fm.waiting, false)
	if !strings.Contains(viewText(a), "nothing was saved") { t.Fatal("banner") }
	f.errs["AddTask"] = outcomeUnknown("K1"); press(t, a, "enter")
	if !strings.Contains(viewText(a), "enter resends the same request") { t.Fatal("banner") }
	press(t, a, "enter"); eq(t, f.mustLast(t, "AddTask").Mut.Key, "K1") // a replay, never a duplicate
}
func TestFormRetrySendsSnapshot(t *testing.T) { // midnight between attempts
	// clock Wed 2026-10-28 23:59; a test form (openForm) whose submit resolves a.parseMoment(value, datespec.Future) and sends
	// DeferTask(Mut{IfMatch: "g-1", Key: key}, 1, {Until: {Set, V: m}}); type "tomorrow", f.errs["DeferTask"] = outcomeUnknown("K2"), enter;
	// todayMsg with Clock.Today 2026-10-29; enter → DeferTask Mut.Key "K2", Until.V.Date 2026-10-29 (the snapshot, not 10-30)
	// editing the text first ("tomorrow " → "tomorrow") → Mut.Key "" and the body re-resolved
}
func TestFormRetryRefusedWhenStale(t *testing.T) {
	// as above up to the outcome-unknown reply; f.gen = "fedcba9876543210" (restore); enter → no DeferTask call, banner contains
	// "may have changed since the first attempt"; enter again → DeferTask with Mut.Key "" (new request, body re-resolved).
	// Window: submit at T0 (a.clock.Now); before the outcome-unknown opMsg is delivered, a todayMsg moves a.clock.Now to T0 + 5 s → snapshot at == T0;
	// f.clock.Now = T0 + 6 d, resend → outcome unknown again (snapshot keeps at = T0);
	// f.clock.Now = T0 + 7 d + 1 s (only the fake's /clock moves; no todayMsg) → enter → a Clock call, no DeferTask, same refusal banner.
}
func TestLogImplicitLink(t *testing.T) { // FOC-04, FOC-05, FOC-06 (TUI side)
	cases := []struct{ focus *api.Task; cur *api.Block; want api.Opt[int64]; label string }{
		{ptr(task(1, "A")), &api.Block{ID: 9, TaskID: 2}, api.Opt[int64]{Set: true, V: 1}, "→ #1 (focus)"},
		{nil, &api.Block{ID: 9, TaskID: 2}, api.Opt[int64]{Set: true, V: 2}, "→ #2 (current block)"},
		{nil, nil, api.Opt[int64]{Set: true, Null: true}, "→ no task"},
	}
	for _, c := range cases {
		f := newFake(); f.setToday(api.Today{Clock: f.clock, Focus: c.focus, CurrentBlock: c.cur}, `"a-1"`)
		a := newTestApp(t, f); press(t, a, "l") // Today tab (stub until Task 4), nothing selected
		if !strings.Contains(viewText(a), c.label) { t.Fatal(c.label) }
		typeText(t, a, "text"); press(t, a, "enter")
		eq(t, f.mustLast(t, "AddEntry").Req, any(api.AddEntryRequest{TaskID: c.want, Title: "text"}))
	}
}
func TestLogSelectedTaskWins(t *testing.T) { // FOC-08 (TUI side): a tab screen whose selection() is (7, "X", true) → "→ #7 (selected)", TaskID {Set, V: 7} }
func TestDatespecModes(t *testing.T) { // DAT-02, DAT-04, DAT-09, DAT-12 (TUI side; now Wed 2026-10-28 10:00)
	a := newTestApp(t, newFake())
	m, _ := a.parseMoment("thu", datespec.Future); eq(t, m, api.Moment{Date: civil.MustDate("2026-10-29")})
	m, _ = a.parseMoment("wed", datespec.Future); eq(t, m.Date, civil.MustDate("2026-11-04"))
	m, _ = a.parseMoment("mon 09:00", datespec.Future); eq(t, *m.Time, civil.Minute(540))
	s, _ := a.parseSpan("fri 13:00-15:00"); eq(t, s, api.BlockRequest{Date: civil.MustDate("2026-10-30"), Start: 780, End: 900})
	s, _ = a.parseSpan("23:00-24:00"); eq(t, s.End, civil.EndMinute(1440))
	d, _ := a.parseDate("tue", datespec.Past); eq(t, d, civil.MustDate("2026-10-27"))
	d, _ = a.parseDate("wed", datespec.Past); eq(t, d, civil.MustDate("2026-10-28"))
	if _, err := a.parseMoment("nextweek", datespec.Future); err == nil { t.Fatal("DAT-12") }
	if _, err := a.parseDate("tue 10:00", datespec.Past); err == nil { t.Fatal("date only") }
}
func TestFormParseErrorSendsNothing(t *testing.T) { /* a form whose submit uses parseSpan: "25:00-26:00" → errText set, form open, zero API mutations */ }
func TestFormAfterHookRunsOnSuccessOnly(t *testing.T) { /* openForm with an after hook counting calls: success → 1; a failing send → 0 and the form stays */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestAddTask|TestForm|TestLog|TestDatespec' -race -count=1` → FAIL.

- [ ] **Step 3: Implement.** `logLink`: `current().selection()` → `selected`; else `today.Focus` → `focus`; else `today.CurrentBlock.TaskID` → `current block`; else none. Opening the log form also issues `fetchToday(false)` and recomputes the title when it arrives, so the echoed link is fresh (D§5.2); submit sends that link as an explicit `task_id`. Success banners: `#<id> added to triage (new)` (from `resp.Task`), `logged → #42` / `logged (no task)`.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): forms with retry snapshots, add and log with implicit linking` (+ trailer).

---

### Task 4: Today tab and ETag polling

**Files:**
- Create: `internal/tui/tab_today.go`, `internal/tui/poll.go`
- Modify: `go.mod`, `go.sum` (teatest), `internal/tui/app.go` (install the Today screen; `Init` adds `pollTick`; P9 reload)
- Test: `internal/tui/today_test.go`, `internal/tui/poll_test.go`

**Interfaces:**
- Consumes: Plan C Tasks 2–3 Interfaces (`todayMsg`, `fetchToday`, `mutate`, `openForm`, `parseSpan`, `openLog`, `logLink`, `line`, `clip`).
- Produces:
```go
type pollMsg struct{}
func (a *App) pollTick() tea.Cmd // tea.Tick(PollInterval) → pollMsg; nil when PollInterval < 0
func hhmm(m int) string          // 780 → "13:00", 1440 → "24:00"
func spanText(start civil.Minute, end civil.EndMinute) string // "13:00–15:00" (en dash)
func interruptionLine(i api.Interruption, titles map[int64]string, blocks map[int64]api.Block) string
func titlesFrom(td *api.Today) map[int64]string // focus, current block, blocks, triage top, overdue, due soon, entries
```
Today rows are its blocks then the triage-top tasks; the cursor starts unselected (`l` then uses the implicit rule); `j` selects the first row, `esc` clears the selection.

- [ ] **Step 1: Write the failing tests**

```go
func todayFixture() api.Today // Clock testClock(); Focus #42 "Set up keycloak postgres cluster";
	// Blocks (served order; TaskETag "g-<task id>"): b8 #57 08:00–09:00 planned Ended NeedsReblock; b9 #42 09:00–11:00 planned Current; b7 #42 13:00–15:00 planned;
	// TriageCount 3, TriageTop [#57 {new}]; Entries [e9 #60 TaskTitle "Restart stuck pg replica", Time 10:20, "Restarted replica"];
	// Interruptions [{At 10:20, Date 10-28, FromTaskID 42, FromBlockID 9, ToTaskID 60}]; AIChanges 2; Banners [{backup_failed, "disk full"}]
func fakeWithToday(td api.Today) *fakeAPI                // newFake() + setToday(td, `"a-1"`)
func withFocusTitle(td api.Today, title string) api.Today // copy with Focus.Title replaced
func TestTodayRender(t *testing.T) { // BLK-48, BLK-17/RCP-06, INT-08 (TUI side)
	v := viewText(newTestApp(t, fakeWithToday(todayFixture())))
	for _, s := range []string{"▶ #42 Set up keycloak postgres cluster (→11:00)", "b8 08:00–09:00 #57", "reblock?",
		"b9 09:00–11:00 #42", "◀ now", "Triage 3", "10:20 #60 'Restart stuck pg replica' interrupted #42 (block 09:00–11:00)",
		"AI changes: 2 unreviewed — A to review", "⚠ backup"} {
		if !strings.Contains(v, s) { t.Fatalf("missing %q in\n%s", s, v) }
	}
}
func TestTodayNoFocusCurrentBlock(t *testing.T) { /* Focus nil, CurrentBlock b9 (#42, end 11:00) → "▷ #42 Set up keycloak postgres cluster (→11:00)"; neither → "no focus" */ }
func TestTodayActions(t *testing.T) {
	// before any "j": "U" → banner "select a row first (j)", no call.
	// "j" selects b8: "B" → form; "15:00-16:00" enter → Reblock{ID 8, Mut{IfMatch: b8.ETag}, Req {BlockRequest{10-28, 900, 960}, TaskETag: b8.TaskETag}}, no GetTask;
	//   "f" → SetFocus{Req {TaskID {Set, V: 57}}};
	// "j" "j" selects b7: "U" → Unblock{ID 7, Mut{IfMatch: b7.ETag}}; "f" (b7's #42 is the focus) → SetFocus{TaskID {Set, Null: true}};
	// "x" → CompleteTask{ID 42, Mut{IfMatch: focus.ETag}, Req api.CompleteRequest{}}; with Focus nil → banner "no focus task", no call.
}
func TestTodayRefreshAfterOwnOp(t *testing.T) { // REC-05 (TUI side): before draining "x", setToday(focus nil); the op's refresh shows "no focus" }
func TestTodayTinyTerminal(t *testing.T) { // Review Focus 4: WindowSizeMsg{20, 5} → no panic, ≤ 5 lines, each width ≤ 20 }
// poll_test.go
func TestPollNotModifiedKeepsState(t *testing.T) {
	f := fakeWithToday(todayFixture()); a := newTestApp(t, f)
	before := viewText(a); drain(t, a, func() tea.Msg { return pollMsg{} }); eq(t, viewText(a), before)
	eq(t, f.mustLast(t, "Today").INM, `"a-1"`) // the poll sent If-None-Match
}
func TestPollChangeRefetchesVisibleTab(t *testing.T) { /* recScreen on TabBacklog, visible: a 200 poll raises its loads by 1; a 304 poll does not */ }
func TestPollingRunsUnderTeatest(t *testing.T) { // the real program, PollInterval 20 ms
	f := fakeWithToday(todayFixture())
	tm := teatest.NewTestModel(t, NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, PollInterval: 20 * time.Millisecond}), teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), contains("Set up keycloak"), teatest.WithDuration(3*time.Second)) // initial fetch rendered first
	n := f.count("Today")
	f.setToday(withFocusTitle(todayFixture(), "Renamed elsewhere"), `"a-2"`)
	teatest.WaitFor(t, tm.Output(), contains("Renamed elsewhere"), teatest.WithDuration(3*time.Second))
	if f.count("Today") <= n { t.Fatal("no poll after the change") }
	// at least one Today call after n carried INM `"a-1"` (a conditional poll, not a fresh load)
	tm.Send(key("q")); tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestToday|TestPoll' -race -count=1` → FAIL.

- [ ] **Step 3: Implement**, and add teatest now that a test imports it: `go get github.com/charmbracelet/x/exp/teatest/v2@v2.0.0-20260311145557-c83711a11ffa && go mod tidy`; `go list -m github.com/charmbracelet/x/exp/teatest/v2 charm.land/bubbletea/v2 github.com/charmbracelet/x/ansi golang.org/x/sys` prints the pins. `poll.go` (AIDEV-NOTE: ETag poll loop): on `pollMsg`, if a poll is in flight only re-arm, else `fetchToday(true)` + re-arm. A non-304 polled `todayMsg` also returns `current().load(a)` when the visible screen is not Today (P9). Today keys: `f` focuses the selected row's task, or clears focus when that task is already the focus; `f`/`B`/`U` need a selection; `B` and `U` apply to block rows only; `B` sends `Reblock` with the block's `ETag` and `TaskETag`; `l` → `openLog(today, logLink())`. Layout (D§15 leaves it to the builder; the asserted strings are fixed): header, focus line (`▶`/`▷` + ` (→HH:MM)` while that task's block is current), `Blocks`, `Triage N` + top 5 rows, `Overdue`/`Due soon` only when non-empty, `Log` (`e9 10:20 <title> [#60]`), `Interruptions`, AI line. Block suffix: `◀ now` when `Current`, `reblock?` when `NeedsReblock`, else the status when not `planned`. Interruption titles come from `titlesFrom`; unknown titles show `#id` only.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): today tab and ETag polling` (+ trailer; `go.mod`/`go.sum` included).

---

### Task 5: `$EDITOR` integration, detail and history overlays

**Files:**
- Create: `internal/tui/editor.go`, `internal/tui/overlay_detail.go`, `internal/tui/overlay_history.go`
- Modify: `internal/tui/app.go` (route `editorDoneMsg`)
- Test: `internal/tui/editor_test.go`, `internal/tui/detail_test.go`

**Interfaces:**
- Consumes: `editor` (A§6.7 and Plan B Task 19 `FormatTaskBuffer`/`ParseTaskBuffer`, which errors "empty title"); Plan C Tasks 2–3 Interfaces (`mutate`, `openOverlay`, `openForm`); `bubbles/viewport`.
- Produces:
```go
type editTarget struct{ kind string /* "task"|"entry" */; id int64; etag, origTitle, origNotes string; notesOnly bool }
type editorDoneMsg struct{ target editTarget; draft *editor.Draft; err error }
func (a *App) editTask(id int64) tea.Cmd    // GetTask → fresh etag/title/notes → draft → exec → UpdateTask
func (a *App) editEntry(e api.Entry) tea.Cmd // work entry: title+notes buffer → UpdateEntry; kind "complete": notesOnly (A§14 D32)
func (a *App) openDetail(id int64) tea.Cmd  // pushes the detail overlay
func (a *App) openHistory(id int64) tea.Cmd // pushes the history overlay (esc returns to the detail)
```
Detail keys: `e` edit, `t` tags form, `h` history, `j`/`k` scroll, `esc` close.

- [ ] **Step 1: Write the failing tests**

```go
// exec stub via Config.Editor.Exec: counts runs, writes s to the last argv element (the draft path), then returns cb(exitErr)
func stubExec(s string, exitErr error, runs *int) func(*exec.Cmd, tea.ExecCallback) tea.Cmd
func TestEditTaskPatchesChanges(t *testing.T) {
	// details[42] = {Task: task(42,"Old"), Notes "n1"}; Setting "vi"; stub writes "New title\n\nn2\n"
	// drain(a.editTask(42)) → UpdateTask{ID 42, Mut{IfMatch "g-42"}, Req {Title: {Set, V: "New title"}, Notes: {Set, V: "n2\n"}}}; draft file gone
	// title-only change → Req has Title set and Notes unset
}
func TestEditorFailuresKeepDraft(t *testing.T) { // Review Focus 5
	cases := []struct{ name, setting, draftsDir, write string; exitErr, patchErr error; wantRun, wantPatch, wantDraft bool; banner string }{
		{"bad setting", `vi "unclosed`, "", "", nil, nil, false, false, false, "bad editor setting"},       // editor.Command error
		{"draft not creatable", "vi", "<a regular file>", "", nil, nil, false, false, false, "cannot create draft"},
		{"exit 1", "vi", "", "X\n\nY", errors.New("exit status 1"), nil, true, false, true, "editor failed — draft kept at "},
		{"unchanged", "vi", "", "Old\n\nn1", nil, nil, true, false, false, "no changes"},
		{"empty title", "vi", "", "\n\nnotes", nil, nil, true, false, true, "title required — draft kept at "},
		{"server refuses", "vi", "", "T2\n\nn", nil, problem("validation", 422, "too long"), true, true, true, "draft kept at "},
	}
	// draftsDir "" → t.TempDir(); "<a regular file>" → a file created in t.TempDir() (Drafts.New fails)
	// each: editor run iff wantRun; UpdateTask iff wantPatch; banner contains c.banner (+ the draft path when wantDraft); file exists iff wantDraft
}
func TestEditEntry(t *testing.T) { // LIF-15 / D32 (TUI side)
	// work entry e9 "T"/"n": stub writes "T2\n\nn" → UpdateEntry{9, Mut{IfMatch e9.ETag}, Req {Title: {Set, V: "T2"}}} (Notes unset)
	// complete entry: the draft holds exactly the notes ("n"), no title line; stub writes "n2" → UpdateEntry Req {Notes: {Set, V: "n2"}} only;
	// a complete entry whose notes start with a blank line still round-trips (ParseTaskBuffer is not used for notesOnly)
}
func TestDetailOverlay(t *testing.T) {
	// openDetail(42), notes "ok\x1b]52;c;evil\x07" → view shows reasons, slips, "b7 …", "e9 …" and no "\x1b";
	// "t" + "+db -old x" enter → UpdateTask{Req {TagsAdd: ["db", "x"], TagsRemove: ["old"]}};
	// "h" → TaskHistory(42, PageQuery{Limit: 100}) rows "<HH:MM> <op kind> by <actor>  <entity>:<action>";
	// "esc" → the detail again; "esc" → the tab
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestEdit|TestDetail' -race -count=1` → FAIL.

- [ ] **Step 3: Implement.** `editor.go` (AIDEV-NOTE: a draft is discarded only after the server confirms or when nothing changed; otherwise its path is shown, A§9.6). Flow:
  1. `argv, err := editor.Command(Setting, Getenv)`; error → banner `bad editor setting: <err>`, stop.
  2. Buffer: `editor.FormatTaskBuffer(title, notes)`, or exactly `origNotes` when `notesOnly`.
  3. `d, err := editor.Drafts{Dir: DraftsDir}.New(buffer)`; error → banner `cannot create draft: <err>`, stop (no editor run, no PATCH).
  4. `cmd := exec.Command(argv[0], append(argv[1:], d.Path)...) //nolint:gosec // G204: argv is the user's own editor setting, split without a shell (A§10.6)`; run via `Editor.Exec` (default `tea.ExecProcess`).
  5. On `editorDoneMsg`: exec error → banner `editor failed — draft kept at <path>`; `b, err := os.ReadFile(d.Path) //nolint:gosec // G304: the draft path was created by editor.Drafts under the state dir`, error → same banner; `notesOnly` → notes = the whole buffer; otherwise `editor.ParseTaskBuffer` ("empty title" → `title required — draft kept at <path>`).
  6. Nothing changed → `d.Discard()` + `no changes`; else `mutate` with only the changed fields (`UpdateTask` for kind "task", `UpdateEntry` for kind "entry"), `after` → `d.Discard()`, `onErr` → `appendBanner(" — draft kept at <path>")`.
  `editTask` always re-reads the task first so the If-Match is fresh. Tags tokens: `+x` and bare `x` add, `-x` removes.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): $EDITOR editing, detail and history overlays` (+ trailer).

---

### Task 6: List component and the Triage, Backlog and Upcoming tabs

**Files:**
- Create: `internal/tui/list.go`, `internal/tui/tab_triage.go`, `internal/tui/tab_backlog.go`, `internal/tui/tab_upcoming.go`
- Modify: `internal/tui/app.go` (install the three tab screens)
- Test: `internal/tui/list_test.go`, `internal/tui/tabs_test.go`

**Interfaces:**
- Consumes: Plan C Tasks 2–5 Interfaces (`mutate`, `openForm`, `sendFunc`, `parseMoment`, `parseSpan`, `editTask`, `openDetail`).
- Produces:
```go
type tasksMsg struct{ req int; target string; tasks []api.Task; truncated bool; err error }
type upcomingMsg struct{ req int; up *api.Upcoming; err error }
func (a *App) loadTasks(target, view string) tea.Cmd // TaskQuery{View, PageQuery{Limit: 500}} until NextCursor nil, ≤ 5 pages; truncated → banner "showing the first 2500"
type taskList struct{ items []api.Task; cursor int; selID int64; offset int }
func (l *taskList) set(items []api.Task) // keep selID; vanished → clamp to the old index and adopt that row's ID
func (l *taskList) move(delta int)
func (l *taskList) selected() (api.Task, bool)
func (l *taskList) render(a *App, w, h int, row func(api.Task, int) string) string // keeps the cursor visible
func taskRow(a *App, t api.Task, w int) string // "#42 [slipped×2,due] Title…  infra.keycloak +db  due Fri 10-30"
// Shared action forms; `after` runs only after the server confirmed (modes use it to advance and count):
func (a *App) openDefer(t api.Task, allowNone bool, after func(*App, *api.OpResponse) tea.Cmd) tea.Cmd // Future datespec; "none" → Until {Set, Null}
func (a *App) openBlock(t api.Task, after func(*App, *api.OpResponse) tea.Cmd) tea.Cmd                 // span form → BlockTask{t.ID, Mut{IfMatch: t.ETag}, …}
```
Backlog: `J`/`K` reorder, `x` done, `B` block, `d` defer, `f` focus, `e` edit, `D` drop, `enter` detail. Triage list: `j`/`k` (Task 7 adds `enter`). Upcoming: one selectable list = `up.Deferred` then `up.DueSoon` (disjoint per A§3.7), each group under its heading when non-empty; `d` change defer (`none` allowed), `K` keep (P6), `enter` detail.

- [ ] **Step 1: Write the failing tests**

```go
func TestListSelectionFollowsID(t *testing.T) { // Review Focus 1
	l := &taskList{}; l.set([]api.Task{task(1, "a"), task(2, "b"), task(3, "c")}); l.move(1) // on #2
	l.set([]api.Task{task(4, "d"), task(1, "a"), task(2, "b")}); got, _ := l.selected(); eq(t, got.ID, int64(2))
	l.set([]api.Task{task(4, "d"), task(1, "a")}); got, _ = l.selected(); eq(t, got.ID, int64(1)) // #2 gone → clamp
	l.set(nil); if _, ok := l.selected(); ok { t.Fatal("empty list has no selection") }
}
func TestBacklogActionAfterReorderTargetsSelected(t *testing.T) { // Review Focus 1, end to end
	// backlog [#1,#2,#3] on TabBacklog, "j" → #2; then tasks["backlog"] = [#3, #2 (ETag "g-2b"), #1], todayETag `"a-2"`, pollMsg;
	// "x" → CompleteTask{ID 2, Mut{IfMatch: "g-2b"}}
}
func TestTriageListServerOrder(t *testing.T) { // TRI-20 (TUI side): rows render in the order received (#9, #8, #7, #10 …); no client-side sort }
func TestBacklogReorder(t *testing.T) {
	// [#1,#2,#3] on #2: "J" → PlaceTask{2, Mut{IfMatch "g-2"}, {How: "after", Ref: 3}}; "K" → {How: "before", Ref: 1};
	// on #1 "K" and on #3 "J" → no call; after success with reloaded order [#2,#1,#3] the cursor is still on #2
}
func TestBacklogKeys(t *testing.T) {
	// "D" → DropTask{id, Mut{IfMatch}}; "f" → SetFocus{TaskID {Set, V: id}}; "d" + "thu" → DeferTask{Req {Until: {Set, V: Moment{10-29}}}};
	// "B" + "13:00-15:00" → BlockTask{Req {10-28, 780, 900}}; "e" → GetTask(id) (edit flow); "enter" → detail overlay on top
}
func TestActionFormAfterHook(t *testing.T) { /* openDefer/openBlock with a counting after: success → 1; errs["DeferTask"] = problem("validation", 422, "x") → 0, form still open */ }
func TestUpcoming(t *testing.T) { // TRI-26, DUE-18, DUE-19 (TUI side)
	// Deferred [#3 (10-29), #2 (10-29 09:00), #1 (10-30)] render in that order; DueSoon empty → no "Due soon" heading;
	// "K" → PlaceTask{id, Mut{IfMatch}, {How: "keep"}}; "d" + "none" → DeferTask{Req {Until: {Set: true, Null: true}}};
	// DueSoon [#5 due 10-31] → "Due soon" heading; "j"×3 moves from the last deferred row onto #5
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestList|TestBacklog|TestTriageList|TestActionForm|TestUpcoming' -race -count=1` → FAIL.

- [ ] **Step 3: Implement.** `openDefer`/`openBlock` are `openForm` calls whose `submit` resolves the datespec once and returns the `sendFunc`; `after` is passed through. `taskRow`: `#<id>`, badge `[` + reasons joined by `,` + `]` when any (`slipped` shown as `slipped×<Slips.Lifetime>`), sanitized title, `project`, `+tag…`, due marker (`due Fri 10-30`; `overdue` when `DuePhase == "overdue"`), whole row `Truncate(w, "…")`. Empty lists: `nothing in triage` / `backlog is empty` / `nothing upcoming`. Loads use `nextReq`/`latest` per target.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): triage, backlog and upcoming tabs` (+ trailer).

---

### Task 7: Triage-card mode

**Files:**
- Create: `internal/tui/mode_card.go`
- Modify: `internal/tui/tab_triage.go` (`enter` → `enterMode(ModeTriage, newCardMode(selectedID))`), `internal/tui/app.go` (`StartMode == ModeTriage` → card mode with `exitWithMode`)
- Test: `internal/tui/card_test.go`

**Interfaces:**
- Consumes: Plan C Task 6 Interfaces (`loadTasks`, `openDefer`, `openBlock`); Plan C Task 3 Interfaces (`openForm`, `confirm`, `parseTaskRef`); Plan C Task 2 Interfaces (`enterMode`, `mutate`).
- Produces:
```go
type cardDeck struct {
	queue                    []api.Task     // triage order as served
	idx                      int
	seen                     map[int64]bool // shown this session; skipped tasks are not re-queued
	skipped, placed, blocked int            // placed counts every successful placement incl. blocks; blocked counts successful B
}
func (d *cardDeck) refill(tasks []api.Task) // append unseen tasks in order
func (d *cardDeck) current() (api.Task, bool)
func (d *cardDeck) advance()
func (d *cardDeck) replace(t api.Task)      // after 412: swap in Problem.Current
func (d *cardDeck) placedOK(blocked bool) func(*App, *api.OpResponse) tea.Cmd // after-hook: placed++, blocked++ if blocked, advance, reload at the end
func newCardMode(startID int64) *cardMode   // screen
func cardKeys(a *App, d *cardDeck, k tea.KeyPressMsg) (tea.Cmd, bool) // shared with Plan step 1
func slipText(s api.Slips) string           // "slipped ×2 lifetime, ×1 since last triage"; "" when Lifetime is 0
```
Keys (card modes only, A§14 issue 12): `k` keep · `t` top · `b` bottom · `p` place before/after N · `d` defer · `B` block · `x` done · `D` drop · `s` skip · `A` accept all `new` → bottom. Direct ops (`k t b x D`, and `p`/`d`/`B` through forms) pass `d.placedOK(…)` as their success hook, so the deck moves only after the server confirmed.

- [ ] **Step 1: Write the failing tests**

```go
func cardApp(t *testing.T, f *fakeAPI) *App // newTestApp with StartMode ModeTriage
func TestCardPlacementKeys(t *testing.T) { // TRI-10, TRI-11, TRI-12 (TUI side)
	for _, c := range []struct{ k string; want api.PlaceRequest }{{"k", api.PlaceRequest{How: "keep"}}, {"t", api.PlaceRequest{How: "top"}}, {"b", api.PlaceRequest{How: "bottom"}}} {
		f := newFake(); f.tasks["triage"] = []api.Task{task(1, "A", "new")}; a := cardApp(t, f); press(t, a, c.k)
		got := f.mustLast(t, "PlaceTask"); eq(t, got.ID, int64(1)); eq(t, got.Mut.IfMatch, "g-1"); eq(t, got.Req, any(c.want))
	}
	// "p" + "before 3" → {How: "before", Ref: 3}; "p" + "a #3" → {How: "after", Ref: 3}; "p" + "x" → form error, no call
}
func TestCardOtherKeys(t *testing.T) { // TRI-13, TRI-14, TRI-15, TRI-16 (TUI side)
	// "d"+"+2d" → DeferTask{Req {Until {Set, V: {Date 10-30}}}}; "B"+"13:00-15:00" → BlockTask{1, Mut{IfMatch "g-1"}, {10-28, 780, 900}} and deck.blocked == 1;
	// "x" → CompleteTask{1, Mut{IfMatch "g-1"}, {}}; "D" → DropTask{1, Mut{IfMatch "g-1"}}; each success advances to the next card;
	// a failing op (errs["DropTask"] = problem("conflict", 409, "x")) leaves the card in place and placed unchanged
}
func TestTRI17_CardSkipSendsNothing(t *testing.T) { // TRI-17 (owned by Plan C): two cards; "s" → zero mutations; card 2 shown; at the end #1 is not re-shown; view "Triage clear (1 skipped)" }
func TestCardAcceptNew(t *testing.T) { // TRI-18 (TUI side)
	// triage [#1{new}, #2{new}, #3{new,slipped}, #4{surfaced}]: "A" → "accept 2 new → bottom? (y/n)"; "y" →
	// AcceptNew{Req.Items: [{ID 1, ETag "g-1"}, {ID 2, ETag "g-2"}]} (exactly-{new} tasks, server order); "n" → no call;
	// no qualifying task → banner "no new-only tasks", no call
}
func TestCardStale412(t *testing.T) { // TRI-19 (TUI side)
	// f.errs["PlaceTask"] = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: <JSON of #1 with ETag "g-1b", reasons new+overdue>}};
	// "k" → banner "changed elsewhere — refreshed"; the card shows "overdue"; exactly one PlaceTask (no retry); "k" again → Mut.IfMatch "g-1b"
}
func TestCardDoubleKeySendsOnce(t *testing.T) { // Review Focus 2
	f := newFake(); f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}; a := cardApp(t, f)
	_, c1 := a.Update(key("k")); _, c2 := a.Update(key("k")) // second press before the first opMsg arrives
	drain(t, a, c1); drain(t, a, c2); eq(t, f.count("PlaceTask"), 1); eq(t, f.mustLast(t, "PlaceTask").ID, int64(1))
}
func TestCardShowsSlips(t *testing.T) { // SLP-12 (TUI side): Slips{Lifetime 2, SinceTriage 1} → "slipped ×2 lifetime, ×1 since last triage" }
func TestCardExit(t *testing.T) { /* StartMode ModeTriage: "q" → a.quitting; from the Triage tab via "enter": "esc" → back on TabTriage, mode ModeTabs */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestCard|TestTRI17' -race -count=1` → FAIL.

- [ ] **Step 3: Implement.** Card: `Card i/n`, `#id title`, reasons in served order with `since` dates, `slipText`, due/defer, project/tags/priority, the first 5 lines of sanitized notes, legend `k keep · t top · b bottom · p place · d defer · B block · x done · D drop · s skip · A accept new`. At the end of the queue reload triage and `refill`; nothing unseen → `Triage clear (N skipped)`. A 412 decodes `Problem.Current` into `api.Task` and `replace`s the card (else reloads). `p` grammar: `^(before|after|b|a)\s+#?([1-9][0-9]*)$`, case-insensitive. `AcceptNew` items use the anonymous element type of `api.AcceptNewRequest.Items`; its success hook adds the item count to `placed` and reloads.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): triage-card mode` (+ trailer).

---

### Task 8: Plan mode

**Files:**
- Create: `internal/tui/mode_plan.go`
- Modify: `internal/tui/app.go` (`StartMode == ModePlan` → plan mode with `exitWithMode`)
- Test: `internal/tui/plan_test.go`

**Interfaces:**
- Consumes: Plan C Task 7 Interfaces (`cardDeck` incl. `placed`/`blocked`, `cardKeys`); Plan C Task 6 Interfaces (`taskList`, `taskRow`, `openBlock` with its `after` hook); Plan C Task 3 Interfaces (`parseSpan`).
- Produces:
```go
type blocksMsg struct{ req int; blocks []api.Block; err error }
type slot struct{ Start, End int } // minutes, half-open
func freeSlots(blocks []api.Block, from int) []slot // planned blocks only; gaps in [from, 1440) of ≥ 15 min
func newPlanMode() *planMode                        // fields include deck *cardDeck and step2Blocked int
```
Steps: 1) triage cards with every card key (skipped when triage is empty); 2) backlog top 10 (`ListTasks(TaskQuery{View: "backlog", PageQuery{Limit: 10}})`) beside today's free slots (`ListBlocks(BlockQuery{From: &today, To: &today})`), `j`/`k`, `B` = `openBlock(selected, after: step2Blocked++ and reload blocks)` restricted to today's date; `enter` → step 3; 3) summary: today's blocks and `placed <deck.placed> · blocked <deck.blocked + step2Blocked> · triage left <N>`; `enter`/`q` ends.

- [ ] **Step 1: Write the failing tests**

```go
func TestFreeSlots(t *testing.T) {
	bs := []api.Block{{Start: 780, End: 900, Status: "planned"}, {Start: 600, End: 660, Status: "planned"}, {Start: 700, End: 760, Status: "cancelled"}}
	eq(t, freeSlots(bs, 605), []slot{{660, 780}, {900, 1440}}) // from inside a block; cancelled ignored
	eq(t, freeSlots(bs, 1430), []slot(nil))                     // 10 minutes left < 15
	eq(t, freeSlots(nil, 0), []slot{{0, 1440}})
}
func TestPlanFlow(t *testing.T) { // D§4.1 walkthrough
	// triage [#42{new}]; backlog [#7, #8]; fake blocks today [09:00–10:00, 13:00–15:00]; clock 10:00
	// step 1: "B" + "13:00-15:00" → BlockTask{42, Mut{IfMatch "g-42"}, {10-28, 780, 900}}; triage then reloads empty → step 2 view has "free 10:00–13:00, 15:00–24:00"
	// step 2: "j" "B" "16:00-17:00" → BlockTask on #8; "enter" → "placed 1 · blocked 2 · triage left 0"; "enter" → a.quitting
}
func TestPlanEmptyTriageStartsAtStep2(t *testing.T) { /* triage empty → the first view shows the backlog and free slots */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestFreeSlots|TestPlan' -race -count=1` → FAIL.

- [ ] **Step 3: Implement.** `from` = `clock.Now.Hour()*60 + Minute()` (the server-zone offset travels in `api.Time`). Slot text `free 10:00–13:00, 15:00–24:00`, or `no free time left today`.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): plan mode` (+ trailer).

---

### Task 9: Recap mode

**Files:**
- Create: `internal/tui/mode_recap.go`
- Modify: `internal/tui/app.go` (`StartMode == ModeRecap` → recap of `RecapDate`, zero → `Clock.Today`, with `exitWithMode`)
- Test: `internal/tui/recap_test.go`

**Interfaces:**
- Consumes: Plan C Task 3 Interfaces (`openLog`, `link`, `openForm`, `parseTaskRef`); Plan C Task 4 Interfaces (`interruptionLine`, `spanText`); Plan C Task 2 Interfaces (`mutate`).
- Produces: `type dayMsg struct{ req int; day *api.Day; err error }`; `func newRecapMode(date civil.Date) *recapMode`.

Rows: the day's blocks, then its unlinked entries; the cursor starts on the first row. Block row: `w` worked · `m` missed · `a` auto (`SetOutcome` with the block's ETag; mode keys are offered before the global `a`), `x` done its task (`CompleteTask{}` with `Mut{IfMatch: block.TaskETag}`), `l` log for the recap date linked to the block's task. Unlinked-entry row: `L` link (form: task ID; hint lists the day's block tasks) → `RelinkEntry`. `l` without a block selected: recap date = today → implicit rule; otherwise a picker form `task # or - for none` comes first (FOC-07).

- [ ] **Step 1: Write the failing tests**

```go
// Day 2026-10-27 (blocks carry TaskETag "g-<task id>"): Blocks b1 #1 13:00–15:00 {missed, auto, Slipped}, b2 #2 09:00–10:00 {worked, auto};
// Entries [e5 #2 "x"]; Unlinked [e6 "Answered Bob's DNS question"]; Interruptions [10:20 from #42 b9 to #60]
func TestRecapOutcomeKeys(t *testing.T) { // BLK-44, BLK-45, BLK-49, BLK-50 (TUI side)
	// RecapDate 10-27: "w" → SetOutcome{1, Mut{IfMatch b1.ETag}, {Outcome: "worked"}}; "j" "m" → b2 {"missed"}; "a" → {"auto"}; no add form opened
}
func TestRecapStartedTodayBlock(t *testing.T) { // BLK-47/R9 (TUI side): today's planned block, Started false → "w" sends nothing, banner "not started — use unblock"; Started true → SetOutcome sent }
func TestRecapDoneTask(t *testing.T) { // "x" on b1 → CompleteTask{1, Mut{IfMatch: b1.TaskETag}, {}}; no GetTask call }
func TestRecapLinkUnlinked(t *testing.T) { // RCP-09 (TUI side): "j" "j" (e6), "L", "1", enter → RelinkEntry{6, Mut{IfMatch e6.ETag}, {TaskID {Set, V: 1}}}; "abc" → form error, no call }
func TestRecapBackdatedLogNeedsChoice(t *testing.T) { // FOC-07 (TUI side)
	// cursor on e6: "l" → picker; "-" enter → log form; "x" enter → AddEntry{TaskID {Set, Null: true}, Title "x", WorkDate &10-27};
	// picker "2" → TaskID {Set, V: 2}; cursor on b1: "l" → log form titled "→ #1 (selected)", WorkDate &10-27
}
func TestRecapGlobals(t *testing.T) { /* in recap, "?" → help shown (global fallback); "1" → still in recap (undo in modes is tested in Task 11) */ }
func TestRecapShowsInterruptions(t *testing.T) { /* the view contains interruptionLine for the day's interruption */ }
func TestRecapDateHeader(t *testing.T) { /* zero RecapDate → "Recap Wed 2026-10-28"; 10-27 → "Recap Tue 2026-10-27" */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run TestRecap -race -count=1` → FAIL.

- [ ] **Step 3: Implement.** Block row: `b1 13:00–15:00 #1 <title>  missed (auto) slipped`. Every successful op reloads the day through `refresh`.

- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit** — `feat(tui): recap mode` (+ trailer).

---

### Task 10: Log tab

**Files:**
- Create: `internal/tui/tab_log.go`
- Modify: `internal/tui/app.go` (install the Log screen)
- Test: `internal/tui/log_test.go`

**Interfaces:**
- Consumes: Plan C Task 5 Interfaces (`editEntry`); Plan C Task 3 Interfaces (`openLog`, `openForm`, `confirm`, `parseTaskRef`); Plan C Task 2 Interfaces (`mutate`).
- Produces: `type entriesMsg struct{ req int; page *api.Page[api.Entry]; appendPage bool; err error }`. Range `From = clock.Today.AddDays(-13)`, `To = clock.Today`, recomputed on every load (follows midnight via `/today`).

Keys: `j`/`k`; moving onto the last row while `NextCursor` is set loads the next page (`Limit 100`) and appends; `l` log (implicit rule: Log rows are entries, so `selection()` is false); `e` edit via `$EDITOR`; `L` relink (task ID or `-`); `X` delete (confirm `delete e9? (y/n)`).

- [ ] **Step 1: Write the failing tests**

```go
func TestLogTabRangeAndPaging(t *testing.T) {
	// first call ListEntries(EntryQuery{From: &10-15, To: &10-28, PageQuery{Limit: 100}}); entriesNext = "c2": moving onto the last row
	// issues exactly one ListEntries with Cursor "c2" and appends; a row reads "Wed 10-28  e9 10:20 Restarted replica  [#60]"
}
func TestLogTabActions(t *testing.T) {
	// "L" + "42" → RelinkEntry{9, Mut{IfMatch e9.ETag}, {TaskID {Set, V: 42}}}; "L" + "-" → {TaskID {Set, Null: true}};
	// "X" "y" → DeleteEntry{9, Mut{IfMatch e9.ETag}}; "X" "n" → no call; "e" → the exec stub runs on a draft
}
func TestLogTabMidnight(t *testing.T) { /* after a todayMsg whose Clock.Today is 10-29, the next load asks for 10-16..10-29 */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run TestLogTab -race -count=1` → FAIL.
- [ ] **Step 3: Implement** `tab_log.go` (day headers `Wed 10-28`; entries in served order, newest first).
- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.
- [ ] **Step 5: Commit** — `feat(tui): log tab` (+ trailer).

---

### Task 11: Search and AI-changes overlays, undo and redo

**Files:**
- Create: `internal/tui/overlay_search.go`, `internal/tui/overlay_ai.go`
- Modify: `internal/tui/app.go` (globals `/`, `A`, `u`, `ctrl+r`)
- Test: `internal/tui/search_test.go`, `internal/tui/ai_test.go`, `internal/tui/undo_test.go`

**Interfaces:**
- Consumes: Plan C Task 5 Interfaces (`openDetail`); Plan C Task 3 Interfaces (`openForm`); Plan C Task 2 Interfaces (`openOverlay`, `mutate`).
- Produces: `searchMsg{req int; page *api.Page[api.SearchHit]; err error}`, `aiMsg{req int; ai *api.AIChanges; appendPage bool; err error}`, `newSearchOverlay(q string) screen`, `newAIOverlay() screen` (fields: `ops []api.OpSummary`, `next *string`).

`/` opens a query form → `Search(SearchQuery{Q: q, Kind: "all", PageQuery{Limit: 50}})` → rows `task #42 <title> — <snippet>` / `entry e9 Wed 10-28 <title>`; `enter` on a task hit, or an entry hit with `TaskID`, pushes the detail overlay (esc returns to the results); a standalone entry → banner `standalone entry e9`. `A` in `ModeTabs` (card modes keep accept-new, P7) opens AI changes: `AIChanges(false, PageQuery{Limit: 50})` (unreviewed only), rows `<seq> <HH:MM> <kind> by <actor>` + ` [reverted]`; moving onto the last row while `next` is set loads the next page and appends; `R` → `Revert(Mut{}, op_id)` then reload. `A` inside the overlay (AIDEV-NOTE: AI-review watermark — `ThroughSeq` marks every op ≤ it reviewed, so it may only cover ops the user has had on screen): if `next != nil`, load every remaining page into the list, banner `loaded all N unreviewed changes — review them, then press A again`, no review call; else `ReviewAIChanges(Mut{}, ReviewRequest{ThroughSeq: highest Seq in the list})` (SCP-11). `u` → `Undo(Mut{})`, `ctrl+r` → `Redo(Mut{})`; banners `undone · <effects>` / `redone · <effects>`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSearchToDetail(t *testing.T) { /* "/" "keycloak" enter → Search Q "keycloak", Kind "all", Limit 50; a snippet with "\x1b" renders without ESC; enter on the task hit → GetTask(42); "esc" → back to the results */ }
func TestAIChanges(t *testing.T) { // SCP-11, UND-11 (TUI side)
	// aiPages [{Ops [{Seq 12, OpID "aa…", Kind "entry.add", Actor "token:ai"}, {Seq 10, …}]}]: "A" → overlay; "R" → Revert opID "aa…";
	// "A" → ReviewAIChanges{Req {ThroughSeq: 12}}; in ModeTriage "A" is accept-new (no AIChanges call)
}
func TestAIReviewNeverCoversUnseen(t *testing.T) { // 51 unreviewed
	// aiPages[0]: 50 ops seq 101..52, NextCursor "c2"; aiPages[1]: op seq 51, NextCursor nil
	// "A" (open) then "A" → AIChanges with Cursor "c2", zero ReviewAIChanges, banner "loaded all 51 unreviewed changes";
	// "A" → ReviewAIChanges{Req {ThroughSeq: 101}} exactly once
}
func TestUndoRedo(t *testing.T) { // UND-18 (TUI side)
	// "u" → one Undo, banner starts "undone"; errs["Undo"] = problem("nothing_to_undo", 409, "") → "nothing to undo";
	// "ctrl+r" → Redo; nothing_to_redo → "nothing to redo"; each success refetches /today;
	// globals inside modes: after enterMode(ModeTriage, &recScreen{}) and in recap mode, "u" → Undo and "ctrl+r" → Redo
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui -run 'TestSearch|TestAI|TestUndoRedo' -race -count=1` → FAIL.
- [ ] **Step 3: Implement** both overlays and the four globals.
- [ ] **Step 4: Run to see them pass** — same command → PASS; `make check` → exit 0.
- [ ] **Step 5: Commit** — `feat(tui): search and AI-changes overlays, undo/redo` (+ trailer).

---

### Task 12: `Run`, CLI hook, catalog guard, end-to-end tests

**Files:**
- Create: `internal/tui/run.go`, `internal/cli/cmd_tui.go`
- Modify: `internal/service/catalog_coverage_test.go` (remove `"TRI-17": "C"` from `notPlanA`, Plan A Task 38)
- Test: `internal/tui/run_test.go`, `internal/tui/e2e_test.go`, `internal/cli/cmd_tui_test.go`

**Interfaces:**
- Consumes: everything above; `testutil.StartServer` (A§13.1); Plan B Task 21 (`TUIRequest`, `launchTUI`, `envFrom`, `(*env).clientConfig`, `(*env).colorEnabled`, `usagef`, `run(ctx, args, stdin, stdout, stderr, sysDeps)` for tests). Plan B Task 24b's `cmd_modes.go` owns `tui [--tab]`, `plan`, `recap [--date]` and `triage -i`; while `launchTUI` is nil they return `usagef("the TUI is not built in")`.
- Produces:
```go
var ErrUsage = errors.New("usage")
type RunOptions struct {
	API       API
	StartTab  Tab
	StartMode Mode
	RecapSpec string // `tkr recap --date`: datespec Past, date only; "" = today
	Color     bool
	Editor    EditorEnv
	In        io.Reader
	Out       io.Writer
}
func Run(ctx context.Context, o RunOptions) error
// internal/cli/cmd_tui.go — the only cli file importing tui; registers no commands
func init() { launchTUI = runTUI }
func runTUI(cmd *cobra.Command, req TUIRequest) error
```
`Run`: `Clock`, then `Generation` (errors returned unchanged, so the CLI prints its unreachable message and exits 3, A§9.6); resolve `RecapSpec` with `datespec.Parse(spec, Past, ref)` (a time part or a parse error → `fmt.Errorf("%w: …", ErrUsage)`); `NewApp`; `tea.NewProgram(app, tea.WithContext(ctx), tea.WithInput(o.In), tea.WithOutput(o.Out)).Run()`. `runTUI`: `e := envFrom(cmd)`; map `req.Mode` `tabs`→`ModeTabs` (with `ParseTab(req.Tab)`, `""` → Today), `triage`→`ModeTriage`, `plan`→`ModePlan`, `recap`→`ModeRecap` (`RecapSpec: req.RecapDate`); anything else or a bad tab → `usagef`; then `!e.sys.StdinTTY || !e.sys.StdoutTTY` → `usagef("the TUI needs a terminal")`; `cfg, err := e.clientConfig()`; `c, err := client.NewFromConfig(cfg, "tui", nil)`; `tui.Run(cmd.Context(), tui.RunOptions{API: c, …, Color: e.colorEnabled(), Editor: tui.EditorEnv{Setting: cfg.Editor, Getenv: e.sys.Getenv, DraftsDir: e.paths.DraftsDir}, In: e.in, Out: e.out})`; `errors.Is(err, tui.ErrUsage)` → `usagef("%v", err)`; other errors returned unchanged (Plan B's `exitCode` gives 3 for not-sent).

- [ ] **Step 1: Write the failing tests**

```go
// run_test.go
func TestRunRecapSpec(t *testing.T) { /* RecapSpec "tue 10:00" and "nextweek" → errors.Is(err, ErrUsage); the program never starts */ }
func TestRunUnreachable(t *testing.T) { /* errs["Clock"] = notSent() → errors.Is(err, client.ErrNotSent) */ }
// e2e_test.go — real server and client, fake clock (A§13.1), through teatest
func TestE2ETriageCardKeep(t *testing.T) { // TRI-10 end to end
	srv := testutil.StartServer(t); c := srv.Client
	r, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "Set up keycloak postgres cluster"}); if err != nil { t.Fatal(err) }
	clk, _ := c.Clock(t.Context())
	tm := teatest.NewTestModel(t, NewApp(Config{API: c, Ctx: t.Context(), Clock: *clk, StartMode: ModeTriage, PollInterval: -1}), teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), contains("Set up keycloak"), teatest.WithDuration(3*time.Second))
	tm.Send(key("k")); teatest.WaitFor(t, tm.Output(), contains("Triage clear"), teatest.WithDuration(3*time.Second))
	tm.Send(key("q")); tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	d, _ := c.GetTask(t.Context(), r.Task.ID); eq(t, len(d.Reasons), 0); eq(t, d.View, "backlog")
}
func TestE2EStaleCard(t *testing.T) { // TRI-19: card shown; UpdateTask through c bumps the version; "k" → "changed elsewhere — refreshed"; task still in triage }
func TestE2ERecapWorked(t *testing.T) { // BLK-44
	// srv.Clock.Set(2026-10-27 10:00 in testutil.Zone); AddTask + BlockTask 13:00–15:00 today via c; srv.Clock.Set(2026-10-28 00:30);
	// recap RecapDate 2026-10-27: "w" → GetTask shows the block "worked" with outcome_source "user" and reasons ["continue"]
}
// internal/cli/cmd_tui_test.go
func TestTUIHookInstalled(t *testing.T) { /* via Plan B's run(ctx, args, …, sysDeps{StdinTTY: true, StdoutTTY: true}): launchTUI != nil; `tui --tab nope` → exit 2;
	with StdinTTY false, `plan` → exit 2 and stderr "the TUI needs a terminal"; `recap --date nextweek` against StartServer → exit 2 */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/tui ./internal/cli -run 'TestRun|TestE2E|TestTUIHook' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** `run.go` and `cmd_tui.go` (no edits to Plan B's files beyond the hook assignment); remove `"TRI-17": "C"` from `notPlanA`.

- [ ] **Step 4: Run the package tests, the catalog guard, then the gate**

Run: `go test ./internal/tui ./internal/cli ./internal/archtest -race -count=1` → PASS.
Run: `go test ./internal/service -run TestCatalogCoverage -count=1` → PASS (finds `TestTRI17_CardSkipSendsNothing` in `internal/tui`).
Run: `make check` → exit 0.
Manual smoke (report the outcome): `tkr tui` against a dev server opens on Today; `1`–`5`, `?`, `q` work; `tkr plan` and `tkr recap` open their modes.

- [ ] **Step 5: Commit** — `feat(cli): launch the TUI from tkr tui, plan, recap and triage -i` (+ trailer).

---

## Behavior-catalog scenarios

Plan C owns **TRI-17** (Plan A Task 38 `notPlanA`): `TestTRI17_CardSkipSendsNothing` (Task 7); Task 12 removes it from `notPlanA`. Every other catalog ID is owned by Plan A (service) or Plan B (CLI); Plan C's tests cover only their TUI-visible side and cite the ID in a comment: Task 3 — FOC-04, FOC-05, FOC-06, FOC-08, DAT-02, DAT-04, DAT-09, DAT-12; Task 4 — BLK-17/RCP-06, BLK-48, INT-08, REC-05; Task 5 — LIF-15 (D32); Task 6 — TRI-20, TRI-26, DUE-18, DUE-19; Task 7 — TRI-10 … TRI-16, TRI-18, TRI-19, SLP-12; Task 9 — BLK-44, BLK-45, BLK-47 (R9), BLK-49, BLK-50, RCP-09, FOC-07; Task 11 — SCP-11, UND-11, UND-18; Task 12 re-runs TRI-10, TRI-19 and BLK-44 against the real server.

## Planner decisions & open items

**Engineering decisions (no user input needed)**

- **P1** teatest pin `v2.0.0-20260311145557-c83711a11ffa`: the newest commit touching `exp/teatest/v2` (2026-03-11, ≥ 14 days old). Every later pseudo-version through 2026-10-25 has identical module content, so this meets A§1.1's "newest pseudo-version ≥ 14 days old" without pinning an unrelated repo commit. Verified in a scratch module: compiles and runs with the A§1.1 pins, no version bumps, key strings as asserted. Modules are added in the task that first imports them (charm in Task 2, teatest in Task 4) so `go mod tidy` keeps them.
- **P2** `tui.API` is an exact subset of `*client.Client` (A§6.7) with a compile-time assertion, so tests use an in-package fake and errors are the client's own types — no adapter layer.
- **P3** The task buffer uses Plan B's `editor.FormatTaskBuffer`/`ParseTaskBuffer` (the same format as `tkr edit`; agreed with planner-b); notes-only buffers for `complete` entries bypass `ParseTaskBuffer`. The editor runs through `tea.ExecProcess`, not `editor.Edit` (which blocks); the G204 exec and G304 draft read carry the two exact `//nolint:gosec` directives in Task 5.
- **P4** Outcome-unknown: the TUI has no pending store (A§6.7). A form keeps the text and an immutable snapshot (resolved request, Idempotency-Key, generation, time); `enter` with unchanged text resends exactly that snapshot, but refuses (and drops it) when the generation changed or 7 days passed since the first attempt (checked against fresh `GET /clock`; the timestamp never resets on repeated outcome-unknown replies), after which `enter` sends a fresh request. Key-press ops refresh and say "check before repeating".
- **P5** Files beyond A§1.2's list for `tui`: `doc.go`, `api.go`, `screen.go`, `banner.go`, `render.go`, `dates.go`, `link.go`, `editor.go`, `list.go`, `mode_card.go`, `overlay_*.go`, `run.go`.
- **P6** Upcoming-tab "keep" is `K`, not `k`: A§11.1 gives Upcoming `k` keep while `j/k` navigate every list — the collision A§14 issue 12 resolved for triage keys. `K` matches the capital-letter convention for mutating keys; the service op is unchanged (catalog TRI-26 mentions "TUI Upcoming `k`" informally). **This is the one visible key-binding change; the user may prefer another key.**
- **P7** `A` outside card modes opens the AI-changes overlay (A§14 issue 15 "TUI `A`"); in card modes it stays accept-all-new; inside the overlay it marks reviewed only after every unreviewed change has been loaded into the list (the watermark covers all ops ≤ it).
- **P8** Today-tab rows start unselected so `l` uses the implicit rule unless the user picks a row; list tabs, cards and recap block rows count as "a selected task" (A§11.1). The log form echoes the link, refreshed from `/today` when it opens. Today `f` on the already-focused task clears focus.
- **P9** A changed (200) poll while another tab is visible also reloads that tab (A§11.1 only says "poll `/today`"). Polls never overlap; responses older than the latest request per target are dropped.
- **P10** Forms close only on success and then run the caller's success hook (modes advance and count there); failures keep the typed text.
- **P11** One mutation in flight at a time; mutating keys pressed meanwhile get `busy…` and are dropped, never queued.
- **P12** Card session: skipped cards are not re-shown in the same session; `A` asks `y/n`; `D` and `x` do not confirm (undo exists, as in the CLI); Log `X` delete confirms (like CLI `delete`).
- **P13** Recap `x` completes as of today (D§9.2 "`x` = normal done"); `l` on a past recap date asks for a task or `-` first (D§5.2).
- **P14** Plan step 2 free slots: gaps ≥ 15 min between planned blocks in [now, 24:00); no prefilled span. List loads fetch pages of 500 up to 2500 tasks; the Log tab and AI overlay page on demand. Plain mode (no colour) emits no escape codes and marks the selection with `› `.
- **P15** CLI hook — **agreed with planner-b and used verbatim by both plans**: `type TUIRequest struct{ Mode, Tab, RecapDate string }` (Mode ∈ `tabs|triage|plan|recap`; raw strings, Plan C validates and resolves) and `var launchTUI func(cmd *cobra.Command, req TUIRequest) error`. Plan B Task 24b's `cmd_modes.go` registers the four commands and returns `usagef("the TUI is not built in")` (exit 2) while the hook is nil; Plan C's `cmd_tui.go` sets it in `init()` and reaches Plan B's `env` through `envFrom(cmd)`, `clientConfig()` and `colorEnabled()`. The TUI refuses a non-terminal stdin/stdout (exit 2); an unreachable server at start is the CLI's unreachable error (exit 3).
- **P16** teatest is imported from `internal/tui/*_test.go`, not through `testutil` (its API takes `tea.Model`, and A§1.3 lets `testutil` import teatest but not bubbletea). Confirmed by Plan A Task 1: its archtest checks only non-test `Imports`, and its `cliFileExceptions` already allows `cmd_tui.go` → `tui`.
- **P17** Key routing: the form, the overlay stack, the mode and the tab screen are offered a key in that order; unconsumed keys fall back to globals (`a l / u ctrl+r ? esc` everywhere; `1`–`5`, `tab`, `A`, `q`-quit only in plain tabs). Overlays form a stack, so History opened from Detail (or Detail from Search) returns to its parent on `esc`.

**Open items for the lead:** none.

**Needs the user:** none, unless the user wants a different key than `K` for Upcoming keep (P6).
