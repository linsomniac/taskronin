# Taskronin — Architecture (Draft A1)

Status: DRAFT A1, 2026-10-25. Behaviour is defined by the design
(`2026-10-25-taskronin-design.md`, Draft v6, cited as **D§n**). This document
fixes *how*: packages, types, signatures, schema, wire contracts, procedures.
Where it sharpens or corrects the design it says so in §14; those resolutions are
already applied below.

Conventions: **MUST**/**SHOULD** are normative. Code blocks are declarations,
not bodies. "Op" = one mutating service call = one SQLite write transaction =
one `op_id` (D§3). "Today"/"now" always mean the server's configured zone.

---

## 1. Package layout

### 1.1 Module, toolchain, dependencies

```
module taskronin            // round-2 Q1; see "Renaming" below

go 1.26.0
toolchain go1.26.10

require (
	charm.land/bubbles/v2                  v2.2.1   // TUI widgets (plan C)
	charm.land/bubbletea/v2                v2.1.0   // TUI runtime (plan C)
	charm.land/lipgloss/v2                 v2.0.6   // TUI + CLI styling
	github.com/BurntSushi/toml             v1.6.0   // config
	github.com/charmbracelet/x/ansi        v0.11.9  // display width/truncate (already in the lipgloss graph)
	github.com/microcosm-cc/bluemonday     v1.0.27  // HTML sanitizer (plan D)
	github.com/modelcontextprotocol/go-sdk v1.8.0   // MCP stdio server
	github.com/spf13/cobra                 v1.10.2  // CLI
	github.com/yuin/goldmark               v1.8.6   // markdown (plan D)
	golang.org/x/crypto                    v0.58.0  // argon2id
	golang.org/x/sys                       v0.49.0  // flock, SO_PEERCRED
	golang.org/x/term                      v0.47.0  // no-echo password prompt, IsTerminal
	golang.org/x/time                      v0.16.0  // rate.Limiter (password gate); already required by go-sdk
	modernc.org/sqlite                     v1.60.1  // pure-Go SQLite (bundles SQLite ≥ 3.53)
)
// test-only, added in plan C: github.com/charmbracelet/x/exp/teatest/v2 (pseudo-version only;
// pin the newest pseudo-version that is ≥ 14 days old at that time).
```

- Pin policy: newest tagged release that is ≥ 14 days old (verified against
  proxy.golang.org on 2026-10-25). Upgrades are deliberate commits.
  `go mod verify` runs in `make check`.
- Additions beyond the fixed list, justified: `x/ansi` (width math identical to
  Lip Gloss; already compiled in), `x/term` (password prompt; already in
  `x/crypto`'s module graph), `x/time/rate` (already required by go-sdk),
  `github.com/google/jsonschema-go` (imported by `mcpserver` only, for explicit
  tool schemas, §10; already required by go-sdk — keep its version equal to
  go-sdk's requirement).
- Deliberately **not** added: a shell-words library (own `editor.SplitWords`,
  ~60 lines, fuzzed), `coreos/go-systemd` (own `LISTEN_FDS` + `sd_notify`,
  ~80 lines), any UUID library (`crypto/rand` hex).
- `cmd/tkr` blank-imports `time/tzdata` so zone loading never depends on host files.
- htmx: `internal/web/static/htmx-2.0.11.min.js` from npm `htmx.org@2.0.11`
  `dist/htmx.min.js` (52 182 bytes), **SHA-256
  `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`**, recorded in
  `internal/web/static/SHA256SUMS`, checked by `make assets-verify` and by a Go
  test over the embedded bytes. (htmx 4.x changed its config surface; stay on 2.x.)
- Tools pinned in the Makefile: `golangci-lint` v2.14 (installed),
  `golang.org/x/vuln/cmd/govulncheck@v1.9.0`.

**Renaming the module** (Q1): the only hand-edited lines are `module` in go.mod
and `local-prefixes` in `.golangci.yml`; `make rename-module NEW=<path>` rewrites
import prefixes mechanically. Nothing else may hard-code `taskronin/` (the
Makefile derives it with `go list -m`).

### 1.2 Packages

| Package | Responsibility | Main files |
|---|---|---|
| `cmd/tkr` | `main`: `os.Exit(cli.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`; blank-imports `time/tzdata`. Only place that calls `os.Exit`. | `main.go` |
| `internal/buildinfo` | `Version`, `Commit` (ldflags), `APIVersion = "v1"`. | `buildinfo.go` |
| `internal/clock` | `Clock` interface, `Real`, `Fake`. Only package allowed to call `time.Now`. | `clock.go`, `fake.go` |
| `internal/civil` | `Date`, `Minute`, `Zone`; (date, minute) ↔ instant; DST policy (D§5.12). | `date.go`, `minute.go`, `zone.go` |
| `internal/datespec` | Relative date/time grammar → civil values. Zone-free (needs only "today" + week start). | `parse.go`, `range.go` |
| `internal/textsafe` | Input validators (D§10.6), terminal/log sanitizer, width/truncate. | `validate.go`, `sanitize.go`, `width.go` |
| `internal/domain` | Pure types: IDs, enums, entities, Actor/Scopes, Op/Event, errors. No I/O. | `ids.go`, `enums.go`, `task.go`, `entry.go`, `block.go`, `actor.go`, `op.go`, `errors.go` |
| `internal/fsutil` | `WriteFileAtomic` (temp→fsync→rename→fsync dir), `FsyncDir`, `CheckPrivate` (mode/owner), `EnsureDir0700`, umask. | `atomic.go`, `perms.go` |
| `internal/lock` | Exclusive `flock` on `<datadir>/taskronin.lock`. | `flock.go` |
| `internal/config` | XDG paths, server/client TOML schema, defaults, validation, writing. | `paths.go`, `server.go`, `client.go`, `load.go`, `write.go` |
| `internal/store` | SQLite only: DSNs, open, migrations, typed CRUD/queries returning domain types, FTS, scrub, export file format types + rows (`export.go`, shared by `service.Export` and `offline.Import`), sequences. **No business rules, no events.** | `open.go`, `dsn.go`, `migrate.go`, `q.go`, `tasks.go`, `triage.go`, `entries.go`, `blocks.go`, `tags.go`, `focus.go`, `interruptions.go`, `ops.go`, `events.go`, `idem.go`, `undo.go`, `tokens.go`, `sessions.go`, `settings.go`, `fts.go`, `scrub.go`, `export.go`, `sequences.go`, `migrations/0001_init.sql` |
| `internal/auth` | Token format/parse/hash, argon2id, `PasswordGate`, session id + CSRF secrets. | `token.go`, `password.go`, `gate.go`, `session.go` |
| `internal/backup` | File-level procedures: snapshot, verify, rotate, list, inventory, staging, journaled swap, purge ledger. | `snapshot.go`, `verify.go`, `rotate.go`, `inventory.go`, `stage.go`, `swap.go`, `ledger.go` |
| `internal/service` | **All rules and all authorization.** Op runner, reconcile, derivation/slip/continue, rank, revert/undo/redo, idempotency, invariants, views, report, search, tokens, settings, password, sessions, purge, backup/export orchestration. | `service.go`, `op.go`, `reconcile.go`, `derive.go`, `slip.go`, `rank.go`, `revert.go`, `undo.go`, `idem.go`, `invariants.go`, `effects.go`, `tasks.go`, `place.go`, `blocks.go`, `entries.go`, `focus.go`, `aichanges.go`, `views.go`, `report.go`, `search.go`, `tokens.go`, `settings.go`, `password.go`, `sessions.go`, `purge.go`, `backup.go`, `export.go` |
| `internal/api` | Wire DTOs (JSON), request types, problem codes, header names, ETag/cursor codecs, domain→wire converters. | `types.go`, `requests.go`, `problem.go`, `headers.go`, `etag.go`, `cursor.go`, `convert.go` |
| `internal/systemd` | `LISTEN_FDS`/`LISTEN_FDNAMES` parsing, `sd_notify`, unit templates. | `listen.go`, `notify.go`, `units.go`, `units/*.tmpl` |
| `internal/server` | Listeners, peer creds, middleware, `/api/v1` adapters (decode → one service call → encode), schedulers, startup/shutdown. | `server.go`, `listeners.go`, `peercred.go`, `middleware.go`, `routes.go`, `decode.go`, `respond.go`, `h_tasks.go`, `h_entries.go`, `h_blocks.go`, `h_ops.go`, `h_admin.go`, `h_views.go`, `schedulers.go`, `lifecycle.go` |
| `internal/markdown` | goldmark (no raw HTML; images rendered as links) → bluemonday policy. | `render.go` |
| `internal/web` | `/ui` (plan D): sessions, CSRF, Host/Origin checks, headers, templates, static. Calls `service` in-process. | `web.go`, `routes.go`, `session.go`, `csrf.go`, `headers.go`, `h_*.go`, `templates/*.html`, `static/*` |
| `internal/client` | Go client for `/api/v1` over the unix socket: token file, headers, idempotency keys, retries, not-sent vs outcome-unknown classification, pending-request store, problem decoding. | `client.go`, `transport.go`, `retry.go`, `pending.go`, `errors.go`, `tasks.go`, `entries.go`, `blocks.go`, `views.go`, `ops.go`, `admin.go` |
| `internal/report` | Render `api.Report` as text / markdown (D§14). | `text.go`, `markdown.go` |
| `internal/editor` | `$VISUAL`/`$EDITOR` via `SplitWords` (no shell), drafts dir lifecycle. | `editor.go`, `shellwords.go`, `drafts.go` |
| `internal/offline` | Lock-holding commands that open the DB directly: `init`, `migrate`, `restore`, `import`, `purge`, `scrub-backups`, `passwd --reset`, offline `backup`. | `init.go`, `migrate.go`, `restore.go`, `import.go`, `purge.go`, `scrub.go`, `passwd.go`, `backup.go` |
| `internal/cli` | Cobra tree, arg parsing, human/JSON output, exit codes, prompts. | `main.go`, `root.go`, `output.go`, `errors.go`, `ids.go`, `logargs.go`, `cmd_*.go` |
| `internal/tui` | Bubble Tea app (plan C): tabs, Plan/Recap/Triage modes, polling. | `app.go`, `keys.go`, `styles.go`, `poll.go`, `tab_*.go`, `mode_plan.go`, `mode_recap.go`, `forms.go` |
| `internal/mcpserver` | `tkr mcp`: stdio MCP server over `client`. | `server.go`, `tools_read.go`, `tools_write.go`, `inputs.go` |
| `internal/archtest` | Test-only package enforcing §1.3. | `deps_test.go` |
| `internal/testutil` | Test helpers (§13). Imported only from `_test.go`. | `clock.go`, `store.go`, `service.go`, `server.go`, `golden.go`, `scenario.go` |

Repository root: `go.mod`, `go.sum`, `Makefile`, `.golangci.yml`,
`scripts/fuzz.sh`, `README.md` (install, status-line snippet, off-host backup
example with restic/rsync, threat-model summary), `docs/`.

### 1.3 Allowed dependency graph

Only the internal edges below are allowed (stdlib always allowed; third-party
as listed). Anything not listed is forbidden.

| Package | May import (internal) | Third-party |
|---|---|---|
| `buildinfo`, `clock` | — | — |
| `civil` | — | — |
| `datespec` | `civil`, `domain` (for `Moment`/`BlockSpan` only) | — |
| `textsafe` | — | `x/ansi` |
| `domain` | `civil` | — |
| `fsutil`, `lock` | — | `x/sys/unix` |
| `config` | `civil`, `fsutil` | `toml` |
| `api` | `domain`, `civil` | — |
| `auth` | `domain`, `clock` | `x/crypto/argon2`, `x/time/rate` |
| `store` | `domain`, `civil` | `modernc.org/sqlite` |
| `backup` | `store`, `domain`, `civil`, `clock`, `fsutil` | — |
| `service` | `domain`, `civil`, `clock`, `store`, `backup`, `auth`, `textsafe` | — |
| `markdown` | — | goldmark, bluemonday |
| `web` | `service`, `domain`, `civil`, `datespec`, `auth`, `markdown`, `textsafe`, `clock`, `buildinfo` (reports are rendered from `service.Report` by templates) | — |
| `systemd` | — | `x/sys/unix` |
| `server` | `service`, `api`, `auth`, `web`, `store`, `backup`, `config`, `lock`, `fsutil`, `systemd`, `clock`, `civil`, `domain`, `textsafe`, `buildinfo` | `x/sys/unix` |
| `client` | `api`, `domain`, `civil`, `config`, `fsutil`, `clock` | — |
| `report` | `api`, `civil`, `textsafe` | — |
| `editor` | `fsutil`, `config`, `clock` | — |
| `offline` | `store`, `backup`, `auth`, `config`, `lock`, `fsutil`, `systemd`, `domain`, `civil`, `clock`, `textsafe`, `service` (only to build a temporary `service.Service` over a staged or offline store for `ReplayLedger`, `Reconcile`, `CheckAll` and system ops through `run`, §14 D34) | `x/term` |
| `tui` | `client`, `api`, `domain`, `civil`, `datespec`, `textsafe`, `editor`, `config` | bubbletea, bubbles, lipgloss |
| `mcpserver` | `client`, `api`, `domain`, `civil`, `datespec`, `config`, `buildinfo` | go-sdk, jsonschema-go |
| `archtest` | — (stdlib `go/parser`, `os/exec`) | — |
| `testutil` | anything (test-only; imported only from `_test.go`) | teatest (plan C) |
| `cli` | `client`, `api`, `domain`, `civil`, `datespec`, `textsafe`, `report`, `editor`, `config`, `buildinfo`, `fsutil`; **only** `cmd_serve.go` → `server`; **only** `cmd_{init,migrate,restore,import,purge,scrub,passwd,backup}.go` → `offline`; `cmd_tui.go`/`cmd_plan.go`… → `tui`; `cmd_mcp.go` → `mcpserver` | cobra, lipgloss, `x/term` |
| `cmd/tkr` | `cli` | — |

Rules:
- **R1** Business rules and authz live only in `service`. `server`/`web`
  handlers: decode → exactly one service call → encode.
- **R2** `cli` verbs, `tui`, `mcpserver` reach data only through `client`
  (exceptions are the files named above).
- **R3** `service` never imports `server`, `web`, `api`, `client`. `store`
  never imports `service`. `web` never imports `store` (sessions go through
  `service`).
- **R4** Only `clock` calls `time.Now`; nothing uses `time.Local`
  (forbidigo, §13.4).
- **R5** `domain`, `civil`, `datespec`, `textsafe`, `api`, `report` do no I/O.
- Enforcement: `internal/archtest/deps_test.go` runs `go list -json ./...`,
  and fails on any internal import edge or third-party module not in a table
  literally copied from §1.3, and on the per-file `cli` exceptions (it parses
  imports with `go/parser`).

### 1.4 Repository conventions for implementers

- `gofmt`/`goimports` clean; `make check` green before every commit.
- Committed migration files are never edited (§5.1). No secrets in the repo,
  test fixtures included (tests generate tokens/passwords at runtime).
- Add `AIDEV-NOTE:` / `AIDEV-TODO:` / `AIDEV-QUESTION:` anchors; grep for
  existing anchors before editing a package; never delete an `AIDEV-NOTE`.
  Required anchors (at minimum): `service.run` (single write path),
  `reconcileTx`, `setBlock` (slip/continue hooks), `revertTx` (conflict and
  net-zero rule), `civil.Zone.Instant` (DST), `store.ScrubEntities` and the
  ledger-first order in `PurgeLive`, derived-event skipping in `revertTx`, `backup.OpenLedger` (torn-tail truncation),
  `backup.Swap` (journal steps), the FTS triggers and views, the pragma
  verification hook, `socketGuard` + owner-token rule, `web/csrf.go`, the
  seq-based acknowledgement comparisons (§4.2), the shutdown drain (§4.10).
- Every exported function has a type-annotated signature (Go makes this
  automatic) and a doc comment; error values wrap with `%w`; `slog` only.

---

## 2. Core types

### 2.1 Representation rules

| Concept | Go | SQLite | JSON |
|---|---|---|---|
| Instant | `time.Time` (UTC internally) | `INTEGER` unix **milliseconds** | RFC 3339, ms precision, server-zone offset: `"2026-10-26T15:00:00.000-06:00"` |
| Calendar date | `civil.Date` | `TEXT 'YYYY-MM-DD'` (CHECK-validated) | `"2026-10-26"` |
| Minute of day | `civil.Minute` (`int16`, 0…1440) | `INTEGER` | `"HH:MM"` (`"24:00"` only as a block end) |
| Optional non-string | `sql.Null[T]` | `NULL` | `null` |
| Optional string | `string`, `""` = none | `NULL` (store maps `""`↔`NULL`) | `null` |
| IDs | named `int64` types | `INTEGER` | number |
| `op_id` | `domain.OpID [16]byte` | `TEXT` 32 lowercase hex | `"9f…"` (32 hex) |
| Bool | `bool` | `INTEGER 0/1` | `true/false` |

- All timestamps written by one op use the op's single `now` (§4.2). Ordering
  and acknowledgement comparisons never use timestamps; they use `ops.seq`
  (`last_placed_seq`, `slipped_seq`, `resolved_seq`, `task_triage.seq`, §4.2).
- Containment ("current block", "started", "ended") is computed on **instants**
  via `civil.Zone.Instant`, never by comparing minutes (fall-back hour).

### 2.2 `civil`

```go
package civil

type Date struct{ y int16; m, d uint8 } // zero value is invalid; optional dates use sql.Null[Date]

func NewDate(y int, m time.Month, d int) (Date, error)
func ParseDate(s string) (Date, error)            // strict "YYYY-MM-DD", real calendar date
func MustDate(s string) Date                      // tests only
func (d Date) String() string
func (d Date) IsZero() bool
func (d Date) AddDays(n int) Date
func (d Date) DaysSince(o Date) int
func (d Date) Weekday() time.Weekday
func (d Date) Compare(o Date) int                 // also Before, After, Equal via ==
func (d Date) MarshalText() ([]byte, error)       // + UnmarshalText
func (d Date) Value() (driver.Value, error)       // + (*Date).Scan

type Minute int16                                  // minutes since local midnight

const EndOfDay Minute = 1440
func ParseHHMM(s string, allow2400 bool) (Minute, error)  // "9:05", "09:05"; "24:00" only if allow2400

type EndMinute Minute // block end, 1..1440: text form allows "24:00"; a distinct type so wire
                      // schemas (MCP) and validation differ from Minute (0..1439)
func (m Minute) String() string                   // "09:05"
// MarshalText/UnmarshalText ("HH:MM"), Value/Scan (int64)

type GapPolicy uint8
const (
	GapForward GapPolicy = iota // nonexistent wall time → first valid instant after (defer/due)
	GapReject                   // nonexistent wall time → error (blocks)
)

type Zone struct{ name string; loc *time.Location }

func LoadZone(name string) (Zone, error)          // IANA only; rejects "", "Local", "UTC0"-style abbreviations
func (z Zone) Name() string
func (z Zone) DateOf(t time.Time) Date
func (z Zone) MinuteOf(t time.Time) Minute        // wall-clock minute (display/"--at" default)
func (z Zone) StartOfDay(d Date) time.Time        // instant of d 00:00 (GapForward)
func (z Zone) Instant(d Date, m Minute, p GapPolicy) (time.Time, error)
	// m == 1440 → StartOfDay(d+1). Ambiguous (fall-back) → the EARLIER instant,
	// chosen by probing both candidate offsets; never relies on time.Date's choice.
	// AIDEV-NOTE: DST correctness lives here; fuzz + table tests for gaps/overlaps.
func (z Zone) DeferMoment(d Date, m sql.Null[Minute]) time.Time // date-only → 00:00 of d
func (z Zone) DueMoment(d Date, m sql.Null[Minute]) time.Time   // date-only → StartOfDay(d+1) ("24:00")
```

### 2.3 `clock`

```go
package clock

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time // schedulers only
}
type Real struct{}
type Fake struct{ /* mu, now, waiters */ }
func NewFake(t time.Time) *Fake
func (f *Fake) Set(t time.Time)              // fires due After channels
func (f *Fake) Advance(d time.Duration)
```

### 2.4 `domain`: IDs, versions, enums

```go
package domain

type (
	TaskID         int64
	EntryID        int64
	BlockID        int64
	TagID          int64
	InterruptionID int64
	TokenID        int64
	EventID        int64
	OpSeq          int64 // ops.seq, monotonic; used for "AI changes since"
)

type OpID [16]byte
func NewOpID() OpID                          // crypto/rand
func ParseOpID(s string) (OpID, error)       // 32 lowercase hex
func (id OpID) String() string               // + Value/Scan, MarshalText/UnmarshalText

type Version int64                           // per-row; starts at 1
type Generation string                       // settings.db_generation: 16 lowercase hex (64 random bits); new on restore
type Expect struct {                         // parsed If-Match / etag fields
	Gen     Generation
	Version Version
}

// NOTE: enum constants are written compactly below; in code every constant MUST carry
// the named type explicitly (`StatusDone TaskStatus = "done"`), otherwise Go makes it an
// untyped string. `exhaustive` lint relies on the named types.
type TaskStatus string
const (StatusOpen TaskStatus = "open"; StatusDone = "done"; StatusDropped = "dropped")

type TriageReason string
const (
	ReasonSlipped  TriageReason = "slipped"
	ReasonOverdue  TriageReason = "overdue"
	ReasonDue      TriageReason = "due"
	ReasonContinue TriageReason = "continue"
	ReasonSurfaced TriageReason = "surfaced"
	ReasonReopened TriageReason = "reopened"
	ReasonManual   TriageReason = "manual"
	ReasonNew      TriageReason = "new"
)
// TriageOrder is highest-first (D§5.4). Rank() = len(TriageOrder) - index.
var TriageOrder = [...]TriageReason{ReasonSlipped, ReasonOverdue, ReasonDue, ReasonContinue,
	ReasonSurfaced, ReasonReopened, ReasonManual, ReasonNew}

type EntryKind string;     const (EntryWork EntryKind = "work"; EntryComplete = "complete")
type BlockStatus string;   const (BlockPlanned BlockStatus = "planned"; BlockDone = "done"; BlockWorked = "worked"; BlockMissed = "missed"; BlockCancelled = "cancelled")
type OutcomeSource string; const (OutcomeAuto OutcomeSource = "auto"; OutcomeUser = "user")
type CancelReason string;  const (CancelUser CancelReason = "user"; CancelTaskClosed = "task_closed"; CancelDeferred = "deferred"; CancelRescheduled = "rescheduled")
type DuePhase string;      const (DueNone DuePhase = "none"; DueDue = "due"; DueOverdue = "overdue") // also tasks.due_ack
type Priority string;      const (PriorityNone Priority = ""; PriorityH = "H"; PriorityM = "M"; PriorityL = "L")
type View string;          const (ViewTriage View = "triage"; ViewBacklog = "backlog"; ViewUpcoming = "upcoming"; ViewDone = "done"; ViewDropped = "dropped"; ViewDeleted = "deleted"; ViewAll = "all")
type UserOutcome string;   const (SetWorked UserOutcome = "worked"; SetMissed = "missed"; SetAuto = "auto")
type PlaceHow string;      const (PlaceKeep PlaceHow = "keep"; PlaceTop = "top"; PlaceBottom = "bottom"; PlaceBefore = "before"; PlaceAfter = "after")

type Moment struct {        // defer/due value (D§5.7)
	Date civil.Date
	Min  sql.Null[civil.Minute] // 0..1439
}
type BlockSpan struct {     // half-open [Start, End) on Date; 0 ≤ Start < End ≤ 1440
	Date  civil.Date
	Start civil.Minute
	End   civil.EndMinute
}
```

### 2.5 Actor, channel, scopes

```go
type ActorKind uint8
const (ActorOwner ActorKind = iota + 1; ActorToken; ActorSystem)

type Channel string
const (ChanCLI Channel = "cli"; ChanTUI = "tui"; ChanWeb = "web"; ChanMCP = "mcp"; ChanAPI = "api"; ChanSystem = "system")
// Channel is informational (X-Taskronin-Channel header; "web" set by web). Never used for authz,
// except: token management refuses ChanWeb (D§10.3) — web is in-process, so that value is trustworthy.

type Scopes uint8
const (
	ScopeRead Scopes = 1 << iota
	ScopeCapture
	ScopeWrite
	ScopeDestroy
	ScopeAdmin
	AllScopes = ScopeRead | ScopeCapture | ScopeWrite | ScopeDestroy | ScopeAdmin
	MCPDefaultScopes = ScopeRead | ScopeCapture | ScopeWrite // D§9.4
)
func ParseScopes(s string) (Scopes, error)   // "read,capture,write"; "" → 0 (system ops), unknown name → error
func (s Scopes) Has(req Scopes) bool          // all bits of req present
func (s Scopes) String() string               // canonical order: read,capture,write,destroy,admin

type Actor struct {
	Kind             ActorKind
	TokenID          TokenID   // 0 for system and for web-session owner
	TokenName        string    // set only for ActorToken
	Scopes           Scopes
	Channel          Channel
	MutationsPerHour int       // 0 = unlimited (owner, system)
	RemoteKey        string    // "socket" or client IP; password-gate bucket key
}
// Key is persisted in ops.actor, undo_stack.actor, idempotency.actor, tasks.origin:
// "owner" | "token:<name>" | "system". Token names are unique forever (revoked rows kept).
func (a Actor) Key() string
func (a Actor) IsAI() bool // Kind == ActorToken (D§9.4 "AI changes" = any non-owner token)
func SystemActor() Actor
```

### 2.6 Entities

```go
type Task struct {
	ID            TaskID
	Title, Notes  string
	Status        TaskStatus
	Rank          int64
	LastPlacedAt  sql.Null[time.Time]
	LastPlacedSeq sql.Null[OpSeq]       // ordering/acknowledgement (§4.2)
	DeferDate     sql.Null[civil.Date]
	DeferMin      sql.Null[civil.Minute]
	DueDate       sql.Null[civil.Date]
	DueMin        sql.Null[civil.Minute]
	DueAck        DuePhase
	DueAckDate    sql.Null[civil.Date]
	Project       string     // "" = none
	Priority      Priority
	Origin        string     // Actor.Key() of creator
	CompletedAt   sql.Null[time.Time]
	CompletedDate sql.Null[civil.Date]
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     sql.Null[time.Time]
	PurgedAt      sql.Null[time.Time]   // set by ScrubEntities
	Version       Version
}

type TriageRow struct {
	TaskID TaskID
	Reason TriageReason
	Since  time.Time // informational
	Seq    OpSeq     // op that added it; triage ordering (§4.2)
}

type LogEntry struct {
	ID          EntryID
	TaskID      sql.Null[TaskID]
	Kind        EntryKind
	Title       string
	Notes       string
	WorkDate    civil.Date
	OccurredMin sql.Null[civil.Minute]
	Reopened    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   sql.Null[time.Time]
	PurgedAt    sql.Null[time.Time]
	Version     Version
}

type TimeBlock struct {
	ID            BlockID
	TaskID        TaskID
	Span          BlockSpan
	Status        BlockStatus
	OutcomeSource OutcomeSource // "" while planned/cancelled
	CancelReason  CancelReason  // "" unless cancelled
	Slipped       bool
	SlippedAt     sql.Null[time.Time] // last time it became slipped; kept after unslip
	SlippedSeq    sql.Null[OpSeq]     // op that last slipped it (acknowledgement, §4.2)
	CreatedAt     time.Time
	ResolvedAt    sql.Null[time.Time] // set on every status change; NULL while planned
	ResolvedSeq   sql.Null[OpSeq]
	DeletedAt     sql.Null[time.Time] // only set by revert of the creating op
	Version       Version
}

type Focus struct {        // singleton row
	TaskID  sql.Null[TaskID]
	Date    sql.Null[civil.Date]
	SetAt   sql.Null[time.Time]
	Version Version
}

type Interruption struct {
	ID          InterruptionID
	At          time.Time
	Date        civil.Date
	FromTaskID  sql.Null[TaskID]
	FromBlockID sql.Null[BlockID]
	ToTaskID    TaskID
	OpID        OpID
	DeletedAt   sql.Null[time.Time]
}

type Token struct {
	ID               TokenID
	Name             string
	SecretHash       [32]byte
	Scopes           Scopes
	IsOwner          bool
	MutationsPerHour int
	CreatedAt        time.Time
	ExpiresAt        sql.Null[time.Time]
	LastUsedAt       sql.Null[time.Time]
	RevokedAt        sql.Null[time.Time]
}
```

Read-side views (returned by `service`, converted to wire types by `api`):

```go
type TaskView struct {
	Task
	Reasons          []TriageRow // highest first
	Tags             []string    // sorted, case-insensitive
	View             View        // computed classification (§4.4)
	Deferred         bool        // defer moment > now
	DuePhase         DuePhase
	DueSoon          bool        // due_date ≤ today + due_soon_days
	SlipsLifetime    int
	SlipsSinceTriage int
}
type EntryView struct {
	LogEntry
	TaskTitle string // "" for standalone entries
}
type TaskDetail struct {
	TaskView
	Entries       []EntryView  // non-deleted, newest first
	Blocks        []BlockView  // all non-deleted
	Interruptions []Interruption
}
type BlockView struct {
	TimeBlock
	TaskTitle    string
	TaskVersion  Version // the task's version, for reblock's task precondition (R4-7)
	Current      bool // planned and [start,end) contains now
	Started      bool // start instant ≤ now
	Ended        bool // end instant ≤ now
	NeedsReblock bool // planned, Ended, no non-deleted entry for (task, date)
}
```

### 2.7 Ops and events

```go
type OpKind string // "<area>.<verb>"; full list in §3.10
type OpState string
const (OpActive OpState = "active"; OpReverted OpState = "reverted")

type OpRecord struct {
	Seq         OpSeq
	ID          OpID
	Kind        OpKind
	Actor       string  // Actor.Key()
	Channel     Channel
	At          time.Time
	Scopes      Scopes  // scopes this op required; revert requires the same (D§5.10)
	Revertible  bool    // false for system, admin, purge, and any op touching purged content
	State       OpState
	RevertsOpID sql.Null[OpID] // set on revert/undo/redo ops
	Via         string  // "" | "revert" | "undo" | "redo"
}

type EntityKind string
const (
	EntTask EntityKind = "task"; EntTriage = "triage"; EntTaskTags = "task_tags"; EntEntry = "entry"
	EntBlock = "block"; EntFocus = "focus"; EntInterruption = "interruption"
	EntToken = "token"; EntSetting = "setting"; EntAuth = "auth"
)
type Action string
const (
	ActInsert Action = "insert"; ActUpdate = "update"; ActMove = "move"
	ActTombstone = "tombstone"; ActUntombstone = "untombstone"
	ActAdd = "add"; ActRemove = "remove"; ActSet = "set"; ActRevoke = "revoke"; ActPurge = "purge"
)

type Event struct {
	ID       EventID
	OpID     OpID
	Entity   EntityKind
	EntityID int64            // triage/task_tags: task id; focus: 1; setting: 0 (key in JSON)
	TaskID   sql.Null[TaskID] // denormalized for task history
	Action   Action
	Before   json.RawMessage  // changed fields only, keyed by column name; NULL for insert/add
	After    json.RawMessage  // changed fields only; NULL for tombstone-less removes
	Derived  bool             // recorded by recompute (block re-derivation + its hook triage changes); revert re-derives instead of replaying (§4.6)
}
type EntityRef struct {
	Entity EntityKind
	ID     int64
}
```

Event payload conventions (field-level, column names as keys):

| Entity / action | Before | After |
|---|---|---|
| task insert | — | all user fields |
| task update | changed fields | changed fields |
| task move | `{"after_id": N\|null}` (predecessor in global `(rank,id)` order) | `{"after_id": M\|null}` |
| task/entry tombstone, untombstone | `{"deleted_at": …}` | `{"deleted_at": …}` |
| triage add / remove | — / `{"reason","since","seq"}` | `{"reason","since","seq"}` / — |
| task_tags set | `{"tags":[…]}` | `{"tags":[…]}` |
| entry insert/update | as task | as task |
| block insert/update | changed columns | changed columns |
| focus set | `{"task_id","date"}` | `{"task_id","date"}` |
| interruption insert / tombstone | — | row |
| token insert / revoke | — | `{"name","scopes","expires_at"}` (never secrets) |
| setting update | `{"key","value"}` | `{"key","value"}` |
| auth update | — | `{"what":"password_changed"}` (content-free) |
| task/entry purge | — | `{"ids":[…]}` (content-free, D§5.11) |

### 2.8 Errors

```go
type Code string
const (
	CodeMalformed          Code = "malformed"                // 400
	CodeUnauthenticated    Code = "unauthenticated"          // 401
	CodeForbidden          Code = "forbidden"                // 403
	CodePeerRejected       Code = "peer_rejected"            // 403
	CodePasswordRequired   Code = "password_required"        // 403
	CodePasswordInvalid    Code = "password_invalid"         // 403
	CodeNotFound           Code = "not_found"                // 404
	CodeDeleted            Code = "deleted"                  // 409 mutating a tombstoned entity
	CodeConflict           Code = "conflict"                 // 409 state/invariant violation
	CodeRevertConflict     Code = "revert_conflict"          // 409 (+Conflicts)
	CodeNotRevertible      Code = "not_revertible"           // 409
	CodePrecondition       Code = "precondition_failed"      // 412 (+Current)
	CodeTooLarge           Code = "body_too_large"           // 413
	CodeUnsupportedMedia   Code = "unsupported_media_type"   // 415
	CodeValidation         Code = "validation"               // 422 (+Fields)
	CodeIdemMismatch       Code = "idempotency_mismatch"     // 422
	CodeNothingToUndo      Code = "nothing_to_undo"          // 409 empty undo stack
	CodeNothingToRedo      Code = "nothing_to_redo"          // 409 empty redo stack
	CodePreconditionReq    Code = "precondition_required"    // 428 missing If-Match
	CodeIdemRequired       Code = "idempotency_key_required" // 428 missing Idempotency-Key
	CodeGenerationMismatch Code = "generation_mismatch"      // 412 request bound to another db_generation (R3-3)
	CodeRateLimited        Code = "rate_limited"             // 429 (+RetryAfter)
	CodeInternal           Code = "internal"                 // 500
	CodeBusy               Code = "busy"                     // 503 (+RetryAfter)
)

type Error struct {
	Code       Code
	Detail     string           // safe for users; never contains secrets
	Fields     []FieldError
	Conflicts  []RevertConflict
	Current    any              // current representation for 412 (TaskView, LogEntry, BlockView)
	RetryAfter time.Duration
	Err        error            // wrapped cause; logged, never sent
}
func (e *Error) Error() string
func (e *Error) Unwrap() error
func Errorf(code Code, format string, args ...any) *Error
func AsError(err error) (*Error, bool) // non-*Error → CodeInternal

type FieldError struct{ Field, Message string }
type RevertConflict struct {
	OpID     OpID
	Kind     OpKind
	Actor    string
	At       time.Time
	Entity   EntityKind
	EntityID int64
}
```

These codes are the **only** error vocabulary (the behavior catalog aligns to
them). Catalog shorthand → code: `stale` → `precondition_failed`;
`unauthorized` → `unauthenticated` (bad/missing/expired token, owner token off
the socket, cookie on `/api/v1`) or `peer_rejected` (socket peer UID mismatch,
`Forwarded`/`X-Forwarded-*`/`Via` present); password errors are 403
(`password_required`, `password_invalid`), not 401; a missing
`Idempotency-Key` is `idempotency_key_required` (428) and a malformed one
`malformed` (400); a
mutation that targets or references a tombstoned task/entry/block is `deleted`
(409), not `not_found` (GET still returns tombstoned records). An op that
returns an error changes nothing and writes no events — the single exception
is `Undo`/`Redo` popping an entry that can never succeed (§4.6, `committedError`).

---

## 3. Service API

### 3.1 Conventions

```go
package service

type Options struct {
	Store       *store.Store
	Clock       clock.Clock
	Zone        civil.Zone
	WeekStart   time.Weekday
	DueSoonDays int            // default 3
	SlipGrace   time.Duration  // 15m (D§5.6)
	UndoDepth   int            // 1000 per actor per stack
	Backups     *backup.Manager
	Ledger      *backup.Ledger
	Passwords   *auth.PasswordGate
	Logger      *slog.Logger
}
func New(o Options) (*Service, error)

// Meta accompanies every mutating call.
type Meta struct {
	Actor domain.Actor
	Idem  Idem              // zero only for idempotency-exempt ops
	Gen   domain.Generation // required on every mutation (header Taskronin-Generation / web `_gen`); offline callers pass the live value
}
type Idem struct {
	Key    string // 16..128 chars [A-Za-z0-9_-]
	Method string // "POST" / "PATCH" (web: "POST")
	Route  string // route pattern + path values, e.g. "POST /api/v1/tasks/42/complete"
	Body   []byte // canonical: json.Marshal of the strictly-decoded request struct (web: sorted form values)
}

type OpResult struct {
	OpID     domain.OpID     // zero when NoChange
	Created  bool            // HTTP 201
	Replayed bool
	NoChange bool
	Refs     []domain.EntityRef
	Task     *domain.TaskView
	Entry    *domain.LogEntry
	Block    *domain.BlockView
	Tasks    []domain.TaskView // bulk ops
	Effects  []Effect          // derived from the op's events (rebuildable on replay)
	Warnings []string          // e.g. "logged against a done task"; empty on replay
}

type Effect struct {
	Kind    EffectKind
	TaskID  domain.TaskID
	BlockID domain.BlockID
	EntryID domain.EntryID
	Reason  domain.TriageReason
	From    string // e.g. old title, old date
	To      string
}
type EffectKind string // created, updated, retitled, placed, moved, deferred, undeferred, due_set,
	// due_cleared, blocked, block_moved, block_cancelled, block_resolved, outcome_set, slipped,
	// unslipped, reason_added, reason_removed, focus_set, focus_cleared, interrupted,
	// entry_logged, entry_updated, entry_relinked, entry_deleted, entry_restored, completed,
	// reopened, dropped, deleted, undeleted, reverted, reviewed, purged
```

Mutating methods have the shape `func (s *Service) X(ctx context.Context, m Meta, in XIn) (*OpResult, error)`.
Reads have the shape `func (s *Service) Y(ctx context.Context, a domain.Actor, in YIn) (YOut, error)`.
Every method first checks `a.Scopes.Has(required)` (403 `forbidden`) before touching the DB.

Column legend for the tables below — **Scope**: required scopes. **Pl**: placement
(D§5.5; `o.place`, §4.5: deletes all reason rows, sets `last_placed_at`/`last_placed_seq`,
`due_ack = duePhase`, `due_ack_date = today`, and suppresses `slipped`/`continue`
additions for that task within the op). **Every placement clears a pending defer**
(`defer_date`/`defer_min` → NULL) — `Place`, `AcceptNew`, `Block`, `Reblock`,
`Complete`, `Drop`, `Defer(nil)`, `AddTask` with a placement — except `Defer` with a
moment, which sets one (P-3). **IfM**: `Expect` required (412 on mismatch).
**Idem**: `Meta.Idem` required. **Events**: entity:action written (besides
derived block/triage events from §4.5).

### 3.2 Tasks

| Method | Input (beyond `ID`/`Expect`) | Scope | Pl | IfM | Idem | Events / key rules |
|---|---|---|---|---|---|---|
| `AddTask` | `Title, Notes string; Due *Moment; Tags []string; Project string; Priority; Place *AddPlacement` | capture; **+write** if `Place != nil` (§14 A18) | if `Place` | — | ✓ | task:insert; triage:add `new` unless placed; task_tags:set; block:insert / focus:set / interruption:insert per placement. Rank = bottom unless placed. `Due` moment must be > now (A12); `--due` is not a placement. `Place.Ref` missing → `not_found`, tombstoned → `deleted`. `Place` from an `ActorToken` → `forbidden`: token-created tasks always land in triage (R-b). `Place{block}` uses the `Block` helper `o.createBlock` with identical rules (R-n). |
| `UpdateTask` | `Title, Notes, Project *string; Priority *Priority; TagsAdd, TagsRemove []string` | write | — | ✓ | ✓ | task:update, task_tags:set. Retitle is its own effect. |
| `SetDue` | `Due *Moment` (nil clears) | write | — | ✓ | ✓ | task:update(due_date, due_min, due_ack=none, due_ack_date=NULL); triage:remove due/overdue. `validation` if the due moment ≤ now (A12; `today` date-only is fine — its moment is 24:00) or if it violates D§5.7 against an existing defer. |
| `Place` | `How PlaceHow; Ref TaskID` | write | ✓ | ✓ | ✓ | task:move (unless keep), task:update(placement fields; clears defer if set — §14 D5); triage:remove all. `Ref` missing → `not_found`, tombstoned → `deleted`. |
| `AcceptNew` | `Items []struct{ID; Expect}` | write | ✓ each | per item | ✓ | One op, all-or-nothing (C6): empty or duplicate IDs → `validation`; any item whose reasons are not exactly `{new}` → `conflict` naming it; any stale item → `precondition_failed`. Placed at the bottom in ascending `new` seq order. |
| `Defer` | `Until *Moment` (nil = clear defer: `defer ID none`, A24) | write | ✓ | ✓ | ✓ | Set: task:update(defer_*); rank never changes; blocks via `closeForDefer` (`cancelled/deferred`, slip if today unstarted & outside grace); focus cleared if focused. `validation` if the defer moment ≤ now, the task is overdue ("change the due date first"), or defer ≥ due (D§5.7). **`nil`**: only clears `defer_*` and places (= `Place{keep}`); never touches blocks or focus. |
| `MarkManual` | — | write | — | ✓ | ✓ | triage:add `manual`; task:update(defer cleared) if deferred (§14 D4). |
| `Complete` | `As *string; Date civil.Date (zero=today); LogTitle, LogNotes string` | write | ✓ | ✓ | ✓ | task:update(title if As, status=done, completed_*); entry:insert kind=complete (title = LogTitle else final title); blocks per D§5.6 `done --date`; triage:remove all; focus:set nil if focused. Date ≤ today; `completed_date = Date`, `completed_at = now` even when backdated. From `open` only. |
| `Reopen` | `RemoveEntry bool` | write; **+destroy** if `RemoveEntry` | — | ✓ | ✓ | task:update(status=open, completed_*=NULL, due_ack=none, due_ack_date=NULL); entry:update(reopened=1) on the task's **non-deleted** `complete` entry with `reopened=0`, or if none exists the newest (`ORDER BY id DESC LIMIT 1`) purged `complete` entry with `reopened=0` (content untouched, never re-tombstoned) — and with `RemoveEntry` also entry:tombstone unless already tombstoned; triage:add `reopened`. From `done` or `dropped`. With `RemoveEntry` the recompute may re-derive the completion day's block to `missed` → slip + `slipped` reason (kept as specified, A23; the CLI echoes it). |
| `Drop` | — | destroy | ✓ | ✓ | ✓ | task:update(status=dropped); blocks per D§5.6; triage:remove all; focus cleared. From `open` only. |
| `Delete` | — | destroy | — | ✓ | ✓ | task:tombstone; blocks per D§5.6 (`drop`/`delete`); triage:remove all; focus cleared. |
| `Undelete` | — | **write** (A18) | — | ✓ | ✓ | Purged task → `conflict` ("purged", R3-4). task:untombstone; if open: triage:add `manual` and clear a future defer (like `MarkManual`, D4); not open: no reason (A3). Blocks stay as they are; focus untouched. |

```go
type AddPlacement struct {
	How   string           // "top"|"bottom"|"before"|"after"|"defer"|"block"|"now"
	Ref   domain.TaskID    // before/after
	Defer domain.Moment    // defer
	Block domain.BlockSpan // block
}
```
`now` = top + focus + interruption per §14 D35.

### 3.3 Blocks

| Method | Input | Scope | Pl | IfM | Idem | Events / key rules |
|---|---|---|---|---|---|---|
| `Block` | `TaskID; Expect (task); Span` | write | ✓ | ✓ task | ✓ | task:update(defer cleared + placement fields); block:insert via `o.createBlock`. Task open (done/dropped → `conflict`, C2), not deleted (`deleted`); a deferred task is allowed — blocking clears the defer (D§5.3); `Span.Date ≥ today`; end instant > now, start may be past (A4, `validation`); overlap → `conflict` naming the block; start or end at a nonexistent DST wall time → `validation`, including an end exactly at the gap start such as `01:00–02:00` on a spring-forward day (A5; message suggests `03:00`). |
| `Reblock` | `BlockID; Expect (block); TaskExpect domain.Expect; Span` | write | ✓ (task) | ✓ block + task | ✓ | Old block `planned` and unstarted, new date = its date → block:update in place (no new row). Old block `planned` otherwise → it ends (unstarted: `cancelled/rescheduled` + slip per table; started: resolved by derivation — a started block is never updated in place) and block:insert. Old block already resolved or cancelled (`missed`/`worked`/`done`/`cancelled`) → untouched, block:insert only (A7). Overlap is checked after the old block ends; any failure leaves everything unchanged. |
| `Unblock` | `BlockID; Expect` | write | — | ✓ | ✓ | Unstarted → `cancelled/user` (+slip per table); started → resolved by derivation. Block not `planned` → `conflict`. Not a placement. |
| `SetOutcome` | `BlockID; Expect; Outcome UserOutcome` | write | — | ✓ | ✓ | `worked`/`missed`: status set, `outcome_source=user`, `resolved_at=now`; slip/continue rules apply (a user `worked` adds `continue` like a re-derive, A8). Allowed on resolved blocks and on today's `planned` blocks once started (A9); unstarted planned or cancelled → `conflict` ("use unblock"). `auto`: date < today → re-derive (auto); date = today → back to `planned` (resolves at midnight). |

### 3.4 Entries

| Method | Input | Scope | Pl | IfM | Idem | Events / key rules |
|---|---|---|---|---|---|---|
| `AddEntry` | `TaskID sql.Null[TaskID]` (explicit, D§5.2); `Title, Notes; WorkDate (zero=today); At sql.Null[Minute]` | capture | — | — | ✓ | entry:insert; recompute (§4.5). WorkDate ≤ today; if WorkDate = today and At set, At ≤ now's minute. `At` is 0–1439 and never converted to an instant, so a nonexistent DST wall time is accepted (A20). Task must exist and not be deleted (closed is OK + warning). |
| `UpdateEntry` | `Title, Notes *string; WorkDate *civil.Date; At *sql.Null[Minute]` | destroy | — | ✓ | ✓ | entry:update; recompute over before∪after pairs. `complete` entries: only `Notes` editable. Purged → `conflict`. |
| `RelinkEntry` | `TaskID sql.Null[TaskID]` | write | — | ✓ | ✓ | entry:update(task_id); recompute over before∪after pairs (a `null` side contributes no pair). `complete` entries → `validation`. Purged → `conflict`. |
| `DeleteEntry` | — | destroy | — | ✓ | ✓ | entry:tombstone; recompute. Refused for a `complete` entry whose task is done (use `Reopen{RemoveEntry}`). |
| `UndeleteEntry` | — | destroy | — | ✓ | ✓ | entry:untombstone; recompute. Purged → `conflict` (R3-4). |

### 3.5 Focus

| Method | Input | Scope | Pl | IfM | Idem | Events / key rules |
|---|---|---|---|---|---|---|
| `SetFocus` | `TaskID sql.Null[TaskID]; Interrupt bool` | write | — | — (§14 D3) | ✓ | focus:set (date=today); interruption:insert per §14 D35. Task must be open and not deferred (`conflict`, C2), not deleted (`deleted`). Revertible; revert restores focus only when the before-image still satisfies I6 (C1, §4.6). |

### 3.6 Revert / undo / redo / AI changes

| Method | Input | Scope | Idem | Rules |
|---|---|---|---|---|
| `Revert` | `OpID` | the reverted op's `Scopes`; a non-owner actor may revert only its own ops | ✓ | Forward op (`via="revert"`), pushed on the actor's undo stack. §4.6. |
| `Undo` | — | scopes of the popped op | ✓ | §4.6; `via="undo"`; never pushed. Empty stack: §3.9. |
| `Redo` | — | scopes of the popped op | ✓ | §4.6; `via="redo"`; never pushed. Empty stack: §3.9. |
| `ListAIChanges` (read) | `IncludeReviewed bool; Cursor; Limit` → `AIChanges{ReviewedThrough OpSeq; Unreviewed int; Ops []OpSummary; NextCursor string}` | read | — | Ops with `actor LIKE 'token:%'` plus any `settings.retention_lowered` op; newest first. |
| `MarkAIReviewed` | `Through OpSeq` | write + **`ActorOwner` only** (R-a) | ✓ | `Through ≥ o.rec.Seq` (i.e. beyond the latest op committed before this one) → `validation`. setting:update `ai_reviewed_through = max(current, Through)`. Not revertible. |

### 3.7 Reads and aggregates

| Method | Input → Output | Scope | Reconcile first |
|---|---|---|---|
| `Clock` | → `ClockView{Now; Today; Zone string; WeekStart; DueSoonDays}` | any authenticated | — |
| `WhoAmI` | → `{Actor; Scopes}` | any | — |
| `Today` | → `TodayView` (D§5.4 Today, incl. `TriageTop` = first 5 triage tasks, `DueSoon`, `Overdue`) | read | ✓ |
| `Upcoming` | → `UpcomingView{Deferred []TaskView /* defer moment, then rank */; DueSoon []TaskView /* non-deferred, due_date − today ≤ due_soon_days */}` | read | ✓ |
| `Status` | → `StatusView` (§9.5) | read | ✓ |
| `Day` | `Date` → `DayView{Date; Blocks []BlockView; Entries []EntryView; Unlinked []EntryView; Interruptions []Interruption}` (recap) | read | ✓ |
| `ListTasks` | `View; Tag; Project; Limit; Cursor` → `Page[TaskView]`. Order: triage = highest reason, then that reason's `seq` ascending (oldest first, A2; `since` is display-only), then id; backlog = `(rank, id)`; upcoming = defer moment, then rank; done/dropped = completion/update time desc. | read | ✓ for triage/backlog/upcoming |
| `GetTask` | `ID` → `TaskDetail` (tombstoned tasks returned with `View=deleted`) | read | — |
| `ListEntries` | `From, To civil.Date; TaskID *sql.Null[TaskID] (nil=any, Null=standalone); IncludeDeleted; Limit; Cursor` → `Page[EntryView]`, ordered `work_date` desc, timed before untimed, `occurred_min` desc, `created_at` desc, id desc. `From`/`To` required (span ≤ 366 d) unless `TaskID` is a task. | read | — |
| `GetEntry`, `GetBlock` | `ID` | read | — |
| `ListBlocks` | `From, To civil.Date; TaskID sql.Null[TaskID]` → `[]BlockView` ordered by date, start, id. `From`/`To` required (span ≤ 366 d) unless `TaskID` is set. | read | — |
| `History` | `TaskID; Limit; Cursor` → `Page[EventView]` (events with `task_id = ID`, joined with ops) | read | — |
| `ListOps` / `GetOp` | `Actor; SinceSeq; Limit; Cursor` / `OpID` → `OpSummary` / `OpDetail{OpRecord; Events}` | read | — |
| `Report` | `From, To; Tag; Project; Todo, Upcoming bool` → `service.Report` (same shape as `api.Report`, §6.5, with domain types; web renders it with templates). `From > To` → `validation`. | read | ✓ if Todo/Upcoming |
| `Search` | `Query; Kind (task\|entry\|all); Limit; Cursor` → `Page[SearchHit]` | read | — |
| `ListTags` | → `[]TagCount` | read | — |
| `Settings` | → `SettingsView{KeepDaily, KeepWeekly int}` | read | — |

Filters everywhere (`ListTasks`, `Report`, MCP, web, CLI `-t`/`-P`): `Tag` is an
exact, case-insensitive tag match; `Project = X` matches `X` and its dotted
descendants on a dot boundary — `project = :x OR substr(project, 1, length(:x) + 1) = :x || '.'`
(so `-P infra` matches `infra.keycloak` but not `infrastructure`; A21). Under
either filter, standalone entries (no task) are excluded.

### 3.8 Admin, auth, data lifecycle

| Method | Input | Scope / auth | Idem | Rules |
|---|---|---|---|---|
| `CreateToken` | `Name; Scopes; ExpiresAt sql.Null[time.Time]; MutationsPerHour` → `TokenCreated{Token; Secret string}` | admin; refused for `ChanWeb` | **exempt** (secret in response) | Name `[A-Za-z0-9._-]{1,64}`, unique forever; `owner.*` reserved for owner tokens. Not revertible. token:insert. |
| `ListTokens` | → `[]Token` (no hashes) | admin; not web | — | |
| `RevokeToken` | `ID` | admin; not web | ✓ | Owner token refused (rotate via `passwd --reset`). token:revoke. |
| `UpdateSettings` | `KeepDaily, KeepWeekly *int; Password string` | admin; password iff lowering | **exempt** | Floors 3/1 (D§7.3). Lowering → password via gate and op kind `settings.retention_lowered` (listed in AI changes); otherwise `settings.update`. |
| `ChangePassword` | `Current, New string` | admin; `Actor.Kind == ActorOwner` | **exempt** | Verify `Current` through gate; min 8, max 1024 bytes; revokes all web sessions; auth:update. |
| `AuthenticateToken` | `bearer string; verifiedSocket bool; ch Channel; remoteKey string` → `domain.Actor` | none (it is authentication) | — | §7.1; owner token only with `verifiedSocket`; owner + `ChanMCP` → `forbidden`. |
| `Login` (web) | `RemoteKey, Password` → `Session{CookieValue, CSRFToken, ExpiresAt}` | none | exempt | Refused if `web_login_disabled`. |
| `Logout` / `Authenticate` (web) | cookie value → `domain.Actor{Owner, ChanWeb, AllScopes}` + CSRF secret | session | — | Idle 7 d / absolute 30 d. |
| `RunBackup` | → `backup.SnapshotInfo` | admin | exempt (not a DB op; a duplicate snapshot is harmless) | §12.1 |
| `BackupStatus` | → `BackupStatus{Snapshots; LastSuccessAt; LastError; Healthy; Warning}` | admin | — | Healthy = last success ≤ 48 h and last attempt succeeded. |
| `Export` | `w io.Writer` | admin | — | §12.4; one read tx. |
| `PurgeLive` | `Target (task\|entry); ID; WithEntries bool` → `PurgeLiveResult{TaskIDs, EntryIDs, LinkedNotIncluded []int64; Changed bool}` | **offline only** (S4): called by `offline.Purge` after it holds the lock and verified the password; never routed by the server | — | §12.6 step 5 |
| `Reconcile` | — | internal (system) | — | §4.4 |
| `ReapplyLedger` | — | internal (startup/restore/import) | — | §12.6 |
| `FlushUsage` | — | internal | — | Writes coalesced `last_used_at`/`last_seen_at` (≤ 1/min, no events). |

### 3.9 Error and invariant summary per op family

| Situation | Code |
|---|---|
| Task/entry/block missing | `not_found` |
| Target tombstoned (any mutation except `Undelete*`, purge) | `deleted` |
| Target purged (`purged_at` set): undelete, update, relink (purged entities are immutable; `Reopen` may still set `reopened=1` on a purged completion entry) | `conflict` ("purged") |
| Mutation with a missing / stale `Taskronin-Generation` | `precondition_required` / `generation_mismatch` |
| Illegal status transition (e.g. done→drop, complete on dropped) | `conflict` |
| Overlap; block on a non-open task; focus on a non-open or deferred task; unblock/outcome on the wrong block state | `conflict` |
| Field-level: past block end, past due/defer moment, future work/completion date, defer/due ordering (D§5.7), deferring an overdue task, DST gap, relinking a `complete` entry, bad enum/length | `validation` |
| Undo/redo on an empty stack (the one definition; §3.6, §4.6, §6.4 refer here) | `nothing_to_undo` / `nothing_to_redo` |
| If-Match missing / stale (incl. generation) | `precondition_required` / `precondition_failed` |

### 3.10 Op kinds

`task.add task.update task.due task.place task.accept_new task.defer task.manual
task.complete task.reopen task.drop task.delete task.undelete block.create
block.reblock block.unblock block.outcome entry.add entry.update entry.relink
entry.delete entry.undelete focus.set op.revert op.undo op.redo ai.review
token.create token.revoke settings.update settings.retention_lowered auth.password purge.task purge.entry
system.reconcile system.ledger_replay system.import system.restore`.
Non-revertible: `ai.review`, `token.*`, `settings.*`, `auth.password`,
`purge.*`, `system.*`, and any op with `purged=1`.

---

## 4. Transactions and concurrency

### 4.1 Connections

Every DSN is an escaped `file:` URI built with
`(&url.URL{Scheme: "file", OmitHost: true, Path: absPath, RawQuery: q.Encode()}).String()`.
SQLite consumes `mode`; modernc consumes its underscore keys (accepted by
v1.60.1); SQLite ignores the rest. **`mode=rw` for every open of an existing
database** — a missing file is an error ("database not found; run `tkr init`"),
never a new empty DB. `mode=rwc` only for files this process is creating:
`init` staging and the staged copies of restore, import and scrub (§12.0).

Common params: `_busy_timeout=5000&_foreign_keys=1&_synchronous=FULL&_defensive=1&_dqs=0&_pragma=secure_delete(1)&_pragma=trusted_schema(0)`.

| Pool | Extra params | Pool settings | Used for |
|---|---|---|---|
| writer | `mode=rw&_txlock=immediate&_journal_mode=WAL` | `MaxOpenConns(1)`, `MaxIdleConns(1)`, no lifetime | every op, reconcile, maintenance, migrations |
| reader | `mode=rw&_txlock=deferred&_journal_mode=WAL&_query_only=1` | `MaxOpenConns(4)` | all reads, in `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` |
| snapshot | `mode=rw&_txlock=deferred&_journal_mode=WAL` | `MaxOpenConns(1)` | `VACUUM INTO` only (§12.1) |
| staging | `mode=rwc&_txlock=immediate&_journal_mode=DELETE` | ephemeral | **copies only** (init/restore/import/scrub staging, snapshot verify). `journal_mode` is persistent, so it is never used on the live DB or an original snapshot. |

- **Pragma verification** (`store/open.go`): a package-level
  `sqlite.RegisterConnectionHook` checks every new connection against the
  values its DSN requested — `journal_mode`, `synchronous`=2, `foreign_keys`=1,
  `busy_timeout`=5000, `secure_delete`=1, `trusted_schema`=0, `query_only`;
  defensive mode by `PRAGMA writable_schema=ON` then reading back 0; `dqs=0` by
  `SELECT "taskronin_dqs_probe"` failing. Any mismatch fails the connection
  (and therefore `store.Open`). AIDEV-NOTE anchor.
- `store.Open` runs `PRAGMA quick_check` on the writer and refuses on anything but
  `ok`; asserts `sqlite_version() ≥ 3.44` (FTS5 `secure-delete`).
- `store.Q` = `interface{ ExecContext; QueryContext; QueryRowContext }`; every
  store function takes a `Q`.
- `store.MapErr(err) error`: a SQLite constraint error whose message starts with
  `taskronin:` (the guard triggers, §5.2) becomes `domain.CodeConflict` with that
  message; UNIQUE/CHECK violations become `conflict`/`validation` as appropriate;
  anything else is `internal`. Every store write passes errors through it.

### 4.2 Time and ordering

- `now` = `clock.Now().Truncate(time.Millisecond)`, read **once per
  transaction**; it drives every date, deadline, block, rate-window and expiry
  decision. There is no logical clock: after a backward clock correction the
  server simply uses the corrected time.
- **Ordering and acknowledgement use `ops.seq`** (AUTOINCREMENT, strictly
  increasing, carried across restore/import by `RaiseSequences`):
  `tasks.last_placed_seq`, `time_blocks.slipped_seq`/`resolved_seq`,
  `task_triage.seq`. Comparisons such as "slipped since last triage" are
  `slipped_seq > COALESCE(last_placed_seq, 0)`. A slip and a placement in the
  same op share a seq, so the slip counts as acknowledged (`>` is false).
- `*_at` instants are informational (display, history, rate windows, expiry).
- `duePhase(t, now, today, zone)`: `overdue` if a due is set and
  `DueMoment ≤ now`; else `due` if `due_date ≤ today`; else `none`. Placements
  set `due_ack = duePhase(…)`, `due_ack_date = today`.

### 4.3 The op runner (`service/op.go`)

```go
type opClass uint8
const (classForward opClass = iota + 1; classRevert; classUndo; classRedo)

type op struct {
	tx       *sql.Tx
	rec      domain.OpRecord             // rec.Seq set by the ops INSERT (step 11)
	actor    domain.Actor
	now      time.Time
	today    civil.Date
	target   *domain.OpRecord            // revert/undo/redo: selected in step 7
	placed   map[domain.TaskID]bool
	dirty    map[domain.EntityRef]bool   // tasks/entries/blocks/focus: version++ (+updated_at) once at finalize
	pairs    map[pairKey]struct{}        // (task_id, work_date) for recompute
	events   []domain.Event
	warnings []string
}
type opSpec struct {
	kind           domain.OpKind
	class          opClass
	scopes         domain.Scopes // forward ops; revert/undo/redo take the target's (step 7)
	idem           bool          // Meta.Idem required
	revertible     bool
	ownerOnly      bool
	notWeb         bool
	noReconcile    bool          // historical-copy ledger replay only (R2-2)
	structuralOnly bool          // invariants: CheckStructural rules only (R2-2)
}
// committedError: a stack entry that can never succeed. The runner deletes the stack row,
// stores the terminal error in the idempotency row, commits, and returns Err (§4.6).
type committedError struct{ Err *domain.Error; StackRow undoRowKey }

func (s *Service) run(ctx context.Context, m Meta, spec opSpec, fn func(*op) error) (*OpResult, error)
```

`run` steps (AIDEV-NOTE: the single write path; the writer is used elsewhere
only by §4.10 maintenance, `FlushUsage` and migrations; `ReapplyLedger` and
offline/staged ops also go through `run`):

1. **Static authz.** Forward ops: `Actor.Scopes.Has(spec.scopes)`, `ownerOnly`,
   `notWeb` → `forbidden`. Revert/undo/redo check only `ownerOnly`/`notWeb` here;
   their scopes come from the target in step 7, **after** the idempotency lookup (R2-5).
2. If `spec.idem`: missing key → `idempotency_key_required` (428); malformed → `malformed` (400).
   Every mutation: `Meta.Gen` missing → `precondition_required`; ≠ the cached
   `db_generation` → `generation_mismatch` (412) (R3-3). A request bound to a
   database that was since restored can therefore never apply — including
   undo/redo/revert, which carry no If-Match.
3. `ctx = context.WithoutCancel(ctx)` with the 15 s op budget (§4.11); register in
   the service's in-flight `sync.WaitGroup`; if shutdown has begun → `busy`.
4. `BEGIN IMMEDIATE` (writer `BeginTx`). `SQLITE_BUSY` after `busy_timeout` → `busy` (503, `Retry-After: 1`).
5. `now`, `today` (§4.2).
6. If `spec.idem`: look up `(actor.Key(), key)`. Same hash → roll back and replay:
   a stored success returns its status, refs and current representations
   (effects rebuilt from the stored op's events; none for a stored no-op); a
   stored terminal error (status 409) returns the same code and detail. Different
   hash → `idempotency_mismatch`.
7. **Target selection** (revert/undo/redo only, now that a replay is excluded):
   `Revert` loads the op by id (`not_found`); `Undo`/`Redo` take the top of the
   actor's stack (empty → §3.9 code, rolled back). Then
   `Actor.Scopes.Has(target.Scopes)`, and a non-owner actor must be the target's
   actor → `forbidden`. Permanent failures (§4.6) → `committedError`.
8. Unless `spec.noReconcile`: `reconcileTx(tx, now)` — a complete system op (own
   `ops` row with a lower seq, events, **its own version bumps finalized here**),
   only if there is work (§4.4). The user op's `If-Match` checks in step 12 compare
   against post-reconcile versions (a reason added by reconcile → 412 on a stale placement).
9. `SAVEPOINT user_op`.
10. Rate cap: if `actor.MutationsPerHour > 0`: `SELECT COUNT(*) FROM ops WHERE actor = ? AND at > ?` (now − 1 h) ≥ cap → `rate_limited`, `RetryAfter = oldest_in_window + 1h − now`.
11. Insert the `ops` row (`state='active'`, resolved `scopes`, `revertible`; for
    revert/undo/redo `via` and `reverts_op_id` in the **same** INSERT) → `rec.Seq`.
12. `fn(op)`: preconditions (`Expect` vs current versions), validation, writes via
    `store` (errors through `store.MapErr`), events via `op.record*`. Every
    triage-reason or `task_tags` change calls `o.markTaskDirty(id)`.
13. Finalize: `recompute(op)` (§4.5) → `checkInvariants(op)` (§4.7; structural
    rules only if `spec.structuralOnly`) → version (+`updated_at`) bump once per
    dirty entity.
14. Result paths:
    - **Forward op with no events**: `ROLLBACK TO user_op` (drops the ops row); if
      `spec.idem`, insert an idempotency row (`op_id` NULL, status 200, refs =
      the addressed entity if any); `RELEASE`, `COMMIT`; return `NoChange`.
    - **Revert/undo/redo with no domain events** (e.g. undo of a focus switch whose
      before-image is no longer valid): still succeeds — the ops row stays, the
      target's state chain and the stack movement are finalized (R2-4). Never a no-op.
    - **Forward / revert** with `spec.revertible`: push `rec.ID` on the actor's undo
      stack, clear the actor's redo stack, trim to `UndoDepth`.
    - **Undo / redo**: move the stack entry as §4.6 says; never pushed themselves.
15. If `spec.idem`: insert the idempotency row (status 201/200, refs).
16. `RELEASE user_op; COMMIT`.
17. Errors:
    - `committedError` (from step 7 or `fn`): roll back any savepoint, delete the
      named stack row, insert an idempotency row with status 409 and the error's
      full problem body as `error_json` (if `spec.idem`), `COMMIT`, return `Err`. No ops row, no event.
      A retry with the same key replays that error instead of touching the next entry.
    - anything else: `ROLLBACK TO user_op; RELEASE; COMMIT` (reconcile is kept; before
      step 9 a plain rollback); return the error. Commit failure → `internal`.
18. After commit: load result views via the reader pool; leave the WaitGroup.

Read path: `s.read(ctx, a, scopes, reconcileFirst, fn func(q store.Q) error)` →
authz → optional `s.Reconcile(ctx)` (which registers in the in-flight WaitGroup
like a scheduler job while it holds the writer) → reader `BeginTx(ReadOnly)` →
`fn` → commit.

### 4.4 Reconcile (`service/reconcile.go`, D§5.8)

```go
func (s *Service) Reconcile(ctx context.Context) error            // cheap check on reader; writer only if needed
func (s *Service) needsReconcile(ctx context.Context, q store.Q, now time.Time) (bool, error)
func (s *Service) reconcileTx(tx *sql.Tx, now time.Time) error   // authoritative; inside the caller's tx
```
`needsReconcile` = any of (open, non-deleted tasks unless noted):
1. `defer_date ≤ today` and `DeferMoment ≤ now`;
2. `due_date ≤ today`, `due_ack='none'`, no `due` row, no planned block today, not deferred;
3. `due_date ≤ today`, no `overdue` row, `DueMoment ≤ now`, not (`due_ack='overdue'` and `due_ack_date=today`);
4. any non-deleted `planned` block with `date < today`;
5. focus set and (`focus.date ≠ today`, or its task is not open, is deleted, or is deferred).

`reconcileTx` performs steps 1–5 of D§5.8 in that order as one system op
(`system.reconcile`, actor `system`, not revertible), recording events and
finalizing its dirty set (version bumps) before returning. Reason rows are
inserted with `INSERT … ON CONFLICT DO NOTHING` (`since = now`, `seq` = the
reconcile op's seq; an existing row keeps both, no event). Step 4 resolves each
block with `status = derive(task, date)`, `outcome_source='auto'`, then applies
§4.5 transitions. Steps 2 and 3 may both fire for one task (`{due, overdue}`),
kept as specified (A11).

View classification (`View` field and list filters), after reconcile:
`deleted` if `deleted_at`; else `done`/`dropped` by status; else `upcoming` if
defer moment > now; else `triage` if ≥ 1 reason; else `backlog`.

### 4.5 Derivation, slip and continue (`service/derive.go`, `service/slip.go`)

```go
func derive(q store.Q, task domain.TaskID, d civil.Date) (domain.BlockStatus, error) // done|worked|missed (D§5.6)
func (o *op) setBlock(b *domain.TimeBlock, ch blockChange) error // the ONLY way block status/slipped change
type blockChange struct {
	Status  domain.BlockStatus
	Source  domain.OutcomeSource // "" for planned/cancelled
	Cancel  domain.CancelReason
	Slipped *bool // nil = unchanged
	Restore bool  // revert: write the before-image verbatim, skip rule hooks
}
```
`setBlock` records `block:update`. On any status change it sets
`resolved_at = now`, `resolved_seq = o.rec.Seq` (back to `planned`: both NULL,
`outcome_source` NULL). Unless `Restore`, it applies:

| Transition | Hook |
|---|---|
| `slipped` 0→1 | `slipped_at = now`, `slipped_seq = seq`; if the task is open, not deleted, and `!o.placed[task]` → add `slipped` (`ON CONFLICT DO NOTHING`). |
| `slipped` 1→0 | Remove `slipped` unless another block of the task has `slipped=1 AND slipped_seq > COALESCE(last_placed_seq, 0)`. |
| status → `worked` (task open **and** not deleted) | Add `continue` unless `o.placed[task]` or the task has a non-deleted `planned` block with `date > b.date`. |
| status `worked` → other | Remove `continue` unless another block has `status='worked' AND resolved_seq > COALESCE(last_placed_seq, 0)` (§14 D7). |

- Slip value on resolution/re-derivation: `slipped = (status == missed)`.
- `slipOnCancel(b, reason)`: `false` for `task_closed`; otherwise
  `b.date == today && start instant > now && now − created_at ≥ SlipGrace`
  (future-day cancellations never slip; D§5.6 table).
- `SetOutcome{auto}` on today's block → `setBlock{Status: planned, Slipped: false}`
  (resets `outcome_source`, `resolved_*`; the unslip hook runs).
- **Recompute** (`recompute(op)`): for every `(task_id, work_date)` in `o.pairs`
  (entry before- and after-images from every entry write and from revert),
  re-derive each non-deleted `auto`-resolved block of that task on that date;
  `setBlock` when the status differs. Planned, cancelled and user-set blocks are untouched. Every event recorded during
  recompute (block re-derivations and the triage changes their hooks make) has
  `derived = 1` (§4.6).
- Placement (`o.place(task)`): delete all reason rows, `last_placed_at = now`,
  `last_placed_seq = seq`, `due_ack = duePhase`, `due_ack_date = today`,
  `o.placed[task] = true`, mark dirty.
- Closing ops (`Complete`, `Drop`, `Delete`) write the task's new status /
  tombstone **before** closing its blocks, so the hooks see a non-open task and
  add no reasons. Helpers (`service/blocks.go`): `closeForDone(o, task, D)`,
  `closeForDropOrDelete(o, task)`, `closeForDefer(o, task)`, `endEarly(o, b, cancel)`;
  "started" = start instant ≤ now. `Block`/`AddTask{block}` share one helper
  `o.createBlock(task, span)` (validation + overlap check + insert).

### 4.6 Revert, undo, redo (`service/revert.go`, `service/undo.go`)

Events recorded while `recompute` runs — the block re-derivations and the
`slipped`/`continue` triage changes their hooks make — carry `derived = 1`
(§2.7). They are consequences of entries, so revert never replays them; it
re-derives instead (R2-1).

`revertTx(o *op, target domain.OpRecord) error`:
1. `target.Revertible` else `not_revertible`; `target.State == active` else
   `revert_conflict` ("already reverted by <actor>").
2. Conflict check over the target's **non-derived** events: for each key —
   `(entity, entity_id)`, except triage `(triage, task_id, reason)`; focus events
   are ignored (C1) — any event (derived or not) with `id > max(target event ids)`
   on the same key from an op `X ≠ target` counts, except net-zero pairs (ops with
   `state='reverted'` and the active reversals pointing at them). Any remaining →
   `revert_conflict` listing them.
3. Pre-check: every block whose before-image is a non-deleted `planned` block is
   checked for overlap against current planned blocks not touched by this
   revert → `conflict` naming the block, before any write (the trigger stays a
   backstop via `store.MapErr`).
4. Apply the target's **non-derived** events in reverse id order, recording events
   for `o`: insert → tombstone (task/entry/block/interruption) or delete the row
   (triage add); update → write `Before` fields; tombstone ↔ untombstone; triage
   remove → re-insert with the original `since` and `seq`; task_tags set →
   `Before.tags`; move → place immediately after `Before.after_id` (top if null);
   block changes via `setBlock{Restore: true}`. Entry writes add their pairs; the
   target's derived events contribute their `(task_id, date)` pairs too.
5. Focus last (C1): restore the focus before-image only if it satisfies I6 now
   (its date is today and its task is open, not deleted, not deferred); otherwise
   leave focus unchanged. If the current focus then violates I6 (its task was
   tombstoned/closed by this revert), clear it.
6. `recompute(o)` with normal hooks: e.g. reverting a backdated AI log flips
   Monday's block back to `missed`, slips it with `slipped_seq` = this op's seq
   and re-adds `slipped` even if the task was placed since; with a second
   Monday entry still present nothing changes and no event is written.
   Then, for every task whose block this revert restored out of `slipped=1` or out
   of `status='worked'`, run the §4.5 removal checks (remove `slipped` / `continue`
   unless another qualifying block remains), so no reason is orphaned (R4-6).
   Conversely, when reversing the target's events removes a `slipped`/`continue`
   reason that the target added, re-evaluate it from the post-revert state and
   keep (re-add) it if another block still justifies it — `slipped`: a block with
   `slipped=1 AND slipped_seq > COALESCE(last_placed_seq, 0)`; `continue`: per the
   continue rule of §4.5 (task open, a block `worked` with `resolved_seq >
   COALESCE(last_placed_seq, 0)`, no later planned block) (P-2).
   `checkInvariants`; violation → `conflict` (whole op rolled back).
7. State chain (R2-11): `target.state = 'reverted'`, `o.rec.RevertsOpID = target.ID`;
   then walk down `reverts_op_id` from the target: each next op's state is the
   opposite of the op above it (`reverted` above → `active` below and vice versa).
   E.g. T0 ← R (revert) ← U (undo of R) ← D (redo): after D, U = reverted,
   R = active, T0 = reverted.

Undo/redo (`undo_stack` rows per actor key; `seq` = max+1 within `(actor, stack)`;
the top is the highest `seq`; selected in runner step 7):
- `Undo`: empty → §3.9. Top op `T`: not revertible →
  `committedError{not_revertible}`; `T.state = reverted` (by another actor or a
  per-op `Revert`) → `committedError{revert_conflict "already reverted by <actor>"}`
  (A16). Otherwise `revertTx(T)` with `via="undo"`; on success move `T` to the redo stack.
- `Redo`: empty → §3.9. Top `T` must be `reverted` with an active
  reversal `R` (`reverts_op_id = T`), else `committedError{revert_conflict}`; if
  `T` or `R` is not revertible (e.g. purged) → `committedError{not_revertible}`.
  Otherwise `revertTx(R)` with `via="redo"`; on success move `T` back to the undo stack.
- A `revert_conflict` from step 2 or a `conflict` from steps 3/6 leaves both stacks
  unchanged (A14; no `undo --skip` in v1).
- Owner channels (cli/tui/web) share the `owner` stacks.

### 4.7 Invariants (`service/invariants.go`)

| ID | Invariant | Kind |
|---|---|---|
| I1a | `planned` blocks only on open, non-deleted tasks | structural |
| I1b | `planned` blocks only on non-deferred tasks (defer moment > now) | time-dependent |
| I2 | No overlapping non-deleted `planned` blocks (also a trigger) | structural |
| I3 | If defer and due both set: defer moment < due moment and `defer_date ≤ due_date` | structural |
| I4 | A non-deleted `done` task has exactly one non-deleted `complete` entry with `reopened=0`, **or** none and ≥ 1 purged (`purged_at` set) `complete` entries (ledger replay, R-e/R2-12); a non-deleted open or dropped task has no non-deleted `complete` entry with `reopened=0` (`Reopen` always sets `reopened=1`, also when it tombstones) | structural |
| I5 | Triage rows only on open, non-deleted tasks | structural |
| I6 | Focus is NULL or an open, non-deleted, non-deferred task with `focus.date = today` | time-dependent |
| I7 | Ranks unique | structural |
| I8 | `complete` entries have a task; `kind` never changes (also triggers) | structural |

- Per op, `checkInvariants(op)` checks only touched tasks/blocks (I7 as
  `EXISTS (SELECT 1 FROM tasks WHERE rank = ? AND id <> ?)` per moved/inserted task).
- `CheckStructural(q)` and `CheckTimeDependent(q, now)` scan everything;
  `CheckAll = both`. Restore and import run `ReapplyLedger` and a reconcile on the
  staged copy **before** `CheckAll` (R-f); scrubbing a historical snapshot runs
  `CheckStructural` only (a snapshot must not be reconciled).

### 4.8 Rank (`service/rank.go`)

- `Gap = 1 << 16`. New/bottom: `MAX(rank) + Gap`; top: `MIN(rank) − Gap` (over **all** tasks).
- before N / after N: midpoint between N and its predecessor/successor in `(rank, id)` order (no neighbour → `N.rank ∓ Gap`); if the integer gap < 2, `store.RenormalizeRanks` first (`WITH r AS (SELECT id, ROW_NUMBER() OVER (ORDER BY rank, id) n FROM tasks) UPDATE tasks SET rank = (SELECT n FROM r WHERE r.id = tasks.id) * 65536`), then recompute. Renormalization records no events and bumps no versions (D§15).
- Move events record the predecessor id (`after_id`) before and after (§2.7).
- Renormalize also when `|rank| > 1<<60`.

### 4.9 Idempotency (`service/idem.go`)

- `request_hash = HMAC-SHA256(auth_state.idem_hmac_key, actorKey ‖ 0x00 ‖ Method ‖ 0x00 ‖ Route ‖ 0x00 ‖ Body)`.
- Stored in the op's transaction, including successful no-ops (`op_id` NULL,
  status 200); no response body (D§7.5). `entity_refs` = JSON `[{"entity":"task","id":42}]`.
- Expiry: maintenance deletes rows with `created_at < now − 7 d` hourly.
- Committed pops of revert/undo/redo store their terminal error as the full
  problem body (`error_json`: code, detail, conflicts; status 409) so a retry
  replays it exactly (§4.3 step 17).
- Exempt (never stored, never logged with bodies): `Login`, `ChangePassword`,
  `UpdateSettings`, `CreateToken`, `RunBackup` (purge is offline, §12.6). A test asserts no
  idempotency row, ops/event JSON, pending-request file, or log line contains a
  test password.

### 4.10 Server lifecycle, schedulers, shutdown (`server/lifecycle.go`, `server/schedulers.go`)

Startup (`server.Run(ctx, cfg, deps) error`):
1. Load + validate server config (timezone required, D§5.12). `unix.Umask(0o077)`.
2. Ensure data/state/runtime dirs 0700 and owned by the uid.
3. `lock.Acquire(<datadir>/taskronin.lock)` (`LOCK_EX|LOCK_NB`); failure → "already running".
4. Refuse if a swap journal exists (`<datadir>/swap.journal` or `<backupdir>/scrub.journal`): "run `tkr restore --resume|--rollback`" / "`tkr scrub-backups --resume`". Then `CleanupTemps` (§12.0).
5. `store.Open` (writer/reader/snapshot, `mode=rw`); pragma verification; `quick_check`.
6. Schema: newer than binary or checksum mismatch → refuse. Pending → verified pre-migrate snapshot (§12.1, kind `premigrate`) → migrate.
7. Refuse if the `auth_state` row is missing ("run `tkr init`").
8. `OpenLedger` (truncates a torn tail; a damaged ledger only raises the `purge_ledger_damaged` banner, §12.6); `ReapplyLedger`; then **always** `wal_checkpoint(TRUNCATE)` and confirm truncation (`busy=0`, `log=0`) — no other connection is in use yet; failure → refuse to start (R3-2). A `pending` record raises the `purge_incomplete` banner.
9. If `settings.tz_last_seen ≠ config tz` → warn (floating wall-clock semantics, D§5.12) and `UPDATE` it.
10. `Reconcile`.
11. Listeners (§6.1): `systemd.Listeners()` (`LISTEN_PID`/`LISTEN_FDS`/`LISTEN_FDNAMES`, name `api`) else create the socket (unlink a stale one — lock is held — then bind, `chmod 0600`). Stale means a connect to it is refused; if a connect succeeds (e.g. `taskronin.socket` is active while the service is stopped) or fails otherwise, refuse to start and keep the file: "socket in use — is taskronin.socket active? use `systemctl --user start taskronin.service`" (final review A-Minor-4). Web TCP listener only if `web.enabled` and plan D handlers exist; loopback only.
12. If plain-loopback web is enabled, warn if other human accounts exist (UID ≥ 1000 with a login shell, other than ours, from `/etc/passwd`).
13. Start schedulers; `systemd.Notify("READY=1")`.

Schedulers (goroutines on a shared cancelable ctx, timing via `clock.After`;
each job registers in the in-flight WaitGroup while it runs):

| Job | Cadence | Work |
|---|---|---|
| reconcile | every minute, aligned to :00 + 500 ms | `svc.Reconcile` |
| maintenance | minutely: `FlushUsage`; hourly: idempotency expiry, expired web sessions | writer tx, no events |
| backup | daily at `backup.time` (default 03:30 local); at startup if last success > 24 h | `svc.RunBackup` as system; observes the shutdown ctx |

Shutdown (SIGTERM/SIGINT), AIDEV-NOTE anchor:
1. `Notify("STOPPING=1")`; mark the service as shutting down (new ops → `busy`).
2. `http.Server.Shutdown` with a 10 s context on all servers (stops accepting; in-flight handlers continue).
3. Cancel the scheduler/shutdown context: running backups abort and delete their own temp files; schedulers stop.
4. Wait on the in-flight WaitGroup (detached ops have a 15 s budget, so this
   completes within ~20 s; it is never abandoned — the DB and the instance lock
   stay held until every op and job has returned).
5. `FlushUsage` → `PRAGMA wal_checkpoint(TRUNCATE)` → close DBs → remove the
   socket file if we created it → release the lock.
systemd `TimeoutStopSec=45s` (§8.6) covers 10 + 20 s plus margin.

### 4.11 Time budgets

| Operation | Server budget | Client timeout |
|---|---|---|
| ordinary op (detached writer tx) | 15 s (incl. 5 s busy wait) | `timeout` = 20 s (client default) |
| reads / aggregates | 10 s | 20 s |
| `status --short` | — | 300 ms |
| `RunBackup` (`POST /backups`), `Export` | 10 min, abort on shutdown | 11 min |
| shutdown | HTTP 10 s, drain ≤ 20 s | systemd stop 45 s |

Long routes set their write deadline to their budget with
`http.ResponseController.SetWriteDeadline`. The client classifies failures as
**not sent** (dial/connection refused before any byte was written → safe to
re-run; the CLI echoes the command) or **outcome unknown** (written, no complete
response → retried with the same idempotency key; if still unknown, the CLI
saves the request as a pending request, §9.6). Once an attempt of a mutation was
written without a complete response, **any** non-success answer to a retry
(`401`, `500`, `generation_mismatch`, `409`, `422`, `503` …) leaves it outcome-unknown
(saved as pending if keyed); only a success settles it (final review A-Minor-2).

---

## 5. Database schema

### 5.1 Migrations (`store/migrate.go`)

- Files: `internal/store/migrations/NNNN_name.sql`, embedded with `embed.FS`,
  applied in numeric order. **Committed files are never edited** (AIDEV-NOTE in
  each file header); fixes go in a new file.
- Bootstrap (created by the migrator, not by a migration):
  ```sql
  CREATE TABLE IF NOT EXISTS schema_migrations (
      version    INTEGER PRIMARY KEY,
      name       TEXT    NOT NULL,
      checksum   TEXT    NOT NULL,   -- lowercase hex SHA-256 of the file bytes
      applied_at INTEGER NOT NULL    -- unix ms
  ) STRICT;
  ```
- `store.SchemaStatus(ctx) (SchemaStatus{Current, Latest int; Pending []int}, error)`:
  an applied version absent from the binary → `ErrSchemaTooNew` (refuse);
  an applied checksum ≠ embedded → `ErrChecksumMismatch` (refuse).
- `store.Migrate(ctx, preMigrate func(context.Context) error) ([]int, error)`:
  if pending, call `preMigrate` (a verified snapshot, §12.1) and abort if it
  fails; then per file: `BEGIN IMMEDIATE` → one `ExecContext` of the whole file
  (modernc executes multi-statement strings) → insert `schema_migrations` →
  `COMMIT`; afterwards `PRAGMA foreign_key_check` must return no rows and
  `PRAGMA integrity_check` must return `ok`.
- Migrations are deterministic: no random values. `offline.Init` inserts
  `db_generation`, `auth_state`, and the owner token after migrating.
- A test applies every migration to an empty DB and to a DB built at each prior
  version (from `testdata/schema_vN.sql` fixtures captured at release).

### 5.2 `0001_init.sql`

All instants are `INTEGER` unix ms; dates `TEXT 'YYYY-MM-DD'` validated with
`x IS strftime('%Y-%m-%d', x)` (rejects `2026-02-30`, which `date()` would
normalize); minutes `INTEGER`. Every table is `STRICT`. Verified against
SQLite 3.53 with `defensive=on` and `trusted_schema=OFF`.

```sql
-- 0001_init.sql — Taskronin initial schema.
-- AIDEV-NOTE: committed migration; checksummed; NEVER edit. Add NNNN_*.sql instead.
-- AIDEV-NOTE: FTS external content is a VIEW of live rows (deleted_at IS NULL) so that
--   'rebuild' and content integrity-check agree with the trigger rules (see §14 issue 2).

-- ---------------------------------------------------------------- tasks
CREATE TABLE tasks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    title          TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    notes          TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(notes AS BLOB)) <= 65536),
    status         TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','dropped')),
    rank           INTEGER NOT NULL,
    last_placed_at INTEGER,
    last_placed_seq INTEGER,              -- ops.seq of the last placement (§4.2)
    defer_date     TEXT    CHECK (defer_date IS NULL OR defer_date IS strftime('%Y-%m-%d', defer_date)),
    defer_min      INTEGER CHECK (defer_min IS NULL OR defer_min BETWEEN 0 AND 1439),
    due_date       TEXT    CHECK (due_date IS NULL OR due_date IS strftime('%Y-%m-%d', due_date)),
    due_min        INTEGER CHECK (due_min IS NULL OR due_min BETWEEN 0 AND 1439),
    due_ack        TEXT    NOT NULL DEFAULT 'none' CHECK (due_ack IN ('none','due','overdue')),
    due_ack_date   TEXT    CHECK (due_ack_date IS NULL OR due_ack_date IS strftime('%Y-%m-%d', due_ack_date)),
    project        TEXT    CHECK (project IS NULL OR (length(project) BETWEEN 1 AND 100
                                  AND project NOT GLOB '*[^A-Za-z0-9._-]*')),
    priority       TEXT    CHECK (priority IS NULL OR priority IN ('H','M','L')),
    origin         TEXT    NOT NULL,
    completed_at   INTEGER,
    completed_date TEXT    CHECK (completed_date IS NULL OR completed_date IS strftime('%Y-%m-%d', completed_date)),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    deleted_at     INTEGER,
    purged_at      INTEGER,
    version        INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (defer_min IS NULL OR defer_date IS NOT NULL),
    CHECK (due_min IS NULL OR due_date IS NOT NULL),
    CHECK ((status = 'done') = (completed_at IS NOT NULL)),
    CHECK ((completed_at IS NULL) = (completed_date IS NULL))
) STRICT;
CREATE INDEX tasks_rank       ON tasks(rank, id);
CREATE INDEX tasks_open_rank  ON tasks(rank, id) WHERE status = 'open' AND deleted_at IS NULL;
CREATE INDEX tasks_defer      ON tasks(defer_date) WHERE defer_date IS NOT NULL;
CREATE INDEX tasks_due        ON tasks(due_date) WHERE due_date IS NOT NULL AND status = 'open';
CREATE INDEX tasks_completed  ON tasks(completed_date) WHERE completed_date IS NOT NULL;
CREATE INDEX tasks_project    ON tasks(project) WHERE project IS NOT NULL;

CREATE TABLE task_triage (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    reason  TEXT    NOT NULL CHECK (reason IN ('slipped','overdue','due','continue',
                                               'surfaced','reopened','manual','new')),
    since   INTEGER NOT NULL,             -- informational
    seq     INTEGER NOT NULL,             -- ops.seq that added it; ordering
    PRIMARY KEY (task_id, reason)
) STRICT, WITHOUT ROWID;
CREATE INDEX task_triage_reason ON task_triage(reason, seq);

CREATE TABLE tags (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE
               CHECK (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^A-Za-z0-9._-]*'),
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE task_tags (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE RESTRICT,
    PRIMARY KEY (task_id, tag_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX task_tags_tag ON task_tags(tag_id, task_id);

-- ---------------------------------------------------------------- log entries
CREATE TABLE log_entries (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER REFERENCES tasks(id) ON DELETE RESTRICT,
    kind         TEXT    NOT NULL CHECK (kind IN ('work','complete')),
    title        TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    notes        TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(notes AS BLOB)) <= 65536),
    work_date    TEXT    NOT NULL CHECK (work_date IS strftime('%Y-%m-%d', work_date)),
    occurred_min INTEGER CHECK (occurred_min IS NULL OR occurred_min BETWEEN 0 AND 1439),
    reopened     INTEGER NOT NULL DEFAULT 0 CHECK (reopened IN (0,1)),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    deleted_at   INTEGER,
    purged_at    INTEGER,
    version      INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (kind = 'work' OR task_id IS NOT NULL),
    CHECK (reopened = 0 OR kind = 'complete')
) STRICT;
CREATE INDEX log_entries_day  ON log_entries(work_date, occurred_min, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX log_entries_task ON log_entries(task_id, work_date) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------- time blocks
CREATE TABLE time_blocks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id        INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    date           TEXT    NOT NULL CHECK (date IS strftime('%Y-%m-%d', date)),
    start_min      INTEGER NOT NULL CHECK (start_min BETWEEN 0 AND 1439),
    end_min        INTEGER NOT NULL CHECK (end_min BETWEEN 1 AND 1440),
    status         TEXT    NOT NULL DEFAULT 'planned'
                   CHECK (status IN ('planned','done','worked','missed','cancelled')),
    outcome_source TEXT    CHECK (outcome_source IS NULL OR outcome_source IN ('auto','user')),
    cancel_reason  TEXT    CHECK (cancel_reason IS NULL
                                  OR cancel_reason IN ('user','task_closed','deferred','rescheduled')),
    slipped        INTEGER NOT NULL DEFAULT 0 CHECK (slipped IN (0,1)),
    slipped_at     INTEGER,
    slipped_seq    INTEGER,
    created_at     INTEGER NOT NULL,
    resolved_at    INTEGER,
    resolved_seq   INTEGER,
    deleted_at     INTEGER,
    version        INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (start_min < end_min),
    CHECK ((status IN ('done','worked','missed')) = (outcome_source IS NOT NULL)),
    CHECK ((status = 'cancelled') = (cancel_reason IS NOT NULL)),
    CHECK ((status = 'planned') = (resolved_at IS NULL)),
    CHECK (status <> 'done' OR outcome_source = 'auto'),
    CHECK (slipped = 0 OR (slipped_at IS NOT NULL AND slipped_seq IS NOT NULL)),
    CHECK ((status = 'planned') = (resolved_seq IS NULL))
) STRICT;
CREATE INDEX time_blocks_day     ON time_blocks(date, start_min) WHERE deleted_at IS NULL;
CREATE INDEX time_blocks_task    ON time_blocks(task_id, date);
CREATE INDEX time_blocks_planned ON time_blocks(date) WHERE status = 'planned' AND deleted_at IS NULL;

-- AIDEV-NOTE: backstop for invariant I2; the service checks first and reports a friendly error.
CREATE TRIGGER time_blocks_no_overlap_ins BEFORE INSERT ON time_blocks
WHEN NEW.status = 'planned' AND NEW.deleted_at IS NULL
BEGIN
    SELECT RAISE(ABORT, 'taskronin: overlapping planned block')
    WHERE EXISTS (SELECT 1 FROM time_blocks b
                  WHERE b.date = NEW.date AND b.status = 'planned' AND b.deleted_at IS NULL
                    AND b.start_min < NEW.end_min AND NEW.start_min < b.end_min);
END;
CREATE TRIGGER time_blocks_no_overlap_upd
BEFORE UPDATE OF date, start_min, end_min, status, deleted_at ON time_blocks
WHEN NEW.status = 'planned' AND NEW.deleted_at IS NULL
BEGIN
    SELECT RAISE(ABORT, 'taskronin: overlapping planned block')
    WHERE EXISTS (SELECT 1 FROM time_blocks b
                  WHERE b.id <> NEW.id AND b.date = NEW.date AND b.status = 'planned'
                    AND b.deleted_at IS NULL
                    AND b.start_min < NEW.end_min AND NEW.start_min < b.end_min);
END;

-- ---------------------------------------------------------------- focus, interruptions
CREATE TABLE focus (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    task_id INTEGER REFERENCES tasks(id),
    date    TEXT    CHECK (date IS NULL OR date IS strftime('%Y-%m-%d', date)),
    set_at  INTEGER,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((task_id IS NULL) = (date IS NULL))
) STRICT;
INSERT INTO focus (id) VALUES (1);

-- ---------------------------------------------------------------- ops, events, undo, idempotency
CREATE TABLE ops (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id         TEXT    NOT NULL UNIQUE CHECK (length(op_id) = 32 AND op_id NOT GLOB '*[^0-9a-f]*'),
    kind          TEXT    NOT NULL,
    actor         TEXT    NOT NULL,
    channel       TEXT    NOT NULL CHECK (channel IN ('cli','tui','web','mcp','api','system')),
    at            INTEGER NOT NULL,
    scopes        TEXT    NOT NULL,
    revertible    INTEGER NOT NULL CHECK (revertible IN (0,1)),
    state         TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','reverted')),
    reverts_op_id TEXT    REFERENCES ops(op_id),
    via           TEXT    CHECK (via IS NULL OR via IN ('revert','undo','redo')),
    purged        INTEGER NOT NULL DEFAULT 0 CHECK (purged IN (0,1)),
    CHECK ((via IS NULL) = (reverts_op_id IS NULL))
) STRICT;
CREATE INDEX ops_actor_at ON ops(actor, at);
CREATE INDEX ops_reverts  ON ops(reverts_op_id) WHERE reverts_op_id IS NOT NULL;

CREATE TABLE interruptions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            INTEGER NOT NULL,
    date          TEXT    NOT NULL CHECK (date IS strftime('%Y-%m-%d', date)),
    from_task_id  INTEGER REFERENCES tasks(id),
    from_block_id INTEGER REFERENCES time_blocks(id),
    to_task_id    INTEGER NOT NULL REFERENCES tasks(id),
    op_id         TEXT    NOT NULL REFERENCES ops(op_id),
    deleted_at    INTEGER,
    CHECK (from_task_id IS NOT NULL OR from_block_id IS NOT NULL)
) STRICT;
CREATE INDEX interruptions_day ON interruptions(date) WHERE deleted_at IS NULL;

CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id       TEXT    NOT NULL REFERENCES ops(op_id),
    entity      TEXT    NOT NULL CHECK (entity IN ('task','triage','task_tags','entry','block',
                                                   'focus','interruption','token','setting','auth')),
    entity_id   INTEGER NOT NULL,
    task_id     INTEGER REFERENCES tasks(id),
    action      TEXT    NOT NULL CHECK (action IN ('insert','update','move','tombstone','untombstone',
                                                   'add','remove','set','revoke','purge')),
    before_json TEXT    CHECK (before_json IS NULL OR json_valid(before_json)),
    after_json  TEXT    CHECK (after_json IS NULL OR json_valid(after_json)),
    derived     INTEGER NOT NULL DEFAULT 0 CHECK (derived IN (0,1))  -- written by recompute (§4.6)
) STRICT;
CREATE INDEX events_entity ON events(entity, entity_id, id);
CREATE INDEX events_task   ON events(task_id, id) WHERE task_id IS NOT NULL;
CREATE INDEX events_op     ON events(op_id, id);

CREATE TABLE undo_stack (
    actor TEXT    NOT NULL,
    stack TEXT    NOT NULL CHECK (stack IN ('undo','redo')),
    seq   INTEGER NOT NULL,
    op_id TEXT    NOT NULL REFERENCES ops(op_id),
    PRIMARY KEY (actor, stack, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE idempotency (
    actor        TEXT    NOT NULL,
    key          TEXT    NOT NULL CHECK (length(key) BETWEEN 16 AND 128 AND key NOT GLOB '*[^A-Za-z0-9_-]*'),
    request_hash BLOB    NOT NULL CHECK (length(request_hash) = 32),
    op_id        TEXT    REFERENCES ops(op_id),   -- NULL for a stored no-op or terminal error
    status_code  INTEGER NOT NULL CHECK (status_code IN (200, 201, 409)),
    entity_refs  TEXT    NOT NULL CHECK (json_valid(entity_refs)),
    error_json   TEXT    CHECK (error_json IS NULL OR json_valid(error_json)), -- 409 only: full problem body of a committed pop
    created_at   INTEGER NOT NULL,
    CHECK (op_id IS NOT NULL OR status_code IN (200, 409)),
    CHECK ((status_code = 409) = (error_json IS NOT NULL)),
    PRIMARY KEY (actor, key)
) STRICT, WITHOUT ROWID;
CREATE INDEX idempotency_created ON idempotency(created_at);

-- ---------------------------------------------------------------- settings and auth
-- settings keys: db_generation, tz_last_seen, backup_keep_daily, backup_keep_weekly,
--   last_backup_at, last_backup_attempt_at, last_backup_error, ai_reviewed_through.
--   Secrets live in auth_state (never exported).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
INSERT INTO settings (key, value) VALUES
    ('backup_keep_daily', '7'), ('backup_keep_weekly', '4'), ('ai_reviewed_through', '0'),
    ('last_backup_at', ''), ('last_backup_attempt_at', ''),
    ('last_backup_error', ''), ('tz_last_seen', '');

CREATE TABLE auth_state (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash       TEXT    NOT NULL CHECK (password_hash GLOB '$argon2id$*'),
    password_changed_at INTEGER NOT NULL,
    web_login_disabled  INTEGER NOT NULL DEFAULT 0 CHECK (web_login_disabled IN (0,1)),
    idem_hmac_key       BLOB    NOT NULL CHECK (length(idem_hmac_key) = 32)
) STRICT;

CREATE TABLE api_tokens (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE
                       CHECK (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^A-Za-z0-9._-]*'),
    secret_hash        BLOB    NOT NULL CHECK (length(secret_hash) = 32),
    scopes             TEXT    NOT NULL,
    is_owner           INTEGER NOT NULL DEFAULT 0 CHECK (is_owner IN (0,1)),
    mutations_per_hour INTEGER NOT NULL DEFAULT 0 CHECK (mutations_per_hour >= 0),
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER,
    last_used_at       INTEGER,
    revoked_at         INTEGER,
    CHECK ((is_owner = 1) = (name GLOB 'owner.*'))
) STRICT;
CREATE UNIQUE INDEX api_tokens_one_owner ON api_tokens(is_owner) WHERE is_owner = 1 AND revoked_at IS NULL;

CREATE TABLE web_sessions (
    id_hash      BLOB    PRIMARY KEY CHECK (length(id_hash) = 32),
    csrf_secret  BLOB    NOT NULL CHECK (length(csrf_secret) = 32),
    remote       TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,   -- absolute (created_at + 30 d)
    last_seen_at INTEGER NOT NULL    -- idle limit 7 d, flushed ≤ 1/min
) STRICT, WITHOUT ROWID;
CREATE INDEX web_sessions_expires ON web_sessions(expires_at);

-- ---------------------------------------------------------------- full-text search
CREATE VIEW tasks_fts_src AS
    SELECT id, title, notes FROM tasks WHERE deleted_at IS NULL;
CREATE VIRTUAL TABLE tasks_fts USING fts5(
    title, notes, content='tasks_fts_src', content_rowid='id',
    tokenize='unicode61 remove_diacritics 2');
INSERT INTO tasks_fts (tasks_fts, rank) VALUES ('secure-delete', 1);
CREATE TRIGGER tasks_fts_ins AFTER INSERT ON tasks WHEN NEW.deleted_at IS NULL
BEGIN
    INSERT INTO tasks_fts (rowid, title, notes) VALUES (NEW.id, NEW.title, NEW.notes);
END;
CREATE TRIGGER tasks_fts_upd AFTER UPDATE OF title, notes, deleted_at ON tasks
BEGIN
    INSERT INTO tasks_fts (tasks_fts, rowid, title, notes)
        SELECT 'delete', OLD.id, OLD.title, OLD.notes WHERE OLD.deleted_at IS NULL;
    INSERT INTO tasks_fts (rowid, title, notes)
        SELECT NEW.id, NEW.title, NEW.notes WHERE NEW.deleted_at IS NULL;
END;

CREATE VIEW log_entries_fts_src AS
    SELECT id, title, notes FROM log_entries WHERE deleted_at IS NULL;
CREATE VIRTUAL TABLE log_entries_fts USING fts5(
    title, notes, content='log_entries_fts_src', content_rowid='id',
    tokenize='unicode61 remove_diacritics 2');
INSERT INTO log_entries_fts (log_entries_fts, rank) VALUES ('secure-delete', 1);
CREATE TRIGGER log_entries_fts_ins AFTER INSERT ON log_entries WHEN NEW.deleted_at IS NULL
BEGIN
    INSERT INTO log_entries_fts (rowid, title, notes) VALUES (NEW.id, NEW.title, NEW.notes);
END;
CREATE TRIGGER log_entries_fts_upd AFTER UPDATE OF title, notes, deleted_at ON log_entries
BEGIN
    INSERT INTO log_entries_fts (log_entries_fts, rowid, title, notes)
        SELECT 'delete', OLD.id, OLD.title, OLD.notes WHERE OLD.deleted_at IS NULL;
    INSERT INTO log_entries_fts (rowid, title, notes)
        SELECT NEW.id, NEW.title, NEW.notes WHERE NEW.deleted_at IS NULL;
END;

-- ---------------------------------------------------------------- guard triggers
CREATE TRIGGER tasks_no_delete         BEFORE DELETE ON tasks         BEGIN SELECT RAISE(ABORT, 'taskronin: tasks are tombstoned, never deleted'); END;
CREATE TRIGGER log_entries_no_delete   BEFORE DELETE ON log_entries   BEGIN SELECT RAISE(ABORT, 'taskronin: entries are tombstoned, never deleted'); END;
CREATE TRIGGER time_blocks_no_delete   BEFORE DELETE ON time_blocks   BEGIN SELECT RAISE(ABORT, 'taskronin: blocks are tombstoned, never deleted'); END;
CREATE TRIGGER interruptions_no_delete BEFORE DELETE ON interruptions BEGIN SELECT RAISE(ABORT, 'taskronin: interruptions are tombstoned, never deleted'); END;
CREATE TRIGGER ops_no_delete           BEFORE DELETE ON ops           BEGIN SELECT RAISE(ABORT, 'taskronin: ops are append-only'); END;
CREATE TRIGGER events_no_delete        BEFORE DELETE ON events        BEGIN SELECT RAISE(ABORT, 'taskronin: events are append-only'); END;
-- Only purge may rewrite before_json/after_json (content scrub); identity columns are frozen.
CREATE TRIGGER events_frozen BEFORE UPDATE ON events
WHEN NEW.id IS NOT OLD.id OR NEW.op_id IS NOT OLD.op_id OR NEW.entity IS NOT OLD.entity
  OR NEW.entity_id IS NOT OLD.entity_id OR NEW.task_id IS NOT OLD.task_id OR NEW.action IS NOT OLD.action
  OR NEW.derived IS NOT OLD.derived
BEGIN
    SELECT RAISE(ABORT, 'taskronin: events are append-only');
END;
CREATE TRIGGER log_entries_kind_fixed BEFORE UPDATE OF kind ON log_entries
WHEN NEW.kind IS NOT OLD.kind
BEGIN
    SELECT RAISE(ABORT, 'taskronin: entry kind is immutable');
END;
CREATE TRIGGER log_entries_complete_bound BEFORE UPDATE OF task_id ON log_entries
WHEN OLD.kind = 'complete' AND NEW.task_id IS NOT OLD.task_id
BEGIN
    SELECT RAISE(ABORT, 'taskronin: complete entries cannot be relinked');
END;
CREATE TRIGGER time_blocks_task_fixed BEFORE UPDATE OF task_id ON time_blocks
WHEN NEW.task_id IS NOT OLD.task_id
BEGIN
    SELECT RAISE(ABORT, 'taskronin: block task is immutable');
END;
```

### 5.3 Schema notes

- **`sqlite_sequence`**: every `AUTOINCREMENT` table (`tasks`, `log_entries`,
  `time_blocks`, `tags`, `interruptions`, `ops`, `events`, `api_tokens`) gets a
  row on first insert. `store.Sequences(q) (map[string]int64, error)` reads them;
  `store.RaiseSequences(q, map[string]int64) error` sets each to
  `max(current, given)` (`UPDATE … ; INSERT … WHERE NOT EXISTS`). Used by restore,
  import, and export (D§7.3). IDs are never reused or renumbered.
- **FTS**: never run `'rebuild'` except in a test or a future repair command
  (it is safe now because content is the live-rows view, but triggers are the
  normal path). Tests run `INSERT INTO tasks_fts(tasks_fts, rank) VALUES('integrity-check', 1)`
  (content-checking mode) after every scenario. Search queries are built by
  `service.ftsQuery(user string) (string, error)`: split on whitespace, each term
  double-quoted with `"` doubled; trailing `*`s are stripped and a single prefix
  `*` is emitted outside the quotes only if the remaining stem is non-empty (a
  bare `*` term is dropped); terms joined with spaces (implicit AND); no terms
  left → `validation`.
  Results: `bm25()` order, `snippet()` 10 tokens; snippets are user text (sanitized at render).
- **Rank** is not `UNIQUE` (renormalization rewrites all rows in one statement);
  invariant I7 checks uniqueness.
- **`*_seq` columns** hold `ops.seq` values for ordering/acknowledgement (§4.2);
  `*_at` columns are informational. `purged_at` marks rows scrubbed by
  `ScrubEntities` (I4 accepts a purged completion entry).
- **`ops.state`/`reverts_op_id`/`scopes`** make revert, undo/redo and the
  net-zero rule cheap (§4.6); see §14 issue 1.

---

## 6. HTTP API

### 6.1 Listeners and route classes

| Listener | Address | Serves | Auth |
|---|---|---|---|
| **api** | unix socket (§8.1); systemd fd named `api` or self-bound | `/api/v1/*`, `/healthz` | `Authorization: Bearer` **and** peer UID = server UID |
| **web** (plan D) | TCP `127.0.0.1:7878` (config); loopback only in v1 | `/ui/*`, `/static/*`, `/healthz`, `/` → 303 `/ui/`; **any `/api/v1/*` → 401** | session cookie (+CSRF) |

- Two `http.Server`s with D§10.2 timeouts (`ReadHeaderTimeout` 10 s,
  `ReadTimeout` 30 s, `WriteTimeout` 60 s, `IdleTimeout` 120 s,
  `MaxHeaderBytes` 64 KiB) and `ConnContext: peercred.ConnContext`.
  Long routes extend their write deadline to their §4.11 budget with
  `http.ResponseController.SetWriteDeadline` (`POST /backups`, `GET /export`:
  10 min).
- The API is **not** routed on TCP in v1 (§14 D13). The authn middleware
  still enforces "owner token only on a verified socket" so the rule holds if
  a later remote mode mounts the API on TCP.
- Route-class invariant (D§10.3): the api chain deletes any `Cookie` header
  before authn; the web chain deletes any `Authorization` header. A test sends
  cookie-only requests to `/api/v1` (socket → 401) and bearer-only requests to
  `/ui` (→ 303/401).

### 6.2 Middleware chains (`server/middleware.go`, `web/web.go`)

API (socket), outermost first:
1. `recoverer` → 500 `internal`, stack logged.
2. `requestID` → 16-hex `X-Request-ID` in ctx and response.
3. `accessLog` → slog: method, route pattern, status, duration, request id, actor key. Never bodies, never `Authorization`.
4. `socketGuard` → `ConnInfo.Transport == unix && PeerOK && PeerUID == os.Getuid()`, and no `Forwarded`, `X-Forwarded-*`, or `Via` header; else 403 `peer_rejected`.
5. `routeClass(api)` → only `/api/v1/` and `/healthz` (else 404); strip `Cookie`.
6. `bodyLimit` → `http.MaxBytesReader(1 MiB)`; bodies require `Content-Type: application/json` (415).
7. `authn` → `svc.AuthenticateToken(ctx, bearer, verifiedSocket, channel, remoteKey)` → `domain.Actor` in ctx, where authn itself derives `verifiedSocket` from `ConnInfo` exactly as step 4 does (never a constant; final review A-Minor-1); 401 + `WWW-Authenticate: Bearer` on failure; skipped only for `/healthz`. `X-Taskronin-Channel` ∈ {cli, tui, mcp, api} (default api); owner token + `mcp` channel → 403 (defence in depth for D§9.4).
8. `apiHeaders` → `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, `X-Taskronin-Version`.
9. Handler (`decode` strict → one service call → `respond`).

Web (TCP, plan D):
1. `recoverer`, `requestID`, `accessLog`.
2. `hostAllowlist` → derived from `web.listen`: the literal `host:port`, plus `localhost:<port>` when the host is `127.0.0.1` or `::1`; anything else → 400 (DNS rebinding).
3. `securityHeaders` → CSP and friends exactly as D§10.4 on every response.
4. `routeClass(web)` → `/ui/`, `/static/`, `/healthz`, `/`; strip `Authorization`; `/api/v1/*` → 401 problem.
5. `bodyLimit` (1 MiB, forms only).
6. `session` (all `/ui/*` except `/ui/login`) → `svc.Authenticate(cookie)`; missing/expired → GET 303 `/ui/login`, POST 401 with `HX-Redirect: /ui/login`.
7. `csrf` on every POST except `POST /ui/login` (no session yet; protected by the strict Origin/Sec-Fetch-Site check alone, §7.5).
8. Handler.

### 6.3 Conventions

| Header | Direction | Rule |
|---|---|---|
| `Authorization: Bearer tkr_<id>_<secret>` | req | every `/api/v1` request except `/healthz` |
| `If-Match: "<gen>-<version>"` | req | required on every mutation of an existing task/entry/block (428 if absent, 412 if stale incl. generation). Exactly one strong ETag; `*` not accepted. |
| `Idempotency-Key: <16–128 [A-Za-z0-9_-]>` | req | required on every POST/PATCH except the exempt routes marked **ex** below: absent → 428 `idempotency_key_required`, malformed → 400 `malformed`. Client: 32 hex chars from `crypto/rand`, reused on retry. |
| `X-Taskronin-Channel` | req | informational (cli, tui, mcp, api) |
| `Taskronin-Generation: <16 hex>` | req | required on every POST/PATCH (missing → 428 `precondition_required`, other → 412 `generation_mismatch`); clients read it from `GET /clock` or `GET /whoami` (R3-3) |
| `If-None-Match` | req | aggregates (`/today`, `/status`) → 304 when equal |
| `ETag` | resp | entity: `"<gen>-<version>"`; aggregate: `"a-<first 16 hex of SHA-256(body)>"` computed after reconcile |
| `Location` | resp | on 201 |
| `Retry-After` | resp | 429, 503 |
| `X-Request-ID`, `X-Taskronin-Version` | resp | always |

- JSON: `DisallowUnknownFields`, single value (trailing data → 400), UTF-8;
  responses `application/json`, problems `application/problem+json` (RFC 9457).
- Pagination: `?limit=` (default 100, max 500) `&cursor=`; response
  `{"items": [...], "next_cursor": "…"|null}`. Cursor = base64url(JSON
  `{"v":1,"k":[sort keys],"id":N}`), validated strictly; search uses
  `{"v":1,"o":offset}`.
- Mutations return `200`/`201` with an `OpResponse` (§6.5); a no-op returns 200
  with `"op_id": null` and empty effects. Replays return the stored status code
  and the **current** representation with `"replayed": true`.

### 6.4 Endpoints

Scope column: required token scopes (§7.3). **IfM** = If-Match required.
**ex** = idempotency-exempt (credential-bearing, D§7.5). All paths below are
under `/api/v1` unless shown otherwise.

| Method & path | Scope | Request → Response | Codes / notes |
|---|---|---|---|
| `GET /healthz` (both listeners) | none | → `ok` (text/plain) | 200 |
| `GET /clock` | any | → `Clock` | |
| `GET /whoami` | any | → `WhoAmI` | |
| `GET /today` | read | → `Today` | reconcile; ETag/304 |
| `GET /status` | read | → `Status` | reconcile; ETag/304 |
| `GET /days/{date}` | read | → `Day` | recap data |
| `GET /upcoming` | read | → `Upcoming` | deferred + due-soon (D§5.4) |
| `GET /report?from&to&tag&project&todo&upcoming` | read | → `Report` | `from`,`to` required ISO dates; `to-from ≤ 366 d` |
| `GET /search?q&kind&limit&cursor` | read | → `Page[SearchHit]` | `kind` task\|entry\|all |
| `GET /tags` | read | → `[]TagCount` | |
| `GET /tasks?view&tag&project&limit&cursor` | read | → `Page[Task]` | `view` required (triage\|backlog\|upcoming\|done\|dropped\|deleted\|all); reconcile for triage/backlog/upcoming |
| `POST /tasks` | capture (+write if `place`) | `AddTaskRequest` → `OpResponse{task}` | 201; `place` from a non-owner token → 403 (R-b) |
| `GET /tasks/{id}` | read | → `TaskDetail` | ETag |
| `PATCH /tasks/{id}` | write | `UpdateTaskRequest` → `OpResponse{task}` | IfM |
| `POST /tasks/{id}/due` | write | `DueRequest` → `OpResponse{task}` | IfM |
| `POST /tasks/{id}/place` | write | `PlaceRequest` → `OpResponse{task}` | IfM; placement |
| `POST /tasks/{id}/defer` | write | `DeferRequest` → `OpResponse{task}` | IfM; placement; `{"until": null}` clears the defer (A24) |
| `POST /tasks/{id}/block` | write | `BlockRequest` → `OpResponse{task, block}` | IfM (task); 201; placement |
| `POST /tasks/{id}/triage` | write | `{}` → `OpResponse{task}` | IfM; adds `manual` |
| `POST /tasks/{id}/complete` | write | `CompleteRequest` → `OpResponse{task, entry}` | IfM; placement |
| `POST /tasks/{id}/reopen` | write (+destroy if `remove_entry`) | `ReopenRequest` → `OpResponse{task}` | IfM |
| `POST /tasks/{id}/drop` | destroy | `{}` → `OpResponse{task}` | IfM; placement |
| `POST /tasks/{id}/delete` | destroy | `{}` → `OpResponse{task}` | IfM |
| `POST /tasks/{id}/undelete` | write | `{}` → `OpResponse{task}` | IfM |
| `GET /tasks/{id}/history?limit&cursor` | read | → `Page[EventView]` | |
| `POST /triage/accept-new` | write | `AcceptNewRequest` → `OpResponse{tasks}` | per-item etags; all-or-nothing (C6); placement |
| `GET /entries?from&to&task&include_deleted&limit&cursor` | read | → `Page[Entry]` | `task` = id \| `none`; `from`/`to` required (≤ 366 d) unless `task` is an id |
| `POST /entries` | capture | `AddEntryRequest` → `OpResponse{entry}` | 201; `task_id` key required |
| `GET /entries/{id}` | read | → `Entry` | ETag |
| `PATCH /entries/{id}` | destroy | `UpdateEntryRequest` → `OpResponse{entry}` | IfM |
| `POST /entries/{id}/relink` | write | `RelinkRequest` → `OpResponse{entry}` | IfM |
| `POST /entries/{id}/delete` | destroy | `{}` → `OpResponse{entry}` | IfM |
| `POST /entries/{id}/undelete` | destroy | `{}` → `OpResponse{entry}` | IfM |
| `GET /blocks?from&to&task` | read | → `[]Block` | `from`/`to` required (≤ 366 d) unless `task` is set |
| `GET /blocks/{id}` | read | → `Block` | ETag |
| `POST /blocks/{id}/reblock` | write | `ReblockRequest` → `OpResponse{block, task}` | IfM (block) + `task_etag`; placement |
| `POST /blocks/{id}/unblock` | write | `{}` → `OpResponse{block}` | IfM |
| `POST /blocks/{id}/outcome` | write | `OutcomeRequest` → `OpResponse{block}` | IfM |
| `POST /focus` | write | `FocusRequest` → `OpResponse{task?}` | no IfM (§14 D3) |
| `GET /ops?actor&since_seq&limit&cursor` | read | → `Page[OpSummary]` | |
| `GET /ops/{op_id}` | read | → `OpDetail` | |
| `POST /ops/{op_id}/revert` | scopes of op | `{}` → `OpResponse` | 409 `revert_conflict`/`not_revertible` |
| `POST /undo`, `POST /redo` | scopes of op | `{}` → `OpResponse` | 409: empty stack (§3.9), `revert_conflict`, `not_revertible` |
| `GET /ai-changes?all&limit&cursor` | read | → `AIChanges` | |
| `POST /ai-changes/review` | write, owner only | `ReviewRequest` → `OpResponse` | 422 if `through_seq` > latest op seq |
| `GET /settings` | read | → `Settings` | |
| `PATCH /settings` | admin (+password if lowering) | `SettingsRequest` → `Settings` | **ex** |
| `GET /tokens` | admin | → `[]TokenInfo` | |
| `POST /tokens` | admin | `CreateTokenRequest` → `TokenCreated` | **ex**; 201; 409 name taken |
| `POST /tokens/{id}/revoke` | admin | `{}` → `TokenInfo` | owner token → 409 |
| `POST /password` | admin, owner token | `PasswordRequest` → 204 | **ex** |
| `GET /backups` | admin | → `BackupStatus` | |
| `POST /backups` | admin | `{}` → `Snapshot` | **ex**; 201; long |
| `GET /export` | admin | → export JSON stream (§12.4) | long; `Content-Disposition: attachment` |

### 6.5 Wire types (`internal/api`)

```go
type Time struct{ time.Time }   // JSON "2006-01-02T15:04:05.000Z07:00" in the server zone
type Moment struct {
	Date civil.Date    `json:"date"`
	Time *civil.Minute `json:"time"` // null = date-only
}
type Opt[T any] struct{ Set, Null bool; V T } // tri-state: absent / null / value
func (o Opt[T]) IsZero() bool                 // !Set — with `,omitzero` an unset field is omitted
func (o Opt[T]) MarshalJSON() ([]byte, error) // Null (or !Set) → null; else V
func (o *Opt[T]) UnmarshalJSON(b []byte) error // key present → Set; null → Null

type Reason struct { Reason string `json:"reason"`; Since Time `json:"since"` }
type Slips  struct { Lifetime int `json:"lifetime"`; SinceTriage int `json:"since_triage"` }

type Task struct {
	ID            int64       `json:"id"`
	Title         string      `json:"title"`
	Notes         string      `json:"notes"`
	Status        string      `json:"status"`
	View          string      `json:"view"`
	Reasons       []Reason    `json:"reasons"`
	Defer         *Moment     `json:"defer"`
	Deferred      bool        `json:"deferred"`
	Due           *Moment     `json:"due"`
	DuePhase      string      `json:"due_phase"`
	DueSoon       bool        `json:"due_soon"`
	Project       *string     `json:"project"`
	Tags          []string    `json:"tags"`
	Priority      *string     `json:"priority"`
	Origin        string      `json:"origin"`
	Slips         Slips       `json:"slips"`
	LastPlacedAt  *Time       `json:"last_placed_at"`
	CompletedAt   *Time       `json:"completed_at"`
	CompletedDate *civil.Date `json:"completed_date"`
	CreatedAt     Time        `json:"created_at"`
	UpdatedAt     Time        `json:"updated_at"`
	DeletedAt     *Time       `json:"deleted_at"`
	Version       int64       `json:"version"`
	ETag          string      `json:"etag"`
}
type TaskDetail struct {
	Task
	Entries       []Entry        `json:"entries"`
	Blocks        []Block        `json:"blocks"`
	Interruptions []Interruption `json:"interruptions"`
}
type Entry struct {
	ID        int64         `json:"id"`
	TaskID    *int64        `json:"task_id"`
	TaskTitle *string       `json:"task_title"`
	Kind      string        `json:"kind"`
	Title     string        `json:"title"`
	Notes     string        `json:"notes"`
	WorkDate  civil.Date    `json:"work_date"`
	Time      *civil.Minute `json:"time"`
	Reopened  bool          `json:"reopened"`
	CreatedAt Time          `json:"created_at"`
	UpdatedAt Time          `json:"updated_at"`
	DeletedAt *Time         `json:"deleted_at"`
	Version   int64         `json:"version"`
	ETag      string        `json:"etag"`
}
type Block struct {
	ID            int64        `json:"id"`
	TaskID        int64        `json:"task_id"`
	TaskTitle     string       `json:"task_title"`
	TaskETag      string       `json:"task_etag"` // send as ReblockRequest.task_etag / web `task_etag`
	Date          civil.Date   `json:"date"`
	Start         civil.Minute `json:"start"`
	End           civil.EndMinute `json:"end"`
	Status        string       `json:"status"`
	OutcomeSource *string      `json:"outcome_source"`
	CancelReason  *string      `json:"cancel_reason"`
	Slipped       bool         `json:"slipped"`
	SlippedAt     *Time        `json:"slipped_at"`
	Current       bool         `json:"current"`
	Started       bool         `json:"started"`
	Ended         bool         `json:"ended"`
	NeedsReblock  bool         `json:"needs_reblock"`
	CreatedAt     Time         `json:"created_at"`
	ResolvedAt    *Time        `json:"resolved_at"`
	Version       int64        `json:"version"`
	ETag          string       `json:"etag"`
}
type Interruption struct {
	ID          int64      `json:"id"`
	At          Time       `json:"at"`
	Date        civil.Date `json:"date"`
	FromTaskID  *int64     `json:"from_task_id"`
	FromBlockID *int64     `json:"from_block_id"`
	ToTaskID    int64      `json:"to_task_id"`
}
type Effect struct {
	Kind    string `json:"kind"`
	TaskID  int64  `json:"task_id,omitempty"`
	BlockID int64  `json:"block_id,omitempty"`
	EntryID int64  `json:"entry_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}
type OpResponse struct {
	OpID     *string  `json:"op_id"`
	Replayed bool     `json:"replayed"`
	Task     *Task    `json:"task,omitempty"`
	Entry    *Entry   `json:"entry,omitempty"`
	Block    *Block   `json:"block,omitempty"`
	Tasks    []Task   `json:"tasks,omitempty"`
	Effects  []Effect `json:"effects"`
	Warnings []string `json:"warnings"`
}
type Clock struct {
	Now           Time       `json:"now"`
	Today         civil.Date `json:"today"`
	Timezone      string     `json:"timezone"`
	WeekStart     string     `json:"week_start"`
	DueSoonDays   int        `json:"due_soon_days"`
	ServerVersion string     `json:"server_version"`
	Generation    string     `json:"generation"` // db_generation; send as Taskronin-Generation
}
type Banner struct{ Kind, Message string } // kinds: backup_failed, backup_stale, ai_changes, purge_incomplete, purge_ledger_damaged, tz_changed
type BackupHealth struct {
	LastSuccessAt *Time  `json:"last_success_at"`
	LastError     string `json:"last_error"`
	Healthy       bool   `json:"healthy"`
}
type Today struct {
	Clock         Clock          `json:"clock"`
	Focus         *Task          `json:"focus"`
	CurrentBlock  *Block         `json:"current_block"`
	Blocks        []Block        `json:"blocks"`
	TriageCount   int            `json:"triage_count"`
	TriageTop     []Task         `json:"triage_top"` // first 5 in triage order
	Overdue       []Task         `json:"overdue"`
	DueSoon       []Task         `json:"due_soon"`
	Entries       []Entry        `json:"entries"`
	Interruptions []Interruption `json:"interruptions"`
	AIChanges     int            `json:"ai_changes"`
	Backup        BackupHealth   `json:"backup"`
	Banners       []Banner       `json:"banners"`
}
type Status struct {
	Focus        *StatusTask  `json:"focus"`         // {id, title}
	CurrentBlock *StatusBlock `json:"current_block"` // {id, task_id, title, end}
	Triage       int          `json:"triage"`
	Due          int          `json:"due"`           // due-phase + overdue open tasks
	AI           int          `json:"ai"`
	BackupWarn   bool         `json:"backup_warning"`
	PurgeWarning bool         `json:"purge_warning"` // purge_incomplete or purge_ledger_damaged
}
type Day struct {
	Date          civil.Date     `json:"date"`
	Blocks        []Block        `json:"blocks"`
	Entries       []Entry        `json:"entries"`
	Unlinked      []Entry        `json:"unlinked"`
	Interruptions []Interruption `json:"interruptions"`
}
type Report struct {
	From     civil.Date            `json:"from"`
	To       civil.Date            `json:"to"`
	Tag      *string               `json:"tag"`
	Project  *string               `json:"project"`
	Days     []ReportDay           `json:"days"`  // ascending; days without entries omitted
	Tasks    map[string]ReportTask `json:"tasks"` // key = task id
	Todo     *ReportTodo           `json:"todo,omitempty"`
	Upcoming []Task                `json:"upcoming,omitempty"`
}
type ReportDay  struct { Date civil.Date `json:"date"`; Lines []ReportLine `json:"lines"` }
type ReportLine struct {
	EntryID  int64         `json:"entry_id"`
	Kind     string        `json:"kind"`
	Title    string        `json:"title"`
	Notes    string        `json:"notes"`
	TaskID   *int64        `json:"task_id"`
	Time     *civil.Minute `json:"time"`
	Reopened bool          `json:"reopened"`
}
type ReportTask struct { Title string `json:"title"`; Status string `json:"status"`; Deleted bool `json:"deleted"` }
type ReportTodo struct { Triage []Task `json:"triage"`; Backlog []Task `json:"backlog"` }
type SearchHit struct {
	Kind     string      `json:"kind"`   // task|entry
	ID       int64       `json:"id"`
	TaskID   *int64      `json:"task_id"`
	Title    string      `json:"title"`
	Snippet  string      `json:"snippet"`
	Status   string      `json:"status,omitempty"`
	WorkDate *civil.Date `json:"work_date,omitempty"`
}
type Page[T any] struct { Items []T `json:"items"`; NextCursor *string `json:"next_cursor"` }
type TagCount    struct { Name string `json:"name"`; Count int `json:"count"` } // open, non-deleted tasks
type StatusTask  struct { ID int64 `json:"id"`; Title string `json:"title"` }
type StatusBlock struct { ID int64 `json:"id"`; TaskID int64 `json:"task_id"`; Title string `json:"title"`; End civil.EndMinute `json:"end"` }
type Upcoming    struct { Deferred []Task `json:"deferred"`; DueSoon []Task `json:"due_soon"` }
type OpSummary struct {
	Seq       int64    `json:"seq"`
	OpID      string   `json:"op_id"`
	Kind      string   `json:"kind"`
	Actor     string   `json:"actor"`
	Channel   string   `json:"channel"`
	At        Time     `json:"at"`
	State     string   `json:"state"`
	Revertible bool    `json:"revertible"`
	Via       *string  `json:"via"`
	TaskIDs   []int64  `json:"task_ids"`
	Effects   []Effect `json:"effects"`
}
type EventView struct {
	ID       int64           `json:"id"`
	Op       OpSummary       `json:"op"` // Effects omitted
	Entity   string          `json:"entity"`
	EntityID int64           `json:"entity_id"`
	Action   string          `json:"action"`
	Before   json.RawMessage `json:"before"`
	After    json.RawMessage `json:"after"`
}
type OpDetail  struct { Op OpSummary `json:"op"`; Events []EventView `json:"events"` }
type AIChanges struct { ReviewedThrough int64 `json:"reviewed_through"`; Unreviewed int `json:"unreviewed"`; Ops []OpSummary `json:"ops"`; NextCursor *string `json:"next_cursor"` }
type WhoAmI    struct { Actor string `json:"actor"`; Scopes string `json:"scopes"`; Owner bool `json:"owner"`; Generation string `json:"generation"` }
type TokenInfo struct {
	ID int64 `json:"id"`; Name string `json:"name"`; Scopes string `json:"scopes"`; Owner bool `json:"owner"`
	MutationsPerHour int `json:"mutations_per_hour"`; CreatedAt Time `json:"created_at"`
	ExpiresAt *Time `json:"expires_at"`; LastUsedAt *Time `json:"last_used_at"`; RevokedAt *Time `json:"revoked_at"`
}
type TokenCreated struct { Token TokenInfo `json:"token"`; Secret string `json:"secret"` } // shown once
type Settings     struct { BackupKeepDaily int `json:"backup_keep_daily"`; BackupKeepWeekly int `json:"backup_keep_weekly"` }
type Snapshot     struct { Name string `json:"name"`; Path string `json:"path"`; Kind string `json:"kind"`; At Time `json:"at"`; Size int64 `json:"size"`; SchemaVersion int `json:"schema_version"` }
type BackupStatus struct { BackupHealth; Dir string `json:"dir"`; Snapshots []Snapshot `json:"snapshots"` }
```

Requests (`internal/api/requests.go`):

```go
type AddTaskRequest struct {
	Title    string        `json:"title"`
	Notes    string        `json:"notes,omitempty"`
	Due      *Moment       `json:"due,omitempty"`
	Tags     []string      `json:"tags,omitempty"`
	Project  string        `json:"project,omitempty"`
	Priority string        `json:"priority,omitempty"`
	Place    *AddPlacement `json:"place,omitempty"`
}
type AddPlacement struct {
	How   string        `json:"how"`             // top|bottom|before|after|defer|block|now
	Ref   int64         `json:"ref,omitempty"`   // before/after
	Defer *Moment       `json:"defer,omitempty"`
	Block *BlockRequest `json:"block,omitempty"`
}
type UpdateTaskRequest struct {
	Title      Opt[string] `json:"title,omitzero"`    // null rejected
	Notes      Opt[string] `json:"notes,omitzero"`    // null rejected
	Project    Opt[string] `json:"project,omitzero"`  // null clears
	Priority   Opt[string] `json:"priority,omitzero"` // null clears
	TagsAdd    []string    `json:"tags_add,omitempty"`
	TagsRemove []string    `json:"tags_remove,omitempty"`
}
type DueRequest      struct { Due Opt[Moment] `json:"due"` }    // key required; null clears
type DeferRequest    struct { Until Opt[Moment] `json:"until"` } // key required; null clears the defer
type PlaceRequest    struct { How string `json:"how"`; Ref int64 `json:"ref,omitempty"` }
type BlockRequest    struct { Date civil.Date `json:"date"`; Start civil.Minute `json:"start"`; End civil.EndMinute `json:"end"` }
type ReblockRequest  struct { BlockRequest; TaskETag string `json:"task_etag"` }
type OutcomeRequest  struct { Outcome string `json:"outcome"` } // worked|missed|auto
type CompleteRequest struct {
	As    *string     `json:"as,omitempty"`
	Date  *civil.Date `json:"date,omitempty"`
	Log   string      `json:"log,omitempty"`
	Notes string      `json:"notes,omitempty"`
}
type ReopenRequest    struct { RemoveEntry bool `json:"remove_entry"` }
type AcceptNewRequest struct { Items []struct { ID int64 `json:"id"`; ETag string `json:"etag"` } `json:"items"` }
type AddEntryRequest  struct {
	TaskID   Opt[int64]    `json:"task_id"` // MUST be present: number or null (D§5.2)
	Title    string        `json:"title"`
	Notes    string        `json:"notes,omitempty"`
	WorkDate *civil.Date   `json:"work_date,omitempty"`
	Time     *civil.Minute `json:"time,omitempty"`
}
type UpdateEntryRequest struct {
	Title    Opt[string]       `json:"title,omitzero"`
	Notes    Opt[string]       `json:"notes,omitzero"`
	WorkDate Opt[civil.Date]   `json:"work_date,omitzero"`
	Time     Opt[civil.Minute] `json:"time,omitzero"` // null clears
}
type RelinkRequest      struct { TaskID Opt[int64] `json:"task_id"` } // present; null = standalone
type FocusRequest       struct { TaskID Opt[int64] `json:"task_id"`; Interrupt bool `json:"interrupt,omitempty"` }
type ReviewRequest      struct { ThroughSeq int64 `json:"through_seq"` }
type PasswordRequest    struct { Current string `json:"current"`; New string `json:"new"` }
type SettingsRequest    struct { BackupKeepDaily *int `json:"backup_keep_daily,omitempty"`; BackupKeepWeekly *int `json:"backup_keep_weekly,omitempty"`; Password string `json:"password,omitempty"` }
type CreateTokenRequest struct {
	Name             string `json:"name"`
	Scopes           string `json:"scopes"`
	ExpiresAt        Opt[Time] `json:"expires_at"` // key required; null = never (explicit choice, D§10.3)
	MutationsPerHour int    `json:"mutations_per_hour"`
}
```

Password-bearing request structs implement `slog.LogValuer` returning a
redacted value, and `Idem.Body` is never computed for them. Every `Opt[T]`
field documented as "key required" is rejected with `validation` when `!Set`
(the decoder checks this, so `null` and "absent" are never confused); those
fields carry no `omitzero` and clients always set them. Optional `Opt[T]` fields
carry `,omitzero`, so the canonical `Idem.Body` of `{}` and `{"project":null}`
differs (tested).

### 6.6 Problems

```go
type Problem struct {
	Type       string          `json:"type"`   // "urn:taskronin:problem:" + code
	Title      string          `json:"title"`  // http.StatusText(status)
	Status     int             `json:"status"`
	Detail     string          `json:"detail"`
	Code       string          `json:"code"`
	RequestID  string          `json:"request_id"`
	Errors     []FieldError    `json:"errors,omitempty"`
	Conflicts  []Conflict      `json:"conflicts,omitempty"`
	Current    json.RawMessage `json:"current,omitempty"`     // 412: Task|Entry|Block
	RetryAfter int             `json:"retry_after,omitempty"` // seconds
}
type FieldError struct { Field string `json:"field"`; Message string `json:"message"` }
type Conflict   struct {
	OpID string `json:"op_id"`; Kind string `json:"kind"`; Actor string `json:"actor"`; At Time `json:"at"`
	Entity string `json:"entity"`; EntityID int64 `json:"entity_id"`
}
```
HTTP status per code is the table in §2.8; `server/respond.go` owns the mapping
(`func statusFor(domain.Code) int`), and a test asserts every `Code` maps.
`internal` problems never include `Err` text.

### 6.7 `client` package API and client-side helpers

Used by `cli`, `tui` and `mcpserver`; the only way they reach data (R2, §1.3).

```go
package client

type Options struct {
	Socket     string        // unix socket path (config.Client.Socket)
	TokenFile  string        // checked by New: regular file, owned by the euid, no group/other bits
	Channel    string        // "cli" | "tui" | "mcp" → X-Taskronin-Channel
	Timeout    time.Duration // per request; default 20 s (§4.11); long calls use their own budget
	Pending    *PendingStore // nil disables saving outcome-unknown requests (TUI/MCP pass nil)
	NewKey     func() string // idempotency key; default 32 hex chars from crypto/rand (tests inject)
	Clock      clock.Clock
}
func New(o Options) (*Client, error) // reads + checks the token; no network I/O
func NewFromConfig(cfg *config.Client, channel string, pending *PendingStore) (*Client, error)

// Generation is fetched lazily from GET /whoami and cached for the process; every mutation
// sends it as Taskronin-Generation. A generation_mismatch reply refreshes the cache (the
// failed mutation is never resent).
func (c *Client) Generation(ctx context.Context) (string, error)
func (c *Client) RefreshGeneration(ctx context.Context) (string, error)
func (c *Client) Actor(ctx context.Context) (string, error) // WhoAmI.Actor, cached with the generation

// Mut carries per-mutation preconditions. Every mutation also sends Authorization,
// X-Taskronin-Channel, Taskronin-Generation and Content-Type: application/json.
type Mut struct {
	IfMatch string // entity ETag "<gen>-<version>"; required where §6.4 says IfM, else ""
	Key     string // Idempotency-Key; "" → Options.NewKey(); a resend passes the saved key
}

// Queries
type PageQuery   struct{ Limit int; Cursor string }
type TaskQuery   struct{ View, Tag, Project string; PageQuery }
type EntryQuery  struct{ From, To *civil.Date; Task *int64; Standalone, IncludeDeleted bool; PageQuery }
type BlockQuery  struct{ From, To *civil.Date; Task *int64 }
type ReportQuery struct{ From, To civil.Date; Tag, Project string; Todo, Upcoming bool }
type SearchQuery struct{ Q, Kind string; PageQuery }
type OpsQuery    struct{ Actor string; SinceSeq int64; PageQuery }

// Reads (GET). Entity ETags come back in the representation's ETag field.
func (c *Client) Health(ctx context.Context) error
func (c *Client) Clock(ctx context.Context) (*api.Clock, error)
func (c *Client) WhoAmI(ctx context.Context) (*api.WhoAmI, error)
func (c *Client) Today(ctx context.Context, ifNoneMatch string) (t *api.Today, etag string, notModified bool, err error)
func (c *Client) Status(ctx context.Context, ifNoneMatch string) (s *api.Status, etag string, notModified bool, err error)
func (c *Client) Day(ctx context.Context, d civil.Date) (*api.Day, error)
func (c *Client) Upcoming(ctx context.Context) (*api.Upcoming, error)
func (c *Client) Report(ctx context.Context, q ReportQuery) (*api.Report, error)
func (c *Client) Search(ctx context.Context, q SearchQuery) (*api.Page[api.SearchHit], error)
func (c *Client) Tags(ctx context.Context) ([]api.TagCount, error)
func (c *Client) ListTasks(ctx context.Context, q TaskQuery) (*api.Page[api.Task], error)
func (c *Client) GetTask(ctx context.Context, id int64) (*api.TaskDetail, error)
func (c *Client) TaskHistory(ctx context.Context, id int64, p PageQuery) (*api.Page[api.EventView], error)
func (c *Client) ListEntries(ctx context.Context, q EntryQuery) (*api.Page[api.Entry], error)
func (c *Client) GetEntry(ctx context.Context, id int64) (*api.Entry, error)
func (c *Client) ListBlocks(ctx context.Context, q BlockQuery) ([]api.Block, error)
func (c *Client) GetBlock(ctx context.Context, id int64) (*api.Block, error)
func (c *Client) ListOps(ctx context.Context, q OpsQuery) (*api.Page[api.OpSummary], error)
func (c *Client) GetOp(ctx context.Context, opID string) (*api.OpDetail, error)
func (c *Client) AIChanges(ctx context.Context, all bool, p PageQuery) (*api.AIChanges, error)
func (c *Client) Settings(ctx context.Context) (*api.Settings, error)
func (c *Client) Tokens(ctx context.Context) ([]api.TokenInfo, error)
func (c *Client) Backups(ctx context.Context) (*api.BackupStatus, error)
func (c *Client) Export(ctx context.Context, w io.Writer) error // streams; 11 min budget

// Mutations (POST/PATCH, idempotency-keyed). All return *api.OpResponse.
func (c *Client) AddTask(ctx context.Context, m Mut, r api.AddTaskRequest) (*api.OpResponse, error)
func (c *Client) UpdateTask(ctx context.Context, m Mut, id int64, r api.UpdateTaskRequest) (*api.OpResponse, error) // PATCH
func (c *Client) SetDue(ctx context.Context, m Mut, id int64, r api.DueRequest) (*api.OpResponse, error)
func (c *Client) PlaceTask(ctx context.Context, m Mut, id int64, r api.PlaceRequest) (*api.OpResponse, error)
func (c *Client) DeferTask(ctx context.Context, m Mut, id int64, r api.DeferRequest) (*api.OpResponse, error)
func (c *Client) BlockTask(ctx context.Context, m Mut, id int64, r api.BlockRequest) (*api.OpResponse, error)
func (c *Client) MarkManual(ctx context.Context, m Mut, id int64) (*api.OpResponse, error)        // POST …/triage
func (c *Client) CompleteTask(ctx context.Context, m Mut, id int64, r api.CompleteRequest) (*api.OpResponse, error)
func (c *Client) ReopenTask(ctx context.Context, m Mut, id int64, r api.ReopenRequest) (*api.OpResponse, error)
func (c *Client) DropTask(ctx context.Context, m Mut, id int64) (*api.OpResponse, error)
func (c *Client) DeleteTask(ctx context.Context, m Mut, id int64) (*api.OpResponse, error)
func (c *Client) UndeleteTask(ctx context.Context, m Mut, id int64) (*api.OpResponse, error)
func (c *Client) AcceptNew(ctx context.Context, m Mut, r api.AcceptNewRequest) (*api.OpResponse, error) // per-item etags; m.IfMatch unused
func (c *Client) AddEntry(ctx context.Context, m Mut, r api.AddEntryRequest) (*api.OpResponse, error)
func (c *Client) UpdateEntry(ctx context.Context, m Mut, id int64, r api.UpdateEntryRequest) (*api.OpResponse, error) // PATCH
func (c *Client) RelinkEntry(ctx context.Context, m Mut, id int64, r api.RelinkRequest) (*api.OpResponse, error)
func (c *Client) DeleteEntry(ctx context.Context, m Mut, id int64) (*api.OpResponse, error)
func (c *Client) UndeleteEntry(ctx context.Context, m Mut, id int64) (*api.OpResponse, error)
func (c *Client) Reblock(ctx context.Context, m Mut, blockID int64, r api.ReblockRequest) (*api.OpResponse, error)
func (c *Client) Unblock(ctx context.Context, m Mut, blockID int64) (*api.OpResponse, error)
func (c *Client) SetOutcome(ctx context.Context, m Mut, blockID int64, r api.OutcomeRequest) (*api.OpResponse, error)
func (c *Client) SetFocus(ctx context.Context, m Mut, r api.FocusRequest) (*api.OpResponse, error)
func (c *Client) Revert(ctx context.Context, m Mut, opID string) (*api.OpResponse, error)
func (c *Client) Undo(ctx context.Context, m Mut) (*api.OpResponse, error)
func (c *Client) Redo(ctx context.Context, m Mut) (*api.OpResponse, error)
func (c *Client) ReviewAIChanges(ctx context.Context, m Mut, r api.ReviewRequest) (*api.OpResponse, error)
func (c *Client) RevokeToken(ctx context.Context, m Mut, id int64) (*api.TokenInfo, error)
// Idempotency-exempt (no key; never saved as pending; still send Taskronin-Generation):
func (c *Client) UpdateSettings(ctx context.Context, r api.SettingsRequest) (*api.Settings, error)
func (c *Client) CreateToken(ctx context.Context, r api.CreateTokenRequest) (*api.TokenCreated, error)
func (c *Client) ChangePassword(ctx context.Context, r api.PasswordRequest) error
func (c *Client) RunBackup(ctx context.Context) (*api.Snapshot, error) // 11 min budget

// Errors
type ProblemError struct{ Status int; Problem api.Problem } // any non-2xx with problem+json
func (e *ProblemError) Error() string
func (e *ProblemError) Code() domain.Code
func (e *ProblemError) Is(target error) bool // matches ErrGenerationMismatch for code generation_mismatch
var (
	ErrNotSent            = errors.New("request not sent")             // dial failed / no byte written: safe to re-run
	ErrOutcomeUnknown     = errors.New("request outcome unknown")      // written, no complete response after retries
	ErrGenerationMismatch = errors.New("database generation changed")  // restored since the generation was read
	ErrTokenFile          = errors.New("token file unusable")          // missing, wrong owner or mode
	ErrPendingExpired     = errors.New("pending request too old to resend safely (the server keeps idempotency records for 7 days)")
	ErrActorMismatch      = errors.New("pending request belongs to another actor")
)
type OutcomeUnknownError struct{ Key string; Saved bool; Err error } // errors.Is(err, ErrOutcomeUnknown); Saved = written to Pending
func IsCode(err error, code domain.Code) bool

// Pending requests (§9.6)
type PendingRequest struct {
	Key, Method, Path, IfMatch, Generation string
	Actor     string // WhoAmI.Actor at save time ("owner" | "token:<name>") (R4-2)
	Body      json.RawMessage
	CreatedAt time.Time
	LastError     string    // problem code + detail of the last unsuccessful resend (M1); "" if none
	LastAttemptAt time.Time
}
type PendingStore struct{ Dir string; Clock clock.Clock } // <state>/pending, 0700; files 0600 via fsutil.WriteFileAtomic
func (s *PendingStore) Save(p PendingRequest) error
func (s *PendingStore) List() ([]PendingRequest, error)
func (s *PendingStore) MarkFailed(key, lastError string) error // keeps the file, records the error
func (s *PendingStore) Discard(key string) error              // explicit user discard, or after success
func (c *Client) Resend(ctx context.Context, p PendingRequest) (*api.OpResponse, error)
	// refuses with ErrPendingExpired (age ≥ PendingResendWindow = 7 d − 1 h), ErrGenerationMismatch (saved ≠ current generation)
	// or ErrActorMismatch (saved Actor ≠ the current token's WhoAmI.Actor);
	// only a success (incl. an idempotent replay of a success) lets the caller Discard the file;
	// an unsuccessful definitive answer → MarkFailed (the body is kept until `tkr pending discard`)
```

Behaviour: retries per §9.6 (transport errors ×2, `503` once, same key); a
mutation that ends as outcome-unknown is saved to `Pending` (if set and not
password-bearing) and returned as `*OutcomeUnknownError`; `412` returns
`*ProblemError` with `Problem.Current` holding the current representation; the
client never re-sends a stale edit by itself.

Client-side helpers:

```go
package textsafe
func ValidateTitle(s string) (string, error)   // strip BOM, trim, UTF-8, no C0/C1/newline/bidi/U+2028-9, 1..500 runes
func ValidateNotes(s string) (string, error)   // CRLF→LF, ≤ 65536 bytes, C0 only \n \t, no C1/bidi/U+2028-9
func ValidateName(s string, max int) error     // tags, projects, token names: [A-Za-z0-9._-]{1,max}
func Sanitize(s string) string                  // terminal/log: non-printable → U+FFFD (keeps \n, \t, ZWJ/ZWNJ)
func SanitizeLine(s string) string              // Sanitize + \n, \t → U+FFFD
func Width(s string) int                        // display width (x/ansi)
func Truncate(s string, width int, tail string) string

package editor
func Command(configured string, getenv func(string) string) ([]string, error) // config editor, $VISUAL, $EDITOR, "vi"; SplitWords
func SplitWords(s string) ([]string, error)    // '…', "…" (\ escapes \ " $ `), \ outside quotes; unbalanced → error; no expansion, never a shell
type Drafts struct{ Dir string; Clock clock.Clock } // <state>/drafts, 0700
type Draft struct{ Path string }
func (d Drafts) New(initial string) (*Draft, error) // <ts>-<rand>.md, 0600, written before the editor opens
func Edit(ctx context.Context, argv []string, d *Draft, stdin io.Reader, stdout, stderr io.Writer) (string, error)
func (d *Draft) Discard() error                   // only after the server confirmed

package config
type Paths struct {
	ConfigDir, DataDir, StateDir, RuntimeDir string
	ServerConfig, ClientConfig, Socket, OwnerToken, TokensDir string
	DraftsDir, PendingDir, StagingDir, BackupDir, LedgerFile, DB string
}
func DefaultPaths(getenv func(string) string, home string) (Paths, error) // XDG rules of §8.1
type Server struct{ /* §8.2 */ }
type Client struct {
	Socket, TokenFile      string
	Timeout, StatusTimeout time.Duration
	Editor, Color          string
	MCP                    struct{ TokenFile string }
}
func LoadServer(path string) (*Server, error)            // strict (unknown keys → error) + ValidateServer
func LoadClient(path string, p Paths) (*Client, error)   // missing file → defaults from p; + ValidateClient
func ValidateServer(s *Server) error
func ValidateClient(c *Client) error
func WriteServer(path string, s *Server) error           // 0600, fsutil.WriteFileAtomic
func WriteClient(path string, c *Client) error
```
(`datespec` signatures are in §9.2.)

---

## 7. Authentication and authorization

### 7.1 Tokens (`internal/auth/token.go`)

```go
const TokenPrefix = "tkr_"
// Format: tkr_<id decimal, no leading zero>_<base64url(32 random bytes), 43 chars, no padding>
// Regex:  ^tkr_([1-9][0-9]{0,18})_([A-Za-z0-9_-]{43})$
func NewSecret() (raw [32]byte, err error)
func FormatToken(id domain.TokenID, raw [32]byte) string
func ParseToken(s string) (id domain.TokenID, raw [32]byte, err error)
func HashSecret(raw [32]byte) [32]byte   // SHA-256
```
`service.AuthenticateToken(ctx, bearer string, verifiedSocket bool, ch domain.Channel, remoteKey string) (domain.Actor, error)`:
parse → load row by id (reader) → `subtle.ConstantTimeCompare(hash, row.secret_hash)`
(a dummy compare runs when the id is unknown, same 401 either way) →
reject revoked or `expires_at ≤ now` → if `is_owner` require `verifiedSocket`
→ Actor{Owner or Token(name), scopes, `MutationsPerHour`} → mark `last_used_at`
in the in-memory usage map (flushed ≤ 1/min).

### 7.2 Owner token

| Aspect | Rule |
|---|---|
| Row | `api_tokens` with `is_owner=1`, name `owner.<YYYYMMDDTHHMMSS.mmmZ>-<4 hex>`, all scopes, no expiry, no rate cap; at most one unrevoked (partial unique index). Actor key is always `owner`. |
| File | `$XDG_STATE_HOME/taskronin/owner.token`, 0600, content `tkr_…\n`, written with `fsutil.WriteFileAtomic`. Docs: never sync or commit it. |
| Created | `tkr init`. |
| Replaced | `tkr passwd --reset` (revokes **all** tokens and sessions, mints a new owner token, rewrites the file); `tkr restore` when the current DB is unreadable (D§7.3 step 4). |
| Accepted | Only on socket connections that pass the peer-UID check (`verifiedSocket`); never over TCP; never with channel `mcp`. |
| Client checks | `client` refuses a token file that is not a regular file, not owned by the euid, or has any group/other permission bits. |
| Revocation online | Refused (409); rotate with `passwd --reset`. |

### 7.3 Scopes per actor

| Actor | Scopes | Restrictions |
|---|---|---|
| `owner` (owner token over verified socket) | all | — |
| `owner` (web session, channel `web`) | all | token management refused (D§10.3); retention lowering needs password re-entry; purge and backup scrubbing are offline commands |
| `token:<name>` | as granted; MCP preset `read,capture,write`, 60 mutations/hour | may revert only its own ops; never `ChangePassword` |
| `system` | internal | never authenticated; reconcile, maintenance, scheduled backups |

`capture` = `AddTask` without placement, `AddEntry`. `write`, `destroy`,
`admin` per the §3 tables (D§10.3). Summary matrix (single source; the
behavior catalog's SCP table aligns to it):

| Scope | Operations |
|---|---|
| read | all GETs (views, today/status/day, report, search, history, ops, ai-changes, settings, clock) |
| capture | `AddTask` without placement; `AddEntry` |
| write | `AddTask` with placement (needs capture **and** write; owner only — tokens get `forbidden`, R-b); `UpdateTask` (title/notes/tags/project/priority); `SetDue`; `Place`; `AcceptNew`; `Defer` (incl. `none`); `MarkManual`; `Block`; `Reblock`; `Unblock`; `SetOutcome`; `Complete`; `Reopen` (no remove); `Undelete` (task); `RelinkEntry`; `SetFocus`; `MarkAIReviewed` (owner only, R-a) |
| destroy | `Drop`; `Delete`; `UpdateEntry`; `DeleteEntry`; `UndeleteEntry`; `Reopen{RemoveEntry}`. (`tkr purge` and `tkr scrub-backups` are offline: instance lock + password, no token, S4.) |
| admin | tokens (not web); `UpdateSettings`; `ChangePassword` (owner); `RunBackup`; `BackupStatus`; `Export` |
| scopes of the target op | `Revert`, `Undo`, `Redo` (tokens: own ops only, §14 D15) |

### 7.4 Password (`internal/auth/password.go`, `gate.go`)

```go
const (ArgonTime = 3; ArgonMemoryKiB = 64 * 1024; ArgonThreads = 1; ArgonKeyLen = 32; SaltLen = 16)
func HashPassword(pw string) (string, error)            // "$argon2id$v=19$m=65536,t=3,p=1$<b64 salt>$<b64 key>"
func VerifyPassword(encoded, pw string) (bool, error)   // constant-time compare of derived key
func ValidateNewPassword(pw string) error               // 8..1024 bytes, valid UTF-8

type PasswordGate struct { /* sem chan struct{} (cap 1); global *rate.Limiter; perKey LRU(1024) of *rate.Limiter */ }
func NewPasswordGate(c clock.Clock) *PasswordGate        // global: 1/s burst 5; per key: 1 per 5 s burst 5
func (g *PasswordGate) Verify(ctx context.Context, key, encoded, pw string) (bool, error)
	// Waits on both buckets (max 15 s, else domain CodeRateLimited with RetryAfter),
	// then holds the semaphore for one argon2 verification. No lockout (D§10.3).
```
- Keys: client IP for web, `"socket"` for API callers.
- Password bytes are never logged, stored outside `auth_state`, hashed into
  idempotency rows, or kept beyond the request (request structs zeroed after use
  where practical).

### 7.5 Web sessions and CSRF (plan D, `web/session.go`, `web/csrf.go`)

- Login: `POST /ui/login` (form `password`) → strict Origin/Sec-Fetch-Site check
  (exempt from the session CSRF token, which cannot exist yet) →
  `svc.Login(ctx, remoteIP, pw)` → new session: id = 32 random bytes (cookie
  value base64url), stored as SHA-256; `csrf_secret` = 32 random bytes;
  `expires_at = now + 30 d`; idle limit 7 d via `last_seen_at`. Any presented
  old session is deleted (regeneration). 303 → `/ui/`.
- Cookie (plain loopback, v1): `tr_session=<v>; HttpOnly; Path=/; SameSite=Lax`.
  (TLS mode later: `__Host-tr_session; Secure; HttpOnly; Path=/; SameSite=Lax`.)
- CSRF token = base64url(HMAC-SHA256(csrf_secret, "tkr-csrf-v1")); emitted in a
  hidden `_csrf` field on every form and as `hx-headers='{"X-CSRF-Token":"…"}'`
  on `<body>`. Every POST except login must carry a matching token (constant-time compare)
  **and** pass the origin check: `Origin` is `http://` + one of the host allowlist
  entries of §6.2 (so `[::1]` works when bound there), or `Sec-Fetch-Site:
  same-origin`; reject when both headers are absent or either mismatches.
- `ChangePassword` and `passwd --reset` delete all sessions. Logout deletes the row.
- Web form idempotency: each rendered form carries `_nonce` (128-bit hex) used as
  `Idem.Key`; `Idem.Body` = sorted, re-encoded form values minus `_csrf`/`_nonce`.
- Web forms carry full ETags (`"<gen>-<version>"`): `etag` of the target,
  `task_etag` for reblock, one `etag` per item for accept-new; the handler parses
  them into `domain.Expect`. Every mutating form (incl. undo/redo/revert buttons)
  also carries `_gen` = the generation the page was rendered with → `Meta.Gen`.

### 7.6 Peer credentials (`server/peercred.go`)

```go
type Transport uint8
const (TransportUnix Transport = iota + 1; TransportTCP)
type ConnInfo struct {
	Transport Transport
	PeerUID   uint32
	PeerPID   int32
	PeerOK    bool            // SO_PEERCRED read succeeded
	Remote    netip.AddrPort  // TCP only
}
func ConnContext(ctx context.Context, c net.Conn) context.Context
	// *net.UnixConn → SyscallConn().Control(fd → unix.GetsockoptUcred(fd, SOL_SOCKET, SO_PEERCRED))
func ConnInfoFrom(ctx context.Context) ConnInfo
```
Peer credentials alone never authenticate (D§10.2); they are a second factor
for the owner token and a gate for all socket requests.

---

## 8. Configuration, init, systemd

### 8.1 Paths (`internal/config/paths.go`)

| Item | Default | Mode |
|---|---|---|
| Server config | `$XDG_CONFIG_HOME/taskronin/server.toml` | 0600 |
| Client config | `$XDG_CONFIG_HOME/taskronin/client.toml` | 0600 |
| Data dir | `$XDG_DATA_HOME/taskronin/` | 0700 |
| Database | `<data>/taskronin.db` (+`-wal`, `-shm`) | 0600 |
| Instance lock | `<data>/taskronin.lock` | 0600 |
| Purge ledger | `<data>/purge-ledger` | 0600 |
| Swap journals / staging | `<data>/swap.journal`, `<backupdir>/scrub.journal`; staging dir `<data>/staging/`; `.tkr-tmp-*` next to targets (§12.0) | 0600 / 0700 |
| Pre-restore / pre-import sets | `<data>/taskronin.db.pre-restore-<UTC ts>/`, `…pre-import-<UTC ts>/` | 0700 |
| Backups | `<data>/backups/` (`backup.dir`) | 0700 / files 0600 |
| State dir | `$XDG_STATE_HOME/taskronin/` | 0700 |
| Owner token | `<state>/owner.token` | 0600 |
| Other token files | `<state>/tokens/<name>.token` | 0600 |
| Drafts | `<state>/drafts/` | 0700 / 0600 |
| Pending requests (outcome unknown) | `<state>/pending/<key>.json` | 0700 / 0600 |
| Socket | `$XDG_RUNTIME_DIR/taskronin/sock`; if `XDG_RUNTIME_DIR` unset: `<state>/sock` (ownership checked) | dir 0700, socket 0600 |
| systemd units | `$XDG_CONFIG_HOME/systemd/user/taskronin.{service,socket}` | 0644 |

XDG defaults per spec (`~/.config`, `~/.local/share`, `~/.local/state`). Tests
set the `XDG_*` variables to temp dirs; there is no other path override.

### 8.2 `server.toml`

```toml
# Written by `tkr init`. Unknown keys are an error.
timezone      = "America/Denver"   # REQUIRED, IANA name (D§5.12); serve refuses without it
week_start    = "monday"           # monday..sunday
due_soon_days = 3                  # 0..30

[paths]
data_dir = "/home/sean/.local/share/taskronin"   # absolute, resolved by init
socket   = "/run/user/1000/taskronin/sock"       # must match the .socket unit

[web]
enabled              = true              # handlers exist from plan D; earlier builds ignore
listen               = "127.0.0.1:7878"  # loopback only in v1
allow_plain_loopback = true              # set by init after the single-user check (D§10.2)

[backup]
dir  = "/home/sean/.local/share/taskronin/backups"
time = "03:30"                           # daily, server zone

[log]
level  = "info"                          # debug|info|warn|error
format = "text"                          # text|json
```

Retention (`backup_keep_*`) is **not** here: it lives in DB `settings` so that
lowering it is password-gated and audited (§14 D17).

### 8.3 `client.toml`

```toml
socket         = "/run/user/1000/taskronin/sock"
token_file     = "/home/sean/.local/state/taskronin/owner.token"
timeout        = "20s"     # ≥ the server's 15 s op budget (§4.11)
status_timeout = "300ms"
editor         = ""        # overrides $VISUAL / $EDITOR; split with editor.SplitWords
color          = "auto"    # auto|always|never; NO_COLOR honoured

[mcp]
token_file = "/home/sean/.local/state/taskronin/tokens/mcp.token"
```
Missing client config → defaults from §8.1 (the CLI works without the file).

### 8.4 Validation (`config.ValidateServer`, `config.ValidateClient`)

- `toml.MetaData.Undecoded()` non-empty → error naming the keys.
- `timezone` loads via `civil.LoadZone`; `week_start` valid; `due_soon_days` 0–30.
- Paths absolute, clean, no `?` or `#`, not world-writable parents.
- `web.listen` host is a loopback IP literal (`127.0.0.0/8`, `::1`) or `localhost`
  (mapped to `127.0.0.1`); non-loopback → error (v1, D§10.2). If `web.enabled`
  and `!allow_plain_loopback` → web listener refused with an explanation (no TLS in v1).
- `backup.time` parses as `HH:MM`; `backup.dir` absolute, and neither the data dir itself nor
  `<data>/staging` or anything inside it (`CleanupTemps` empties staging at every start and
  offline command, which would wipe the snapshots; final review B-M13).
- Durations parse with `time.ParseDuration`; `status_timeout ≤ 2s`.

### 8.5 `tkr init` (`offline/init.go`)

Flags: `--timezone TZ`, `--password-stdin`, `--no-systemd`, `--yes`, `--mcp-token`,
`--units-only` (re-render and reinstall the systemd units from the existing config; touches nothing else).
1. Refuse if `<data>/taskronin.db` exists ("already initialized"). Only `init` creates a DB (D§6).
   Also refuse when no DB exists but files of an earlier one remain: `<data>/purge-ledger`,
   `taskronin.db-wal`/`-shm`, or a swap/scrub journal (the journals are already refused by the shared
   offline preamble with its `--resume`/`--rollback` advice). The message names the files, says that
   `tkr restore` recovers that database from a backup, and that starting fresh requires deliberately
   moving them aside: ledger records carry bare row ids, so a leftover ledger would scrub the new
   database's tasks (final review B-C1).
2. Timezone: resolve `/etc/localtime` symlink → IANA name; confirm on TTY (or `--timezone`).
3. Password: prompt twice with `x/term` (or one line from stdin); `auth.ValidateNewPassword`.
4. Single-user check: list `/etc/passwd` accounts with UID ≥ 1000 and a login shell; if only the current user, confirm and set `allow_plain_loopback = true`; otherwise warn and leave it false.
5. `umask 077`; create dirs (§8.1); acquire the instance lock.
6. Write `server.toml` and `client.toml` (if absent) with resolved absolute paths.
7. Stage the DB in a fresh dir under `<data>/staging/` (`mode=rwc`; covered by `CleanupTemps`): `Migrate` (no pre-migrate backup for an empty DB); insert `db_generation`, `UPDATE` `tz_last_seen`, `auth_state` (hash, random `idem_hmac_key`), the owner token row; close and fsync. Write `owner.token` (`WriteFileAtomic`). **Publish the DB last**: rename the staged file to `<data>/taskronin.db`, fsync the dir. If interrupted before that rename no DB exists and `init` can simply be re-run (it rewrites the token).
8. `--mcp-token`: create `mcp` token (MCP preset, expiry prompted) → `<state>/tokens/mcp.token`; print the MCP client snippet (`{"command": "tkr", "args": ["mcp"]}`).
   The expiry is prompted (TTY only; otherwise it never expires) right after the step 3–4 answers and
   validated **before any file is written**, with the `tkr token create` grammar (`Nh`, `Nd`,
   `YYYY-MM-DD`, `never`; `domain.ParseTokenExpiry`); an invalid answer is a usage error (exit 2) and
   nothing is created (B-I3).
9. Unless `--no-systemd`: write units (§8.6), `systemctl --user daemon-reload`, `enable --now taskronin.socket`, `enable taskronin.service`.
10. Print next steps (status-line snippet for shell/tmux, `tkr add`).

### 8.6 systemd user units (`internal/systemd/units/*.tmpl`)

`taskronin.socket`:
```ini
[Unit]
Description=Taskronin API socket

[Socket]
ListenStream={{.Socket}}
FileDescriptorName=api
SocketMode=0600
DirectoryMode=0700
RemoveOnStop=yes

[Install]
WantedBy=sockets.target
```

`taskronin.service`:
```ini
[Unit]
Description=Taskronin task server
Requires=taskronin.socket
After=taskronin.socket

[Service]
Type=notify
NotifyAccess=main
ExecStart={{.Binary}} serve
Restart=on-failure
RestartSec=2s
TimeoutStopSec=45s
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths={{.DataDir}} {{.StateDir}} {{.RuntimeDir}}{{range .ExtraRW}} {{.}}{{end}}
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources

[Install]
WantedBy=default.target
```
- `{{.Binary}}` = `os.Executable()` resolved at init; `ExtraRW` = backup dir if
  outside the data dir. Re-run `tkr init --units-only` after moving the binary.
- The service starts at login (web listener) and is also socket-activated
  (CLI always works). Offline commands tell the user to stop **both** units
  (`systemctl --user stop taskronin.socket taskronin.service`), because a
  connection to the socket would otherwise restart the server.
- A test renders the units and, when `systemd-analyze` exists, runs
  `systemd-analyze --user verify` on them (skipped otherwise).

---

## 9. CLI reference (`internal/cli`)

### 9.1 Global behaviour

- Entry: `cli.Main(ctx, args, stdin, stdout, stderr) int` (exit code). Cobra
  `SilenceUsage`/`SilenceErrors`; errors rendered by `cli/errors.go`.
- Persistent flags: `--json` (print API JSON / problem JSON to stdout),
  `--socket PATH`, `--token-file PATH`, `--color auto|always|never`.
- Requests carry `X-Taskronin-Channel: cli` (TUI: `tui`, MCP: `mcp`).
- IDs: tasks `42` or `#42`; blocks `b7` or `7`; entries `e9` or `9`
  (`cli/ids.go`). Output always prints the prefixed form. Every verb that takes
  an ID uses these forms, including `tkr purge task|entry ID` (final review B-M9).
- Mutations of existing records: `GET` the record, then send its ETag in
  `If-Match` (two requests over the socket). On 412 the CLI prints what changed
  and exits 6; it never retries.
- `client` reads the generation from `GET /whoami` once per process (CLI
  invocation, TUI/MCP session) and sends `Taskronin-Generation` on every
  mutation. On `generation_mismatch` (the DB was restored) the TUI/MCP refetch it
  and refresh their views, but never resend the failed mutation.
- Every mutation echoes what it did from `OpResponse.effects` (D§5.9), e.g.
  `#42 'Set up keycloak postgres cluster' completed; ✓ logged Mon 2026-10-26`.
- All user text printed to the terminal goes through `textsafe.Sanitize` /
  `SanitizeLine`; columns use `textsafe.Truncate` (x/ansi widths).
- Colour via Lip Gloss only when stdout is a TTY, `--color` allows it, and `NO_COLOR` is unset.
- Destructive commands (`delete`, `purge`, `restore`, `token revoke`) confirm on a TTY or need `-y/--yes`.

### 9.2 Relative dates

- The CLI resolves datespecs locally against `GET /clock` (fetched lazily, once
  per invocation): `datespec.Ref{Today, WeekStart}`. The server validates and
  the response echoes resolved values, which the CLI prints
  (`deferred to Fri 2026-10-30 09:00`).
- Mode: **Past** (weekday = most recent incl. today) for `log --date`,
  `done --date`, `recap --date`, `report --from/--to`, `entries --date`;
  **Future** (weekday = next, excl. today) for `defer`, `due`, `block --date`,
  `reblock --date`, `add --defer/--due/--block`.

Grammar (`internal/datespec`, case-insensitive):
```
spec    = date [ ws clock ] | clock                 ; clock alone → today (Future commands then reject it if ≤ now, A19)
date    = "today" | "yesterday" | "tomorrow" | weekday | rel | iso
weekday = mon|monday|tue|tues|tuesday|wed|weds|wednesday|thu|thur|thurs|thursday
        | fri|friday|sat|saturday|sun|sunday
rel     = ("+"|"-") 1*3DIGIT ("d"|"w")              ; "+0d" = today
iso     = 4DIGIT "-" 2DIGIT "-" 2DIGIT
clock   = 1*2DIGIT ":" 2DIGIT                       ; 0:00–23:59
span    = [ date ws ] clock "-" clock               ; end may be "24:00"; used by --block, block, reblock
```
```go
type Mode uint8
const (Past Mode = iota + 1; Future)
type Ref struct { Today civil.Date; WeekStart time.Weekday }
func Parse(s string, m Mode, r Ref) (domain.Moment, error)
func ParseSpan(s string, m Mode, r Ref) (domain.BlockSpan, error)
func ParseClock(s string, allow2400 bool) (civil.Minute, error)
```

### 9.3 Commands

| Command | Args / flags | API | Notes |
|---|---|---|---|
| `tkr`, `tkr today` | — | `GET /today` | Dashboard: header, banners, focus/current block, today's blocks (`b7 13:00–15:00 #42 … reblock?`), triage count + top 5, overdue/due-soon (only if present), today's log, interruptions, AI-changes banner. Golden-tested layout. |
| `tkr add TITLE` | one of `--top --bottom --after N --before N --defer D --block SPAN --now`; `--due D`; `-t TAG`…; `-P PROJECT`; `-p H\|M\|L`; `-e` (notes in `$EDITOR`) | `POST /tasks` | `#57 added to triage (new)` / `#57 top of backlog`. TITLE is one argument (quote it); >1 positional → usage error "quote your title". |
| `tkr log [ID] TEXT` | `-N`, `--date D`, `--at HH:MM`, `-e`, `-m NOTE` | [`GET /status`] `POST /entries` | §9.4 |
| `tkr done ID` | `--as TITLE`, `--date D`, `--log TEXT`, `-m NOTE` | `POST /tasks/{id}/complete` | |
| `tkr reopen ID` | `--remove-entry` | `POST …/reopen` | Hints that `tkr undo` of the `done` op restores cancelled blocks exactly. With `--remove-entry`, echoes any block that now counts as missed (slip, A23). |
| `tkr drop ID` | — | `POST …/drop` | |
| `tkr delete ID` / `tkr undelete ID` | `-y` | `POST …/delete` / `…/undelete` | |
| `tkr undo` / `tkr redo` | — | `POST /undo` / `/redo` | Prints the reverted op's effects. |
| `tkr revert OPID` | — | `POST /ops/{op}/revert` | |
| `tkr purge task\|entry ID` / `tkr purge --repair-ledger` | `--with-entries`, `--keep-recovery-files`, `-y` | `offline.Purge` | **Offline** (S4, §12.6): needs the server stopped; password prompt (TTY only). Scrubs the live DB and every backup set, then (unless `--keep-recovery-files`) deletes this host's pending requests and drafts after confirmation; prints scrubbed IDs, linked entries left alone, per-set results, and that earlier exports/log lines are out of reach (D§5.11). |
| `tkr scrub-backups` | `--check`, `--resume`, `--keep-recovery-files`, `-y` | `offline.ScrubBackups` | Offline (S1, §12.7): the standalone/resumable form of purge steps 7–9; password prompt on a TTY. `--check` exits 8 when any set `contains` purged content (§9.6); it rewrites no backup but still finishes an interrupted rewrite and replays the ledger on the live DB (§12.7 step 2–3), and its help says so (B-M7). |
| `tkr pending` / `tkr pending retry [KEY]` / `tkr pending discard KEY` | — | lists (with last error) / resends with the original idempotency key / deletes a saved request | §9.6 |
| `tkr plan` | — | TUI Plan mode | |
| `tkr recap` | `--date D` | TUI Recap mode | |
| `tkr triage` | `-i`, `--manual ID`, `--accept-new` | `GET /tasks?view=triage`; `-i` → TUI; `POST …/triage`; `POST /triage/accept-new` | |
| `tkr move ID top\|bottom\|before N\|after N` | — | `POST …/place` | |
| `tkr keep ID` | — | `POST …/place {"how":"keep"}` | |
| `tkr defer ID D\|none` | — | `POST …/defer` | `none` clears the defer: a placement, rank unchanged, task back in backlog (A24). |
| `tkr due ID D\|none` | — | `POST …/due` | |
| `tkr block ID SPAN` | `--date D` | `POST /tasks/{id}/block` | `SPAN` = `HH:MM-HH:MM` (or `D HH:MM-HH:MM`). |
| `tkr reblock BID SPAN` | `--date D` | `GET /blocks/{id}`, `GET /tasks/{tid}`, `POST …/reblock` | |
| `tkr unblock BID` | — | `POST …/unblock` | |
| `tkr outcome BID worked\|missed\|auto` | — | `POST …/outcome` | Recap without the TUI. |
| `tkr now [ID]` | `--clear`, `--interrupt` | `POST /focus` | Interruption rule §14 D35. Undo restores the previous focus only if it is still valid today (C1). |
| `tkr status` | `--short`, `--ascii` | `GET /status` | §9.5 |
| `tkr upcoming` | — | `GET /upcoming` | deferred (by defer moment) + due soon |
| `tkr ls` | `--view V` (default triage then backlog), `-t TAG`, `-P PROJECT` (matches dotted descendants, §3.7), `--all` | `GET /tasks` | `#42  [slipped×1] Set up keycloak…  infra.keycloak +db  due Fri` |
| `tkr show ID` | — | `GET /tasks/{id}` | Fields, reasons, slips, blocks, entries. Notes printed raw (sanitized), not rendered. |
| `tkr edit ID` | — | `GET`, `$EDITOR`, `PATCH /tasks/{id}` | Buffer: line 1 = title, blank line, rest = notes. Draft kept on any failure (§9.6). |
| `tkr history ID` | `--limit N` | `GET /tasks/{id}/history` | |
| `tkr search QUERY…` | `--kind task\|entry\|all` | `GET /search` | Args joined with spaces. |
| `tkr tag ID +TAG\|-TAG…` | — | `PATCH /tasks/{id}` | |
| `tkr project ID NAME\|none` | — | `PATCH /tasks/{id}` | |
| `tkr priority ID H\|M\|L\|none` | — | `PATCH /tasks/{id}` | |
| `tkr entries` | `--date D` \| `--from D --to D`, `--task ID\|none` | `GET /entries` | |
| `tkr entry show\|edit\|delete\|undelete EID` | — | `GET`/`PATCH`/`POST …/delete`/`…/undelete` | |
| `tkr entry link EID ID\|none` | — | `POST /entries/{id}/relink` | |
| `tkr report` | `--week` (default) \| `--last-week` \| `--month` \| `--today` \| `--from D --to D`; `-t`, `-P`; `--format text\|md\|json`; `--todo`; `--upcoming` | `GET /report` | §9.7 |
| `tkr ai-changes` | `--all`, `--revert OPID`, `--ack` | `GET /ai-changes`, `POST /ops/{op}/revert`, `POST /ai-changes/review` | `--ack` marks through the newest seq shown. |
| `tkr settings` / `tkr settings set KEY VALUE` | keys `backup.keep_daily`, `backup.keep_weekly` | `GET`/`PATCH /settings` | Prompts for password when lowering. |
| `tkr token create NAME` | `--scopes LIST` \| `--mcp`; `--expires DUR\|DATE\|never` (prompted if absent on a TTY); `--rate N`; `--write-file [PATH]` | `POST /tokens` | Prints the secret once; `--write-file` writes `<state>/tokens/NAME.token` (0600). |
| `tkr token list` / `tkr token revoke ID\|NAME` | `-y` | `GET /tokens` / `POST /tokens/{id}/revoke` | |
| `tkr passwd` | `--reset` | `POST /password`; `--reset` → `offline.PasswdReset` | |
| `tkr backup` | `--list` | `POST /backups` / `GET /backups`; offline fallback when the server is down and the lock is free | |
| `tkr restore SNAPSHOT` \| `--resume` \| `--rollback` | `-y` | `offline.Restore*` | §12.2; `--resume`/`--rollback` also finish or undo an interrupted import. |
| `tkr export` | `-o FILE` (default `./taskronin-export-YYYYMMDD.json`) | `GET /export` | Streams to a 0600 temp file, fsync, rename. |
| `tkr import FILE` | `-y` | `offline.Import` | §12.5 |
| `tkr migrate` | `--status` | `offline.Migrate` | |
| `tkr init` | §8.5 (+ `--units-only`) | `offline.Init` | |
| `tkr serve` | — | `server.Run` | |
| `tkr tui` | `--tab today\|triage\|backlog\|upcoming\|log` | TUI | |
| `tkr mcp` | `--token-file PATH` | `mcpserver.Run` | |
| `tkr version` | — | `GET /clock` (best effort) | client and server versions |
| `tkr completion bash\|zsh\|fish` | — | — | cobra built-in |

### 9.4 `log` arguments and implicit linking

```go
// parseLogArgs implements D§9.1. isID: ^#?[1-9][0-9]{0,18}$
func parseLogArgs(args []string, noTask, editor bool) (id domain.TaskID, hasID bool, text string, err error)
```
| Positional args | Result |
|---|---|
| 0 | `-e` → title from the first non-empty editor line; else usage "missing text". |
| 1, numeric | `-e` → ID + editor; else usage "missing text after task id (did you mean: tkr log 42 \"…\")". |
| 1, text | text |
| 2, first numeric | ID + text |
| 2, first not numeric | usage "quote your text: tkr log \"…\"" |
| ≥ 3 | usage "quote your text" |
| `-N` with an ID | usage "-N conflicts with a task id" |

Link resolution: explicit ID → that task; `-N` → `null`; else if the resolved
`--date` ≠ today → usage "backdated entries need a task id or -N" (D§5.2);
else if **interactive** (stdin is a TTY) → `GET /status`: focus task, else the
current block's task, else `null`, echoed (`→ #42 (focus)`); non-interactive
without ID/`-N` → usage error. The API request always carries explicit `task_id`.

### 9.5 `status --short` contract

- One request: `GET /status`, total deadline `status_timeout` (300 ms), socket only.
- Output: one line (or nothing), segments joined by ` · `:
  1. focus: `▶ #42 <title ≤ 24 cols>` + ` (→15:00)` when a current block of that task exists;
     no focus but a current block: `▷ #42 <title> (→15:00)`;
  2. `triage N` (N > 0); 3. `due N` (N > 0); 4. `ai N` (N > 0);
  5. `⚠ backup` if `backup_warning`; 6. `⚠ purge` if `purge_warning` (a purge is incomplete or the ledger is damaged).
- `--ascii`: `>` / `>.` markers, ` | ` separator, `!backup`, `!purge`.
- No colour unless `--color always`. Never prompts, never reads stdin, never
  writes stderr, **always exits 0**; on any error (unreachable, timeout, auth)
  prints nothing.
- Example: `▶ #42 Set up keycloak postgres… (→15:00) · triage 3 · due 1 · ai 2`.

### 9.6 Errors, exit codes, unreachable server

| Exit | Meaning |
|---|---|
| 0 | success |
| 1 | operation refused: `validation`, `idempotency_mismatch`, `malformed`, `idempotency_key_required`, `body_too_large`, `unsupported_media_type`, `internal`, other |
| 2 | usage error (args, flags, datespec) |
| 3 | server unreachable or timed out |
| 4 | `unauthenticated`, `forbidden`, `peer_rejected`, `password_*`, token-file problems |
| 5 | `not_found` |
| 6 | `conflict`, `deleted`, `revert_conflict`, `not_revertible`, `nothing_to_undo`, `nothing_to_redo`, `precondition_*`, `generation_mismatch` |
| 7 | `rate_limited`, `busy` |
| 8 | a check-only run found work still to do: `scrub-backups --check` reports a set that `contains` purged content |
| 130 | interrupted |

- Human rendering (stderr): `tkr: <detail>`, then `  <field>: <message>` per
  field error, conflicts as `  op <8 hex> <kind> by <actor> at <time> (<entity> <id>)`.
  A local (offline) `internal` failure has no safe detail of its own: its detail, and an offline
  per-set `error (…)` line, is the cause sanitized with `textsafe.SanitizeLine` (B-I1). This output
  reaches only the owner's own terminal; API problems still never carry a cause.
  `--json`: the problem object on stdout.
- Not sent — server unreachable before any byte was written (exit 3):
  ```
  tkr: cannot reach the Taskronin server at /run/user/1000/taskronin/sock: connection refused
  tkr: nothing was saved. Your command was:
    tkr add 'Restart stuck pg replica' --now
  tkr: start it with: systemctl --user start taskronin.socket
  ```
  The echo re-quotes argv with POSIX single quotes (`cli.quoteArgs`), sanitized;
  if an editor draft exists its path is printed too. The "nothing was saved" line
  and the echo belong to mutations only: a read omits them (B-M2). For
  `tkr pending retry`, a not-sent or outcome-unknown resend says instead that the
  saved request is still pending and to run `tkr pending retry` again (a resend
  never re-saves and never touches its file; B-M1).
- Client retries (`client/retry.go`): requests with an `Idempotency-Key`, and
  GETs, are retried up to 2 times (100 ms, 400 ms) on transport errors and once
  on `503 busy` (honouring `Retry-After` ≤ 2 s), always with the same key. 4xx
  is never retried.
- Failure classes (§4.11): **not sent** (`client.ErrNotSent`: dial failed or no
  request byte was written) → the message above ("nothing was saved", exit 3).
  **Outcome unknown** (`client.ErrOutcomeUnknown`: written, no complete
  response, after retries) → the CLI saves `<state>/pending/<key>.json` (0600:
  method, path, If-Match, generation, actor, body, created_at) and prints "outcome unknown — saved
  as pending request <key>; run `tkr pending retry`" (exit 3). `tkr pending retry`
  resends with the same key; only a success (or an idempotent replay of a
  success) deletes the file. An unsuccessful definitive answer (any 4xx/5xx
  problem) keeps the file and records the error in it (`last_error`); the user
  removes it explicitly with `tkr pending discard KEY` (M1).
  Password-bearing requests are never saved. A pending request whose age reaches the
  7-day idempotency window minus a 1-hour safety margin (`client.PendingResendWindow`:
  `created_at` is stamped up to the 20 s call budget after the server's commit; final
  review A-Minor-3) is never resent automatically: `tkr pending retry`
  refuses it and tells the user to check the outcome manually (`tkr show`,
  `tkr entries`) and then `tkr pending discard` it (R2-10). It likewise refuses a
  request whose saved generation differs from the server's current one (the DB
  was restored since; R3-3), or whose saved actor differs from the token now in
  use (R4-2).
- Drafts (`editor.Draft`): `-e`/`edit` write `<state>/drafts/<ts>-<rand>.md`
  (0600) before the editor opens; deleted only after the server confirms;
  otherwise the path is printed.

### 9.7 Report rendering (`internal/report`, D§14)

- Range flags (all inclusive): `--week` = the full current week from
  `week_start` (default, §14 D12); `--last-week` = the previous full week;
  `--month` = the full current calendar month; `--today`; `--from D --to D`
  (`from > to` → usage error). `-t`/`-P` per §3.7 (dotted descendants;
  standalone entries excluded). An empty range prints the header only, exit 0.
- Text:
  ```
  Fri 2026-10-23
    - Added Ansible role for postgres cluster        [#42 Set up keycloak postgres cluster]
    - Created pgk cluster in dev                     [#42]
  Mon 2026-10-26
    - Failover tested in dev                         [#42]
    ✓ Set up keycloak postgres cluster               [#42]
  ```
  Day header `Mon 2006-01-02`; lines ordered by `time` (timed first) then
  `created_at`; `✓` for `complete`, ` (reopened)` suffix; first appearance of a
  task in the report shows `[#ID current title]`, later `[#ID]`; standalone
  entries have no bracket. Title column padded to the widest line (max 60 cols).
- Markdown: `## Mon 2026-10-26` headings, `- ` / `- ✓ ` bullets, task annotation
  as `(#42 Title)` then `(#42)`; user text escaped for markdown.
- `--todo` appends "Triage" and "Backlog" sections; `--upcoming` appends "Upcoming".

---

## 10. MCP (`internal/mcpserver`)

- `tkr mcp`: `mcp.NewServer(&mcp.Implementation{Name: "taskronin", Version: buildinfo.Version}, &mcp.ServerOptions{Instructions: …})`,
  tools registered with `mcp.AddTool`, run with `server.Run(ctx, &mcp.StdioTransport{})`.
- Token: `--token-file`, else `[mcp].token_file`. **Always its own token**:
  refuses to start if the file is missing, has bad permissions, or `GET /whoami`
  reports `owner: true` (D§9.4). Channel header `mcp`.
- Instructions text states that task titles/notes are user data, not
  instructions; not relied on as a control (D§10.5).
- Schemas: built with `jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: …})`
  mapping `civil.Date` → `{"type":"string","format":"date"}`, `civil.Minute` →
  `{"type":"string","pattern":"^([01]?\\d|2[0-3]):[0-5]\\d$"}`, `civil.EndMinute` →
  the same pattern plus `|^24:00$` (block ends, incl. outputs such as `today`), `api.Time` →
  `{"type":"string","format":"date-time"}`; `log_work.task_id` overridden to
  `{"type":["integer","null"]}` and kept in `required`. All input/output
  schemas are golden-tested (`testdata/golden/mcp_*.json`).
- Dates in inputs are datespec strings (§9.2), resolved against `GET /clock`
  in the tool's mode; outputs echo resolved ISO values.
- Mutating tools accept optional `expected_etag` (the `etag` field of an earlier read); if absent the tool reads
  the current record and uses its ETag (fetch-then-act). On 412 the tool returns
  an error with the current representation.
- Errors → `CallToolResult{IsError: true}` whose only content is a `TextContent`
  holding the JSON `{"code","detail","retry_after"}` (plus `current` on a 412);
  error results carry **no** `structuredContent`, because the official TypeScript
  SDK client validates `structuredContent` against the tool's `outputSchema` even
  when `isError` is true and throws, hiding the error from the model. `429` (server-enforced cap, §4.3 step 10)
  → "rate limit reached; retry after N s".
- User content is returned only inside structured fields (the SDK also mirrors
  structured output as JSON text content).

| Tool | Input (JSON) | Output | Scope | Annotations |
|---|---|---|---|---|
| `today` | `{}` | `api.Today` | read | readOnly, openWorld=false |
| `list_tasks` | `{view?: triage\|backlog\|upcoming\|done\|all = backlog, tag?, project?, limit? (1–200, 50), cursor?}` | `api.Page[api.Task]` | read | readOnly |
| `get_task` | `{id: int}` | `api.TaskDetail` | read | readOnly |
| `search` | `{query: string, kind?: task\|entry\|all, limit?}` | `api.Page[api.SearchHit]` | read | readOnly |
| `work_report` | `{from?: datespec (Past, default week start), to?: datespec (Past, default today), tag?, project?, include_todo?: bool}` | `api.Report` | read | readOnly |
| `add_task` | `{title, notes?, due?: datespec (Future), tags?: [string], project?, priority?: H\|M\|L}` | `api.OpResponse` | capture | destructive=false, idempotent=false; never accepts placement (always lands in triage) |
| `log_work` | `{task_id: int\|null (required), title, notes?, date?: datespec (Past, default today), time?: "HH:MM"}` | `api.OpResponse` | capture | destructive=false |
| `complete_task` | `{id, as_title?, date?: datespec (Past), log_title?, notes?, expected_etag?}` | `api.OpResponse` | write | destructive=false |
| `reopen_task` | `{id, expected_etag?}` | `api.OpResponse` | write | destructive=false (no `remove_entry`) |
| `defer_task` | `{id, until: datespec (Future) \| "none", expected_etag?}` | `api.OpResponse` | write | destructive=false |
| `set_due` | `{id, due: datespec (Future) \| "none", expected_etag?}` | `api.OpResponse` | write | destructive=false, idempotent=true |
| `block_time` | `{task_id, date?: datespec (Future, default today), start: "HH:MM", end: "HH:MM", expected_etag?}` | `api.OpResponse` | write | destructive=false |
| `update_task` | `{id, title?, notes?, tags_add?, tags_remove?, project?: string\|null, priority?: H\|M\|L\|null, expected_etag?}` | `api.OpResponse` | write | destructive=false |
| `place_task` | `{id, how: keep\|top\|bottom\|before\|after, ref_id?, expected_etag?}` | `api.OpResponse` | write | destructive=false |

Input structs live in `mcpserver/inputs.go` (`json` tags as above, `jsonschema`
tags = descriptions). Every write tool maps to exactly one API mutation (plus
optional reads), so every AI op appears in AI changes with per-op revert.

---

## 11. TUI and Web inventories

### 11.1 TUI (plan C, `internal/tui`)

Model: one `tea.Model` (`app`) with a tab bar, an optional mode (Plan, Recap,
Triage-card), and overlays (detail, history, search, AI changes, forms). Every
API call is a `tea.Cmd` returning a typed msg; the model never blocks.
Refresh: poll `GET /today` every 5 s with `If-None-Match` (§14 D25), plus an
immediate refetch of the visible tab after any own op. On 412: banner "changed
elsewhere — refreshed", no retry. Width math via Lip Gloss; all user text via
`textsafe.SanitizeLine` before styling.

| Screen | Data (API) | Actions (API) |
|---|---|---|
| Today tab | `GET /today` | `f` focus (`POST /focus`), `B` reblock passed block, `U` unblock, `l` log, `x` done focus task |
| Triage tab (list) | `GET /tasks?view=triage` | `enter` opens Triage-card mode on the selection |
| Backlog tab | `GET /tasks?view=backlog` | `J`/`K` reorder (`POST …/place before/after` neighbour), `x`, `B`, `d`, `f`, `e`, `D` drop |
| Upcoming tab | `GET /upcoming` | `d` change defer, `k` keep (undefer → backlog) |
| Log tab | `GET /entries?from=today-13&to=today` (paged) | `l` log, `e` edit entry (`PATCH`), `L` relink, `X` delete |
| Triage-card mode (`tkr triage -i`) | `GET /tasks?view=triage` one card at a time with reasons and slip counts | `k` keep · `t` top · `b` bottom · `p` place before/after N · `d` defer · `B` block · `x` done · `D` drop · `s` skip · `A` accept all `new` → bottom (`POST /triage/accept-new`) |
| Plan mode (`tkr plan`) | 1) triage cards; 2) backlog top 10 with today's free slots (`GET /blocks?from=today&to=today`); 3) summary | triage keys; `B` block today (`POST /tasks/{id}/block`); `enter` next step |
| Recap mode (`tkr recap [--date]`) | `GET /days/{date}` | per block: `w` worked / `m` missed / `a` auto (`POST …/outcome`), `x` done task; per unlinked entry: `L` link (task picker → relink); `l` log another entry for that date |
| Detail overlay | `GET /tasks/{id}` | `e` edit ($EDITOR), `t` tags, `h` history (`GET …/history`) |
| Search overlay | `GET /search` | `enter` → detail |
| AI changes overlay | `GET /ai-changes` | `R` revert op (`POST /ops/{op}/revert`), `A` mark reviewed |
| Global | — | `1`–`5`/`tab` tabs, `j`/`k` move (list views only), `a` add, `l` log, `/` search, `u` undo, `ctrl+r` redo, `?` help, `q` quit |

Triage keys apply only in card modes, so `k` (keep) never collides with `j/k`
navigation (§14 issue 12). The TUI uses the same implicit-link rule as the CLI
for `l` without a selected task; with a selected task it links to it.

### 11.2 Web (plan D, `internal/web`)

Server-rendered `html/template`; htmx 2.0.11 from `/static/`; no inline
script or style; config via
`<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"selfRequestsOnly":true,"includeIndicatorStyles":false,"historyCacheSize":0,"refreshOnHistoryMiss":true}'>`;
no `hx-on`; confirmations via `hx-confirm`. GET handlers perform no user ops
(reconcile system writes are allowed, D§5.8). Responsive CSS; move buttons, no
drag-and-drop. Markdown notes via `markdown.Render` (D§10.4).

| Route | Service calls |
|---|---|
| `GET /ui/login`, `POST /ui/login`, `POST /ui/logout` | `Login`, `Logout` |
| `GET /ui/` (Today) | `Today` |
| `GET /ui/triage`, `/ui/backlog` | `ListTasks` |
| `GET /ui/upcoming` | `Upcoming` |
| `GET /ui/log?from&to` | `ListEntries` |
| `GET /ui/tasks/{id}`, `/ui/tasks/{id}/history` | `GetTask`, `History` |
| `GET /ui/recap?date` | `Day` |
| `GET /ui/report?…`, `/ui/search?q` | `Report`, `Search` |
| `GET /ui/ai-changes` | `ListAIChanges` |
| `POST /ui/tasks` | `AddTask` |
| `POST /ui/tasks/{id}/{place,defer,due,block,triage,complete,reopen,drop,delete,undelete,edit,tags}` | the matching service op (form carries `etag` — plus `task_etag` for reblock, per-item `etag`s for accept-new — and `_gen`, `_nonce`, `_csrf`) |
| `POST /ui/triage/accept-new` | `AcceptNew` |
| `POST /ui/entries`, `/ui/entries/{id}/{edit,relink,delete,undelete}` | entry ops |
| `POST /ui/blocks/{id}/{reblock,unblock,outcome}` | block ops |
| `POST /ui/focus`, `/ui/undo`, `/ui/redo`, `/ui/ops/{op}/revert`, `/ui/ai-changes/review` | as named |
| `GET /static/htmx-2.0.11.min.js`, `/static/app.css` | embedded, `Cache-Control: public, max-age=31536000, immutable` |

---

## 12. Backup, restore, export, import, purge procedures

### 12.0 Shared machinery (`internal/backup`)

- **Serialization.** `backup.Manager.mu` serializes every snapshot and rotation
  in the server. Offline commands (restore, import, purge, scrub-backups,
  migrate, passwd --reset, offline backup) hold the instance lock, so no server
  runs concurrently.
- **Temporary artifacts (R2-6).** Every copy or temp file the code creates lives
  either in the staging dir `<data>/staging/` (0700; `Stage` copies, staged
  restore/import/init DBs) or, when it must be renamed onto a target on the same
  filesystem, next to that target with the prefix `.tkr-tmp-` (snapshot temps,
  scrub outputs, quarantine dirs). All are created exclusively with unique names
  (`os.CreateTemp`/`os.MkdirTemp`). Ones taking part in a swap are recorded in its
  journal (`New`, `Aside`). `CleanupTemps` — run at server startup and at the
  start of every offline command, under the lock — deletes everything in
  `<data>/staging/` and every `.tkr-tmp-*` in the data dir, backup dir and
  `*.pre-restore-*`/`*.pre-import-*` dirs, **except** paths referenced by an
  active journal (those are finished by `--resume`). Leftover copies may hold
  unscrubbed content, so they are removed, never kept.
- **Staging** — `Stage(ctx, set FileSet) (*Staged, error)`: copies a database
  file set (the DB plus its `-wal` if present) into a fresh dir under
  `<data>/staging/`, opens the copy with a **normal** connection (staging DSN) so
  SQLite recovers the WAL, sets `journal_mode=DELETE` (checkpoints and removes
  sidecars) and returns a single self-contained file. Never `immutable=1`.
- **Journaled swap** — replaces a database file set crash-safely:
  ```go
  type SwapKind string // "restore" | "import" | "scrub"
  type SwapJournal struct {
  	Version           int
  	Kind              SwapKind
  	Step              string // "prepared" | "moved-old" | "installed" | "aside-deleted"
  	New               string // staged replacement: one DELETE-mode file, verified, fsynced
  	Target            string // path of the DB being replaced; its -wal/-shm move with it
  	Aside             string // dir receiving the old set: <target>.pre-restore-<ts>/, .pre-import-<ts>/, or a .tkr-tmp- quarantine dir
  	DeleteAside       bool   // scrub: delete the old set after install (its content must not survive)
  	OwnerTokenPending bool   // restore only: a new owner token was minted (secret never journaled)
  	StartedAt         time.Time
  }
  func Swap(ctx context.Context, journalPath string, j *SwapJournal, onInstalled func() error) error
  	// idempotent per step; onInstalled (may be nil) runs after "installed", before the journal is removed
  func ResumeSwap(ctx context.Context, journalPath string) error
  func RollbackSwap(ctx context.Context, journalPath string) error           // restore/import only, before "aside-deleted"
  ```
  Steps: write journal (`prepared`, atomic, fsync dir) → mkdir `Aside`, move
  `Target`, `-wal`, `-shm` into it, fsync both dirs (`moved-old`) → rename `New`
  to `Target`, fsync dir (`installed`) → if `DeleteAside`: unlink the old set,
  fsync (`aside-deleted`) → remove the journal, fsync dir. Live-DB swaps
  (restore, import) journal at `<data>/swap.journal`; snapshot scrubs at
  `<backupdir>/scrub.journal`. `serve` refuses to start while either exists.
- **Ledger replay** — `service.ReplayLedger(ctx, recs, mode)`, one system op
  (`system.ledger_replay`):
  - `mode = live` (startup, restore/import staging): normal `run`, reconcile allowed.
  - `mode = historical` (snapshots and pre-restore/pre-import sets being scrubbed):
    `opSpec{noReconcile: true, structuralOnly: true}` — the copy keeps its
    point-in-time state; only content changes (R2-2).
  - task IDs: if not yet tombstoned, apply delete semantics first — cancel its
    `planned` blocks as `cancelled/task_closed` (no derivation, no slip), delete
    its triage rows, clear focus if it points at it; then tombstone+scrub the row;
  - entry IDs (incl. a task's `complete` entries): tombstone+scrub, even when the
    entry is the active completion entry of a done task (I4 accepts purged ones);
  - `store.ScrubEntities`: rows with `purged_at IS NULL` are scrubbed, each in
    **one** `UPDATE … SET deleted_at = COALESCE(deleted_at, :now), purged_at = :now,
    title = '[purged]', notes = ''` so the FTS trigger sees the old content; the
    task's `project` and `task_tags` are cleared and orphaned `tags` deleted.
    **Events are scrubbed content-aware, independent of `ops.purged` (R3-1):**
    every `task`/`task_tags`/`entry` event whose entity is in the set and whose
    payload still holds content — a `title` ≠ `"[purged]"`, a non-empty `notes`,
    a non-null `project` or a non-empty `tags`, tested on `before_json` and
    `after_json` with `COALESCE(json_extract(j,'$.title'),'[purged]') <> '[purged]'
    OR COALESCE(json_extract(j,'$.notes'),'') <> '' OR json_extract(j,'$.project')
    IS NOT NULL OR COALESCE(json_array_length(j,'$.tags'),0) > 0` — is rewritten (`title`→`"[purged]"`, `notes`→`""`, `project`→`null`,
    `tags`→`[]`). `ops.purged=1, revertible=0` is set for every op with an event
    on the set (revertibility only). If no row or event needs changing, the
    replay records no events and is a true no-op (no ops row, R2-12).
  - **Checkpoint (R3-2):** server startup (before listeners) and every offline
    command run `wal_checkpoint(TRUNCATE)` after ledger replay
    **unconditionally**, confirm truncation (`busy=0`, `log=0`), and fail loudly
    otherwise — a crash between a scrub commit and its checkpoint is thus always
    finished, even though the replay finds nothing left to change. Offline
    snapshots are taken only after that checkpoint; online snapshots use
    `VACUUM INTO`, which copies logical content only, never stale WAL frames or
    free pages. No pending flag or background worker exists.

### 12.1 Snapshot (`backup.Manager`)

```go
type Kind string // "daily" | "manual" | "premigrate" | "prescrub"
type SnapshotInfo struct { Name, Path string; Kind Kind; At time.Time; Size int64; SchemaVersion int }
type Retention struct { Daily, Weekly int } // floors 3 / 1
type Manager struct { Dir string; Clock clock.Clock; Snap *sql.DB; mu sync.Mutex }
func (m *Manager) Snapshot(ctx context.Context, kind Kind) (SnapshotInfo, error)
func Verify(ctx context.Context, path string) (schemaVersion int, err error)
func (m *Manager) Rotate(ctx context.Context, r Retention) (removed []string, err error)
func Inventory(ctx context.Context, recs []LedgerRecord, sets []FileSet) ([]InventoryItem, error) // offline only
func List(dir string) ([]SnapshotInfo, error)
type InventoryItem struct { Path string; Status string /* contains | clean */; Err string }
```
1. Under `m.mu`: name `taskronin-<YYYYMMDDTHHMMSS.mmmZ>-<4 hex>-<kind>.db`; temp via `os.CreateTemp(dir, ".tkr-tmp-*.db")` (exclusive; `VACUUM INTO` accepts an existing empty file).
2. `VACUUM INTO ?` (temp path bound; if the driver refuses a parameter there, a single-quoted literal with `'` doubled) on the snapshot pool; observes ctx (shutdown aborts and deletes the temp); `chmod 0600`.
3. `Verify`: staging connection on the temp file, `PRAGMA integrity_check` = `ok`, FTS content integrity-check on both tables, max `schema_migrations.version`; close; assert no sidecars.
4. fsync temp → rename to final → fsync dir.
5. `settings`: `last_backup_at`, `last_backup_attempt_at`, `last_backup_error=''` (no events).
6. Rotate (only after success): keep the newest snapshot per day for the last `Daily` days, newest per ISO week for the last `Weekly` weeks, everything < 24 h old, and the newest 3 overall; delete only files matching the name pattern. Never touches `*.pre-restore-*`/`*.pre-import-*`.
On failure: delete the temp, set `last_backup_error`, keep older snapshots; banner (D§7.3).
`offline.Backup` (server down): lock → refuse if any swap journal exists (R4-8) → `CleanupTemps` → `ReapplyLedger` (live) + confirmed checkpoint → the same steps with an offline writer.

### 12.2 Restore (`offline/restore.go`)

0. Acquire the lock (server stopped; the message names both units); refuse if a swap journal exists; `CleanupTemps`; confirm (`-y`).
1. `Stage` the snapshot file set (the original is only read, never opened read-write, R4-5).
2. `Verify` the staged copy; `schema ≤ binary`; `Migrate` it to the binary schema.
3. Open the current DB if possible (`mode=rw`, `quick_check`): read `auth_state`, `api_tokens`, sequences.
   Only a **missing** DB or a **genuinely damaged** one (SQLite `CORRUPT`/`NOTADB`, a failed
   `quick_check`, or a failed structural check: no `auth_state`/`api_tokens`/`sqlite_sequence` table or
   no `auth_state` row) counts as unreadable below. Any other failure (permission, I/O, busy, lock, …)
   fails the restore before anything changes, with the cause shown (B-I2).
4. In the staged copy: replace `auth_state` and `api_tokens` with the current ones; `RaiseSequences(max)`; new `db_generation`; `DELETE FROM web_sessions`; `DELETE FROM idempotency`; record a `system.restore` op + content-free event.
   Current DB unreadable: revoke every token, mint a new owner token, set `web_login_disabled=1`, sequences = max over snapshot and all readable snapshots + 10 000; print a loud notice.
5. Over the staged store (a temporary `service.Service`): `ReplayLedger(live)` → `Reconcile` (real clock) → `CheckAll`; `integrity_check`; FTS integrity-check; close; assert no sidecars; fsync.
   A damaged ledger does not refuse the restore, but prints a warning naming `tkr purge --repair-ledger`
   (a record on a damaged line is not replayed; B-M4).
6. `Swap(ctx, <data>/swap.journal, &SwapJournal{Kind: restore, Aside: <live>.pre-restore-<ts>/}, onInstalled)` where `onInstalled` writes `owner.token` if one was minted — it runs after `installed` and **before** the journal is removed (R4-10).
   If the swap fails or is interrupted after its journal was written, the command prints the
   `tkr restore --resume` / `tkr restore --rollback` hint (B-M3; import likewise).
- `tkr restore --resume` / `--rollback` drive `ResumeSwap`/`RollbackSwap` on `<data>/swap.journal` (also for an interrupted import). If `OwnerTokenPending` and the token file was not written, `--resume` tells the user to run `tkr passwd --reset`; if the check itself fails (e.g. an I/O error reading the file), `--resume` still completes and prints that hint with the cause (B-M12).

### 12.3 Migrate (`offline/migrate.go`)

Lock → refuse if any swap journal exists (R4-8) → `CleanupTemps` → open (`mode=rw`) → `SchemaStatus` (`--status` prints and
exits) → pre-migrate snapshot (§12.1) → `Migrate`. `serve` does the same at
startup (§4.10).

### 12.4 Export (`service.Export`, `client.Export`)

One reader transaction; streamed JSON:
```json
{"format":"taskronin-export","version":1,"schema_version":1,
 "exported_at":"2026-10-26T09:00:00.000-06:00","db_generation":"…",
 "sequences":{"tasks":57,"log_entries":210,"…":0},
 "settings":{"backup_keep_daily":"7","backup_keep_weekly":"4","ai_reviewed_through":"12"},
 "tables":{"tasks":[{"id":42,"title":"…","…":null}],"task_triage":[],"tags":[],"task_tags":[],
           "log_entries":[],"time_blocks":[],"focus":[],"interruptions":[],"ops":[],"events":[],
           "undo_stack":[]}}
```
Rows are column→value maps (`store.ExportTable`). Never exported: `auth_state`,
`api_tokens`, `web_sessions`, `idempotency`, `schema_migrations`, other
settings. The client writes `-o` via temp + fsync + rename, mode 0600.

### 12.5 Import (`offline/import.go`) — staged (S2)

1. Lock; `CleanupTemps`; no swap journal; the live DB must exist (`tkr init`) and hold no user data — no rows in `tasks`, `log_entries`, `time_blocks`, `tags`, `interruptions` (ops/events from `init --mcp-token` or `passwd --reset` are fine).
2. Parse the header: `format`, `version ≤ 1`, `schema_version ≤ binary`.
3. Create a staged DB under `<data>/staging/` (staging DSN `mode=rwc`); `Migrate`; copy `auth_state` and `api_tokens` from the live DB; **mint a new `db_generation`** (like restore, R4-3).
4. One transaction on the staged DB (R3-5 ordering):
   a. insert the file's rows in dependency order (`ops`, `tasks`, `tags`, `task_tags`, `task_triage`, `log_entries`, `time_blocks`, `interruptions`, `focus` (update id 1), `events`, `undo_stack`), **preserving** their `ops.seq`, `events.id` and every `*_seq` reference; only known columns (unknown → error); titles/notes re-validated with `textsafe`; triggers maintain FTS; settings allowlist;
   b. `RaiseSequences(max(file, live))` (R4-10);
   c. re-insert the live DB's bootstrap history (`token.*`/`auth.*` ops and their events, e.g. from `init --mcp-token` or `passwd --reset`) with **fresh** `seq`/`id` allocated by AUTOINCREMENT above the raised marks, preserving their `op_id`s (`undo_stack` and `events` reference `op_id`). An op whose `op_id` is already in the file is skipped after checking it is identical (kind, actor, channel, at, scopes, and its events); a difference → error (R4-4);
   d. `system.import` op with a content-free event (row counts).
5. Over the staged store: `ReplayLedger(live)` → `Reconcile` → `CheckAll`; `integrity_check`, `foreign_key_check`, FTS integrity-check; close; fsync. A damaged ledger prints the same warning as restore (B-M4).
6. `Swap{Kind: import, Aside: <live>.pre-import-<ts>/}`.
Any failure before step 6 leaves the live DB untouched; `CleanupTemps` removes the staging copy, so the import can simply be retried.

### 12.6 Purge — offline only (`offline/purge.go`, `service/purge.go`, `backup/ledger.go`; S4)

```go
type LedgerRecord struct {
	V       int              `json:"v"`
	Rec     string           `json:"rec"`   // random 128-bit hex
	At      int64            `json:"at"`
	State   string           `json:"state"` // "pending" | "complete"
	Tasks   []domain.TaskID  `json:"tasks,omitempty"`
	Entries []domain.EntryID `json:"entries,omitempty"`
	RecoveryFiles string     `json:"recovery_files,omitempty"` // complete lines only: "deleted" | "kept" (--keep-recovery-files) | "none" (H1)
	Sum     string           `json:"sum"`   // hex SHA-256 of this record's JSON encoding with Sum = ""
}
type LedgerHealth struct { Damaged bool; DroppedLines []int }
type Ledger struct{ Path string; mu sync.Mutex }
func OpenLedger(path string) (*Ledger, LedgerHealth, error)
	// Under the instance lock. A final line WITHOUT a terminating newline is a torn
	// append: truncated back to the end of the last complete line, fsynced, before any
	// append. A complete (newline-terminated) line that fails to parse or whose Sum is
	// wrong — anywhere in the file — is corruption: Damaged = true, the line is kept on
	// disk and skipped; all valid records still replay (R2-8).
func (l *Ledger) Append(r LedgerRecord) error     // refused while Damaged; O_APPEND 0600, one line, fsync file + dir
func (l *Ledger) Records() ([]LedgerRecord, error) // valid records only
func (l *Ledger) Repair() ([]int, error)          // rewrite from valid records (temp+fsync+rename+dir fsync); returns dropped line numbers
```
`tkr purge task|entry ID [--with-entries] [-y]` (owner, offline; D§5.11 as amended by S4):
1. Acquire the instance lock; if held: "the server is running — `systemctl --user stop taskronin.socket taskronin.service` first". Refuse if a swap journal exists (resume it first). `CleanupTemps`.
2. Prompt for the password (TTY only, refuse otherwise) and verify against `auth_state` with `auth.VerifyPassword` (1 s delay after a failure, 3 attempts).
3. `OpenLedger`; `Damaged` → refuse ("run `tkr purge --repair-ledger`").
4. Open the live DB (`mode=rw`) with a temporary `service.Service`; `ReplayLedger(live)` (completes any earlier intent) + confirmed checkpoint (R3-2).
5. `service.PurgeLive(ctx, m, in)` through `run` (actor `owner`, channel `cli`, op `purge.task`/`purge.entry`, not revertible, no idempotency), one writer transaction: validate the target (exists, tombstoned — else `conflict` "delete it first"; for a live entry of a deleted task the message says to use `tkr purge task <id> --with-entries`, which is how a deleted task's entries, incl. its completion entry, are purged); select the exact set (task + its `complete` entries + with `--with-entries` all linked entries; or the entry; report linked entries left out); **`Append` the `pending` record (fsync) while holding the writer** — skipped when an existing record already covers exactly this set and every row in it is already purged (a re-run); `ScrubEntities`; content-free `purge` events; commit. A failure after the append is completed by `ReplayLedger` (immediately, or by the next startup/offline command after a crash). A `pending` line can therefore outlive a rolled-back scrub; that is harmless — the next replay performs the scrub it describes.
6. `PRAGMA wal_checkpoint(TRUNCATE)`, confirmed — no other connection exists, so it truncates; otherwise fail loudly (the record stays `pending`; every later start or offline command checkpoints unconditionally, R3-2).
7. Scrub every snapshot and `*.pre-restore-*`/`*.pre-import-*` set exactly as `tkr scrub-backups` (§12.7, steps 4–6).
8. **Client recovery files (R4-1, H1)** — `offline.CleanRecoveryFiles(ctx, paths, keep, assumeYes, prompt) (mode string, err error)`, shared with §12.7: with `--keep-recovery-files` → mode `kept`; no files → `none`; otherwise list this host's `<state>/pending/` and `<state>/drafts/` files (request bodies and editor drafts may hold the purged text), show the counts, and delete them on confirmation (default yes; `-y` skips the prompt) → `deleted`. Answering "no" returns no mode: the records stay `pending` and the CLI says to re-run with `--keep-recovery-files` to complete without deleting. Purge itself never creates pending or draft files; recovery files on other hosts are out of reach (v1 is single-host).
9. **Completion** — only if step 7 left every set clean or rewritten, step 8 returned a mode, and the ledger is not damaged: `Append` `complete` lines for **all** still-`pending` records, each carrying `recovery_files = mode` (the durable record of the cleanup or opt-out). Otherwise leave them `pending` and tell the user to run `tkr scrub-backups`.
10. Print: scrubbed IDs, linked entries left alone, per-set results, and that earlier exports and log lines are out of reach.
- Re-running on an already-purged item changes nothing and appends no new record if one already covers the set.
- `tkr purge --repair-ledger` (lock + password): `Repair()`, prints the dropped lines **immediately** (before a replay or checkpoint that could fail; B-M5), then `ReplayLedger(live)` + checkpoint.
- `ReapplyLedger(ctx)` = `ReplayLedger(live)` over all valid records; it never writes `complete` lines (only purge step 9 and scrub-backups step 7 do, after the recovery-file step). Runs at server startup (then checkpoint before listeners) and in every staged copy. While a record is `pending` the dashboard shows a `purge_incomplete` banner; a damaged ledger shows `purge_ledger_damaged`.

### 12.7 Scrub backups — offline (`offline/scrub.go`)

`tkr scrub-backups [--check] [--resume] [--keep-recovery-files] [-y]` — the standalone and resumable form of purge steps 7–9:
1. Acquire the instance lock (server stopped); refuse if `<data>/swap.journal` exists (R4-8).
2. Prompt for the password (TTY only) and verify as in §12.6 — before touching any file.
   Then, with an existing `<backupdir>/scrub.journal`, `ResumeSwap` it first (always;
   `--resume` just makes it explicit); then `CleanupTemps`.
3. `OpenLedger` (a damaged ledger is allowed here: valid records are still applied, but `complete` lines cannot be appended while it is damaged — step 7 then tells the user to run `tkr purge --repair-ledger`); load records.
4. On the live DB: `ReplayLedger(live)` + confirmed checkpoint (R2-12, R3-2). Unless `--check`: take a fresh verified snapshot (kind `prescrub`).
5. For each other snapshot and each `*.pre-restore-*`/`*.pre-import-*` set: `Stage` it → `Migrate` the staged copy to the binary's schema (an old-schema snapshot must replay with current columns, R3-6) → `ReplayLedger(historical)` → nothing changed **and** the original set has no non-empty (or unstattable/non-regular) `-wal`/`-shm` (superseded WAL frames may hold purged text): `clean`, discard → `--check`: report `contains`, discard → else `CheckStructural`, `integrity_check`, FTS check → `VACUUM INTO` a `.tkr-tmp-` file next to the target → `Verify` → `Swap{Kind: scrub, Aside: <.tkr-tmp- quarantine dir>, DeleteAside: true}`.
6. Stop at the first failure that leaves a journal (the next run resumes that swap first, R2-7); failures without a journal are reported and the batch continues. Snapshots are never deleted, only rewritten.
7. If every set is clean or rewritten, the ledger is not damaged and `pending` records exist (including after `--resume`): run `offline.CleanRecoveryFiles` exactly as purge step 8 (same flags, confirmation and opt-out); only when it returns a mode, `Append` `complete` lines carrying `recovery_files = mode`. Print per-set results. Exit status: any `error` set → 1; `--check` with any `contains` set → 8 (§9.6); otherwise 0.

### 12.8 Offline password reset (`offline/passwd.go`)

`tkr passwd --reset [--password-stdin]`:
1. Acquire the lock (server stopped); refuse if any swap journal exists (R4-8); `CleanupTemps`; open the live DB (`mode=rw`).
2. Read the new password (twice on a TTY); `ValidateNewPassword`; mint a new owner token secret in memory.
3. One transaction through `run` as a system op (`auth.password`, actor `system`, channel `system`, not revertible): update `auth_state` (hash, `password_changed_at`, `web_login_disabled = 0`); revoke every token (`revoked_at = now`, `token:revoke` events); delete all web sessions; insert the new owner token row (`token:insert`); `auth:update` event (content-free).
4. Write `owner.token` with `fsutil.WriteFileAtomic` (temp, fsync, rename, fsync dir).
A crash between 3 and 4 leaves a file holding a revoked token; re-running
`tkr passwd --reset` recovers (the CLI says so on a write failure).

---

## 13. Testing architecture

### 13.1 Helpers (`internal/testutil`)

```go
var Zone civil.Zone // America/Denver (has DST transitions)
func Clock(t testing.TB, local string) *clock.Fake // "2026-10-26 09:00" in Zone
func Store(t testing.TB) *store.Store              // t.TempDir DB, migrated; Cleanup runs FTS
                                                   // content integrity-check + service.CheckAll, then closes
func Service(t testing.TB, opts ...SvcOpt) (*service.Service, *clock.Fake)
func Owner() domain.Actor
func TokenActor(name string, s domain.Scopes) domain.Actor
func Meta(a domain.Actor, gen domain.Generation) service.Meta // fresh random idempotency key + the store's generation
type Server struct {
	Client  *client.Client // owner token
	Clock   *clock.Fake
	Svc     *service.Service
	DataDir, Socket string
}
func StartServer(t testing.TB, opts ...ServerOpt) *Server
	// XDG_* → temp dirs; socket dir from os.MkdirTemp("", "tkr") (sun_path < 108 bytes);
	// offline.Init with password "correct horse battery"; server.Run in a goroutine with
	// the fake clock; waits for /healthz; t.Cleanup stops it.
func Golden(t testing.TB, name string, got []byte) // testdata/golden/<name>.golden; -update rewrites
type Scenario struct{ /* t, svc, clock */ }
func NewScenario(t testing.TB) *Scenario
	// Fluent steps used by service tests: At("2026-10-26 15:01"), Add(title) TaskID,
	// Block(id, "13:00-15:00"), Log(id, text, date), Done(id), Reconcile(),
	// Reasons(id) []domain.TriageReason, BlockOf(id) domain.TimeBlock, Undo(), Redo() …
```

### 13.2 Layers

| Layer | Where | What |
|---|---|---|
| Unit | `civil`, `datespec`, `textsafe`, `auth`, `api`, `editor`, `report` | tables + fuzz; DST gaps/overlaps; grammar; sanitizer properties; golden report output |
| Store | `store` | migrations (empty and every prior version), CHECKs and triggers (overlap, append-only, kind/link immutability, no-delete), FTS sync incl. purge, sequences, export rows, `ScrubEntities` |
| Service | `service` (bulk of tests) | every scenario in the behavior catalog (`2026-10-25-taskronin-behavior-catalog.md`, IDs kept as test names, e.g. `TestBLK27`) plus D§12's service list, as `Scenario` tables: triage reasons/order; reconcile across downtime, DST, midnight; derivation and recompute over before/after pairs; slip table row by row (incl. AI-log revert re-slip); continue rule; lifecycle effects; defer/block exclusivity; due/overdue incl. timed due and daily re-fire; rank incl. renormalization; revert of **every** op kind incl. invariant refusals and net-zero pairs; undo/redo/undo incl. committed pops and revert of a per-op revert; idempotency replay/mismatch incl. stored no-ops; rate cap; **reason added by reconcile → task ETag changes → stale placement gets 412** (R-i); tag change bumps the version; token `AddTask` with placement → 403; `PurgeLive` incl. crash between ledger append and commit (hook); domain-aware ledger replay onto an older store, and a historical replay that leaves blocks/focus/triage untouched; **revert of a backdated AI log after a placement re-slips the block (`slipped_seq` = revert seq) and re-adds `slipped`; with a second entry that day no block/triage event is written** (R2-1); eventless undo/redo still moves the stack and records an ops row (R2-4); a retried committed pop replays the same 409 and leaves the next entry untouched (R2-5); state chain T0/R/U/D after revert → undo → redo (R2-11); `Opt` `omitzero`: `{}` vs `{"project":null}` give different `Idem.Body` hashes and a partial PATCH leaves other fields alone (R2-3); `Undelete` clears a future defer; I4 with two purged completion entries; purged entities refuse undelete/update/relink while `Reopen` still flags a purged completion entry (R3-4); any mutation with a stale `Taskronin-Generation` → 412 `generation_mismatch`, incl. undo (R3-3); `MarkAIReviewed` bound excludes the current op's seq |
| Backup | `backup`, `offline` | snapshot/verify/rotate incl. concurrent snapshot requests (mutex, unique temps); `Stage` recovers a WAL-only change; restore and staged import round-trips incl. auth + sequence carry-over, unreadable current DB, historical focus (reconcile before `CheckAll`); crash at every swap step for restore, import and scrub (hooks in `export_test.go`) then `--resume`/`--rollback`; ledger: torn final line (no newline) truncated, complete-but-bad and mid-file lines → damaged (valid records still replay, new purges refused, `--repair-ledger` rewrites); offline `purge` end to end and crashed at each step (ledger append, live scrub commit, checkpoint, each backup swap, `complete` append) then rerun/resume; leftover `staging/` and `.tkr-tmp-*` files removed at startup and before scrub unless a journal references them; batch scrub stops on a failure that leaves a journal and the next run resumes it first; `scrub-backups` on a pre-restore set with a WAL and on a `schema_v1` fixture snapshot (migrated before replay, R3-6); **sequential purges leave no secret in any event: complete with a secret `--as` retitle → `reopen --remove-entry` → purge the entry → delete + purge the task** (R3-1); crash after the live scrub commit but before the checkpoint → next start/offline command truncates the WAL (R3-2); import into a DB initialized with `--mcp-token` keeps the file's seq/ids and renumbers the bootstrap history (R3-5); a pending request saved before a restore is refused (R3-3); init interrupted before publish; `passwd --reset` crash between commit and token write |
| API | `server` (via `StartServer`) | route-class invariant; socket peer-UID and `Forwarded`/`X-Forwarded-*`/`Via` refusal; owner token over TCP refused; scope matrix generated from `server.Routes()` (the same table the router uses); ETag/generation; idempotency; strict JSON; problem mapping for every `Code` |
| CLI | `cli` | golden tests running `cli.Main` in-process against `StartServer` (stdout, stderr, exit code); `log` arg table; unreachable echo; `status --short` |
| MCP | `mcpserver` | in-memory transports (`mcp.NewInMemoryTransports`) + SDK client; schema goldens; owner-token refusal; 429 mapping |
| TUI | `tui` (plan C) | `teatest` model tests per mode |
| Web | `web` (plan D) | CSRF, Origin/Sec-Fetch-Site, Host allowlist, headers, escaping, markdown sanitization, plain-loopback opt-in |
| Security | cross-cutting | no password in idempotency/ops/events/logs/pending files; token secrets never logged; file modes of DB, backups, exports, drafts, pending, token files; opening a missing DB fails (`mode=rw`); pragma-verification hook rejects a connection with a wrong pragma; htmx asset is 64-hex SHA-256 and matches |
| Lifecycle | `server` | shutdown waits for an in-flight op and a running backup (aborted via ctx) before releasing the lock; new ops during shutdown → `busy` |

Golden files: `<pkg>/testdata/golden/*.golden`, updated with
`go test ./internal/cli -update`; output normalized (op ids, request ids, times
from the fake clock are already deterministic).

### 13.3 Fuzz targets (run in `make fuzz`, seeds in `testdata/fuzz`)

| Target | Package | Property |
|---|---|---|
| `FuzzParse`, `FuzzParseSpan` | datespec | no panic; output round-trips via `String` |
| `FuzzParseDate`, `FuzzParseHHMM` | civil | strict round-trip |
| `FuzzInstant` | civil | `DateOf(Instant(d,m))` stable; no panic across DST |
| `FuzzParseLogArgs` | cli | no panic; never both `-N` and ID |
| `FuzzValidateTitle`, `FuzzValidateNotes` | textsafe | accepted output has no forbidden runes |
| `FuzzSanitize` | textsafe | output has no C0/C1/bidi/U+2028/9 except `\n`/`\t` |
| `FuzzRender` | markdown | no `<script`, no `on*=` attributes, only allowed schemes |
| `FuzzSplitWords` | editor | no panic; quoting round-trips |
| `FuzzParseToken` | auth | no panic; only canonical forms accepted |
| `FuzzParseETag`, `FuzzCursor` | api | no panic; round-trip |
| `FuzzDecodeRequests` | server | strict decoders never panic |
| `FuzzFTSQuery` | service | built query never errors in FTS5 (run against a temp DB) |
| `FuzzLedgerLines` | backup | arbitrary file contents: `OpenLedger` never panics, keeps every valid record, truncates only a final line lacking `\n`, flags any complete bad line as damaged |

### 13.4 Lint (`.golangci.yml`, golangci-lint v2)

```yaml
version: "2"
run:
  timeout: 5m
  go: "1.26"
  tests: true
linters:
  default: standard
  enable:
    - bodyclose
    - copyloopvar
    - depguard
    - durationcheck
    - errorlint
    - exhaustive
    - forbidigo
    - gocritic
    - gosec
    - misspell
    - nilerr
    - noctx
    - nolintlint
    - revive
    - rowserrcheck
    - sqlclosecheck
    - unconvert
    - usestdlibvars
  settings:
    depguard:
      rules:
        main:
          list-mode: lax
          deny:
            - pkg: "math/rand"
              desc: "use crypto/rand"
            - pkg: "log$"
              desc: "use log/slog"
            - pkg: "github.com/mattn/go-sqlite3"
              desc: "cgo driver; use modernc.org/sqlite"
            - pkg: "io/ioutil"
              desc: "deprecated"
    exhaustive:
      default-signifies-exhaustive: true
    forbidigo:
      analyze-types: true
      forbid:
        - pattern: '^time\.Now$'
          msg: "inject clock.Clock"
        - pattern: '^time\.Local$'
          msg: "use the configured civil.Zone"
        - pattern: '^os\.Exit$'
          msg: "return an error; only cmd/tkr/main.go exits"
        - pattern: '^(fmt\.Print(f|ln)?|print|println)$'
          msg: "write to an injected io.Writer"
    gosec:
      config:
        G301: "0700"
        G302: "0600"
        G306: "0600"
    nolintlint:
      require-explanation: true
      require-specific: true
  exclusions:
    generated: lax
    presets:
      - std-error-handling
    rules:
      - path: internal/clock/
        linters: [forbidigo]
        text: "time.Now"
      - path: cmd/tkr/main.go
        linters: [forbidigo]
        text: "os.Exit"
      # Tests deliberately use loose permissions, fixed temp paths and exec; gosec stays
      # strict for all non-test code (incl. internal/testutil).
      - path: '_test\.go'
        linters: [gosec]
formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - taskronin
```
Layering (§1.3) is enforced by `internal/archtest`, not depguard.

gosec policy for production code: G304 (reading a configured path), G204
(exec of `$EDITOR`/`systemctl`) and G302/G306 (the 0644 systemd unit files) are
legitimate only at specific call sites, and each such site is silenced
individually with `//nolint:gosec // <reason>` — `nolintlint` enforces a
specific linter and an explanation. No path- or rule-wide gosec exclusions exist
for non-test code.

### 13.5 Makefile

```make
MODULE      := $(shell go list -m)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS     := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
FUZZTIME    ?= 30s
PREFIX      ?= $(HOME)/.local

.PHONY: build install test fmt fmt-check vet lint vuln mod-verify assets-verify fuzz check rename-module

build:         ; CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/tkr ./cmd/tkr
install:       build ; install -D -m 0755 bin/tkr $(PREFIX)/bin/tkr
fmt:           ; golangci-lint fmt ./...
fmt-check:     ; @out=$$(gofmt -l $$(git ls-files '*.go')); test -z "$$out" || { echo "$$out"; exit 1; }
vet:           ; go vet ./...
lint:          ; golangci-lint run ./...
test:          ; go test -race -count=1 -timeout=30m ./...
vuln:          ; go run $(GOVULNCHECK) ./...
mod-verify:    ; go mod verify
assets-verify: ; @if [ -f internal/web/static/SHA256SUMS ]; then cd internal/web/static && sha256sum -c SHA256SUMS; fi
fuzz:          ; @scripts/fuzz.sh $(FUZZTIME)   # runs each §13.3 target for FUZZTIME
check: fmt-check vet lint test vuln mod-verify assets-verify
rename-module: ; @test -n "$(NEW)" && go mod edit -module $(NEW) && \
	grep -rl --include='*.go' '"$(MODULE)/' . | xargs sed -i 's#"$(MODULE)/#"$(NEW)/#g' && \
	sed -i 's#- $(MODULE)$$#- $(NEW)#' .golangci.yml
```
`make check` = fmt-check + vet + lint + `go test -race` + govulncheck (+ `go mod
verify` and the htmx hash check). Every implementation plan's Definition of
Done includes a green `make check`.

---

## 14. Design issues found, and decisions made

### 14.1 Design issues (defects or ambiguities) and resolutions (applied above)

| # | Issue | Resolution |
|---|---|---|
| 1 | D§7.4 puts actor/revertible on events only; revert needs op-level state (already reverted? reversal of what? scopes required), and the net-zero-pair rule is expensive to derive from events. | `ops` table (`seq`, `state`, `reverts_op_id`, `via`, `scopes`, `revertible`, `purged`); events reference it (§2.7, §4.6, §5). |
| 2 | D§7.6 uses FTS5 external content on the base tables while indexing only live rows. Verified on SQLite 3.53: the content-checking `integrity-check` then reports corruption, and `'rebuild'` would re-index tombstoned rows (including content awaiting purge). | External content = views of live rows (`tasks_fts_src`, `log_entries_fts_src`); triggers unchanged in spirit. Verified: integrity-check (rank 1) and rebuild both pass. |
| 3 | D§5.11 purge "never touches FTS" because targets are tombstoned — but a task's `complete` entries and `--with-entries` entries are usually **not** tombstoned, so their FTS rows would keep the secret. | Purge tombstones every member of the ID set first (trigger removes FTS rows with `secure-delete`), then scrubs (§12.6). |
| 4 | Placements addressed by a block URL (reblock) or in bulk (accept-new) cannot prove the task's triage reasons were seen using only the block's If-Match. | `ReblockRequest.task_etag` and per-item `etag` in `AcceptNewRequest`, both checked. |
| 5 | Continue rule (D§5.6): removal "to missed/done" is undefined when a task has several blocks. | Symmetric to the slip rule: `continue` stays while some block is `worked` with `resolved_seq > last_placed_seq`; `resolved_*` update on every status change (§4.5). |
| 6 | Reconcile "before every op" as a separate transaction leaves a window (e.g. midnight) between reconcile and op, and timestamp comparisons (`slipped_at > last_placed_at`) are ambiguous within a millisecond and wrong after clock corrections. | Reconcile runs inside the op's `BEGIN IMMEDIATE` transaction before a savepoint; one `now` per transaction; acknowledgement compares `ops.seq` values (§4.2, §4.3). |
| 7 | `VACUUM INTO` from the `query_only` reader pool may be refused. | Dedicated snapshot pool without `query_only` (§4.1). |
| 8 | MCP's per-token mutation cap (D§9.4) enforced inside `tkr mcp` is bypassable by calling the socket with the same token. | Server-enforced `api_tokens.mutations_per_hour`, counted from `ops` in the op transaction (§4.3 step 10). |
| 9 | Owner token is "named owner" and names are unique, but `passwd --reset` must mint a new owner token. | Owner tokens named `owner.<ts>` (CHECK), at most one unrevoked; actor key always `owner`; token names unique forever so `token:<name>` in history stays unambiguous. |
| 10 | `WriteTimeout` 60 s (D§10.2) vs backup, scrub, export over the API. | Per-route deadline extension via `ResponseController` (§6.1). |
| 11 | htmx defaults conflict with D§10.4: indicator styles inject inline `<style>` (blocked by `style-src 'self'`); the history cache stores rendered pages (task text) in `localStorage`. | `includeIndicatorStyles:false`, `historyCacheSize:0`, `refreshOnHistoryMiss:true` added to the D§9.3 config; htmx pinned to 2.0.11 (4.x renamed config). |
| 12 | Key collision: D§5.5 `k` = keep vs D§9.2 `j/k` = navigate. | Triage keys apply only in one-task-at-a-time card modes (Triage-card, Plan); lists use `j/k` (§11.1). |
| 13 | Opening a WAL-mode snapshot or restore temp copy for verification creates `-wal`/`-shm` sidecars next to it. | Staging connections (copies only) use `journal_mode=DELETE` and assert no sidecars remain (§4.1, §12.0). |
| 14 | Restore keeps the snapshot's idempotency rows; a client retry after restore could match a stale row. | Restore clears `idempotency` (and sessions) and records a `system.restore` op (§12.2). |
| 15 | "AI changes since last review" needs a review action, but GET handlers must not perform user ops (D§9.3). | Explicit `POST /ai-changes/review` (`tkr ai-changes --ack`, TUI `A`, web button). |
| 16 | "Cookie-only POST to /api/v1 → 401" vs v1 TCP not serving the API. | TCP returns 401 for `/api/v1/*`; the API lives only on the socket in v1. |
| 17 | Token creation returns a one-time secret, but idempotent replays return only current representations (no stored bodies). | `CreateToken` is idempotency-exempt; a retried create fails on the unique name and the user revokes. |
| 18 | "Implicit linking only in interactive CLI" is undefined for scripts. | Interactive = stdin is a TTY; otherwise ID or `-N` required (§9.4). |
| 19 | Restore with an unreadable current DB mints an owner token whose secret is lost if the swap crashes before writing `owner.token`. | Journal records `OwnerTokenPending`; `--resume` instructs `tkr passwd --reset` (§12.2). |
| 20 | MCP SDK validates structured output against inferred schemas; custom-marshalled types (`civil.Date`, `api.Time`) would infer as objects and fail validation. | Explicit `TypeSchemas` and golden-tested schemas (§10). |

### 14.2 Decisions made (no user input needed)

| # | Decision |
|---|---|
| D1 | Instants are `INTEGER` unix ms; dates `TEXT` (strftime-validated); minutes `INTEGER`; all tables `STRICT`; connections use `defensive`, `dqs=0`, `trusted_schema=OFF`. |
| D2 | Focus is a versioned, evented singleton table, not `settings` rows. |
| D3 | `POST /focus` needs no If-Match (a pointer, not a record edit); it is idempotency-keyed. |
| D4 | `triage --manual` on a deferred task clears the defer (it would otherwise be invisible in triage). |
| D5 | `place` (keep/top/bottom/before/after) on a deferred task clears the defer; `defer ID none` = `place keep`. |
| D6 | `done` and `drop` are placements (D§5.5 lists `x` and `D`); `delete` is not but clears reasons (D§5.9). |
| D7 | Continue removal rule — see issue 5. |
| D8 | A block's end instant must be after now; its start may be in the past (block "13:00–15:00" at 13:30). |
| D9 | `--at` on today's date cannot be in the future. |
| D10 | Status transitions: open→done, open→dropped, done→open, dropped→open; others → 409. |
| D11 | Interruptions are rows (tombstoned by revert), not just events. |
| D12 | `tkr report` defaults to the full current week; `--week`/`--last-week`/`--month` are full calendar periods; days ascending; `-t`/`-P` exclude standalone entries. |
| D13 | v1 API is socket-only; TCP serves the web UI only. |
| D14 | "AI changes" = ops by any non-owner token, plus retention-lowering settings changes; reviewed explicitly. |
| D15 | Tokens may revert only their own ops; the owner may revert any revertible op. |
| D16 | CLI, TUI and web share the owner's undo/redo stacks; depth 1000. |
| D17 | Backup retention lives in DB settings (password-gated lowering, audited); everything else in TOML. |
| D18 | Notes: CRLF → LF before validation; titles trimmed. |
| D19 | `status --short` prints nothing and exits 0 on any error or when there is nothing to show. |
| D20 | `tkr log 42` without `-e` is a usage error. |
| D21 | Display prefixes `#42`, `b7`, `e9`; CLI accepts bare or prefixed numbers where the kind is known. |
| D22 | Own `LISTEN_FDS`/`sd_notify`/shell-words code; `time/tzdata` embedded; `x/ansi`, `x/term`, `x/time` direct deps. |
| D23 | MCP dates are datespec strings; `expected_etag` optional (else fetch-then-act). |
| D24 | Snapshot naming/kinds and rotation (newest 3 and anything < 24 h always kept). |
| D25 | TUI polls `/today` every 5 s with `If-None-Match`. |
| D26 | `tkr backup` falls back to an offline snapshot when the server is down. |
| D27 | Passwords 8–1024 bytes; no other composition rules. |
| D28 | Web listens on `127.0.0.1:7878` by default. |
| D29 | The owner token cannot be revoked online; rotate with `passwd --reset`. |
| D30 | `undelete` adds `manual` only when the task is open; no new entries/blocks/focus on deleted tasks (`deleted`). |
| D31 | `import` targets a freshly `init`ed DB with no user data. |
| D32 | Only `notes` of a `complete` entry are editable. |
| D33 | Owner token over the socket with channel `mcp` is refused (defence in depth; `tkr mcp` also refuses it). |
| D34 | `offline` may build a temporary `service.Service` over a staged/offline store (ledger replay, reconcile, `CheckAll`, system ops) — edge `offline → service` in §1.3. |
| D35 | Interruptions (former round-2 Q2, accepted as an engineering call; catalog A22): `tkr add … --now` records an interruption against the current block's task if a block of another task is current, else against the previous focus (if set and different), else none. `tkr now ID` (`SetFocus`) records one only when a **different task's block is current**, or with `--interrupt` (then against the current block's task, else the previous focus). Switching to the current block's own task never records one. |
| D36 | Design deltas (vs. the design text), all intentional: `ops` table holds actor/channel/at/revertible/state (events reference it); `focus`, `interruptions`, `auth_state` tables (password hash and idempotency key out of `settings`); FTS external content via live-row views; `*_seq` ordering columns and `purged_at`; retention in DB settings; v1 API on the socket only; backup scrubbing is the offline `tkr scrub-backups`, not `purge --scrub-backups`, and purge itself is offline (D37; D§5.11); restore journal generalized to a swap journal also used by staged import and scrub; extra commands/endpoints (`tkr pending`, `outcome`, `entry …`, `entries`, `priority`, `revert`, `GET /upcoming`, `GET /days/{date}`, `/redo`, `/whoami`, `POST /ai-changes/review`); owner tokens named `owner.<ts>`. |
| D37 | **Purge is an offline command (S4; design D§5.11 described an API op).** `tkr purge` needs the server stopped (instance lock) and the password on a TTY, then in one run appends the ledger intent, scrubs the live DB, checkpoints the WAL (guaranteed offline), scrubs every snapshot and pre-restore/pre-import set via the journaled swap, and marks the intent complete. Reason: purge is a rare emergency tool; running it online required a checkpoint-pending flag, background worker, long request budgets and shutdown accounting that reviews kept finding races in. Online API/web/MCP purge is removed; `destroy` still covers drop/delete/entry edits. |

### 14.3 Behavior-catalog rulings (catalog §16, A1–A24) as applied

| # | Ruling | Where |
|---|---|---|
| A1 | `AcceptNew` takes only tasks whose reason set is exactly `{new}`; superseded by C6: all-or-nothing, a non-`{new}` item → `conflict`. | §3.2 |
| A2 | Triage tiebreak: the highest reason's age ascending (oldest first) — compared by `seq` (S3), then id. | §3.7 |
| A3 | `Undelete` of a done/dropped task adds no reason. | §3.2 |
| A4 | Block whose end ≤ now → `validation`; start in the past allowed. | §3.3 |
| A5 | Block start or end at a nonexistent DST wall time (incl. end at the gap start) → `validation`. | §3.3 |
| A6 | "Current block" uses instant semantics in the repeated fall-back hour. | §2.1 |
| A7 | Reblock of a resolved or cancelled block creates a new block and leaves the old one alone. | §3.3 |
| A8 | A recap `worked` outcome adds `continue` like a re-derive. | §3.3 |
| A9 | Recap outcome allowed on today's started `planned` blocks (resolves now, `user`). | §3.3 |
| A10 | `done` on a dropped task → `conflict` (reopen first). | §3.2, D10 |
| A11 | `due` and `overdue` may coexist; kept. | §4.4 |
| A12 | Due moment ≤ now → `validation` (`today` date-only is fine). Defer moment ≤ now → `validation` likewise. | §3.2 |
| A13 | Due-soon: `due_date − today ≤ due_soon_days`. | §2.6 `DueSoon` |
| A14 | After `revert_conflict` the op stays on the undo stack. | §4.6 |
| A15 | Superseded by C1: focus events are ignored in revert conflict checks; revert restores the focus before-image only if it satisfies I6, else leaves focus unchanged (and clears a focus the revert itself made invalid); `focus.set` is revertible. | §3.5, §4.6 |
| A16 | Undo whose top op was already reverted → `revert_conflict` "already reverted by …", entry popped. | §4.6 |
| A17 | Idempotency-Key required: missing → `idempotency_key_required` (428), malformed → `malformed` (400) (lead ruling, round 1). | §2.8, §4.3, §6.3 |
| A18 | `AddTask` with a placement needs capture + write; task `Undelete` is `write`. | §3.2, §7.3 |
| A19 | Bare `HH:MM` = today at that time (future-direction commands then reject it if ≤ now). | §9.2 |
| A20 | `--at` at a nonexistent DST wall time is accepted (display/sort only). | §3.4 |
| A21 | `-P X` matches `X` and dotted descendants `X.*` on a dot boundary; standalone entries excluded under tag/project filters. | §3.7, §9.7 |
| A22 | Interruption rule = D35. | §3.5, D35 |
| A23 | `reopen --remove-entry` may re-derive the completion day's block to `missed` (slip + `slipped`); kept; CLI echoes it. | §3.2, §9.3 |
| A24 | `defer ID none`: clears the defer, is a placement, keeps rank (service `Defer{Until: nil}`, `POST …/defer {"until": null}`, CLI `tkr defer ID none`, MCP `defer_task {until: "none"}`). | §3.2, §6.4, §9.3, §10 |

### 14.4 Review round 1 changes

Reviews: Fable (3 HIGH), Codex (9 HIGH), agy (3 HIGH); fixes per the lead's rulings.

| Ref | Finding (source) | Fix (where) |
|---|---|---|
| S1 | Backup scrub not atomic with WAL/SHM; inventory via `immutable=1` misses WAL content; snapshot ops collide (codex, agy) | Scrubbing is offline only (`tkr scrub-backups`); every inspected set goes through `Stage` (normal open, WAL recovered, never `immutable`); replacements use the journaled swap with `DeleteAside`; one manager mutex + exclusive unique temp names (§12.0, §12.1, §12.7, §9.3) |
| S2 | Import has no coherent transaction boundary (codex) | Import stages into a temp DB, replays the ledger, reconciles, validates, then installs via the swap; failure leaves the live DB untouched (§12.5) |
| S3 | Logical op time changes the wall clock; "strictly increasing op times" wording (codex, Fable) | Real injected clock for all time logic; `ops.seq` for ordering/acknowledgement (`last_placed_seq`, `slipped_seq`, `resolved_seq`, `task_triage.seq`) (§4.2, §4.5, §5.2) |
| C1 | Focus vs revert | Focus ignored in conflict checks; before-image restored only if I6 holds; `focus.set` revertible again (§3.5, §4.6) |
| C2 | Block/SetFocus on ineligible tasks | done/dropped → `conflict`, deleted → `deleted`; SetFocus also refuses deferred; `Block` on a deferred task stays allowed because blocking clears the defer (D§5.3, catalog BLK-09) (§3.3, §3.5) |
| C6 | AcceptNew semantics | All-or-nothing: non-`{new}` item → `conflict`, stale item → `precondition_failed` (§3.2) |
| Idem | Missing/malformed Idempotency-Key | `idempotency_key_required` (428) / `malformed` (400) (§2.8, §4.3, §6.3) |
| R-a | Tokens can acknowledge AI changes (codex HIGH) | `MarkAIReviewed` owner only; `Through` ≤ latest committed seq (§3.6, §6.4, §7.3) |
| R-b | AI-created tasks can bypass triage (codex) | Creation-time placement from an `ActorToken` → `forbidden` (§3.2, §6.4, §7.3) |
| R-c | Purge selects its set outside the writer tx; checkpoint-pending crash window (codex HIGH ×2) | One writer tx from target validation through ledger append+fsync, scrub and `purge_checkpoint_pending=1`; cleared only after confirmed truncation (§12.6, §4.10) |
| R-d | Torn ledger tail breaks later appends (codex HIGH) | Per-record SHA-256; `OpenLedger` truncates an invalid final line and fsyncs before appending; mid-file corruption refuses (§12.6) |
| R-e | Ledger replay breaks invariants on older stores (codex HIGH) | Domain-aware `ReplayLedger` (delete semantics first; purged completion entries allowed by I4 via `purged_at`) (§12.0, §4.7) |
| R-f | Restore/import `CheckAll` fails on time-dependent invariants (codex, Fable) | Reconcile the staged copy before `CheckAll`; `CheckStructural` / `CheckTimeDependent` split; snapshot scrub uses structural only (§4.7, §12.2, §12.5) |
| R-g | Shutdown can outlive its drain and drop the lock (codex HIGH) | In-flight WaitGroup over ops, jobs and backups; 15 s op budget; drain ≤ 20 s; lock held until all return; `TimeoutStopSec=45s` (§4.10, §4.11, §8.6) |
| R-h | Revert push rule vs runner; committed pops rolled back (Fable HIGH ×2, codex HIGH) | Explicit forward/revert/undo/redo result paths; `committedError` commits the pop with no ops row/event (§4.3, §4.6) |
| R-i | Version bumps for triage/tag changes; reconcile finalization before If-Match (Fable HIGH, codex) | Triage/tags changes mark the task dirty; reconcile finalizes its versions before the user savepoint; test added (§4.3, §4.4, §13.2) |
| R-j | No-ops bypass idempotency (codex, Fable LOW) | No-ops store a row (`op_id` NULL, 200); `idempotency.op_id` nullable (§4.3, §4.9, §5.2) |
| R-k | Normal opens can create a DB (codex) | Escaped `file:` URIs with `mode=rw`; `mode=rwc` only for init/staging (§4.1) |
| R-l | Init and `passwd --reset` publication (codex, Fable) | Init stages the DB and publishes it last after `owner.token`; §12.8 reset procedure (§8.5, §12.8) |
| R-m | Inconsistent client/server deadlines; "nothing was saved" after a committed op (codex ×2) | Budget table; client default 20 s; not-sent vs outcome-unknown; pending requests resent with the same key via `tkr pending` (§4.11, §8.3, §9.6) |
| R-n | `add --block` skips Block validation (agy HIGH) | Shared `o.createBlock` helper (§3.2, §4.5) |
| R-o | Trigger RAISE surfaces as 500; revert overlap; SetOutcome{auto} resets (agy HIGH, Fable) | `store.MapErr` maps `taskronin:` RAISEs to `conflict`; revert pre-checks overlap; `auto` → planned resets `outcome_source`/`resolved_*`/`slipped` with the unslip hook (§4.1, §4.5, §4.6) |
| R-p | Scrub-then-tombstone could leave secrets in FTS (agy HIGH) | One `UPDATE` per row sets `deleted_at`, `purged_at`, title, notes together (§12.0) |
| R-q | modernc DSN keys (agy) | The underscore keys are accepted (verified in modernc source and by Fable on v1.60.1 — agy's claim is incorrect); added a connection hook that verifies every pragma (§4.1) |
| R-r | Remaining MEDIUM/LOW | `defer none` never touches blocks (§3.2); `duePhase` defined (§4.2); `web` may import `datespec`, renders `service.Report` (§1.3, §3.7); key-required fields are `Opt[T]` with `!Set` rejected (§6.5); triage conflict key per `(task, reason)` (§4.6); `slipOnCancel` false for `task_closed` (§4.5); staging DSN only for copies (§4.1); revert scopes resolved before the tx (§4.3); host allowlist from `web.listen` (§6.2); `completed_at = now` when backdated (§3.2); `AIChanges` shape (§3.6); `undo_stack.seq` defined (§4.6); `/clock` any scope (§3.7, §6.4); `tz_last_seen` UPDATE (§4.10, §8.5); design deltas listed (D36); list orderings (§3.7); htmx hash re-verified as 64 hex (agy's 65 was a miscount) with a length+value test (§13.2); `ParseScopes("")` (§2.5); I7 per-task check (§4.7); login CSRF exemption (§6.2, §7.5); full ETags in web forms (§7.5, §11.2); `Today.TriageTop` and `GET /upcoming` (§3.7, §6.4, §6.5); `civil.EndMinute` allows `24:00` in MCP schemas (§2.2, §6.5, §10); bounded `from`/`to` (§3.7, §6.4); `EntryView` (§2.6) |

### 14.5 Review round 2 changes

Round 2: Fable no CRITICAL/HIGH (3 MEDIUM, 4 LOW); agy 3 HIGH; Codex 5 HIGH. C2 pushback and the R-j superset were accepted.

| Ref | Finding (source) | Fix (where) |
|---|---|---|
| S4 | Online purge machinery: checkpoint-flag race, purge outliving shutdown, post-commit phases outside the drain (Codex H6/H9, Fable) | Purge is offline only (D37): lock + password; ledger intent → live scrub → guaranteed checkpoint → backup scrub via swap → `complete`. Removed: purge endpoints, `PurgeRequest`/`PurgeResult`, `purge_checkpoint_pending` and its worker, purge budgets and shutdown accounting, web purge (§3.8, §4.9–4.11, §5.2, §6, §7.3, §9.3, §11.2, §12.6) |
| R2-1 | Reverting an entry op restores derived block state, so the block never re-slips (agy HIGH) | `events.derived`; revert skips derived events and re-derives through normal `setBlock`; tests incl. the second-entry case (§2.7, §4.5, §4.6, §5.2, §13.2) |
| R2-2 | Ledger replay on historical copies reconciles them to today (agy HIGH, Codex) | `ReplayLedger(historical)`: `opSpec{noReconcile, structuralOnly}`; restore/import keep `live` + reconcile (§4.3, §12.0, §12.7) |
| R2-3 | `Opt[T]` without `omitzero` breaks partial PATCH and `Idem.Body` (agy HIGH) | `,omitzero` on optional `Opt` fields; `IsZero`/`MarshalJSON`/`UnmarshalJSON` defined; canonicalization test (§6.5) |
| R2-4 | Eventless undo leaves the stack stuck (Codex HIGH) | Revert/undo/redo with no domain events still finalize state and stack movement and keep the ops row (§4.3 step 14) |
| R2-5 | Retried committed pop can undo another op; undo replay order (Codex HIGH + MEDIUM, Fable LOW) | Idempotency lookup before target selection/authz; committed pops store their terminal 409 error (`idempotency.error_code`/`error_detail`) (§4.3 steps 1/6/7/17, §4.9, §5.2) |
| R2-6 | Interrupted staging leaves secret copies outside scrubbing (Codex HIGH) | One staging dir + `.tkr-tmp-` prefix; journal-referenced; `CleanupTemps` at startup and every offline command (§12.0, §8.1) |
| R2-7 | Batch scrub can overwrite an outstanding journal (Codex) | Stop at the first failure that leaves a journal; next run resumes it first (§12.7) |
| R2-8 | Ledger corruption handling (Codex, Fable) | Torn (no newline) → truncate; complete bad line anywhere → damaged: replay valid records, banner, refuse new purges, `tkr purge --repair-ledger` (§12.6, §4.10) |
| R2-9 | Triage payloads lack `seq` (Codex, agy) | `seq` in triage add/remove payloads, restored verbatim (§2.7, §4.6) |
| R2-10 | Pending requests outlive deduplication (Codex) | `tkr pending retry` refuses requests older than 7 days (§9.6) |
| R2-11 | `ops.state` wrong after redo of an undone revert (Fable) | State chain walked transitively down `reverts_op_id`; T0/R/U/D test (§4.6 step 7, §13.2) |
| R2-12 | agy MEDIUMs | `ScrubEntities` filters unscrubbed rows and is a true no-op otherwise; offline backup/scrub replay the ledger on the live DB first; `Redo` pops non-revertible targets/reversals; `worked` hook requires not deleted; `Undelete` clears a future defer; I4 accepts ≥ 1 purged completion entries and `Reopen{RemoveEntry}` sets `reopened=1` (§3.2, §4.5, §4.6, §4.7, §12) |
| R2-13 | LOWs | ops INSERT carries `via`+`reverts_op_id` together; CLI exit codes for 400/428/413/415; the step-11 re-read is gone (selection is inside the tx); owner token names with ms + random suffix; `ftsQuery` bare `*`; import emptiness on user-data tables only; web `Origin` derived from the host allowlist; read-path `Reconcile` registers in the WaitGroup (§4.3, §5.3, §7.2, §7.5, §9.6, §12.5) |

### 14.6 Review round 3 changes

Round 3: Fable and agy no CRITICAL/HIGH; Codex 3 HIGH (two overlapping Fable/agy MEDIUMs).

| Ref | Finding (source) | Fix (where) |
|---|---|---|
| R3-1 | Sequential purges leave secrets in events because scrubbing skipped ops already marked `purged` (Codex HIGH, agy) | Event scrubbing is content-aware per event (payload check), independent of `ops.purged`; regression test (§12.0, §13.2) |
| R3-2 | Restart can skip the WAL truncation of a committed scrub (Codex HIGH, Fable, agy) | Startup and every offline command checkpoint (TRUNCATE) unconditionally after replay and confirm it; offline snapshots only after; online snapshots are logical copies (§4.10, §12.0, §12.1, §12.6, §12.7) |
| R3-3 | A pending undo can hit unrelated work after a restore (Codex HIGH) | Every mutation carries `Taskronin-Generation` (web `_gen`); mismatch → 412 `generation_mismatch`; `tkr pending` stores the generation and refuses across a change; `/clock` and `/whoami` return it (§2.8, §3.1, §3.9, §4.3, §6.3, §6.5, §7.5, §9.1, §9.6) |
| R3-4 | Purged records can become mutable again (Codex, agy) | Undelete/update/relink of purged entities → `conflict`; `Reopen` may still set `reopened=1` on a purged completion entry without touching content (§3.2, §3.4, §3.9) |
| R3-5 | Import collides with bootstrap history (Codex, Fable, agy) | File ops/events inserted first with their seq/ids; the destination's `token.*`/`auth.*` history re-inserted above the high-water mark, op_ids preserved (§12.5) |
| R3-6 | Historical scrub never migrates the staged copy (Fable) | `Migrate` the staged copy before `ReplayLedger(historical)`; `schema_v1` fixture test (§12.7) |
| R3-7 | LOWs/MEDIUMs | Damaged ledger blocks `complete` lines and the CLI points to `--repair-ledger`; committed pops store the full problem as `error_json` (conflicts replay too); a purge re-run skips the redundant ledger append; deleted-task entries are purged via `purge task --with-entries` (hint in the entry error); a `pending` line outliving a rolled-back scrub is harmless; empty-stack codes defined once in §3.9; init stages under `<data>/staging/`; `AcceptNew` rejects empty/duplicate items; `MarkAIReviewed` bound uses `seq < o.rec.Seq` (§3.2, §3.6, §3.9, §4.9, §5.2, §8.5, §12.6, §12.7) |

### 14.7 Post-review additions

| Ref | Gap (source) | Addition (where) |
|---|---|---|
| P-1 | `client` methods and client-side helper signatures were undefined (TUI planner) | §6.7: `Client` options/constructor, generation caching, `Mut` (If-Match + key), one method per §6.4 endpoint (incl. `Today`/`Status` with If-None-Match), `ProblemError`/`ErrNotSent`/`ErrOutcomeUnknown`/`ErrGenerationMismatch`, `PendingStore`/`Resend`; `textsafe`, `editor`, `config` signatures; `client` and `editor` may import `clock` (§1.3) |

### 14.8 Review round 4 changes

Round 4: agy no CRITICAL/HIGH; Codex 1 HIGH. Every changed signature, field and procedure, for patching plans:

| Ref | Change | Where |
|---|---|---|
| R4-1 | **Procedure:** `tkr purge` gains step 8a — list this host's `<state>/pending/` and `<state>/drafts/` files, show counts, delete on confirmation (default yes). **New flag** `tkr purge --keep-recovery-files`. Purge never writes pending/draft files. | §12.6, §9.3 |
| R4-2 | **Field** `client.PendingRequest.Actor string` (WhoAmI.Actor at save time). **New method** `(*Client).Actor(ctx) (string, error)` (cached with the generation). **New error** `client.ErrActorMismatch`; `Resend` refuses a different actor. Pending file JSON gains `actor`. | §6.7, §9.6 |
| R4-3 | **Procedure:** import mints a new `db_generation` (no longer copied from the live DB). | §12.5 step 3 |
| R4-4 | **Procedure:** import step 4c skips a bootstrap op whose `op_id` is already in the file after an equality check (kind, actor, channel, at, scopes, events); mismatch → error. | §12.5 |
| R4-5 | **Procedure:** restore stages the snapshot file set first, then verifies/migrates the staged copy; the original is never opened read-write. | §12.2 steps 1–2 |
| R4-6 | **Procedure:** `revertTx` step 6 runs the slipped/continue removal checks for tasks whose restored block left `slipped=1` or `status='worked'`. | §4.6 |
| R4-7 | **Fields** `domain.BlockView.TaskVersion Version`; `api.Block.TaskETag string` (`json:"task_etag"`). | §2.6, §6.5 |
| R4-8 | **Procedure:** `migrate`, `passwd --reset`, offline `backup` refuse when any swap journal exists; `scrub-backups` refuses on `<data>/swap.journal`, verifies the password **before** `ResumeSwap`/`CleanupTemps`. | §12.1, §12.3, §12.7, §12.8 |
| R4-9 | **Rule:** `Reopen` updates the non-deleted `complete` entry with `reopened=0`, else the newest purged one (`ORDER BY id DESC LIMIT 1`). | §3.2 |
| R4-10 | **Signature** `backup.Swap(ctx, journalPath, j, onInstalled func() error) error` (import/scrub pass `nil`; restore writes `owner.token` in `onInstalled`, before the journal is removed). **Move:** export file format types from `internal/api/export.go` to `internal/store/export.go` (`api` no longer has `export.go`). **Procedure:** import calls `RaiseSequences` between inserting the file rows (4a) and the bootstrap history (now 4c, AUTOINCREMENT allocates); purge step 8 appends `complete` for all still-pending records when every set is clean (same rule as scrub-backups). | §12.0, §12.2, §12.5, §12.6, §1.2 |

### 14.9 Review round 5 changes

Final round: agy no CRITICAL/HIGH; Codex 1 HIGH + 1 MEDIUM.

| Ref | Change | Where |
|---|---|---|
| H1 | **Procedure:** recovery-file cleanup (or the explicit `--keep-recovery-files` opt-out) is a prerequisite for any `complete` ledger line. Purge steps reordered: 8 = recovery files, 9 = completion, 10 = print. `scrub-backups` (incl. `--resume`) runs the same step before completing. **New helper** `offline.CleanRecoveryFiles(ctx, paths, keep, assumeYes, prompt) (mode string, err error)`. **New field** `backup.LedgerRecord.RecoveryFiles` (`json:"recovery_files,omitempty"`: `deleted`/`kept`/`none`) on complete lines. **New flag** `tkr scrub-backups --keep-recovery-files`. Declining the prompt leaves records pending. | §12.6, §12.7, §9.3 |
| M1 | **Procedure:** a pending request is deleted only after a success (or an idempotent replay of one); an unsuccessful definitive answer keeps it and records `last_error`. **Fields** `client.PendingRequest.LastError string`, `LastAttemptAt time.Time`. **Methods** `PendingStore.MarkFailed(key, lastError)`; `PendingStore.Drop` renamed `Discard`. **Command** `tkr pending drop` renamed `tkr pending discard KEY`. | §6.7, §9.3, §9.6 |

### 14.10 Plan A follow-ups

| Ref | Change | Where |
|---|---|---|
| P-2 | **Rule:** revert re-evaluates a `slipped`/`continue` reason it would remove (because the target added it) and keeps it when another block still justifies it — the converse of R4-6. | §4.6 step 6 |
| P-3 | **Rule:** every placement clears a pending defer (`Place`, `AcceptNew`, `Block`, `Reblock`, `Complete`, `Drop`, `Defer(nil)`, `AddTask` with a placement); `Defer` with a moment sets one. | §3.1 (placement legend) |
| P-4 | **Signature** `testutil.Meta(a domain.Actor, gen domain.Generation) service.Meta` (every mutation needs `Meta.Gen`). | §13.1 |
| P-5 | **Lint config:** `linters.exclusions.rules` gains `{path: '_test\.go', linters: [gosec]}`; production G304/G204/G302/G306 sites use explicit `//nolint:gosec // <reason>` only (validated against the golangci-lint v2.14 schema). | §13.4 |

---

## 15. Round-2 questions

**Q1. Final Go module path.** The code uses `module taskronin`; renaming is
`make rename-module NEW=…` plus two hand-edited lines (§1.1).
*Recommendation:* if the repo will be hosted, use its import path now
(e.g. `github.com/<you>/taskronin`); if it will stay private on this machine,
keep `taskronin` — it never needs `go get`.

