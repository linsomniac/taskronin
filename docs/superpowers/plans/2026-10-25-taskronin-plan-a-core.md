# Taskronin Plan A — Core Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Taskronin's core Go library — repository scaffolding, `clock`, `civil`, `textsafe`, `domain`, `datespec`, `store` (SQLite schema and queries), `auth`, `fsutil`, the `backup` ledger and snapshot manager, and the `service` layer with every rule and op — ending with a library that passes every service-level scenario of the behavior catalog.

**Architecture:** Leaf packages first (pure types and time math), then `store` (SQLite only: DSNs, pragma verification, migrations, typed queries; no rules, no events), then `service`, whose single write path `run` wraps every op in one `BEGIN IMMEDIATE` transaction with reconcile, events, recompute, invariants, version bumps and idempotency. Scenario tests drive the service through `testutil.Scenario` with a fake clock in America/Denver.

**Tech Stack:** Go 1.26; `modernc.org/sqlite` v1.60.1 (pure Go, FTS5); `golang.org/x/crypto/argon2`; `golang.org/x/time/rate`; `golang.org/x/sys/unix`; `github.com/charmbracelet/x/ansi`; stdlib `testing` incl. native fuzzing; golangci-lint v2.14; govulncheck v1.9.0.

**Spec:** `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (primary, cited **A§n**), `docs/superpowers/specs/2026-10-25-taskronin-design.md` (**D§n**), `docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md` (scenario IDs such as `BLK-27`). Read the cited sections; this plan does not repeat them.

## Global Constraints

- Go `go 1.26.0` / `toolchain go1.26.8`; module path `taskronin` — nothing outside `go.mod` and `.golangci.yml` hard-codes it (the Makefile derives it with `go list -m`). Linux only.
- Dependencies pinned exactly as A§1.1; add a module with `go get <module>@<version>` in the task that first imports it, never `@latest`. Plan A uses only: `modernc.org/sqlite v1.60.1`, `golang.org/x/crypto v0.58.0`, `golang.org/x/sys v0.49.0`, `golang.org/x/time v0.16.0`, `github.com/charmbracelet/x/ansi v0.11.9`.
- `make check` (fmt-check, vet, golangci-lint incl. gosec, `go test -race -count=1 ./...`, govulncheck, `go mod verify`, assets-verify) must be green before every commit.
- Layering exactly A§1.3 (enforced by `internal/archtest` from Task 1). `store` holds no business rules and writes no events; every rule and every authorization check lives in `service`.
- Only `internal/clock` calls `time.Now`; nothing uses `time.Local`; no `fmt.Print*`/`print`/`os.Exit`; `log/slog` only; `crypto/rand` only (forbidigo/depguard, A§13.4).
- Parameterized SQL only (`?` placeholders). The only string-built SQL: constant PRAGMA statements, table/column names taken from allowlists in code, and A§12.1's `VACUUM INTO` literal fallback.
- `internal/store/migrations/0001_init.sql` is copied verbatim from A§5.2 in Task 8 and never edited afterwards (a checksum test freezes it). Never edit a committed migration file.
- No secrets in the repo, fixtures included: tests mint tokens, keys and password hashes at runtime. The one fixed test password is `testutil.Password = "correct horse battery"` (A§13.1).
- Errors crossing the `service` boundary are `*domain.Error` with a `domain.Code` from A§2.8 only; wrap causes with `%w`; `internal` errors never expose cause text in `Detail`.
- Time: an op reads `now = clock.Now().Truncate(time.Millisecond)` once per transaction; ordering and acknowledgement compare `ops.seq` values, never timestamps (A§4.2); "current/started/ended" compare instants from `civil.Zone.Instant`, never minutes (A§2.1).
- Every exported identifier has a doc comment; every enum constant carries its named type explicitly (A§2.4 note).
- AIDEV anchors (A§1.4): `grep -rn 'AIDEV-' internal/<pkg>` before editing a package; never delete an `AIDEV-NOTE`; each task names the anchors it must add.
- Tests: table-driven where natural; run one test with `go test ./internal/<pkg> -run '^TestName$' -race -count=1`. Fuzz targets (A§13.3) are ordinary `Fuzz*` functions run by `make fuzz`.
- **Catalog scenario tests:** one `func Test<PFX><NN>(t *testing.T)` per scenario ID (e.g. `TestBLK27`; variants as `t.Run`) in package `service_test` built with `testutil.NewScenario`. The scenario's catalog row is the test: build its *Given* with real service ops, perform its *When*, and assert every state its *Then* names — reason sets (`s.Reasons`), block tuples (`s.BlockState` == the catalog's `{status, src, slipped, cancel}` text), L/S (`s.Slips`), focus, versions, event keys (`s.EventKeys`), error codes (`testutil.RequireCode`). Catalog conventions (catalog §0): zone America/Denver, default now **Wed 2026-10-28 10:00 MDT**, years default to 2026, fixture blocks are created ≥ 15 min before *When* unless marked `fresh`; build past-dated state by moving the fake clock forward through real ops (`s.At(...)`), never by rewriting rows with SQL unless a task says so.
- Commits: conventional subject (`feat(store): …`); the last line of every commit message is `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; stage only the task's files (never `git add -A`).
- Formatting: `gofmt`/`goimports` with local prefix `taskronin`.
- Execution context (review ruling P1): each implementer receives its task text, these Global Constraints, and read access to the specs and to all four plan files. A task may therefore cite a signature by precise location ("A§3.2 `AddPlacement`", "Task 16a Interfaces: `op.record`") instead of repeating it; every citation names the section or task and the symbol.
- gosec (A§13.4 policy): `_test.go` files are excluded by the config; production code (incl. `internal/testutil`) is never silenced by path or rule. The only expected Plan A suppressions, each written exactly as `//nolint:gosec // G304: <reason>` on the flagged line: `fsutil.FsyncDir` (`os.Open(dir)` — a directory handle for fsync), `backup.OpenLedger` and `(*Ledger).Append` (`os.ReadFile`/`os.OpenFile` of the configured ledger path). Any other gosec finding is fixed, not suppressed.

## Review Focus

1. **Text that is not plain ASCII reaching an op** — invalid UTF-8, a BOM, bidi/C1 controls, CRLF, whitespace-only titles, a 500-rune title of 2-byte runes, notes of exactly 65 536 bytes: every op must answer `validation` (or normalize), never a CHECK-constraint `conflict`/`internal`. Tests: Task 4 boundary table; Task 29 `TestTextInputsEveryOp` over every text-bearing op.
2. **The wall clock moving backward** after a midnight reconcile (NTP correction): no panic, no second resolution, acknowledgement still by seq. Test: Task 23 `TestClockMovesBackward`.
3. **Concurrent callers** (HTTP handlers, the minutely reconcile and reads at once): every op serializes on the single writer, ranks stay unique, no `busy`, `-race` clean. Test: Task 18 `TestConcurrentOps`.
4. **Hostile paging input** — `limit` ≤ 0 or > 500, garbage or cross-list cursors, `from > to`, spans > 366 days: `validation`/`malformed`, never a 500 or a silently wrong page. Test: Task 32 `TestPagingInputs`.
5. **Tags and projects in mixed case or with bad characters** — `Infra` vs `infra` must share one tag row, `-P infra` must not match `infrastructure`, `a b` is `validation` (not a CHECK `conflict`), removing an absent tag is a no-op. Tests: Task 29 `TestUpdateTaskTagsAndProjects`; Task 32 `TestListTasksFilters`.

---

## File structure

| Package (task) | Files |
|---|---|
| root (1) | `go.mod`, `go.sum`, `Makefile`, `.golangci.yml`, `.gitignore`, `scripts/fuzz.sh`, `README.md` |
| `internal/buildinfo` (1), `internal/archtest` (1) | `buildinfo.go`; `deps_test.go` |
| `internal/clock` (2) | `clock.go`, `fake.go` |
| `internal/civil` (2, 3) | `date.go`, `minute.go` (2); `zone.go` (3) |
| `internal/textsafe` (4) | `validate.go`, `sanitize.go`, `width.go` |
| `internal/domain` (5) | `ids.go`, `enums.go`, `task.go`, `entry.go`, `block.go`, `actor.go`, `op.go`, `errors.go`, `cursor.go` |
| `internal/datespec` (6) | `parse.go`, `range.go` |
| `internal/store` (7–12) | `dsn.go`, `open.go`, `q.go`, `errors.go` (7); `migrate.go`, `migrations/0001_init.sql`, `fts.go` (8); `tasks.go`, `rank.go`, `triage.go`, `tags.go`, `focus.go`, `settings.go` (9); `entries.go`, `blocks.go`, `interruptions.go` (10); `ops.go`, `events.go`, `columns.go`, `idem.go`, `undo.go`, `tokens.go`, `sessions.go`, `bootstrap.go` (11); `search.go`, `scrub.go`, `sequences.go`, `export.go` (12) |
| `internal/auth` (13) | `token.go`, `password.go`, `gate.go`, `session.go` |
| `internal/fsutil` (14), `internal/backup` (14, 15) | `atomic.go`, `perms.go`; `ledger.go` (14); `snapshot.go`, `verify.go`, `rotate.go` (15) |
| `internal/service` (16a–37) | `service.go`, `op.go`, `events.go`, `columns.go`, `effects.go`, `reconcile.go` (16a); `views.go`, `tasks.go`, `derive.go` (16b); `invariants.go` (17); `idem.go`, `lifecycle.go` (18); `rank.go`, `place.go` (19); `reconcile.go` (20); `entries.go` (21, 25); `blocks.go` (22, 24); `slip.go` (23); `focus.go` (26); `lifecycle_ops.go` (28, 29); `revert.go` (30); `undo.go` (31); `aichanges.go`, `history.go` (33); `report.go`, `search.go` (34); `tokens.go`, `settings.go`, `password.go`, `usage.go` (35a); `sessions.go` (35b); `backup.go`, `export.go`, `system.go` (36); `purge.go` (37); `export_test.go` (test hooks, 16a+) |
| `internal/testutil` (16a+) | `clock.go`, `store.go`, `service.go`, `scenario.go` (Plan B adds `server.go`, `golden.go`) |

Plan A does not create `cmd/tkr` or any of `api`, `server`, `client`, `cli`, `config`, `lock`, `systemd`, `offline`, `report`, `editor`, `markdown`, `web`, `tui`, `mcpserver` (Plans B–D), nor `backup/stage.go`, `swap.go`, `inventory.go` (Plan B).

## Catalog coverage

Every in-scope scenario is owned by exactly one task (its `Test<PFX><NN>` lives there). Task 38 adds a guard test, scanning every `_test.go` in the module, that fails if an ID has no `func Test<PFX><NN>` or `func Test<PFX><NN>_<suffix>`; Plans B and C remove the IDs they own from its `notPlanA` list in their final tasks (review ruling P5).

| Task | Scenario IDs |
|---|---|
| 3 | DAT-15, DAT-16 |
| 6 | DAT-01, 02, 04, 06, 07, 08, 09, 10, 12, 13 |
| 14 | PRG-08, 13, 14, 15 |
| 16b | TRI-01, 08 |
| 18 | IDM-02, 03, 05, 06, 10, SCP-10 |
| 19 | TRI-02, 03, 04, 09, 10, 11, 12, 18, 22, IDM-08, SCP-03 |
| 20 | TRI-19, 24, DUE-01, 02, 03, 10, 11, 14, 15, 16, REC-01 |
| 21 | DAT-03, 11, 17 |
| 22 | TRI-06, 14, BLK-01, 02, 03, 05, 06, 07, 11, 12, 13, 14, 15, 16, 48 |
| 23 | BLK-17, 18, 19, 20, 21, 23, 25, 26, SLP-01, 02, 03, 06, 12, 14, 15, REC-07, 08, 09, RCP-06, DUE-13 |
| 24 | BLK-04, 10, 27, 28, 30, 31, 32, 33, 35–47, 49, 50, RCP-07, DUE-20 (BLK-41's `done` variant: Task 28) |
| 25 | BLK-24, SLP-04, 08, 13, RCP-01, 02, 03, 04, 08, 09 |
| 26 | TRI-07, FOC-01, 02, INT-01, 02, 03, 04, 05, 07, 10, 11, 12, 13, REC-03, 04 |
| 27 | TRI-05, 13, 26, 27, 28, BLK-09, 29, SLP-07, DUE-04, 05, 06, 07, 08, 09, 12, 17, 21, DAT-05, REC-02, LIF-11 |
| 28 | TRI-15, BLK-22, SLP-09, 16, LIF-01–06, 12–15, 17–20, IDM-07, REC-11 |
| 29 | TRI-16, 21, 23, 29, BLK-08, 34, SLP-10, 11, LIF-07, 08, 09, 10, 16, IDM-01, FOC-03, REC-05 |
| 30 | SLP-05, UND-06, 09, 10, 11, 12, 14, 21, 23, 24 |
| 31 | UND-01–05, 07, 08, 15–20, 22, 25, 27, RCP-05, REC-10, INT-09, SCP-09 |
| 32 | TRI-20, DUE-18, 19, REC-06, 12, DAT-14, INT-08 |
| 33 | TRI-25, SCP-11 |
| 34 | RPT-01, 02, 05, 06, 08, 09, 10 |
| 35a | SCP-04, 07, 12, 13 |
| 36 | IDM-04 |
| 37 | PRG-04, 05, 06, 07, 09, 10, 11, 16, 17, 19, RPT-07, UND-13, 26 |
| 38 | SCP-01, 02, 08 |
| *not Plan A* | TRI-17 (Plan C: TUI skip sends nothing); FOC-04–12, INT-06 (Plan B: CLI log linking and arg parsing); IDM-09 (Plan B: pending requests); SCP-05, 06 (Plan B: HTTP route class, socket guard); RPT-03, 04 (Plan B: report rendering); PRG-01, 02, 03, 12, 18, 20, 21, 22 (Plan B: offline commands). Service halves of DAT-14, REC-06, SCP-04, SCP-12, PRG-05, 09, 10 are Plan A's (above). |

---

### Task 1: Repository scaffolding and the dependency-graph test

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `.gitignore`, `scripts/fuzz.sh`, `README.md`
- Create: `internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`
- Create: `internal/archtest/doc.go` (package comment only, so tools that skip test-only packages still load it), `internal/archtest/deps_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `buildinfo.Version`, `buildinfo.Commit` (`var … string`, set by ldflags; default `"dev"`, `""`), `const buildinfo.APIVersion = "v1"`; the `make` targets of A§13.5; the archtest guard every later package must satisfy.

- [ ] **Step 1: Write the module files and the failing tests**

`go.mod`: `module taskronin`, `go 1.26.0`, `toolchain go1.26.8`, no `require` yet. `Makefile`: verbatim A§13.5. `.golangci.yml`: verbatim A§13.4 as it stands now (incl. the `_test.go` gosec exclusion and the per-call-site policy, ruling P4). `.gitignore`: `/bin/`. `README.md`: a title, one paragraph (what Taskronin is, D§1), and a "Development" section: `make check` before every commit; `make fuzz`; grep `AIDEV-` anchors before editing; migrations are never edited; links to the three specs. `scripts/fuzz.sh` (exact copy, `chmod +x`):

```sh
#!/bin/sh
# Runs every Fuzz* target in internal/ for $1 (default 30s) each.
set -eu
t="${1:-30s}"
for d in $(grep -rl --include='*_test.go' '^func Fuzz' internal | xargs -n1 dirname | sort -u); do
  for f in $(grep -h '^func Fuzz' "$d"/*_test.go | sed 's/^func \(Fuzz[A-Za-z0-9_]*\).*/\1/'); do
    go test "./$d" -run='^$' -fuzz="^$f\$" -fuzztime="$t"
  done
done
```

Tests:

```go
// internal/buildinfo/buildinfo_test.go
func TestAPIVersion(t *testing.T) {
	if APIVersion != "v1" {
		t.Fatalf("APIVersion = %q", APIVersion)
	}
}

// internal/archtest/deps_test.go
func TestCheckerRejectsForbiddenEdges(t *testing.T) {
	got := checkPackages("taskronin", []listedPackage{
		{ImportPath: "taskronin/internal/domain", Imports: []string{"time", "taskronin/internal/store"}},
		{ImportPath: "taskronin/internal/store", Imports: []string{"github.com/mattn/go-sqlite3"}},
		{ImportPath: "taskronin/internal/civil", Imports: []string{"time", "database/sql/driver"}},
	}, nil)
	want := []string{
		"taskronin/internal/domain imports taskronin/internal/store (not allowed by A§1.3)",
		"taskronin/internal/store imports github.com/mattn/go-sqlite3 (not allowed by A§1.3)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}
func TestCheckerCLIFileExceptions(t *testing.T) // cli/cmd_add.go importing taskronin/internal/server → 1 violation;
	// cli/cmd_serve.go importing server → none; cli/cmd_purge.go importing offline → none (fileImports stub)
func TestCheckerUnknownPackage(t *testing.T)    // taskronin/internal/bogus → violation "not in the A§1.3 table"
func TestDependencyGraph(t *testing.T)          // runs `go list -json ./...` on the real tree → zero violations
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/... -race -count=1`
Expected: FAIL — `undefined: APIVersion`, `undefined: checkPackages`.

- [ ] **Step 3: Implement `buildinfo` and the checker**

`internal/buildinfo/buildinfo.go`: `var Version = "dev"`, `var Commit = ""`, `const APIVersion = "v1"`.

`internal/archtest/deps_test.go` (test-only package; all code in the `_test.go` file):

```go
type listedPackage struct{ ImportPath, Dir string; Imports, GoFiles []string }
func checkPackages(module string, pkgs []listedPackage, fileImports func(dir, file string) []string) []string // sorted violations
```

- Classify an import: `== module` or prefix `module+"/"` → internal; first path element contains `.` → third-party; else stdlib (always allowed).
- `allowed`: a map literal copied **literally** from the current A§1.3 table — internal edges per package and third-party labels. Map labels to import prefixes: `x/ansi`→`github.com/charmbracelet/x/ansi`, `x/sys/unix`→`golang.org/x/sys/unix`, `toml`→`github.com/BurntSushi/toml`, `x/crypto/argon2`→`golang.org/x/crypto/argon2`, `x/time/rate`→`golang.org/x/time/rate`, `modernc.org/sqlite`→`modernc.org/sqlite` (incl. subpackages), `goldmark`→`github.com/yuin/goldmark`, `bluemonday`→`github.com/microcosm-cc/bluemonday`, `bubbletea|bubbles|lipgloss`→`charm.land/{bubbletea,bubbles,lipgloss}/v2`, `go-sdk`→`github.com/modelcontextprotocol/go-sdk`, `jsonschema-go`→`github.com/google/jsonschema-go`, `cobra`→`github.com/spf13/cobra`, `x/term`→`golang.org/x/term`, `teatest`→`github.com/charmbracelet/x/exp/teatest/v2`.
- `testutil` and `archtest` are not checked (test-only). `cmd/tkr` may import only `cli`.
- `cli`: the base edges apply to every file; the per-file exceptions of A§1.3 (`cmd_serve.go`→`server`; `cmd_{init,migrate,restore,import,purge,scrub,passwd,backup}.go`→`offline`; `cmd_tui.go`, `cmd_plan.go`→`tui` — the "…" of A§1.3 is extended by the plan that adds another TUI launcher file; `cmd_mcp.go`→`mcpserver`) live in one `cliFileExceptions` map and are checked per file with `go/parser` (`parser.ImportsOnly`).
- An internal package missing from the table → violation.
- Only non-test `Imports` are checked. `TestDependencyGraph` runs `exec.Command("go", "list", "-json", "./...")` from the repo root (`go list -m -f {{.Dir}}`) and decodes the stream with `json.Decoder`.

- [ ] **Step 4: Run the tests and `make check`**

Run: `go test ./internal/... -race -count=1` → PASS. Run: `make check` → exit 0 (govulncheck downloads v1.9.0 on first run).

- [ ] **Step 5: Commit**

```bash
git add go.mod Makefile .golangci.yml .gitignore scripts/fuzz.sh README.md internal/buildinfo internal/archtest
git commit -m "chore: scaffold module, make check, lint config and archtest" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: `clock` and `civil` dates and minutes

**Files:**
- Create: `internal/clock/clock.go`, `internal/clock/fake.go`, `internal/clock/clock_test.go`
- Create: `internal/civil/date.go`, `internal/civil/minute.go`, `internal/civil/date_test.go`, `internal/civil/minute_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: A§2.3 verbatim (`Clock{Now; After}`, `Real`, `Fake`, `NewFake`, `(*Fake).Set`, `(*Fake).Advance`); A§2.2 `Date`, `NewDate`, `ParseDate`, `MustDate`, `String`, `IsZero`, `AddDays`, `DaysSince`, `Weekday`, `Compare`, `MarshalText`/`UnmarshalText`, `Value`/`Scan`, `Minute`, `EndOfDay`, `ParseHHMM`, `EndMinute` — plus `(Date).Before`, `(Date).After`, `(Minute).String`, `(EndMinute).String`, text and SQL methods on `Minute` and `EndMinute`.

- [ ] **Step 1: Write the failing tests**

```go
func TestParseDate(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"2026-10-28", true}, {"2024-02-29", true}, {"2026-02-30", false}, {"2026-2-3", false},
		{"2026-13-01", false}, {"20261028", false}, {" 2026-10-28", false}, {"0999-01-01", false},
	} {
		d, err := ParseDate(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("ParseDate(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && d.String() != tc.in {
			t.Fatalf("round trip %q → %q", tc.in, d)
		}
	}
}
```

Also: `TestDateArithmetic` (`MustDate("2026-10-28").AddDays(4)` = 2026-11-01; `AddDays(-28)` = 2026-09-30; 2026-12-31 +1 = 2027-01-01; `MustDate("2026-11-04").DaysSince(MustDate("2026-10-28"))` = 7; 2026-10-28 is a `time.Wednesday`; `Compare`/`Before`/`After`), `TestDateTextAndSQL` (`Value` = `"2026-10-28"`; `Scan` of `string` and `[]byte`; `Scan("2026-02-30")` → error; zero `Date` `MarshalText` → error), `TestParseHHMM` (`"9:05"`→545, `"09:05"`→545, `"00:00"`→0, `"23:59"`→1439, `"24:00"`→error unless `allow2400` then 1440; `"9:5"`, `"23:60"`, `"24:01"`, `"-1:00"`, `""` → error), `TestMinuteText` (`Minute(545).String()` = `"09:05"`; `Minute` `UnmarshalText("24:00")` → error; `EndMinute(1440)` marshals `"24:00"`; `EndMinute` `UnmarshalText("00:00")` → error), `TestFakeClock` (`After(5*time.Minute)` not ready after `Advance(4m59s)`, ready after one more second carrying t0+5m; `After(0)` ready at once; `Set` may move backwards), `FuzzParseDate` (A§13.3: when accepted, `String()` equals the input — the format is strict) and `FuzzParseHHMM` (when accepted, `ParseHHMM(v.String(), allow)` returns the same value and `String()` is canonical — two digits each, so `"9:05"` and `"09:05"` both give `"09:05"`; seed both forms).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/clock ./internal/civil -race -count=1` → FAIL (undefined identifiers).

- [ ] **Step 3: Implement**

`Date` stores `y int16; m, d uint8`; `NewDate` accepts years 1000–9999 and real calendar dates only. `ParseDate` requires `^\d{4}-\d{2}-\d{2}$` before calendar validation. `ParseHHMM` requires `^\d{1,2}:\d{2}$`, hour 0–23 (or exactly `24:00` when allowed), minute 00–59. `Minute.Value` → `int64`; `Scan` accepts `int64`. `Fake` guards `now` and its waiters with a mutex; `Set`/`Advance` fire every waiter whose deadline ≤ new now. `Real.After` = `time.After`. The single `time.Now` call lives in `clock.go`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/clock ./internal/civil -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/clock internal/civil
git commit -m "feat(civil): add injected clock, civil dates and minutes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: `civil.Zone` and DST policy

**Files:**
- Create: `internal/civil/zone.go`, `internal/civil/zone_test.go`

**Interfaces:**
- Consumes: Task 2 `Date`, `Minute`.
- Produces: A§2.2 `GapPolicy` (`GapForward`, `GapReject`), `Zone`, `LoadZone`, `(Zone).Name`, `DateOf`, `MinuteOf`, `StartOfDay`, `Instant`, `DeferMoment`, `DueMoment`; plus `var ErrNonexistent = errors.New("civil: nonexistent wall time")` and `func (z Zone) Location() *time.Location` (for `api.Time` rendering in the server zone, Plan B).

- [ ] **Step 1: Write the failing tests**

```go
func TestInstant(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, tc := range []struct {
		d    string
		m    Minute
		p    GapPolicy
		want string // RFC 3339 UTC, or "gap"
	}{
		{"2026-10-28", 600, GapReject, "2026-10-28T16:00:00Z"},
		{"2027-03-14", 150, GapForward, "2027-03-14T09:00:00Z"}, // 02:30 does not exist → 03:00 MDT (DUE-04)
		{"2027-03-14", 150, GapReject, "gap"},
		{"2027-03-14", 120, GapReject, "gap"},                   // 02:00 (BLK-12)
		{"2027-03-14", 90, GapReject, "2027-03-14T08:30:00Z"},
		{"2027-03-14", 180, GapReject, "2027-03-14T09:00:00Z"},
		{"2026-11-01", 90, GapReject, "2026-11-01T07:30:00Z"},   // ambiguous → earlier (DUE-03, BLK-14)
		{"2026-11-01", 150, GapReject, "2026-11-01T09:30:00Z"},
		{"2026-11-01", 1440, GapReject, "2026-11-02T07:00:00Z"}, // 25-hour day ends
		{"2027-03-14", 780, GapReject, "2027-03-14T19:00:00Z"},  // BLK-16
		{"2026-11-01", 780, GapReject, "2026-11-01T20:00:00Z"},
	} { /* compare, errors.Is(err, ErrNonexistent) for "gap" */ }
}
```

Also: `TestLoadZone` (`America/Denver`, `UTC` load; `""`, `Local`, `UTC0`, `MST`, `EST5EDT` → error), `TestStartOfDay` (catalog §0 table: 10-29 → 06:00Z; 11-01 → 06:00Z; 11-02 → 07:00Z; 2027-03-14 → 07:00Z; 2027-03-15 → 06:00Z), `TestDAT16` (`DateOf(2026-11-02T06:30Z)` = 2026-11-01; `DateOf(2027-03-15T06:00Z)` = 2027-03-15), `TestMinuteOf` (`MinuteOf(2026-11-01T08:15Z)` = 75), `TestDAT15` (`DeferMoment(2026-11-01, 90)` = 07:30Z), `TestMoments` (`DeferMoment(10-29, null)` = 2026-10-29T06:00Z; `DueMoment(10-28, null)` = 2026-10-29T06:00Z; `DueMoment(10-28, 17:00)` = 2026-10-28T23:00Z), `TestInstantAuckland` (P13; Pacific/Auckland, values from the zone database: gap day 2026-09-27 — `02:30` GapForward → 2026-09-26T14:00Z, GapReject → `ErrNonexistent`, `01:30` → 2026-09-26T13:30Z, `03:30` → 2026-09-26T14:30Z, `StartOfDay(09-27)` = 2026-09-26T12:00Z, `StartOfDay(09-28)` = 2026-09-27T11:00Z; overlap day 2026-04-05 — `02:30` → 2026-04-04T13:30Z (earlier, NZDT), `StartOfDay(04-05)` = 2026-04-04T11:00Z, `StartOfDay(04-06)` = 2026-04-05T12:00Z, `DateOf(2026-04-04T14:30Z)` = 04-05 with `MinuteOf` 150), `FuzzInstant` (A§13.3: zones Denver, Auckland and UTC, years 2000–2100, any day, minute 0–1440, both policies; no panic; `GapForward` never errors; on success `DateOf(t)` = d, or d+1 when m = 1440, and for m < 1440 with `GapReject` `MinuteOf(t)` = m).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/civil -run 'Zone|Instant|StartOfDay|DAT1|MinuteOf|Moments' -race -count=1` → FAIL.

- [ ] **Step 3: Implement `zone.go`**

`LoadZone` accepts `UTC` or names containing `/` that `time.LoadLocation` resolves; everything else is an error. `Instant` derives its candidates from the zone's actual transitions (review ruling P13; AIDEV-NOTE: DST correctness lives here):

```text
if m == 1440: return StartOfDay(d+1)
N := wall (d, m) expressed as if UTC
q := N − offset(N)                         // first guess near the true instant
[s, e) := q.In(loc).ZoneBounds()           // the zone period containing q (zero values: no transitions)
offsets := {offset(q), offset(s − 1ns) if s set, offset(e) if e set}   // the periods before, at and after q
valid := {N − o : o ∈ offsets, wall(N − o) == (d, m)}
valid non-empty → earliest
gap → GapReject: ErrNonexistent; GapForward: the transition T ∈ {s, e} with wall(T − 1ns) < (d, m) < wall(T)
```

`StartOfDay(d)` = `Instant(d, 0, GapForward)`; `DeferMoment`: null minute → `StartOfDay(d)`, else `Instant(d, m, GapForward)`; `DueMoment`: null minute → `StartOfDay(d+1)`, else `Instant(d, m, GapForward)`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/civil -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/civil/zone.go internal/civil/zone_test.go
git commit -m "feat(civil): zone instants with explicit DST gap and overlap policy" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: `textsafe` validators and terminal sanitizer

**Files:**
- Create: `internal/textsafe/validate.go`, `sanitize.go`, `width.go`, `textsafe_test.go`

**Interfaces:**
- Consumes: `github.com/charmbracelet/x/ansi v0.11.9` (`go get` it here).
- Produces (A§6.7, exact): `ValidateTitle(s string) (string, error)`, `ValidateNotes(s string) (string, error)`, `ValidateName(s string, max int) error`, `Sanitize(s string) string`, `SanitizeLine(s string) string`, `Width(s string) int`, `Truncate(s string, width int, tail string) string`; plus `const (MaxTitleRunes = 500; MaxNotesBytes = 65536)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestValidateTitle(t *testing.T) {
	ok := map[string]string{
		"  Set up keycloak  ":          "Set up keycloak",
		"\ufeffx":                      "x",
		strings.Repeat("é", 500):       strings.Repeat("é", 500), // 500 runes, 1000 bytes (Review Focus 1)
	}
	bad := []string{"", "   ", "a\tb", "a\nb", "a\x7fb", "a\u0085b", "a\u202eb", "a\u2066b", "a\u2028b", "\xff",
		strings.Repeat("é", 501)}
	/* ok → (want, nil); bad → error whose message names the problem */
}
```

Also: `TestValidateNotes` (`"a\r\nb\rc"` → `"a\nb\nc"`; `"\tindent"` ok; `"\x07"`, `"\u0085"`, `"\u202e"`, `"\u2029"`, `"\xff"` → error; exactly 65 536 bytes ok; 65 537 → error), `TestValidateName` (`"infra.keycloak"`, `"db_1-x"` ok for max 100; `""`, `"a b"`, `"é"`, 101 chars → error), `TestSanitize` (`"a\x1b[31mb"` → `"a�[31mb"`; `"x\u202ey"` → `"x�y"`; `"\xff"` → `"�"`; `"l1\nl2\t"`, `"é漢"`, `"👨‍👩"` unchanged), `TestSanitizeLine` (`"a\nb\tc"` → `"a�b�c"`), `TestWidthTruncate` (`Width("漢字")` = 4; `Truncate("abcdef", 4, "…")` = `"abc…"`), `FuzzValidateTitle`, `FuzzValidateNotes` (accepted output is valid UTF-8, contains no forbidden rune, respects the limit, and re-validates to itself), `FuzzSanitize` (output valid UTF-8; every rune graphic, `\n`, `\t`, ZWJ/ZWNJ or U+FFFD; no bidi control, U+2028/9).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/textsafe -race -count=1` → FAIL.

- [ ] **Step 3: Implement**

Rules (D§10.6, A§14 D18): strip one leading U+FEFF; reject invalid UTF-8; titles are `strings.TrimSpace`d and allow no C0 (incl. `\t`, `\n`), DEL, C1, U+2028/U+2029 or bidi controls (U+202A–U+202E, U+2066–U+2069), 1–500 runes; notes convert CRLF and lone CR to LF, then allow C0 only `\n`/`\t`, no DEL/C1/U+2028/9/bidi, ≤ 65 536 bytes. Error messages are user-facing ("must not be empty", "is longer than 500 characters", "contains a control character U+0007", "contains a bidirectional control U+202E", "is not valid UTF-8"). `ValidateName` matches `^[A-Za-z0-9._-]{1,max}$`. `Sanitize` keeps `\n`, `\t`, U+200C, U+200D and `unicode.IsGraphic` runes that are not bidi controls; everything else (incl. each invalid byte) → U+FFFD. `Width` = `ansi.StringWidth`; `Truncate` = `ansi.Truncate`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/textsafe -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/textsafe
git commit -m "feat(textsafe): input validators, terminal sanitizer and width helpers" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5: `domain` types, enums, actors, errors and cursors

**Files:**
- Create: `internal/domain/{ids.go, enums.go, task.go, entry.go, block.go, actor.go, op.go, errors.go, cursor.go}`, `internal/domain/domain_test.go`

**Interfaces:**
- Consumes: Task 2 `civil.Date`, `civil.Minute`, `civil.EndMinute`.
- Produces: everything in A§2.4–2.8 verbatim (IDs, `OpID` + methods, `Version`, `Generation`, `Expect`, all enums, `TriageOrder`, `Moment`, `BlockSpan`, `ActorKind`, `Channel`, `Scopes` + `ParseScopes`/`Has`/`String`, `Actor` + `Key`/`IsAI`, `SystemActor`, entities incl. `PurgedAt`, `TaskView`, `EntryView`, `TaskDetail`, `BlockView` **with `TaskVersion Version`** (R4-7), `OpKind`, `OpState`, `OpRecord`, `EntityKind`, `Action`, `Event`, `EntityRef`, `Code` consts, `Error`, `Errorf`, `AsError`, `FieldError`, `RevertConflict`). Planner additions:

```go
func NewGeneration() Generation                 // 16 lowercase hex (crypto/rand)
func (id OpID) IsZero() bool
func (r TriageReason) Rank() int                // len(TriageOrder) - index; unknown → 0
func (x T) Valid() bool                         // on TaskStatus, TriageReason, EntryKind, BlockStatus, OutcomeSource,
                                                // CancelReason, DuePhase, Priority, View, UserOutcome, PlaceHow, Channel
func (m Moment) String() string                 // "2026-10-30" | "2026-10-30 09:00"
func (b BlockSpan) String() string              // "2026-10-28 13:00-15:00"
func ValidationError(fields ...FieldError) *Error // Code validation, Detail "invalid input"
func FormatETag(g Generation, v Version) string   // `"<gen>-<version>"` incl. the quotes (A§6.3); shared by api and web
func ParseETag(s string) (Expect, error)           // exactly one strong ETag in that form; `*`, weak or malformed → *Error CodeMalformed
const ( // every A§3.10 kind, typed OpKind
	OpTaskAdd OpKind = "task.add"; OpTaskUpdate OpKind = "task.update"; OpTaskDue OpKind = "task.due"
	OpTaskPlace OpKind = "task.place"; OpTaskAcceptNew OpKind = "task.accept_new"; OpTaskDefer OpKind = "task.defer"
	OpTaskManual OpKind = "task.manual"; OpTaskComplete OpKind = "task.complete"; OpTaskReopen OpKind = "task.reopen"
	OpTaskDrop OpKind = "task.drop"; OpTaskDelete OpKind = "task.delete"; OpTaskUndelete OpKind = "task.undelete"
	OpBlockCreate OpKind = "block.create"; OpBlockReblock OpKind = "block.reblock"; OpBlockUnblock OpKind = "block.unblock"
	OpBlockOutcome OpKind = "block.outcome"; OpEntryAdd OpKind = "entry.add"; OpEntryUpdate OpKind = "entry.update"
	OpEntryRelink OpKind = "entry.relink"; OpEntryDelete OpKind = "entry.delete"; OpEntryUndelete OpKind = "entry.undelete"
	OpFocusSet OpKind = "focus.set"; OpRevert OpKind = "op.revert"; OpUndo OpKind = "op.undo"; OpRedo OpKind = "op.redo"
	OpAIReview OpKind = "ai.review"; OpTokenCreate OpKind = "token.create"; OpTokenRevoke OpKind = "token.revoke"
	OpSettingsUpdate OpKind = "settings.update"; OpSettingsRetentionLowered OpKind = "settings.retention_lowered"
	OpAuthPassword OpKind = "auth.password"; OpPurgeTask OpKind = "purge.task"; OpPurgeEntry OpKind = "purge.entry"
	OpSystemReconcile OpKind = "system.reconcile"; OpSystemLedgerReplay OpKind = "system.ledger_replay"
	OpSystemImport OpKind = "system.import"; OpSystemRestore OpKind = "system.restore"
)
type Cursor struct{ Keys []any; ID int64; Offset int }
func EncodeCursor(c Cursor) string               // base64url (no padding) of {"v":1,"k":[…],"id":N}; Keys==nil && ID==0 → {"v":1,"o":N}
func DecodeCursor(s string) (Cursor, error)      // strict: bad base64, unknown fields, v≠1, trailing data → *Error CodeMalformed;
                                                 // numbers decode as json.Number
```

- [ ] **Step 1: Write the failing tests**

```go
func TestParseScopes(t *testing.T) {
	s, err := ParseScopes("write,read,capture")
	if err != nil || s != ScopeRead|ScopeCapture|ScopeWrite || s.String() != "read,capture,write" {
		t.Fatalf("got %v %q %v", s, s.String(), err)
	}
	if s, err := ParseScopes(""); err != nil || s != 0 {
		t.Fatalf(`ParseScopes("") = %v, %v`, s, err)
	}
	if _, err := ParseScopes("read,bogus"); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if !AllScopes.Has(ScopeDestroy|ScopeAdmin) || MCPDefaultScopes.Has(ScopeDestroy) || !ScopeRead.Has(0) {
		t.Fatal("Has")
	}
}
```

Also: `TestActorKey` (owner → `"owner"`, token `ai` → `"token:ai"`, `SystemActor()` → `"system"` with channel `system`, scopes 0; `IsAI` only for tokens), `TestOpID` (`NewOpID().String()` matches `^[0-9a-f]{32}$`; `ParseOpID` rejects uppercase and 31 chars; `Value`/`Scan` and text round trips), `TestNewGeneration` (`^[0-9a-f]{16}$`, two calls differ), `TestTriageReasonRank` (slipped 8 … new 1, strictly decreasing along `TriageOrder`), `TestEnumsValid` (every constant valid; `"bogus"` invalid for each type), `TestErrors` (`Errorf(CodeConflict, "x %d", 1).Detail == "x 1"`; `errors.As` finds a wrapped `*Error`; `AsError(io.EOF)` → `CodeInternal`, false), `TestMomentAndSpanString`, `TestETag` (round trip; `*`, `W/"…"`, unquoted, two ETags, non-hex generation → `malformed`), `TestCursor` (round trip of `{Keys: [3, "2026-10-28", 540], ID: 7}` and `{Offset: 40}`; `"garbage"`, `"e30"` (`{}`), a `v:2` cursor → `CodeMalformed`), `FuzzCursor` (no panic; accepted input re-encodes to an equal cursor).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/domain -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the types exactly as A§2.4–2.8 plus the additions above. `Actor.Key()`: `owner`, `token:<TokenName>`, `system`. `AsError` returns `(e, true)` when `errors.As` finds a `*Error`, else `(&Error{Code: CodeInternal, Err: err}, false)`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/domain -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/domain
git commit -m "feat(domain): core types, enums, actors, scopes, errors and cursor codec" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 6: `datespec` grammar and report ranges

**Files:**
- Create: `internal/datespec/parse.go`, `range.go`, `datespec_test.go`

**Interfaces:**
- Consumes: Task 2 `civil`; Task 5 `domain.Moment`, `domain.BlockSpan`.
- Produces: A§9.2 verbatim (`Mode`, `Past`, `Future`, `Ref{Today; WeekStart}`, `Parse`, `ParseSpan`, `ParseClock`); planner additions in `range.go`:

```go
func Week(r Ref) (from, to civil.Date)     // the full week containing r.Today, starting on r.WeekStart
func LastWeek(r Ref) (from, to civil.Date) // the week before Week
func Month(r Ref) (from, to civil.Date)    // the full calendar month of r.Today
```

- [ ] **Step 1: Write the failing tests** (ref = `Ref{Today: 2026-10-28 (Wed), WeekStart: time.Monday}`)

```go
func TestDAT02(t *testing.T) { // Past: weekday = most recent incl. today
	for in, want := range map[string]string{
		"wed": "2026-10-28", "tue": "2026-10-27", "thu": "2026-10-22", "monday": "2026-10-26",
		"Mon": "2026-10-26", "tues": "2026-10-27", "weds": "2026-10-28", "thur": "2026-10-22", "thurs": "2026-10-22",
	} {
		m, err := Parse(in, Past, ref)
		if err != nil || m.Date.String() != want || m.Min.Valid {
			t.Fatalf("Parse(%q) = %v, %v; want %s", in, m, err, want)
		}
	}
}
```

Also, with the catalog's exact values: `TestDAT01` (`today`/`yesterday`/`-2d`/`2026-10-01` → 10-28/10-27/10-26/10-01), `TestDAT04` (Future: `tomorrow` 10-29, `thu` 10-29, `wed` 11-04, `mon` 11-02, `+3d` 10-31, `+2w` 11-11), `TestDAT06` (`15:00` → today, minute 900), `TestDAT07` (`mon 09:00` → 11-02 540; `2026-11-03 14:00` → 840; `+3d 09:00` → 10-31 540), `TestDAT08` (Future: `today` 10-28, `fri` 10-30, `wed` 11-04, `2027-03-14`), `TestDAT09` (Future: `today` 10-28, `fri` 10-30, `wed` 11-04, `yesterday` 10-27 — the service rejects it, Task 22), `TestDAT10` (`ParseClock("14:30", false)` = 870; `"24:00"` and `"9:5"` → error), `TestDAT12` (`2026-13-01`, `2026-2-3`, `nextweek`, `+3x` → error), `TestDAT13` (`Week` 10-26..11-01; `LastWeek` 10-19..10-25; `Month` 10-01..10-31; with `WeekStart: time.Sunday` `Week` = 10-25..10-31), `TestParseSpan` (`13:00-15:00` → today 780–900; `fri 09:00-10:00` (Future) → 10-30; `23:00-24:00` → end 1440; `15:00-13:00`, `13:00-13:00`, `24:00-25:00`, `23:00-01:00` → error; `+0d` = today), `FuzzParse`, `FuzzParseSpan` (no panic; accepted value re-parses from its `String()` to an equal value).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/datespec -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the A§9.2 grammar: case-insensitive, surrounding spaces trimmed, one or more spaces between date and clock, `rel` 1–3 digits, `+0d` = today, `span` without spaces around `-`. `ParseSpan` requires `0 ≤ start < end ≤ 1440` (start never `24:00`). Errors are plain `error`s with user-facing text (the CLI maps them to usage errors). No I/O, no clock.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/datespec -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/datespec
git commit -m "feat(datespec): relative date grammar, block spans and report ranges" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 7: `store` connections, DSNs, pragma verification and error mapping

**Files:**
- Create: `internal/store/{dsn.go, open.go, q.go, errors.go}`, `internal/store/open_test.go`

**Interfaces:**
- Consumes: Task 5 `domain.Error`/codes; `modernc.org/sqlite v1.60.1` (`go get` here) and its `lib` constants.
- Produces:

```go
type Q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
type Pool uint8
const (PoolWriter Pool = iota + 1; PoolReader; PoolSnapshot; PoolStaging)
func DSN(absPath string, p Pool) string
type Store struct{ Writer, Reader, Snap *sql.DB; Path string } // staging: Writer == Reader, Snap == nil
func Open(ctx context.Context, absPath string) (*Store, error)        // live DB, mode=rw
func OpenStaging(ctx context.Context, absPath string) (*Store, error) // copies/new files only, mode=rwc, journal_mode=DELETE
func (s *Store) Close() error
func (s *Store) CheckpointTruncate(ctx context.Context) error        // wal_checkpoint(TRUNCATE) on Writer; error unless busy=0 and log=0
var ErrNotFound = errors.New("store: not found")
func MapErr(err error) error
func QuickCheck(ctx context.Context, q Q) error                       // "ok" or error
func IntegrityCheck(ctx context.Context, q Q) error
```

- [ ] **Step 1: Write the failing tests**

```go
func TestOpenMissingFileFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "taskronin.db")
	if _, err := Open(context.Background(), p); err == nil {
		t.Fatal("Open created or opened a missing database")
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file appeared: %v", err)
	}
}
```

Also: `TestDSN` (writer has `mode=rw`, `_txlock=immediate`, `_journal_mode=WAL`, `_busy_timeout=5000`, `_foreign_keys=1`, `_synchronous=FULL`, `_defensive=1`, `_dqs=0`, `_pragma=secure_delete(1)`, `_pragma=trusted_schema(0)`; reader adds `_query_only=1` and `_txlock=deferred`; snapshot `_txlock=deferred`; staging `mode=rwc&_txlock=immediate&_journal_mode=DELETE`; a path containing a space, `?` and `#` parses back to itself via `url.Parse`), `TestOpenStagingThenOpen` (`OpenStaging` creates the file in DELETE mode, no `-wal` after `Close`; `Open` then reports `journal_mode` = `wal` on Writer, `query_only` = 1 on Reader and 0 on Writer), `TestHookRejectsForeignDSN` (`sql.Open("sqlite", "file:"+p)` → `Ping` fails mentioning `store.DSN`), `TestHookRejectsMismatch` (a DSN whose requested journal mode cannot take effect — the reader DSN with `mode=ro` instead of `mode=rw` on a DELETE-mode file — → `Ping` fails with the hook's mismatch error), `TestMapErr` (on a staging DB with ad-hoc `CREATE TABLE t(x TEXT UNIQUE CHECK (length(x) > 0)) STRICT` and a trigger `RAISE(ABORT, 'taskronin: nope')`: duplicate → `CodeConflict`; `''` → `CodeValidation`; trigger → `CodeConflict` with Detail `nope`; `sql.ErrNoRows` → `ErrNotFound`; a `*domain.Error` passes through; anything else → `CodeInternal`), `TestMapErrBusy` (a second writer `*sql.DB` holds `BEGIN IMMEDIATE`; `Writer.BeginTx` fails after ~5 s; `MapErr` → `CodeBusy`, `RetryAfter` 1 s), `TestCheckpointTruncate` (after writes → nil and the `-wal` file is empty; while a Reader tx holds a snapshot → error).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store -race -count=1` → FAIL.

- [ ] **Step 3: Implement**

DSNs exactly A§4.1 (escaped `file:` URI via `url.URL{Scheme: "file", OmitHost: true, Path: absPath, RawQuery: q.Encode()}`). Pools: writer `SetMaxOpenConns(1)`, `SetMaxIdleConns(1)`, no lifetime; reader 4; snapshot 1; staging one `*sql.DB` with 1 connection used as Writer and Reader. `Open` stats the file first ("database not found; run `tkr init`"), opens the three pools, pings each, runs `QuickCheck` on Writer and requires `sqlite_version() ≥ 3.44`. Pragma verification (AIDEV-NOTE anchor): `sqlite.RegisterConnectionHook` in `init()`; the hook rejects a DSN missing any common parameter ("connection not built by store.DSN"), then checks `journal_mode` (as requested), `synchronous`=2, `foreign_keys`=1, `busy_timeout`=5000, `secure_delete`=1, `trusted_schema`=0, `query_only` (as requested, else 0), defensive (`PRAGMA writable_schema=ON` reads back 0) and dqs (`SELECT "taskronin_dqs_probe"` must fail). `MapErr`: `*sqlite.Error` code `SQLITE_BUSY`/`SQLITE_LOCKED` → `CodeBusy` (RetryAfter 1 s); message containing `taskronin: ` → `CodeConflict`, Detail = the text after it; `SQLITE_CONSTRAINT_UNIQUE`/`PRIMARYKEY`/`FOREIGNKEY` → `CodeConflict`; `SQLITE_CONSTRAINT_CHECK`/`NOTNULL` → `CodeValidation`; `sql.ErrNoRows` → `ErrNotFound`; `nil`, `ErrNotFound`, `*domain.Error`, context errors unchanged; else `CodeInternal` wrapping the cause.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/store -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat(store): DSNs, pools, pragma verification hook and error mapping" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: Migrations, `0001_init.sql` and schema behaviour

**Files:**
- Create: `internal/store/migrate.go`, `internal/store/migrations/0001_init.sql`, `internal/store/fts.go`
- Create: `internal/store/helpers_test.go`, `internal/store/migrate_test.go`, `internal/store/schema_test.go`

**Interfaces:**
- Consumes: Task 7 `Store`, `OpenStaging`, `Open`, `MapErr`, `IntegrityCheck`.
- Produces:

```go
type SchemaStatus struct{ Current, Latest int; Pending []int }
var (
	ErrSchemaTooNew     = errors.New("store: database schema is newer than this binary")
	ErrChecksumMismatch = errors.New("store: migration checksum mismatch")
)
func (s *Store) SchemaStatus(ctx context.Context) (SchemaStatus, error)
func (s *Store) Migrate(ctx context.Context, preMigrate func(context.Context) error) ([]int, error)
func FTSIntegrityCheck(ctx context.Context, q Q) error // content-checking integrity-check (rank 1) on tasks_fts and log_entries_fts
// test helpers (package store, helpers_test.go), used by Tasks 9–12:
func newTestStore(t testing.TB) *Store        // OpenStaging(temp) → Migrate(nil) → Close → Open; t.Cleanup closes
func mustExec(t testing.TB, q Q, query string, args ...any)
```

- [ ] **Step 1: Write `0001_init.sql` and the failing tests**

Copy the SQL block of A§5.2 **verbatim** (including its header comments) to `migrations/0001_init.sql`. Tests:

```go
func TestMigrationFilesFrozen(t *testing.T) {
	frozen := map[string]string{ // AIDEV-NOTE: a committed migration never changes; add NNNN_*.sql instead
		"0001_init.sql": "<sha256 recorded when this test is first written>",
	}
	/* sha256 of each embedded file == frozen[name]; every embedded file is in the map */
}
```

Also: `TestMigrateEmpty` (`Migrate` applies `[1]`; `schema_migrations` row `(1, "init", sha256(file))`; `foreign_key_check` empty; `IntegrityCheck` ok; `SchemaStatus` = `{1, 1, []}`), `TestMigrateNoop` (second `Migrate` returns `[]` and never calls `preMigrate`), `TestPreMigrateFailureAborts` (pending + `preMigrate` error → nothing applied), `TestSchemaTooNew` (row `version 999` → `ErrSchemaTooNew`), `TestChecksumMismatch` (altered checksum → `ErrChecksumMismatch`), `TestMigrateEveryPriorVersion` (loops over `testdata/schema_v*.sql` fixtures — none exist yet — and the empty DB; each migrates to latest cleanly), `TestOverlapTrigger` (planned 13:00–15:00 vs 14:59–16:00 → `MapErr` conflict `overlapping planned block`; 15:00–16:00 accepted; a `cancelled` row ignored; un-deleting/re-planning into an overlap via UPDATE → conflict), `TestNoDeleteTriggers` (DELETE from tasks, log_entries, time_blocks, interruptions, ops, events → conflict), `TestEventsFrozen` (UPDATE `op_id` → conflict; UPDATE `after_json` allowed), `TestEntryGuards` (kind change → conflict; relinking a `complete` entry → conflict), `TestBlockTaskFixed`, `TestDateChecks` (`'2026-02-30'` in `tasks.due_date` → validation), `TestFTSTriggers` (insert task "keycloak cluster" → `tasks_fts MATCH 'keycloak'` finds it; set `deleted_at` → gone; clear it → back; retitle → old term gone; `FTSIntegrityCheck` ok after each step; same for `log_entries`), `TestOwnerTokenUnique` (two unrevoked `is_owner=1` rows → conflict; name `owner.x` with `is_owner=0` → validation).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store -race -count=1` → FAIL.

- [ ] **Step 3: Implement** A§5.1: `//go:embed migrations/*.sql`; bootstrap `schema_migrations` (A§5.1 DDL) created by the migrator; files applied in numeric order (`NNNN_name.sql` → version, name); per file `BEGIN IMMEDIATE` → one `ExecContext` of the whole file → insert the `schema_migrations` row (`applied_at` = `CAST(strftime('%s','now') AS INTEGER) * 1000`, computed by SQLite so `store` never reads the Go clock) → `COMMIT`; afterwards `PRAGMA foreign_key_check` must be empty and `IntegrityCheck` ok. `SchemaStatus`: an applied version absent from the binary → `ErrSchemaTooNew`; a checksum ≠ embedded → `ErrChecksumMismatch`. Record the file's real SHA-256 in `frozen`; from then on the SQL file is never touched.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/store -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): embedded migrations with 0001_init schema, triggers and FTS" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: `store` tasks, rank, triage, tags, focus, settings

**Files:**
- Create: `internal/store/{tasks.go, rank.go, triage.go, tags.go, focus.go, settings.go}`, `internal/store/tasks_test.go`

**Interfaces:**
- Consumes: Task 7 `Q`, `MapErr`, `ErrNotFound`; Task 8 `newTestStore`, `mustExec`; Task 5 domain entities.
- Produces (all write errors pass through `MapErr`; missing rows → `ErrNotFound`; `""` ↔ `NULL` for optional strings, `sql.Null` ↔ `NULL` otherwise; instants as unix ms):

```go
func InsertTask(ctx context.Context, q Q, t *domain.Task) error                 // sets t.ID; Version 1
func GetTask(ctx context.Context, q Q, id domain.TaskID) (domain.Task, error)
func UpdateTask(ctx context.Context, q Q, t domain.Task) error                  // every column except id, rank, created_at, updated_at, version, purged_at
func BumpVersion(ctx context.Context, q Q, ref domain.EntityRef, now time.Time) error // version+1; updated_at = now for task/entry; block/focus: version only
type TaskQuery struct {
	View         domain.View
	Tag, Project string // exact case-insensitive tag; project X or a dotted descendant (A§3.7 SQL)
	AfterKeys    []any  // keyset: sort keys of the previous page's last row (nil = first page)
	AfterID      int64
	Limit        int
}
func ListTasks(ctx context.Context, q Q, tq TaskQuery) ([]domain.Task, error)
type RankPos struct{ ID domain.TaskID; Rank int64 }
func RankBounds(ctx context.Context, q Q) (lo, hi sql.Null[int64], err error)     // over ALL tasks
func RankPrev(ctx context.Context, q Q, p RankPos) (sql.Null[RankPos], error)     // predecessor in global (rank, id) order
func RankNext(ctx context.Context, q Q, p RankPos) (sql.Null[RankPos], error)
func SetRank(ctx context.Context, q Q, id domain.TaskID, rank int64) error
func RenormalizeRanks(ctx context.Context, q Q) error                               // A§4.8 statement; versions untouched
func RankTaken(ctx context.Context, q Q, id domain.TaskID, rank int64) (bool, error) // another task holds rank (I7)
func AddReason(ctx context.Context, q Q, row domain.TriageRow) (bool, error)        // ON CONFLICT DO NOTHING; false = row existed (kept as is)
func RemoveReason(ctx context.Context, q Q, task domain.TaskID, r domain.TriageReason) (sql.Null[domain.TriageRow], error) // the deleted row
func Reasons(ctx context.Context, q Q, task domain.TaskID) ([]domain.TriageRow, error) // highest reason first (TriageOrder), then seq
func TaskTags(ctx context.Context, q Q, task domain.TaskID) ([]string, error)       // sorted case-insensitively
func SetTaskTags(ctx context.Context, q Q, task domain.TaskID, names []string, now time.Time) error // reuses an existing tag row case-insensitively (stored casing wins)
type TagCount struct{ Name string; Count int }
func TagCounts(ctx context.Context, q Q) ([]TagCount, error)                         // open, non-deleted tasks; zero counts omitted; by name
func GetFocus(ctx context.Context, q Q) (domain.Focus, error)
func PutFocus(ctx context.Context, q Q, f domain.Focus) error                        // task_id, date, set_at
func GetSetting(ctx context.Context, q Q, key string) (string, error)
func SetSetting(ctx context.Context, q Q, key, value string) error                   // upsert
type AuthState struct{ PasswordHash string; PasswordChangedAt time.Time; WebLoginDisabled bool; IdemHMACKey [32]byte }
func GetAuthState(ctx context.Context, q Q) (AuthState, error)
func PutAuthState(ctx context.Context, q Q, a AuthState) error                       // upsert row id 1
```

`ListTasks` classification (valid after reconcile, when an open task's `defer_date` is set only while it is deferred) and keyset order — the service builds cursors from the same keys. `top_rank` is a SQL `CASE` that must equal `domain.TriageReason.Rank()` (`slipped` 8 … `new` 1); every key is an `int64` except `defer_date` (ISO date text):

| View | WHERE | ORDER BY | keys |
|---|---|---|---|
| triage | open, not deleted, `defer_date IS NULL`, ≥ 1 reason | top reason rank DESC, that row's seq ASC, id | `[top_rank, top_seq]` |
| backlog | open, not deleted, `defer_date IS NULL`, no reason | rank, id | `[rank]` |
| upcoming | open, not deleted, `defer_date IS NOT NULL` | defer_date, COALESCE(defer_min, 0), rank, id | `[defer_date, defer_min_or_0, rank]` |
| done / dropped | that status, not deleted | completed_at / updated_at DESC, id DESC | `[ms]` |
| deleted | `deleted_at IS NOT NULL` | deleted_at DESC, id DESC | `[ms]` |
| all | not deleted | id | `[]` |

- [ ] **Step 1: Write the failing tests**

```go
func TestListTasksFiltersAndPaging(t *testing.T) {
	st := newTestStore(t)
	// fixture: backlog tasks a(rank 3·Gap), b(1·Gap), c(2·Gap); c project "infra.keycloak", b project "infrastructure",
	// a tagged "Infra"; one triage task with reasons {new seq 4, slipped seq 9}; one deferred; one done; one deleted
	ids := func(v domain.View, tag, project string) []domain.TaskID { /* ListTasks, Limit 100 */ }
	if got := ids(domain.ViewBacklog, "", ""); !slices.Equal(got, []domain.TaskID{b, c, a}) {
		t.Fatalf("backlog = %v", got)
	}
	if got := ids(domain.ViewBacklog, "", "infra"); !slices.Equal(got, []domain.TaskID{c}) {
		t.Fatalf("project infra = %v", got) // never b ("infrastructure")
	}
	if got := ids(domain.ViewBacklog, "infra", ""); !slices.Equal(got, []domain.TaskID{a}) {
		t.Fatalf("tag infra = %v", got)
	}
	/* Limit 1 paging with AfterKeys/AfterID returns b, c, a in three pages; triage with two tasks of equal top reason
	   pages correctly with AfterKeys [top_rank, top_seq]; every view returns its fixture task */
}
```

Also: `TestTaskRoundTrip` (all fields incl. null/optional, `Project ""`↔NULL, `Priority ""`↔NULL), `TestGetTaskNotFound`, `TestUpdateTask`, `TestBumpVersion` (task: version 2 and `updated_at` = now; raw-inserted block and focus: version only), `TestRankQueries` (prev/next with equal ranks broken by id; bounds; `RankTaken`), `TestRenormalizeRanks` (ranks become 65536·n in `(rank, id)` order; versions unchanged), `TestReasons` (rows new seq 1, due seq 5, slipped seq 9 → slipped, due, new; `AddReason` twice → false and `since`/`seq` unchanged; `RemoveReason` returns the row, absent → invalid `Null`), `TestTags` (`SetTaskTags(["Infra","db"])` → `["db","Infra"]`; another task `["infra"]` reuses the row (one `tags` row, name `Infra`); `"a b"` → validation), `TestTagCounts`, `TestFocus` (default `{nil, nil, nil, version 1}`; put/get), `TestSettings` (missing key → `ErrNotFound`; upsert), `TestAuthState`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the functions above (scan helpers per table in their files; `defer rows.Close()`, check `rows.Err()`).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/store -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): task, rank, triage, tag, focus and settings queries" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: `store` entries, blocks, interruptions

**Files:**
- Create: `internal/store/{entries.go, blocks.go, interruptions.go}`, `internal/store/entries_test.go`, `internal/store/blocks_test.go`

**Interfaces:**
- Consumes: Task 7 `Q`, `MapErr`, `ErrNotFound`; Task 8 `newTestStore`, `mustExec`; Task 5 `domain.LogEntry`, `domain.TimeBlock`, `domain.Interruption`, `domain.BlockSpan`; Task 9's NULL/ms mapping conventions.
- Produces:

```go
func InsertEntry(ctx context.Context, q Q, e *domain.LogEntry) error              // sets ID; Version 1
func GetEntry(ctx context.Context, q Q, id domain.EntryID) (domain.LogEntry, error)
func UpdateEntry(ctx context.Context, q Q, e domain.LogEntry) error               // every column except id, kind, created_at, updated_at, version, purged_at
type EntryQuery struct {
	From, To       civil.Date              // zero = unbounded
	Task           *sql.Null[domain.TaskID] // nil = any; invalid Null = standalone only
	IncludeDeleted bool
	Tag, Project   string                  // either excludes standalone entries
	AfterKeys      []any                   // [work_date, untimed 0|1, occurred_min_or_0, created_at_ms]
	AfterID        int64
	Limit          int
}
func ListEntries(ctx context.Context, q Q, eq EntryQuery) ([]domain.LogEntry, error) // work_date DESC, timed first, occurred_min DESC, created_at DESC, id DESC
func ReportEntries(ctx context.Context, q Q, from, to civil.Date, tag, project string) ([]domain.LogEntry, error) // non-deleted; work_date ASC, timed first by occurred_min, created_at, id
func CountEntriesOn(ctx context.Context, q Q, task domain.TaskID, d civil.Date) (work, complete int, err error)   // non-deleted
func EntriesOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.LogEntry, error) // non-deleted, newest first
func CompleteEntries(ctx context.Context, q Q, task domain.TaskID) ([]domain.LogEntry, error) // every complete entry incl. deleted/purged, id ASC

func InsertBlock(ctx context.Context, q Q, b *domain.TimeBlock) error
func GetBlock(ctx context.Context, q Q, id domain.BlockID) (domain.TimeBlock, error)
func UpdateBlock(ctx context.Context, q Q, b domain.TimeBlock) error              // every column except id, task_id, created_at, version
func BlocksOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.TimeBlock, error) // non-deleted; date, start, id
func BlocksBetween(ctx context.Context, q Q, from, to civil.Date, task sql.Null[domain.TaskID]) ([]domain.TimeBlock, error) // non-deleted, inclusive
func PlannedOverlapping(ctx context.Context, q Q, span domain.BlockSpan, exclude ...domain.BlockID) ([]domain.TimeBlock, error) // half-open
func PlannedOn(ctx context.Context, q Q, d civil.Date) ([]domain.TimeBlock, error)
func PlannedBefore(ctx context.Context, q Q, d civil.Date) ([]domain.TimeBlock, error)     // date < d; date, start, id
func PlannedOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.TimeBlock, error)
func AutoResolvedOn(ctx context.Context, q Q, task domain.TaskID, d civil.Date) ([]domain.TimeBlock, error) // outcome_source = 'auto'
func HasPlannedAfter(ctx context.Context, q Q, task domain.TaskID, d civil.Date) (bool, error)
func HasPlannedOn(ctx context.Context, q Q, task domain.TaskID, d civil.Date) (bool, error)
func OtherSlippedSince(ctx context.Context, q Q, task domain.TaskID, exclude domain.BlockID, seq int64) (bool, error) // slipped=1 AND slipped_seq > seq
func OtherWorkedSince(ctx context.Context, q Q, task domain.TaskID, exclude domain.BlockID, seq int64) (bool, error)  // status='worked' AND resolved_seq > seq
func SlipCounts(ctx context.Context, q Q, task domain.TaskID, lastPlacedSeq int64) (lifetime, sinceTriage int, err error)

func InsertInterruption(ctx context.Context, q Q, in *domain.Interruption) error
func GetInterruption(ctx context.Context, q Q, id domain.InterruptionID) (domain.Interruption, error)
func InterruptionsOn(ctx context.Context, q Q, d civil.Date) ([]domain.Interruption, error)          // non-deleted, by at
func InterruptionsOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.Interruption, error) // from or to the task, non-deleted
```
All "non-deleted" unless named otherwise; every block query excludes `deleted_at IS NOT NULL` rows.

- [ ] **Step 1: Write the failing tests**

```go
func TestPlannedOverlapping(t *testing.T) {
	st := newTestStore(t)
	b1 := insertBlock(t, st, task, "2026-10-28", 780, 900, domain.BlockPlanned)
	insertBlock(t, st, task, "2026-10-28", 600, 700, domain.BlockCancelled) // ignored
	for _, tc := range []struct {
		start, end int
		want       []domain.BlockID
	}{{900, 960, nil}, {720, 780, nil}, {899, 960, []domain.BlockID{b1}}, {720, 781, []domain.BlockID{b1}},
		{810, 840, []domain.BlockID{b1}}, {720, 960, []domain.BlockID{b1}}} { /* … */ }
	/* exclude b1 → nil */
}
```

Also: `TestEntryRoundTrip`, `TestListEntriesOrderAndFilters` (order rule above; `Task` nil / invalid Null / value; `IncludeDeleted`; tag and project filters drop standalone entries; keyset paging), `TestReportEntries` (ascending, timed first), `TestCountEntriesOn` (deleted excluded, kinds split), `TestCompleteEntries`, `TestBlockRoundTrip`, `TestPlannedQueries` (`PlannedOn`, `PlannedBefore`, `PlannedOfTask`, `HasPlannedAfter`, `HasPlannedOn`), `TestAutoResolvedOn` (user-resolved and planned excluded), `TestOtherSinceQueries`, `TestSlipCounts` (two slipped blocks with seqs 3 and 7, lastPlacedSeq 5 → 2, 1), `TestInterruptions`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the functions above.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/store -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): entry, block and interruption queries" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: `store` ops, events, revert columns, idempotency, undo stacks, tokens, sessions, bootstrap

**Files:**
- Create: `internal/store/{ops.go, events.go, columns.go, idem.go, undo.go, tokens.go, sessions.go, bootstrap.go}`, `internal/store/ops_test.go`, `internal/store/auth_test.go`

**Interfaces:**
- Consumes: Task 7 `Q`, `MapErr`, `ErrNotFound`; Task 8 `newTestStore`, `mustExec`; Task 9 `AuthState`, `SetSetting`; Task 5 `domain.OpRecord`, `domain.Event`, `domain.EntityRef`, `domain.Token`.
- Produces:

```go
func InsertOp(ctx context.Context, q Q, r *domain.OpRecord) error      // sets r.Seq (ops.seq AUTOINCREMENT)
func GetOp(ctx context.Context, q Q, id domain.OpID) (domain.OpRecord, error)
func SetOpState(ctx context.Context, q Q, id domain.OpID, s domain.OpState) error
func ActiveReversal(ctx context.Context, q Q, target domain.OpID) (sql.Null[domain.OpRecord], error) // active op with reverts_op_id = target
func LatestSeq(ctx context.Context, q Q) (domain.OpSeq, error)
func CountOpsSince(ctx context.Context, q Q, actor string, since time.Time) (n int, oldest sql.Null[time.Time], err error) // at > since
type OpQuery struct {
	Actor               string
	AIOnly              bool // actor LIKE 'token:%' OR kind = 'settings.retention_lowered'
	SinceSeq, BeforeSeq domain.OpSeq // exclusive bounds; 0 = none
	Limit               int
}
func ListOps(ctx context.Context, q Q, oq OpQuery) ([]domain.OpRecord, error) // newest first
func CountAIOps(ctx context.Context, q Q, afterSeq domain.OpSeq) (int, error)
func InsertEvent(ctx context.Context, q Q, e *domain.Event) error            // sets e.ID; NULL for empty Before/After
func EventsOfOp(ctx context.Context, q Q, op domain.OpID) ([]domain.Event, error) // id ASC
type EventKey struct{ Entity domain.EntityKind; ID int64; Reason domain.TriageReason } // Reason only for triage keys
type EventOp struct{ domain.Event; Op domain.OpRecord }
func EventsAfter(ctx context.Context, q Q, k EventKey, afterID domain.EventID) ([]EventOp, error) // any op; triage matches $.reason of after or before
func TaskEvents(ctx context.Context, q Q, task domain.TaskID, beforeID domain.EventID, limit int) ([]EventOp, error) // events.task_id = task, id DESC
func OpTaskIDs(ctx context.Context, q Q, op domain.OpID) ([]domain.TaskID, error)
func ApplyColumns(ctx context.Context, q Q, ent domain.EntityKind, id int64, cols map[string]any) error
	// revert: writes event-payload columns verbatim in one UPDATE. Allowlist — task: title, notes, status, rank,
	// last_placed_at, last_placed_seq, defer_date, defer_min, due_date, due_min, due_ack, due_ack_date, project,
	// priority, completed_at, completed_date, deleted_at; entry: task_id, title, notes, work_date, occurred_min,
	// reopened, deleted_at; block: date, start_min, end_min, status, outcome_source, cancel_reason, slipped,
	// slipped_at, slipped_seq, resolved_at, resolved_seq, deleted_at; interruption: deleted_at; focus: task_id,
	// date, set_at. json.Number → int64; bool → 0/1; unknown column or entity → error.

type IdemRow struct {
	Actor, Key string
	Hash       [32]byte
	OpID       sql.Null[domain.OpID]
	Status     int // 200 | 201 | 409
	Refs       []domain.EntityRef
	ErrorJSON  []byte // 409 only
	CreatedAt  time.Time
}
func GetIdem(ctx context.Context, q Q, actor, key string) (IdemRow, error)
func PutIdem(ctx context.Context, q Q, r IdemRow) error                     // INSERT OR REPLACE
func DeleteIdemBefore(ctx context.Context, q Q, before time.Time) (int64, error)
func ClearIdempotency(ctx context.Context, q Q) error                        // unconditional DELETE (restore step 4, Plan B; ruling P7)

type Stack string
const (StackUndo Stack = "undo"; StackRedo Stack = "redo")
func PushStack(ctx context.Context, q Q, actor string, s Stack, op domain.OpID) error // seq = max+1 in (actor, stack)
func TopOfStack(ctx context.Context, q Q, actor string, s Stack) (seq int64, op domain.OpID, ok bool, err error)
func DeleteStackRow(ctx context.Context, q Q, actor string, s Stack, seq int64) error
func ClearStack(ctx context.Context, q Q, actor string, s Stack) error
func TrimStack(ctx context.Context, q Q, actor string, s Stack, depth int) error     // keep the newest depth rows
func StackOps(ctx context.Context, q Q, actor string, s Stack) ([]domain.OpID, error) // bottom → top

func InsertToken(ctx context.Context, q Q, t *domain.Token) error
func GetToken(ctx context.Context, q Q, id domain.TokenID) (domain.Token, error)
func ListTokens(ctx context.Context, q Q) ([]domain.Token, error)
func RevokeToken(ctx context.Context, q Q, id domain.TokenID, at time.Time) error
func RevokeAllTokens(ctx context.Context, q Q, at time.Time) ([]domain.TokenID, error) // the previously unrevoked ones
func TouchTokens(ctx context.Context, q Q, last map[domain.TokenID]time.Time) error   // last_used_at = max(stored, given)

type Session struct {
	IDHash, CSRFSecret              [32]byte
	Remote                          string
	CreatedAt, ExpiresAt, LastSeenAt time.Time
}
func InsertSession(ctx context.Context, q Q, s Session) error
func GetSession(ctx context.Context, q Q, idHash [32]byte) (Session, error)
func DeleteSession(ctx context.Context, q Q, idHash [32]byte) error
func DeleteAllSessions(ctx context.Context, q Q) (int64, error)
func TouchSessions(ctx context.Context, q Q, last map[[32]byte]time.Time) error
func DeleteExpiredSessions(ctx context.Context, q Q, now time.Time, idle time.Duration) (int64, error) // expires_at ≤ now OR last_seen_at ≤ now − idle

type Bootstrap struct {
	Generation domain.Generation
	TZ         string
	Auth       AuthState
	Owner      domain.Token // IsOwner, Name "owner.<ts>-<hex>", AllScopes; ID ignored
}
func BootstrapDB(ctx context.Context, q Q, b Bootstrap) (domain.TokenID, error) // inserts db_generation, updates tz_last_seen, auth_state, owner row (used by offline init and testutil)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestEventsAfterTriageKey(t *testing.T) {
	st := newTestStore(t)
	a := insertOp(t, st, "task.add")
	b := insertOp(t, st, "task.place")
	e1 := insertEvent(t, st, a, domain.EntTriage, 1, domain.ActAdd, `{"reason":"new","since":1,"seq":1}`)
	insertEvent(t, st, b, domain.EntTriage, 1, domain.ActRemove, `{"reason":"new","since":1,"seq":1}`) // before_json
	insertEvent(t, st, b, domain.EntTriage, 1, domain.ActAdd, `{"reason":"manual","since":2,"seq":2}`)
	got, err := EventsAfter(ctx, st.Reader, EventKey{Entity: domain.EntTriage, ID: 1, Reason: domain.ReasonNew}, e1.ID)
	if err != nil || len(got) != 1 || got[0].Op.ID != b.ID {
		t.Fatalf("got %+v, %v", got, err)
	}
}
```

Also: `TestOpRoundTrip` (seq strictly increasing; via + reverts_op_id stored together; `SetOpState`; `ActiveReversal`), `TestCountOpsSince`, `TestListOpsAIOnly` (token ops and a `settings.retention_lowered` op only, newest first, seq bounds), `TestTaskEvents` (newest first, `beforeID` paging, joined op), `TestApplyColumns` (writes listed columns; `json.Number` → int64; unknown column → error, row untouched), `TestIdem` (put/get, replace, `DeleteIdemBefore`, `ClearIdempotency` empties the table), `TestUndoStack` (push seq max+1; top; delete; clear; trim keeps newest N; actors isolated), `TestTokens` (insert/get/list/revoke/`RevokeAllTokens`/`TouchTokens` keeps the max), `TestSessions` (CRUD, touch, `DeleteExpiredSessions` for both expiry kinds), `TestBootstrapDB` (settings `db_generation` = gen, `tz_last_seen` = `America/Denver`, auth_state row, owner token `is_owner = 1`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the functions above. `Refs` stored as JSON `[{"entity":"task","id":42}]` (A§4.9).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/store -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): ops, events, revert columns, idempotency, undo stacks, tokens and sessions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 12: `store` search, scrub, sequences, export rows

**Files:**
- Create: `internal/store/{search.go, scrub.go, sequences.go, export.go}`, `internal/store/scrub_test.go`

**Interfaces:**
- Consumes: Task 7 `Q`, `MapErr`; Task 8 `newTestStore`, `mustExec`, `FTSIntegrityCheck`; Task 11 `InsertOp`, `InsertEvent` (fixtures).
- Produces:

```go
type FTSHit struct {
	Kind     string // "task" | "entry"
	ID       int64
	TaskID   sql.Null[domain.TaskID]
	Title    string
	Snippet  string // snippet(<fts>, -1, '', '', '…', 10): plain user text
	Status   domain.TaskStatus      // tasks only
	WorkDate sql.Null[civil.Date]   // entries only
	Score    float64                // bm25 (lower is better)
}
func Search(ctx context.Context, q Q, match, kind string, limit, offset int) ([]FTSHit, error) // kind task|entry|all; bm25, then kind, id
type ScrubResult struct{ Rows, Events, Ops int }
func (r ScrubResult) Changed() bool
func ScrubEntities(ctx context.Context, q Q, tasks []domain.TaskID, entries []domain.EntryID, now time.Time) (ScrubResult, error)
func Sequences(ctx context.Context, q Q) (map[string]int64, error)
func RaiseSequences(ctx context.Context, q Q, m map[string]int64) error // each name: max(current, given); inserts a missing row
const (ExportFormat = "taskronin-export"; ExportVersion = 1)
var ExportTables = []string{"tasks", "task_triage", "tags", "task_tags", "log_entries", "time_blocks",
	"focus", "interruptions", "ops", "events", "undo_stack"}
var ExportSettings = []string{"backup_keep_daily", "backup_keep_weekly", "ai_reviewed_through"}
type ExportFile struct { // A§12.4 layout; shared by service.Export and offline.Import (R4-10)
	Format        string                      `json:"format"`
	Version       int                         `json:"version"`
	SchemaVersion int                         `json:"schema_version"`
	ExportedAt    string                      `json:"exported_at"`
	DBGeneration  string                      `json:"db_generation"`
	Sequences     map[string]int64            `json:"sequences"`
	Settings      map[string]string           `json:"settings"`
	Tables        map[string][]map[string]any `json:"tables"`
}
func ExportTable(ctx context.Context, q Q, table string, fn func(row map[string]any) error) error // ExportTables only; primary-key order; values int64|string|nil
```

`ScrubEntities` (A§12.0; AIDEV-NOTE anchor): for every listed row with `purged_at IS NULL`, **one** `UPDATE … SET deleted_at = COALESCE(deleted_at, :now), purged_at = :now, title = '[purged]', notes = ''` (so the FTS trigger sees the old content); tasks also get `project = NULL`, their `task_tags` deleted, orphaned `tags` deleted; events: every `task`/`task_tags` event of a listed task and every `entry` event of a listed entry whose `before_json` or `after_json` still holds content (the A§12.0 predicate) gets `title`→`"[purged]"`, `notes`→`""`, `project`→`null`, `tags`→`[]` — **only keys already present** (`json_type(j, '$.k') IS NOT NULL`), independent of `ops.purged` (R3-1); `ops.purged = 1, revertible = 0` for every op with a `task`/`task_tags`/`triage` event on a listed task or an `entry` event on a listed entry and `purged = 0`. Counts only real changes, so a second call returns `Changed() == false`.

- [ ] **Step 1: Write the failing tests**

```go
func TestScrubEntities(t *testing.T) {
	st := newTestStore(t)
	// fixture: task 1 "secret keycloak" (project "infra", tag "db", live), entry 1 "secret entry" on task 1;
	// ops A (task:insert after {"title":"secret keycloak","project":"infra"}), B (entry:insert after {"title":"secret entry"}),
	// C (triage:add on task 1), D (block:update on an unrelated block)
	r, err := ScrubEntities(ctx, st.Writer, []domain.TaskID{1}, []domain.EntryID{1}, now)
	/* r.Changed(); task title "[purged]", notes "", project NULL, deleted_at set, purged_at = now; no task_tags, no "db" tag;
	   entry likewise; tasks_fts and log_entries_fts MATCH 'secret' → none; FTSIntegrityCheck ok;
	   A.after_json = {"title":"[purged]","project":null} (no "notes" key added); B rewritten; A, B, C purged=1 revertible=0; D untouched;
	   second call → !Changed() */
}
```

Also: `TestSearch` (task and entry hits; `kind` filter; tombstoned rows never hit; offset paging; bm25 order: a title match ranks above a notes-only match; snippet non-empty), `TestSequences` (`Sequences` after inserts; `RaiseSequences` never lowers, inserts a missing name; next insert uses raised value + 1), `TestExportTable` (rows as column maps in primary-key order; unknown table → error).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the functions above.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/store -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): full-text search, content scrub, sequences and export rows" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 13: `auth` tokens, passwords, password gate, sessions

**Files:**
- Create: `internal/auth/{token.go, password.go, gate.go, session.go}`, `internal/auth/auth_test.go`, `internal/auth/export_test.go`

**Interfaces:**
- Consumes: Task 2 `clock`; Task 5 `domain`; `golang.org/x/crypto v0.58.0`, `golang.org/x/time v0.16.0` (`go get` here).
- Produces: A§7.1 and A§7.4 verbatim (`TokenPrefix`, `NewSecret`, `FormatToken`, `ParseToken`, `HashSecret`, `ArgonTime`… `SaltLen`, `HashPassword`, `VerifyPassword`, `ValidateNewPassword`, `PasswordGate`, `NewPasswordGate`, `(*PasswordGate).Verify`), plus:

```go
func OwnerTokenName(now time.Time) (string, error)   // "owner.<YYYYMMDDTHHMMSS.mmmZ>-<4 hex>" (A§7.2), UTC
func NewSessionID() (raw [32]byte, cookie string, err error) // cookie = base64url, no padding
func ParseSessionCookie(cookie string) ([32]byte, error)
func HashSessionID(raw [32]byte) [32]byte               // SHA-256
func NewCSRFSecret() ([32]byte, error)
func CSRFToken(secret [32]byte) string                  // base64url(HMAC-SHA256(secret, "tkr-csrf-v1")) (A§7.5)
// export_test.go: func hashForTest(pw string) string — argon2id with t=1, m=8 KiB, p=1 for fast gate tests
```

- [ ] **Step 1: Write the failing tests**

```go
func TestParseTokenRejectsNonCanonical(t *testing.T) {
	raw, _ := NewSecret()
	good := FormatToken(42, raw)
	if id, got, err := ParseToken(good); err != nil || id != 42 || got != raw {
		t.Fatalf("round trip: %v %v %v", id, got, err)
	}
	sec := strings.TrimPrefix(good, "tkr_42_")
	for _, bad := range []string{"tkr_0_" + sec, "tkr_042_" + sec, "tkr_42_" + sec[:42], "tkr_42_" + sec + "=",
		"tkr_9223372036854775808_" + sec, "TKR_42_" + sec, "tkr_42_" + sec[:42] + "!"} {
		if _, _, err := ParseToken(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	/* a secret whose last base64 char has non-zero padding bits → error (re-encoding must equal input) */
}
```

Also: `TestHashSecret`, `TestOwnerTokenName` (2026-10-28T16:00:00.123Z → prefix `owner.20261028T160000.123Z-`, matches `^owner\.\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{4}$`), `TestHashVerifyPassword` (prefix `$argon2id$v=19$m=65536,t=3,p=1$`; right/wrong password; malformed encoding → error; encoded params above m 1 GiB, t 10 or p 4 → error), `TestValidateNewPassword` (7 bytes, 1025 bytes, invalid UTF-8 → error; 8 and 1024 ok), `TestPasswordGate` (fake clock, `hashForTest`: five immediate `Verify` calls for key `"socket"` succeed without waiting; the sixth blocks until the fake clock advances 5 s; a wait that would exceed 15 s → `*domain.Error` `CodeRateLimited` with `RetryAfter` > 0; a canceled ctx aborts the wait; global bucket shared across keys), `TestSessionCookie` (43-char cookie round-trips; bad input → error), `TestCSRFToken` (equals an HMAC computed in the test), `FuzzParseToken` (no panic; accepted ⇒ `FormatToken(id, raw)` == input).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/auth -race -count=1` → FAIL.

- [ ] **Step 3: Implement**

Token regex `^tkr_([1-9][0-9]{0,18})_([A-Za-z0-9_-]{43})$`; the id must fit `int64`; the secret must re-encode to itself. `VerifyPassword` reads params from the encoding (bounded as tested) and compares derived keys with `subtle.ConstantTimeCompare`. `PasswordGate` (A§7.4): semaphore `chan struct{}` cap 1; global `rate.NewLimiter(1, 5)`; per-key limiter `rate.Every(5*time.Second)` burst 5 in a 1024-entry LRU; reservations use `ReserveN(clock.Now(), 1)` (never the limiter's own clock); delay > 15 s → cancel and return `CodeRateLimited` with `RetryAfter = delay`; otherwise wait on `clock.After(delay)` or `ctx.Done()`, then hold the semaphore for one argon2 verification. No lockout.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/auth -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/auth
git commit -m "feat(auth): token format, argon2id passwords, password gate and session secrets" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 14: `fsutil` and the purge ledger

**Files:**
- Create: `internal/fsutil/atomic.go`, `internal/fsutil/perms.go`, `internal/fsutil/fsutil_test.go`
- Create: `internal/backup/ledger.go`, `internal/backup/ledger_test.go`

**Interfaces:**
- Consumes: Task 5 `domain.TaskID`/`EntryID`; `golang.org/x/sys/unix` (`go get golang.org/x/sys@v0.49.0`).
- Produces:

```go
package fsutil
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error // ".tkr-tmp-*" temp in the same dir → write → fsync → chmod → rename → fsync dir
func FsyncDir(dir string) error
func CheckPrivate(path string) error   // Lstat: regular file (no symlink), owned by the euid, perm & 0o077 == 0
func EnsureDir0700(path string) error  // MkdirAll 0700; an existing dir owned by the euid is chmod'ed to 0700; other owner → error
func SetUmask(mask int) int            // unix.Umask

package backup
// A§12.6 verbatim: LedgerRecord (incl. RecoveryFiles, H1), LedgerHealth, Ledger{Path; mu}, OpenLedger,
// (*Ledger).Append, (*Ledger).Records, (*Ledger).Repair — plus:
func (l *Ledger) Health() LedgerHealth
func (l *Ledger) Pending() ([]LedgerRecord, error) // records whose Rec has no later "complete" line
const (LedgerPending = "pending"; LedgerComplete = "complete")
```

Semantics (A§12.6): a missing file is an empty, healthy ledger (created 0600 by the first `Append`). `OpenLedger` truncates a final line lacking `\n` back to the last complete line and fsyncs (AIDEV-NOTE: torn-tail truncation); any complete line that fails to parse or whose `Sum` is wrong → `Damaged = true`, its 1-based number in `DroppedLines`, the line kept on disk and skipped. `Append` computes `Sum` itself (SHA-256 hex of `json.Marshal` of the record with `Sum = ""`), refuses while damaged, writes one line with `O_APPEND|O_CREATE|O_WRONLY` 0600, fsyncs the file and the directory. Valid records are held in memory after `OpenLedger` and appended to by `Append`. `Repair` rewrites the file from the valid records via `fsutil.WriteFileAtomic` and returns the dropped line numbers; afterwards `Health().Damaged == false`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPRG08(t *testing.T) { // ledger line format
	l, h, err := OpenLedger(filepath.Join(t.TempDir(), "purge-ledger"))
	/* h.Damaged false; Append({V:1, Rec: rec, At: ms, State: "pending", Tasks: [42], Entries: [101]});
	   Append(same Rec, State "complete", RecoveryFiles "deleted") */
	lines := readLines(t, l.Path)
	re := regexp.MustCompile(`^\{"v":1,"rec":"[0-9a-f]{32}","at":\d+,"state":"pending","tasks":\[42\],"entries":\[101\],"sum":"[0-9a-f]{64}"\}$`)
	if len(lines) != 2 || !re.MatchString(lines[0]) || !strings.Contains(lines[1], `"state":"complete"`) ||
		!strings.Contains(lines[1], `"recovery_files":"deleted"`) {
		t.Fatalf("ledger = %q", lines)
	}
	/* file mode 0600; Sum recomputed by the test equals the stored one */
}
```

Also: `TestPRG13` (file ends with half a line → `OpenLedger` truncates it, `Damaged` false, earlier records kept, the next `Append` starts on a fresh line), `TestPRG14` (a newline-terminated line with a wrong `sum`, and separately an unparsable line mid-file → `Damaged`, `DroppedLines` = [n], bad line still on disk, valid records returned, `Append` → error), `TestPRG15` (`Repair` drops the bad line, returns [n], file holds only valid lines, `Health().Damaged` false, `Append` works), `TestLedgerPending` (pending without complete → listed; after its complete line → empty), `TestLedgerMissingFile`, `FuzzLedgerLines` (A§13.3: arbitrary contents → no panic; every valid line kept; only a final line lacking `\n` truncated; any complete bad line → damaged), `TestWriteFileAtomic` (mode 0600, content, no `.tkr-tmp-*` left, replaces an existing file), `TestCheckPrivate` (0600 ok; 0644, a symlink, a directory → error), `TestEnsureDir0700` (creates; tightens 0755 → 0700).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/fsutil ./internal/backup -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the functions above.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/fsutil ./internal/backup -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/fsutil internal/backup
git commit -m "feat(backup): fsutil helpers and checksummed purge ledger" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 15: `backup` snapshot manager — snapshot, verify, rotate, list

**Files:**
- Create: `internal/backup/snapshot.go`, `verify.go`, `rotate.go`, `internal/backup/snapshot_test.go`

**Interfaces:**
- Consumes: Task 7 `store.OpenStaging`, `IntegrityCheck`; Task 8 `store.FTSIntegrityCheck`, `newTestStore`-equivalent (build a migrated store in the test with `store.OpenStaging` → `Migrate` → `Close` → `store.Open`); Task 2 `clock`; Task 14 `fsutil.FsyncDir`.
- Produces: A§12.1 verbatim (`Kind`, `SnapshotInfo`, `Retention`, `Manager{Dir; Clock; Snap; mu}` used as a struct literal, `(*Manager).Snapshot`, `Verify`, `(*Manager).Rotate`, `List`; `Inventory` is Plan B's), plus:

```go
const (KindDaily Kind = "daily"; KindManual Kind = "manual"; KindPremigrate Kind = "premigrate"; KindPrescrub Kind = "prescrub")
const (MinKeepDaily = 3; MinKeepWeekly = 1)
func ParseSnapshotName(name string) (SnapshotInfo, bool) // "taskronin-<YYYYMMDDTHHMMSS.mmmZ>-<4 hex>-<kind>.db"
```

Rules: steps 1–4 of A§12.1 (settings updates are the service's job, Task 36). `Verify` stats the path (missing → error), opens it with `store.OpenStaging`, requires `integrity_check` ok and `FTSIntegrityCheck` ok, reads `MAX(version)` from `schema_migrations`, closes, and fails if a `-wal`, `-shm` or `-journal` sidecar remains. `Rotate` (UTC days and ISO weeks): keep the newest snapshot of each of the last `Daily` days, the newest of each of the last `Weekly` ISO weeks, every snapshot younger than 24 h, and the newest 3 overall; delete only files matching the name pattern; `r` below the floors → error. `List` parses names (no file is opened; `SchemaVersion` stays 0) and stats sizes, newest first.

- [ ] **Step 1: Write the failing tests**

```go
func TestRotate(t *testing.T) {
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))
	names := touchSnapshots(t, dir, // empty files with valid names
		"2026-10-28T03:30Z", "2026-10-27T03:30Z", "2026-10-27T01:00Z", "2026-10-26T03:30Z", "2026-10-25T03:30Z",
		"2026-10-20T03:30Z", "2026-10-13T03:30Z")
	touch(t, filepath.Join(dir, "notes.txt")); mkdir(t, filepath.Join(dir, "taskronin.db.pre-restore-x"))
	m := &Manager{Dir: dir, Clock: clk}
	removed, err := m.Rotate(ctx, Retention{Daily: 3, Weekly: 2})
	/* kept: 10-28, 10-27 03:30, 10-26 (newest of the last 3 days), 10-27 01:00 (newest 3 overall), 10-25 (newest of
	   ISO week 43); removed exactly: 10-20, 10-13; notes.txt and the pre-restore dir untouched;
	   Retention{Daily: 2, Weekly: 1} → error (below the floor) */
}
```

Also: `TestSnapshotRoundTrip` (store with one task → `Snapshot(ctx, KindManual)`: name matches the pattern, mode 0600, `Verify` returns schema 1, no sidecars, no `.tkr-tmp-*` left, the task row is in the copy), `TestSnapshotConcurrent` (two goroutines → two distinct valid files), `TestSnapshotCanceled` (canceled ctx → error and no temp file left), `TestSnapshotCrashPoints` (design §12 crash-during-backup: an `export_test.go` hook `setCrashHook(m *Manager, fn func(step string) error)` stops `Snapshot` without any cleanup — a simulated process death — at `"vacuumed"`, `"verified"` and `"renamed"`; after each, an older snapshot is intact and `Verify`s, `List` shows only complete snapshots (a leftover `.tkr-tmp-*` never matches the name pattern), and a snapshot published at `"renamed"` `Verify`s; a following normal `Snapshot` succeeds), `TestVerifyRejectsCorrupt` (truncated file → error; missing file → error), `TestList` (newest first, kinds parsed, foreign files ignored).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/backup -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above (`VACUUM INTO ?` on `m.Snap`; fall back to a single-quoted literal with `'` doubled only if the driver refuses the parameter).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/backup -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): verified snapshots with rotation" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 16a: Service skeleton — op runner, events, effects, reconcile framework, test harness

**Files:**
- Create: `internal/service/{service.go, op.go, events.go, columns.go, effects.go, reconcile.go, invariants.go, slip.go}`
- Create: `internal/service/export_test.go`, `internal/service/runner_test.go`, `internal/service/effects_test.go`
- Create: `internal/testutil/{clock.go, store.go, service.go, scenario.go}`

**Interfaces:**
- Consumes: Task 7 `store.Store`, `store.Q`, `store.Open`, `store.OpenStaging`, `store.MapErr`, `store.ErrNotFound`; Task 8 `(*store.Store).Migrate`, `store.FTSIntegrityCheck`; Task 9 `store.GetSetting`, `store.GetAuthState`, `store.BumpVersion`; Task 11 `store.InsertOp`, `store.InsertEvent`, `store.EventsOfOp`, `store.GetOp`, `store.PushStack`/`ClearStack`/`TrimStack`/`StackOps`, `store.BootstrapDB`, `store.Bootstrap`, `store.AuthState`; Task 13 `auth.HashPassword`, `auth.NewPasswordGate`, `auth.NewSecret`, `auth.HashSecret`, `auth.OwnerTokenName`; Task 14 `backup.OpenLedger`, `*backup.Ledger`; Task 15 `*backup.Manager`; Task 5 `domain` (A§2.4–2.8); Tasks 2–3 `clock.Fake`, `civil.Zone`.
- Produces (exported): A§3.1 `Options`, `New`, `Meta`, `Idem`, `OpResult`, `Effect`, `EffectKind` with one typed constant per A§3.1 name (`EffCreated EffectKind = "created"` … `EffPurged`), and:

```go
func (s *Service) Generation() domain.Generation // cached settings.db_generation; no ctx, no authz
func (s *Service) Zone() civil.Zone
func (s *Service) Reconcile(ctx context.Context) error // framework: cheap need-check on the reader, writer only when a step needs it
type Page[T any] struct{ Items []T; NextCursor string } // "" = last page
```

- Produces (package-internal, used by every later service task — keep these names):

```go
type opClass uint8
const (classForward opClass = iota + 1; classRevert; classUndo; classRedo)
type opSpec struct { // A§4.3
	kind                                domain.OpKind
	class                               opClass
	scopes                              domain.Scopes
	idem, revertible, ownerOnly, notWeb bool
	noReconcile, structuralOnly         bool
}
type pairKey struct{ Task domain.TaskID; Date civil.Date }
type op struct {
	s *Service; ctx context.Context; tx *sql.Tx; spec opSpec; rec domain.OpRecord; actor domain.Actor
	now time.Time; today civil.Date; gen domain.Generation; target *domain.OpRecord
	placed, rankMoved map[domain.TaskID]bool; dirty, inserted map[domain.EntityRef]bool; pairs map[pairKey]struct{}
	events []domain.Event; warnings []string; refs []domain.EntityRef; created, derived bool
}
func (s *Service) run(ctx context.Context, m Meta, spec opSpec, fn func(*op) error) (*OpResult, error) // AIDEV-NOTE: the single write path
func (s *Service) read(ctx context.Context, a domain.Actor, scopes domain.Scopes, reconcileFirst bool,
	fn func(q store.Q, now time.Time, today civil.Date) error) error // reader tx (ReadOnly); s.Reconcile first when asked
func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx, now time.Time) error) error // writer tx without an ops row
func (o *op) record(e domain.Event) error                         // sets OpID, Derived = o.derived; store.InsertEvent; appends o.events
func (o *op) recordTaskUpdate(before, after domain.Task) error     // diffCols(taskCols(..)); no event when equal; marks dirty
func (o *op) recordEntryUpdate(before, after domain.LogEntry) error
func (o *op) markDirty(ref domain.EntityRef)
func (o *op) markTaskDirty(id domain.TaskID)
func (o *op) markInserted(ref domain.EntityRef)                   // rows created by this op keep version 1
func (o *op) addPair(task sql.Null[domain.TaskID], d civil.Date)   // invalid Null ignored
func (o *op) ref(r domain.EntityRef)                               // result refs, deduplicated, in call order
func (o *op) warn(msg string)
func taskCols(t domain.Task) map[string]any                        // storage representation (rules below)
func entryCols(e domain.LogEntry) map[string]any
func blockCols(b domain.TimeBlock) map[string]any
func diffCols(before, after map[string]any) (b, a map[string]any)
func effectsFromEvents(rec domain.OpRecord, evs []domain.Event) []Effect
func validation(field string, err error) *domain.Error             // domain.ValidationError(FieldError{field, err.Error()})
type reconcileStep struct {
	name string // "surface" | "due" | "overdue" | "resolve" | "focus" (D§5.8 order)
	need func(ctx context.Context, q store.Q, now time.Time, today civil.Date) (bool, error)
	run  func(o *op) error
}
var reconcileSteps []reconcileStep // empty here; Tasks 27 (surface), 20 (due, overdue), 23 (resolve), 26 (focus) add entries in D§5.8 order
func (s *Service) needsReconcile(ctx context.Context, q store.Q, now time.Time) (bool, error)
func (s *Service) reconcileTx(ctx context.Context, tx *sql.Tx, now time.Time) error // AIDEV-NOTE anchor
// no-op stubs filled later: (*op).recompute (Task 23), (*op).checkInvariants (Task 17)
```

- Produces (`export_test.go`, package `service`, for `service_test` tests of later tasks):

```go
func (s *Service) TestOpRaw(ctx context.Context, m Meta, touch []domain.EntityRef, fn func(q store.Q) error) (*OpResult, error)
	// one forward op (kind "test.raw", scope write, not revertible): runs fn on the op tx, marks every touch ref
	// dirty, records one setting:update event so the op commits
var EffectsFromEventsForTest = effectsFromEvents
```

- Produces (`testutil`, imported only by `_test.go` files):

```go
const Password = "correct horse battery"
const DefaultNow = "2026-10-28 10:00" // catalog §0
var Zone civil.Zone                   // America/Denver
func Local(t testing.TB, s string) time.Time // "2026-10-28 10:00[:ss]" | "10-28 10:00" (year 2026) | "2026-11-02T07:00Z" (UTC); wall times via Zone.Instant(GapForward)
func D(s string) civil.Date                  // "10-28" → 2026-10-28; ISO accepted
func Moment(s string) domain.Moment          // "10-30" | "10-30 09:00" | ISO forms
func Clock(t testing.TB, local string) *clock.Fake
func Store(t testing.TB) *store.Store        // temp dir; OpenStaging → Migrate → BootstrapDB (NewGeneration, TZ "America/Denver",
	// auth_state with HashPassword(Password) computed once per test binary, random idem key, owner token) → Close → Open;
	// Cleanup: FTSIntegrityCheck, Close
type SvcOpt func(*service.Options)
func Service(t testing.TB, opts ...SvcOpt) (*service.Service, *clock.Fake) // clock at DefaultNow, Zone, Monday, DueSoonDays 3,
	// SlipGrace 15m, UndoDepth 1000, Ledger in t.TempDir(), NewPasswordGate, discard logger
func Owner() domain.Actor                     // ActorOwner, AllScopes, ChanCLI, RemoteKey "socket"
func TokenActor(name string, sc domain.Scopes) domain.Actor // ActorToken, ChanAPI
func AI() domain.Actor                        // TokenActor("ai", MCPDefaultScopes), ChanMCP, MutationsPerHour 60
func Meta(a domain.Actor, gen domain.Generation) service.Meta // fresh 32-hex key; Method "POST", Route "test", Body = []byte(key) (A§13.1, P-4)
func RequireCode(t testing.TB, err error, want domain.Code)
type Scenario struct {
	T     testing.TB
	Svc   *service.Service
	Clock *clock.Fake
	Store *store.Store
	Actor domain.Actor      // default Owner()
	Last  *service.OpResult // result of the latest fluent op
}
func NewScenario(t testing.TB, opts ...SvcOpt) *Scenario
func (s *Scenario) Ctx() context.Context
func (s *Scenario) At(when string) *Scenario             // Clock.Set(Local(when))
func (s *Scenario) As(a domain.Actor) *Scenario           // shallow copy acting as a
func (s *Scenario) Meta() service.Meta
func (s *Scenario) MetaWith(key, body string) service.Meta
func (s *Scenario) Must(r *service.OpResult, err error) *service.OpResult // Fatal on err; sets Last
func (s *Scenario) Events(op domain.OpID) []domain.Event
func (s *Scenario) EventKeys(op domain.OpID) []string // "task:insert 1", "triage:add 1 new", "block:update 3 [derived]", "focus:set"
func (s *Scenario) OpCount() int
func (s *Scenario) Op(id domain.OpID) domain.OpRecord                       // store.GetOp
func (s *Scenario) Stack(actor string, which store.Stack) []domain.OpID     // store.StackOps
func (s *Scenario) Exec(query string, args ...any)                          // raw SQL on the writer — only where a task says so
```

**Rules this task fixes:**
- Event payloads (A§2.7) use the **storage representation**, keyed by column name: instants → unix ms, dates → `"YYYY-MM-DD"`, minutes → integers, booleans → 0/1, absent optionals → `null`. `taskCols`/`entryCols`/`blockCols` omit `id`, `version`, `created_at`, `updated_at`, `purged_at`; task update diffs also omit `rank` (rank changes are `task:move` events). Insert events carry the full column map. `Event.TaskID`: the task itself, the entry's/block's task, the interruption's `to_task_id`, focus's after task (else before), NULL for token/setting/auth.
- `run` steps implemented here (A§4.3): 1 (static authz, forward only), 4 (`BEGIN IMMEDIATE`; errors through `store.MapErr`), 5, 8 (`reconcileTx`), 9, 11 (ops row: `scopes` = `spec.scopes`, `revertible` = `spec.revertible`), 12, 13 (recompute stub → invariants stub → `store.BumpVersion` once per ref that is dirty **and not inserted by this op**, so a created task/entry/block stays at version 1 even when the same op also places, tags or focuses it), 14 (no events → `ROLLBACK TO user_op`, `NoChange`, no ops row; a forward revertible op with events → `PushStack(undo)`, `ClearStack(redo)`, `TrimStack(undo, UndoDepth)` for `actor.Key()`), 16, 17 (any error → `ROLLBACK TO user_op; RELEASE; COMMIT`, plain rollback before step 9), 18 (results loaded on the reader). Tasks 18, 30, 31 add the remaining steps.
- `reconcileTx` (A§4.4): run every registered step's `need`; none → return without writing. Else `SAVEPOINT reconcile`, insert a `system.reconcile` ops row (actor `system`, channel `system`, scopes `""`, not revertible) with its own `op` (sharing `now`), run the steps in `reconcileSteps` order, `checkInvariants`, bump versions; no events → `ROLLBACK TO reconcile`; `RELEASE`. `Reconcile(ctx)`: `needsReconcile` on a reader tx; only if true, a writer tx running `reconcileTx`.
- Results: refs → exactly one task ref → `Task`; more than one → `Tasks` (ref order); an entry ref → `Entry`; a block ref → `Block` (the last one). `Effects` = `effectsFromEvents(rec, events)`. `Created` = the op set `o.created` (AddTask, Block, AddEntry).
- `effectsFromEvents` (in event order; one event may give several effects):

| Event | Effects |
|---|---|
| `task:insert` | `created` |
| `task:update` | `title` → `retitled` (From/To); `status` → `completed` / `dropped` / `reopened`; `last_placed_seq` → `placed`; `defer_date` → `deferred` (To = moment) or, when the after value is null, `undeferred`; `due_date`/`due_min` → `due_set` (To) or `due_cleared`; any other key → one `updated` |
| `task:move` / `task:tombstone` / `task:untombstone` / `task_tags:set` | `moved` / `deleted` / `undeleted` / `updated` |
| `triage:add` / `triage:remove` | `reason_added` / `reason_removed` (Reason) |
| `block:insert` | `blocked` (To = span) |
| `block:update` | `date`/`start_min`/`end_min` → `block_moved`; `status` → `cancelled` or `deleted_at` set → `block_cancelled`; `status` → done/worked/missed → `outcome_set` if `outcome_source` is `user`, else `block_resolved` (To = status); `slipped` 0→1 → `slipped`, 1→0 → `unslipped` |
| `entry:insert` / `entry:tombstone` / `entry:untombstone` | `entry_logged` / `entry_deleted` / `entry_restored` |
| `entry:update` | `task_id` changed → `entry_relinked`, else `entry_updated` |
| `focus:set` | `focus_set` (TaskID) or `focus_cleared` |
| `interruption:insert` | `interrupted` (From = from task id) |
| `*:purge` | `purged` |
| `setting`/`token`/`auth` events | none |
| op kind `ai.review` | `reviewed` (once) |
| op with `Via ≠ ""` and ≥ 1 event | `reverted` first (From = first 8 hex of `RevertsOpID`); with **no** events the effects are `[]` (UND-25) |

- [ ] **Step 1: Write the failing tests** — `TestEffectsFromEvents` (via `EffectsFromEventsForTest`; one row per mapping-table line on synthetic events, incl. an `op.undo` record with no events → `[]` and one with a `focus:set` event → `[reverted, focus_set]`), `TestRunForbidden` (`TestOpRaw` by `TokenActor("ro", ScopeRead)` → `forbidden`, no ops row), `TestRunErrorRollsBack` (`TestOpRaw` whose fn writes then errors → no ops row, no events, nothing changed), `TestReconcileWithoutSteps` (`Reconcile` → nil, `OpCount` 0), `TestHarnessStore` (`testutil.Store`: `db_generation` 16 hex, `auth_state` hash verifies `Password`, one unrevoked owner token), `TestHarnessLocal` (`"10-28 10:00"` → 2026-10-28T16:00Z; `"2026-11-02T07:00Z"`; `"2026-11-01 01:30"` → 07:30Z), and:

```go
func TestVersionBumpOncePerOp(t *testing.T) {
	s := testutil.NewScenario(t)
	var id domain.TaskID
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error { // a raw row, version 1
		tk := domain.Task{Title: "A", Status: domain.StatusOpen, Rank: 1, DueAck: domain.DueNone, Origin: "owner",
			CreatedAt: testutil.Local(t, testutil.DefaultNow), UpdatedAt: testutil.Local(t, testutil.DefaultNow)}
		err := store.InsertTask(s.Ctx(), q, &tk)
		id = tk.ID
		return err
	}))
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}},
		func(q store.Q) error {
			for _, r := range []domain.TriageReason{domain.ReasonNew, domain.ReasonManual} {
				if _, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: r, Seq: 1}); err != nil {
					return err
				}
			}
			return nil
		}))
	var v int
	s.Store.Reader.QueryRow(`SELECT version FROM tasks WHERE id = ?`, id).Scan(&v)
	if v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service ./internal/testutil -race -count=1` → FAIL (packages do not compile).

- [ ] **Step 3: Implement** the files listed per the rules above and A§3.1, A§4.3. `New` reads `db_generation` (settings) and `idem_hmac_key` (auth_state); `SlipGrace` 0 → 15 m, `UndoDepth` 0 → 1000, `DueSoonDays` used as given, nil `Logger` → `slog.New(slog.DiscardHandler)`. AIDEV-NOTE anchors on `run` (single write path) and `reconcileTx`.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service ./internal/testutil -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): op runner, events, effects, reconcile framework and test harness" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 16b: `AddTask`, `MarkManual`, `GetTask` and the task/entry/block views

**Files:**
- Create: `internal/service/{tasks.go, views.go, derive.go}`, `internal/service/tasks_test.go`
- Modify: `internal/service/export_test.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 16a (`run`, `op` helpers, `markInserted`, `Scenario` core); Task 9 `store.InsertTask`/`GetTask`/`UpdateTask`, `store.AddReason`/`RemoveReason`/`Reasons`, `store.TaskTags`/`SetTaskTags`, `store.RankBounds`; Task 10 `store.EntriesOfTask`, `store.BlocksOfTask`, `store.InterruptionsOfTask`, `store.CountEntriesOn`, `store.SlipCounts`; Task 4 `textsafe.ValidateTitle`/`ValidateNotes`/`ValidateName`.
- Produces:

```go
type AddTaskIn struct {
	Title, Notes string
	Due          *domain.Moment
	Tags         []string
	Project      string
	Priority     domain.Priority
	Place        *AddPlacement
}
type AddPlacement struct { // A§3.2
	How   string // top|bottom|before|after|defer|block|now
	Ref   domain.TaskID
	Defer domain.Moment
	Block domain.BlockSpan
}
type MarkManualIn struct{ ID domain.TaskID; Expect domain.Expect }
func (s *Service) AddTask(ctx context.Context, m Meta, in AddTaskIn) (*OpResult, error)       // Place nil only here; Hows arrive in Tasks 19/22/26/27
func (s *Service) MarkManual(ctx context.Context, m Meta, in MarkManualIn) (*OpResult, error) // defer clearing (D4) arrives in Task 27
func (s *Service) GetTask(ctx context.Context, a domain.Actor, id domain.TaskID) (domain.TaskDetail, error)
// package-internal:
func (o *op) loadTask(id domain.TaskID, exp *domain.Expect, allowDeleted bool) (domain.Task, error)
func (o *op) loadEntry(id domain.EntryID, exp *domain.Expect, allowDeleted bool) (domain.LogEntry, error)
func (o *op) loadBlock(id domain.BlockID, exp *domain.Expect) (domain.TimeBlock, error)
	// missing → not_found ("task #42 not found"); tombstoned && !allowDeleted → deleted; exp != nil: Gen "" →
	// precondition_required, Gen ≠ o.gen or Version ≠ current → precondition_failed with Current = the TaskView/
	// LogEntry/BlockView loaded inside the tx
func (o *op) addReason(task domain.TaskID, r domain.TriageReason) error    // store.AddReason(since now, seq rec.Seq); event + dirty only if added
func (o *op) removeReason(task domain.TaskID, r domain.TriageReason) error // event (before = full row) + dirty only if removed
func (o *op) clearReasons(task domain.TaskID) error
func (o *op) rankBottom() (int64, error)                                   // MAX(rank)+Gap over all tasks; Gap when empty
const Gap int64 = 1 << 16
func duePhase(t domain.Task, now time.Time, today civil.Date, z civil.Zone) domain.DuePhase // A§4.2
func (s *Service) taskView(ctx context.Context, q store.Q, t domain.Task, now time.Time, today civil.Date) (domain.TaskView, error)
func (s *Service) entryView(ctx context.Context, q store.Q, e domain.LogEntry) (domain.EntryView, error)
func (s *Service) blockView(ctx context.Context, q store.Q, b domain.TimeBlock, now time.Time) (domain.BlockView, error)
// export_test.go:
var DuePhaseForTest = duePhase
// testutil:
func (s *Scenario) Expect(id domain.TaskID) domain.Expect
func (s *Scenario) Add(title string, mods ...func(*service.AddTaskIn)) domain.TaskID
func Due(m string) func(*service.AddTaskIn)
func (s *Scenario) MarkManual(id domain.TaskID)
func (s *Scenario) Task(id domain.TaskID) domain.TaskView           // GetTask(...).TaskView
func (s *Scenario) Reasons(id domain.TaskID) []domain.TriageReason  // highest first
func (s *Scenario) Backlog() []domain.TaskID // SQL oracle: open, not deleted, no reasons, defer_date NULL, ORDER BY rank, id
```

Rules: `AddTask` (A§3.2, `Place == nil`): scopes `capture`; title/notes through `textsafe`; tags each `ValidateName(_, 64)` (deduplicated case-insensitively), project `ValidateName(_, 100)` (`""` = none), priority `Valid()`; `Due` minute 0–1439 and `DueMoment > now` else `validation` (field `due`); `origin = actor.Key()`; rank = `rankBottom`; `markInserted(task)`; events `task:insert`, `triage:add new`, `task_tags:set` (only when tags given, before `{"tags":[]}`); the new task has version 1. `MarkManual`: scope `write`, IfM, revertible; `addReason(manual)`; already present → `NoChange`. `taskView` (A§2.6): reasons (highest first), tags, `View` per A§4.4 (deferred ⇔ `DeferMoment > now`), `Deferred`, `DuePhase`, `DueSoon` (`due_date ≤ today + DueSoonDays`), slip counts via `store.SlipCounts(task, COALESCE(last_placed_seq, 0))`. `blockView` (A§2.6, R4-7): `TaskTitle`, `TaskVersion`, `Started` = start instant ≤ now, `Ended` = end instant ≤ now, `Current` = planned ∧ started ∧ ¬ended, `NeedsReblock` = planned ∧ ended ∧ no non-deleted entry for (task, date); instants via `Zone.Instant(…, GapForward)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestTRI01(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	tv := s.Task(id)
	if tv.Status != domain.StatusOpen || tv.View != domain.ViewTriage || tv.Origin != "owner" || tv.Version != 1 ||
		tv.LastPlacedAt.Valid || tv.DueAck != domain.DueNone || tv.Rank != service.Gap {
		t.Fatalf("task = %+v", tv)
	}
	if got := s.Reasons(id); !slices.Equal(got, []domain.TriageReason{domain.ReasonNew}) ||
		!tv.Reasons[0].Since.Equal(testutil.Local(t, testutil.DefaultNow)) {
		t.Fatalf("reasons = %v", tv.Reasons)
	}
	if got := s.EventKeys(s.Last.OpID); !slices.Equal(got, []string{"task:insert 1", "triage:add 1 new"}) {
		t.Fatalf("events = %v", got)
	}
	if !s.Last.Created || s.Last.Task == nil || len(s.Last.Effects) != 2 {
		t.Fatalf("result = %+v", s.Last)
	}
}
```

Also: `TestTRI08` (`Due("10-29")` → `due_date` 10-29, `due_min` null, `due_ack` none, reasons `[new]`, no `placed` effect), `TestCreationKeepsVersion1` (`AddTask` with two tags and a due → version 1, a `task_tags:set` event exists), `TestAddTaskValidation` (title `""` and a title containing U+202E; due 10-27; project `"a b"`; priority `"X"`; tag `"a b"` → `validation` naming the field; nothing written), `TestMarkManual` (`[manual, new]`, version 2, events `["triage:add 1 manual"]`; second call → `NoChange`, zero `OpID`, version 2, `OpCount` unchanged), `TestExpectChecks` (`MarkManual` with zero `Expect` → `precondition_required`; stale version → `precondition_failed` with `Current` a `domain.TaskView` at the current version; other generation → `precondition_failed`), `TestGetTask` (detail of a fresh task; `999` → `not_found`), `TestDuePhase` (via `DuePhaseForTest`: due 10-28 date-only at 10:00 → `due`; due 10-28 09:00 → `overdue`; due 10-29 → `none`; no due → `none`), `TestBlockViewFlags` (raw block 10-28 13:00–15:00 via `Exec`: at 12:59 none set; 13:00 `Current`+`Started`; 15:00 `Ended`+`NeedsReblock`; with a raw work entry that day `NeedsReblock` false; `TaskVersion` = the task's version), `TestForwardOpsPushUndo` (two `AddTask`s → `s.Stack("owner", store.StackUndo)` = `[op1, op2]`; a `NoChange` pushes nothing; `UndoDepth` 2 + three ops → the newest two).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI0[18]|Creation|AddTask|MarkManual|Expect|GetTask|DuePhase|BlockView|ForwardOps' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service ./internal/testutil -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): AddTask, MarkManual, GetTask and task, entry and block views" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 17: Invariants I1–I8

**Files:**
- Modify: `internal/service/invariants.go` (replace the stub), `internal/testutil/store.go`, `internal/testutil/service.go`
- Create: `internal/service/invariants_test.go`

**Interfaces:**
- Consumes: Task 16a `op`, `TestOpRaw`, `Reconcile`, `testutil.Service`/`Store`; Task 10 block queries.
- Produces:

```go
func CheckStructural(ctx context.Context, q store.Q) error                                  // I1a, I2, I3, I4, I5, I7, I8 over everything
func CheckTimeDependent(ctx context.Context, q store.Q, now time.Time, z civil.Zone) error   // I1b, I6
func CheckAll(ctx context.Context, q store.Q, now time.Time, z civil.Zone) error
func (o *op) checkInvariants() error // touched entities only; spec.structuralOnly → structural rules only
// testutil:
func StoreNoCheck(t testing.TB) *store.Store // like Store, without cleanup checks (tests that corrupt state)
```

Violations return `*domain.Error{Code: CodeConflict, Detail: "invariant I3 violated: task #7 …"}` (all violations joined with `"; "`). Per op (A§4.7): touched tasks = dirty task refs ∪ tasks of dirty blocks/entries; touched blocks = dirty block refs ∪ planned blocks of touched tasks; I6 always (one row); I7 only for `o.rankMoved` tasks (`store.RankTaken`); I4 via `store.CompleteEntries` (exactly one non-deleted `reopened=0` complete entry for a non-deleted done task, or none and ≥ 1 purged; none with `reopened=0` alive for open/dropped). `testutil.Store` cleanup gains `CheckStructural`; `testutil.Service` cleanup gains `svc.Reconcile` (Task 16a; it has steps from Task 20 on) then `CheckAll` at the fake clock's time.

- [ ] **Step 1: Write the failing tests** — `StoreNoCheck` plus raw SQL per invariant, each expecting `CheckAll` to fail with its ID in `Detail`: `TestI1aPlannedOnDoneTask`, `TestI1bPlannedOnDeferredTask` (time-dependent only), `TestI2Overlap` (drop `time_blocks_no_overlap_ins` in that test DB first), `TestI3DeferAfterDue`, `TestI4DoneWithoutCompletion` and `TestI4PurgedCompletionAccepted` (one or two purged completion entries → ok), `TestI5TriageOnDoneTask`, `TestI6FocusYesterday` (structural passes, time-dependent fails), `TestI7DuplicateRank`, `TestCheckAllCleanStore`, and:

```go
func TestOpInvariantRollsBack(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}},
		func(q store.Q) error { _, err := q.ExecContext(s.Ctx(), `UPDATE tasks SET due_date = '2026-10-29',
			defer_date = '2026-10-30' WHERE id = ?`, id); return err })
	testutil.RequireCode(t, err, domain.CodeConflict) // I3
	if tv := s.Task(id); tv.DueDate.Valid || tv.Version != 1 {
		t.Fatalf("op not rolled back: %+v", tv)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TestI[0-9]|TestCheckAll|TestOpInvariant' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the checks (SQL per invariant; I1b/I6 evaluate defer moments in Go with `z.DeferMoment`).

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service ./internal/testutil -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): invariants I1-I8 per op and over the whole store" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 18: Idempotency, generation binding, rate cap, shutdown drain

**Files:**
- Create: `internal/service/idem.go`, `internal/service/lifecycle.go`, `internal/service/idem_test.go`, `internal/service/lifecycle_test.go`
- Modify: `internal/service/op.go`

**Interfaces:**
- Consumes: Task 16a runner, `Reconcile`, `TestOpRaw`, `testutil.Scenario.MetaWith`; Task 16b `MarkManual`, `AddTask`; Task 11 `store.GetIdem`/`PutIdem`/`DeleteIdemBefore`/`CountOpsSince`/`EventsOfOp`.
- Produces:

```go
const (IdemTTL = 7 * 24 * time.Hour; OpBudget = 15 * time.Second)
func (s *Service) BeginShutdown()                                    // later ops, writeTx and Reconcile → busy; cancels s.stopCtx
func (s *Service) stopCtx() context.Context                           // canceled by BeginShutdown; long operations (Task 36) observe it
func (s *Service) Stopping() <-chan struct{}                         // closed by BeginShutdown; lets callers interrupt their own blocking I/O (Task 36)
func (s *Service) Drain(ctx context.Context) error                    // waits for every in-flight op/job, or ctx
func (s *Service) ExpireIdempotency(ctx context.Context) (int, error) // deletes rows older than IdemTTL (writeTx, no events)
func (s *Service) enter() (leave func(), err error)                   // in-flight registration; busy after BeginShutdown
func idemHash(key [32]byte, actorKey string, i Idem) [32]byte         // A§4.9
```

Runner steps added (A§4.3): **2** — `spec.idem`: missing key → `idempotency_key_required`; key not `^[A-Za-z0-9_-]{16,128}$` → `malformed`; every mutation: `Meta.Gen == ""` → `precondition_required`, ≠ cached generation → `generation_mismatch`. **3** — `context.WithoutCancel` + `OpBudget` timeout, `enter()`. **6** — lookup `(actor.Key(), key)`; a row older than `IdemTTL` counts as absent (it is replaced); same hash → roll back and replay (`Replayed`, stored status → `Created`, stored refs → current representations, effects rebuilt from `EventsOfOp(stored op)`, none for a stored no-op; stored 409 → the stored error code/detail/conflicts); different hash → `idempotency_mismatch`. **10** — `actor.MutationsPerHour > 0` and `CountOpsSince(actor.Key(), now − 1 h)` ≥ cap → `rate_limited`, `RetryAfter = oldest + 1 h − now`. **14/15** — `NoChange` with `spec.idem` stores a row (`op_id` NULL, status 200, refs = the addressed entity); success stores status 201/200 and refs. `writeTx` and `read`'s reconcile also go through `enter()`. The only writer path that skips `enter()` is Task 35b's `maintTx` (final `FlushUsage` after `Drain`, ruling P12).

- [ ] **Step 1: Write the failing tests**

```go
func TestIDM02(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Must(s.Svc.AddTask(s.Ctx(), s.MetaWith("key-K-0123456789ab", `{"title":"A"}`), service.AddTaskIn{Title: "A"}))
	_, err := s.Svc.AddTask(s.Ctx(), s.MetaWith("key-K-0123456789ab", `{"title":"B"}`), service.AddTaskIn{Title: "B"})
	testutil.RequireCode(t, err, domain.CodeIdemMismatch)
	if s.OpCount() != 1 {
		t.Fatal("second task created")
	}
}
```

Also: `TestIDM03` (owner and `AI()` reuse one key → two tasks), `TestIDM05` (replay at T + 7 d + 1 s → new task #2), `TestIDM06` (no key → `idempotency_key_required`; `"short"` and a 16-char key with `!` → `malformed`; no ops row), `TestIDM10` (catalog row: `MarkManual` on `#1{manual}` → `NoChange` stores a row with NULL `op_id` and refs `[task 1]`; replay → `Replayed`, no effects, current task; same key, other body → `idempotency_mismatch`), `TestReplayReturnsCurrent` (key K `MarkManual`; a later op bumps the version; replay K → `Replayed`, current version, effects equal to the original's), `TestSCP10` (`AI()` with cap 60: 60 `AddTask`s one minute apart succeed; the 61st within the hour → `rate_limited` with `RetryAfter` = oldest + 1 h − now; a `NoChange` does not count; `GetTask` unaffected; owner uncapped), `TestGenerationChecks` (`Gen ""` → `precondition_required`; `"0000000000000000"` → `generation_mismatch`; no ops row), `TestShutdown` (`BeginShutdown` → `AddTask` and `Reconcile` → `busy`; `Drain` returns once an in-flight `TestOpRaw` blocked on a channel finishes), `TestConcurrentOps` (Review Focus 3: 20 goroutines `AddTask` while 5 goroutines call `Reconcile` and `GetTask` → no error, 20 tasks, distinct ranks, cleanup `CheckAll` passes), `TestExpireIdempotency`.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'IDM|SCP10|Replay|Generation|Shutdown|Concurrent|Expire' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** the steps above (AIDEV-NOTE: idempotency lookup precedes target selection, R2-5).

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): idempotency replay, generation binding, rate cap and shutdown drain" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 19: Rank, `Place`, `AcceptNew`, positional `AddTask` placements

**Files:**
- Create: `internal/service/rank.go`, `internal/service/place.go`, `internal/service/place_test.go`
- Modify: `internal/service/tasks.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 16a `op` helpers; Task 16b `loadTask`, `clearReasons`, `duePhase`, `rankBottom`, `AddPlacement`; Task 18 idempotency replay; Task 9 rank queries.
- Produces:

```go
type PlaceIn struct{ ID domain.TaskID; Expect domain.Expect; How domain.PlaceHow; Ref domain.TaskID }
type AcceptItem struct{ ID domain.TaskID; Expect domain.Expect }
type AcceptNewIn struct{ Items []AcceptItem }
func (s *Service) Place(ctx context.Context, m Meta, in PlaceIn) (*OpResult, error)
func (s *Service) AcceptNew(ctx context.Context, m Meta, in AcceptNewIn) (*OpResult, error)
func (o *op) rankTop() (int64, error)
func (o *op) rankBefore(ref domain.Task) (int64, error)
func (o *op) rankAfter(ref domain.Task) (int64, error)
func (o *op) moveTask(t *domain.Task, rank int64) error // SetRank + task:move {"after_id"} before/after (A§2.7); rankMoved
func (o *op) place(id domain.TaskID) error             // A§4.5 placement; also clears defer_*; o.placed[id] = true
// testutil:
func Top() func(*service.AddTaskIn); func Bottom() func(*service.AddTaskIn)
func After(id domain.TaskID) func(*service.AddTaskIn); func Before(id domain.TaskID) func(*service.AddTaskIn)
func (s *Scenario) Place(id domain.TaskID, how domain.PlaceHow, ref ...domain.TaskID)
```

Rules: A§4.8 (top `MIN−Gap`, bottom `MAX+Gap` over all tasks; before/after = midpoint with the neighbour in `(rank, id)` order, no neighbour → `±Gap`; integer gap < 2 or `|rank| > 1<<60` → `store.RenormalizeRanks` then recompute; renormalization writes no events and bumps no versions). `place` (A§4.5): `clearReasons`, `last_placed_at = now`, `last_placed_seq = rec.Seq`, `due_ack = duePhase`, `due_ack_date = today`, `defer_date`/`defer_min` cleared if set (catalog §0, A§14 D5), one `task:update`. `Place` (A§3.2): write, IfM, revertible; `keep` never moves; `before`/`after` need `Ref` (`validation` if 0 or = ID; missing → `not_found`, tombstoned → `deleted`). `AcceptNew` (A§3.2, C6): one op; empty or duplicate items → `validation`; a stale item → `precondition_failed`; an item whose reasons ≠ exactly `{new}` → `conflict` naming it; bottom placement in ascending `new` seq order; `Tasks` in that order. `AddTask` with `Place.How` top/bottom/before/after: scopes `capture|write`, owner only (any `ActorToken` → `forbidden`, R-b), rank by How, `place`, no `new` reason. `AddPlacement` shape: unknown How, or fields that do not belong to the How (`Ref` outside before/after, `Defer` outside defer, `Block` outside block) → `validation` (TRI-09); the Hows `defer`, `block`, `now` return `validation` ("unsupported placement") until Tasks 22, 26, 27 replace that branch.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI02`, `TestTRI03`, `TestTRI04` (the `not_found` variant; the `deleted` variant is Task 29), `TestTRI09`, `TestTRI10`, `TestTRI11` (move events: `{"after_id":2}` → `{"after_id":null}` for top), `TestTRI12` (`not_found` variant), `TestTRI18`, `TestTRI22` (moved from Task 18: `#1{}` in the backlog via `Add(…, Bottom())`, then `MarkManual` with keys K1, K2 — the second is `NoChange` and stores an idempotency row with NULL `op_id`; replaying K2 → `Replayed`, no effects), `TestIDM08` (replay with a stale `Expect` → `Replayed`, stored op wins), `TestSCP03`; plus `TestPlaceValidation` and:

```go
func TestRankRenormalization(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("a", testutil.Bottom()), s.Add("b", testutil.Bottom())
	var ids []domain.TaskID
	for i := 0; i < 20; i++ { // each new task lands right after a: the gap halves until renormalization
		ids = append(ids, s.Add(fmt.Sprintf("t%d", i), testutil.After(a)))
	}
	want := append(append([]domain.TaskID{a}, reverse(ids)...), b)
	if got := s.Backlog(); !slices.Equal(got, want) {
		t.Fatalf("backlog = %v, want %v", got, want)
	}
	/* every rank is a multiple of 65536 offset by later midpoints; no op recorded an event for the renormalization;
	   b's version is still 1 */
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI0[234]|TRI09|TRI1[0128]|TRI22|IDM08|SCP03|Place|Rank' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): rank, Place, AcceptNew and positional add placements" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 20: Reconcile steps due and overdue, and `SetDue`

**Files:**
- Modify: `internal/service/reconcile.go` (add two `reconcileSteps` entries), `internal/service/tasks.go`, `internal/testutil/scenario.go`
- Create: `internal/service/reconcile_test.go`, `internal/service/due_test.go`

**Interfaces:**
- Consumes: Task 16a `Reconcile`, `reconcileTx`, `reconcileStep`, `reconcileSteps`; Task 16b `loadTask`, `addReason`, `removeReason`, `duePhase`; Task 19 `Place` (fixtures).
- Produces:

```go
// reconcileSteps entries "due" (D§5.8 step 2) and "overdue" (step 3), with their need clauses (A§4.4 items 2, 3)
type SetDueIn struct{ ID domain.TaskID; Expect domain.Expect; Due *domain.Moment }
func (s *Service) SetDue(ctx context.Context, m Meta, in SetDueIn) (*OpResult, error)
func (o *op) checkDeferDue(deferM, dueM *domain.Moment) error // D§5.7: defer moment < due moment and defer_date ≤ due_date, else validation
// testutil:
func (s *Scenario) Reconcile()
func (s *Scenario) SetDue(id domain.TaskID, moment string) // "" → clear
```

Steps (the framework is Task 16a's `reconcileTx`). Step 2: open, non-deleted, `defer_date IS NULL`, `due_date ≤ today`, `due_ack = 'none'`, no `due` row, no planned block today → `addReason(due)`. Step 3: open, non-deleted, `DueMoment ≤ now`, no `overdue` row, not (`due_ack = 'overdue'` ∧ `due_ack_date = today`) → `addReason(overdue)`. Because `run` step 8 runs `reconcileTx` before `SAVEPOINT user_op`, a reason it adds bumps the task version before the user op's `Expect` check (TRI-19). `SetDue` (A§3.2): write, IfM, revertible; minute 0–1439; `DueMoment ≤ now` → `validation`; `checkDeferDue` against an existing defer (exercised in Task 27); one `task:update` (`due_date`, `due_min`, `due_ack = none`, `due_ack_date = NULL`); `removeReason` `due` and `overdue`; tombstoned → `deleted`.

- [ ] **Step 1: Write the failing tests** — `TestDUE01`, `TestDUE02`, `TestDUE03` (instants given in UTC with `At("2026-11-01T07:29Z")`), `TestDUE10`, `TestDUE11`, `TestDUE14`, `TestDUE15`, `TestDUE16`, `TestTRI24`, `TestREC01` (due fixture; second `Reconcile` at the same instant → no new ops row, versions unchanged), `TestSetDueValidation` (minute 1440 → validation; `DueMoment ≤ now` → validation; deleted → `deleted` via `TestOpRaw` tombstone + reason cleanup), and:

```go
func TestTRI19(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-27 12:00"))
	stale := s.Expect(id)
	s.At("10-28 10:00") // the op's own reconcile adds overdue (and due) and bumps the version first
	_, err := s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: stale, How: domain.PlaceKeep})
	testutil.RequireCode(t, err, domain.CodePrecondition)
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatal(err)
	}
	if cur := de.Current.(domain.TaskView); cur.Version != stale.Version+1 ||
		!slices.Contains(s.Reasons(id), domain.ReasonOverdue) {
		t.Fatalf("current = %+v", de.Current)
	}
	s.Must(s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: s.Expect(id), How: domain.PlaceKeep}))
	if len(s.Reasons(id)) != 0 {
		t.Fatal("placement did not clear the reasons")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'DUE|TRI19|TRI24|REC01|SetDue' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): reconcile framework with due and overdue steps; SetDue" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 21: `AddEntry`, `GetEntry`, derivation

**Files:**
- Create: `internal/service/entries.go`, `internal/service/entries_test.go`
- Modify: `internal/service/derive.go`, `internal/service/export_test.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 16a `run`, `addPair`, `markInserted`, `warn`; Task 16b `loadTask`, `entryView`; Task 10 `InsertEntry`, `GetEntry`, `CountEntriesOn`; Task 4 `ValidateTitle`/`ValidateNotes`.
- Produces:

```go
type AddEntryIn struct {
	TaskID   sql.Null[domain.TaskID] // explicit: a task or standalone (D§5.2)
	Title    string
	Notes    string
	WorkDate civil.Date             // zero = today
	At       sql.Null[civil.Minute] // 0..1439, never converted to an instant (A20)
}
func (s *Service) AddEntry(ctx context.Context, m Meta, in AddEntryIn) (*OpResult, error)
func (s *Service) GetEntry(ctx context.Context, a domain.Actor, id domain.EntryID) (domain.EntryView, error)
func derive(ctx context.Context, q store.Q, task domain.TaskID, d civil.Date) (domain.BlockStatus, error) // D§5.6
// export_test.go: func DeriveForTest(ctx context.Context, q store.Q, task domain.TaskID, d civil.Date) (domain.BlockStatus, error)
// testutil:
func (s *Scenario) Log(task domain.TaskID, text, date string) domain.EntryID       // task 0 = standalone; date "" = today
func (s *Scenario) LogAt(task domain.TaskID, text, date, at string) domain.EntryID
func (s *Scenario) EntryExpect(id domain.EntryID) domain.Expect
```

Rules (A§3.4): scope `capture`, idem, revertible, `Created`; title/notes through `textsafe`; `WorkDate > today` → `validation` (field `work_date`); `At` outside 0–1439 → `validation`; `WorkDate == today` and `At > MinuteOf(now)` → `validation` (field `time`); task missing → `not_found`, tombstoned → `deleted`, done/dropped → allowed with warning `"logged against a done task"` / `"... dropped task"`; `entry:insert` (kind `work`); `addPair(task, work_date)`. `derive`: a non-deleted `complete` entry for (task, d) → `done`; else a non-deleted `work` entry → `worked`; else `missed`.

- [ ] **Step 1: Write the failing tests** — `TestDAT03` (work date 10-29 → validation; `Complete`'s half is LIF-07), `TestDAT11` (clock 2027-03-15 10:00; `LogAt(#1, "x", "2027-03-14", "02:30")` → `occurred_min` 150), `TestDAT17` (at 10:00: `--at 10:30` today → validation `time`; `09:59` ok; `10:30` yesterday ok), `TestAddEntryTargets` (standalone ok; `999` → `not_found`; entry fields: kind work, version 1, `Reopened` false; events `["entry:insert 1"]`; effect `entry_logged`; `Created`), `TestDerive` (raw rows in a `StoreNoCheck` store: complete on D → done; work on D → worked; deleted work only → missed; work on another day or task → missed), `TestGetEntry` (`TaskTitle` set for linked, `""` standalone; `999` → `not_found`).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'DAT|AddEntry|Derive|GetEntry' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): AddEntry, GetEntry and derived block outcome" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 22: Block creation — `Block`, `AddTask{block}`, `GetBlock`

**Files:**
- Create: `internal/service/blocks.go`, `internal/service/blocks_test.go`
- Modify: `internal/service/tasks.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 16a `run`, `record`, `markInserted`, `ref`; Task 16b `loadTask`, `blockView`, `AddPlacement`; Task 19 `place` and the `AddTask` placement branch; `civil.Zone.Instant` (`GapReject`), `civil.ErrNonexistent`; Task 10 `PlannedOverlapping`.
- Produces:

```go
type BlockIn struct{ TaskID domain.TaskID; Expect domain.Expect; Span domain.BlockSpan }
func (s *Service) Block(ctx context.Context, m Meta, in BlockIn) (*OpResult, error)
func (s *Service) GetBlock(ctx context.Context, a domain.Actor, id domain.BlockID) (domain.BlockView, error)
func (o *op) createBlock(t domain.Task, span domain.BlockSpan) (domain.TimeBlock, error) // shared by Block, AddTask{block}, Reblock (R-n)
func (o *op) spanInstants(span domain.BlockSpan) (start, end time.Time, err error)       // GapReject; end 1440 → next day 00:00
// testutil:
func BlockAt(span string) func(*service.AddTaskIn)
func (s *Scenario) Block(task domain.TaskID, span string) domain.BlockID // "10-28 13:00-15:00" | "2027-03-14 01:30-03:30" | "13:00-15:00" (today)
func (s *Scenario) BlockView(id domain.BlockID) domain.BlockView
func (s *Scenario) BlockState(id domain.BlockID) string                  // catalog tuple, e.g. "{planned, –, 0, –}" (en dash for empty)
func (s *Scenario) BlockExpect(id domain.BlockID) domain.Expect
```

`createBlock` order (A§3.3, BLK-05/06/08/11/12): task open (done/dropped → `conflict`); `0 ≤ start < end ≤ 1440` else `validation`; `Span.Date < today` → `validation`; a start or end at a nonexistent wall time → `validation` naming the time and suggesting the first valid one ("02:30 does not exist on 2027-03-14; the clocks jump to 03:00"); end instant ≤ now → `validation`; overlap with a non-deleted planned block → `conflict` naming it (`"overlaps b1 13:00-15:00 (#1)"`); insert planned; `block:insert`; ref the block. `Block`: write, IfM on the task, idem, revertible, `Created`; task tombstoned → `deleted`; `createBlock` then `place` (which clears a defer, BLK-09 in Task 27); refs task + block. `AddTask` How `block`: `createBlock` + `place` (owner-only placement rule of Task 19 applies).

- [ ] **Step 1: Write the failing tests** — catalog rows `TestBLK01`, `TestBLK02`, `TestBLK03`, `TestBLK05`, `TestBLK06`, `TestBLK07`, `TestBLK11`, `TestBLK12`, `TestBLK13`, `TestBLK14`, `TestBLK15`, `TestBLK16`, `TestBLK48`, `TestTRI06`, `TestTRI14`, and `TestBlockPastDate` (DAT-09's `yesterday` → `validation`). Example:

```go
func TestBLK14(t *testing.T) { // fall-back day: the ambiguous start resolves to the earlier 01:30
	s := testutil.NewScenario(t).At("10-31 12:00")
	id := s.Add("A")
	b := s.Block(id, "2026-11-01 01:30-02:30")
	for _, tc := range []struct {
		at   string
		want bool
	}{{"2026-11-01T07:45Z", true}, {"2026-11-01T08:15Z", true}, {"2026-11-01T09:00Z", true}, {"2026-11-01T09:30Z", false}} {
		if got := s.At(tc.at).BlockView(b).Current; got != tc.want {
			t.Fatalf("Current at %s = %v", tc.at, got)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'BLK|TRI06|TRI14|BlockPastDate' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): block creation with DST, overlap and placement rules" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 23: Block resolution — `setBlock` hooks, recompute, reconcile step 4

**Files:**
- Modify: `internal/service/slip.go` (replace the recompute stub), `internal/service/reconcile.go`, `internal/testutil/scenario.go`
- Create: `internal/service/slip_test.go`

**Interfaces:**
- Consumes: Task 16a `reconcileSteps`, `record`, `markDirty`, `o.derived`, `o.pairs`; Task 16b `addReason`, `removeReason`; Task 21 `derive`, `Scenario.Log`; Task 22 `Block`, `Scenario.BlockState`; Task 10 `PlannedBefore`, `AutoResolvedOn`, `HasPlannedAfter`, `OtherSlippedSince`, `OtherWorkedSince`, `UpdateBlock`.
- Produces:

```go
type blockChange struct {
	Status  domain.BlockStatus
	Source  domain.OutcomeSource // "" for planned/cancelled
	Cancel  domain.CancelReason
	Slipped *bool                // nil = unchanged
	Restore bool                 // revert: write Image verbatim, skip every hook (Task 30)
	Image   map[string]any       // Restore only: block columns to write (planner addition to A§4.5)
}
func (o *op) setBlock(b *domain.TimeBlock, ch blockChange) error // the ONLY way block status/slipped change (AIDEV-NOTE)
func (o *op) resolveByDerivation(b *domain.TimeBlock) error     // Status = derive, Source auto, Slipped = (status == missed)
func (o *op) recompute() error                                  // A§4.5 Recompute; o.derived = true while it writes
func (o *op) afterUnslip(task domain.TaskID) error              // slipped-reason removal check (A§4.5 row 2); reused by Task 30 (R4-6)
func (o *op) afterUnwork(task domain.TaskID) error              // continue-reason removal check (A§4.5 row 4); reused by Task 30
// reconcile step 4: every non-deleted planned block with date < today → resolveByDerivation (+ its need clause)
// testutil:
func (s *Scenario) Slips(id domain.TaskID) (lifetime, sinceTriage int)
```

`setBlock` (A§4.5): writes the block and one `block:update` (changed columns); any status change sets `resolved_at = now`, `resolved_seq = rec.Seq` (back to `planned`: both NULL and `outcome_source` NULL). Hooks unless `Restore`: slipped 0→1 → `slipped_at = now`, `slipped_seq = seq`, and if the task is open, not deleted and `!o.placed[task]` → `addReason(slipped)`; slipped 1→0 → `afterUnslip`; status → `worked` with the task open and not deleted → `addReason(continue)` unless placed or `HasPlannedAfter(task, b.date)`; status `worked` → other → `afterUnwork`. `recompute`: for every pair, every non-deleted `auto`-resolved block of (task, date) is re-derived; `setBlock` only when the status differs.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestBLK17`, `TestBLK18`, `TestBLK19`, `TestBLK20`, `TestBLK21`, `TestBLK23`, `TestBLK25`, `TestBLK26`, `TestSLP01`, `TestSLP02`, `TestSLP03`, `TestSLP06`, `TestSLP12`, `TestSLP14`, `TestSLP15`, `TestREC07`, `TestREC08`, `TestREC09`, `TestRCP06`, `TestDUE13`; `TestRecomputeEventsAreDerived` (SLP-01's `block:update` and triage events carry `[derived]`, the `entry:insert` does not); and Review Focus 2:

```go
func TestClockMovesBackward(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-29 00:00").Reconcile() // B1 missed, #1{slipped}
	ops := s.OpCount()
	s.At("10-28 23:59")             // NTP correction backwards
	s.Reconcile()
	if s.OpCount() != ops || s.BlockState(b) != "{missed, auto, 1, –}" {
		t.Fatalf("re-resolved after the clock moved back: %s", s.BlockState(b))
	}
	s.Place(id, domain.PlaceKeep)
	if l, st := s.Slips(id); l != 1 || st != 0 || len(s.Reasons(id)) != 0 {
		t.Fatalf("L/S = %d/%d reasons %v", l, st, s.Reasons(id))
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'BLK1[7-9]|BLK2[0-6]|SLP|REC0[789]|RCP06|DUE13|Recompute|ClockMoves' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): block resolution, slip and continue hooks, recompute" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 24: `Unblock`, `Reblock`, `SetOutcome`

**Files:**
- Modify: `internal/service/blocks.go`, `internal/testutil/scenario.go`
- Create: `internal/service/reblock_test.go`

**Interfaces:**
- Consumes: Task 23 `setBlock`, `resolveByDerivation`; Task 22 `createBlock`, `spanInstants`; Task 19 `place`; Task 16b `loadBlock`, `loadTask`; Task 10 `PlannedOverlapping`, `UpdateBlock`.
- Produces:

```go
type BlockRef struct{ BlockID domain.BlockID; Expect domain.Expect }
type ReblockIn struct{ BlockID domain.BlockID; Expect, TaskExpect domain.Expect; Span domain.BlockSpan }
type SetOutcomeIn struct{ BlockID domain.BlockID; Expect domain.Expect; Outcome domain.UserOutcome }
func (s *Service) Unblock(ctx context.Context, m Meta, in BlockRef) (*OpResult, error)
func (s *Service) Reblock(ctx context.Context, m Meta, in ReblockIn) (*OpResult, error)
func (s *Service) SetOutcome(ctx context.Context, m Meta, in SetOutcomeIn) (*OpResult, error)
func (o *op) started(b domain.TimeBlock) (bool, error)                           // start instant ≤ now
func (o *op) slipOnCancel(b domain.TimeBlock, c domain.CancelReason) bool         // A§4.5: false for task_closed; else today ∧ unstarted ∧ now − created ≥ SlipGrace
func (o *op) endEarly(b *domain.TimeBlock, c domain.CancelReason) error           // started → resolveByDerivation; else cancelled(c) with slipOnCancel
// testutil:
func (s *Scenario) Unblock(b domain.BlockID)
func (s *Scenario) Reblock(b domain.BlockID, span string) domain.BlockID // the planned block that results
func (s *Scenario) Outcome(b domain.BlockID, o domain.UserOutcome)
```

Rules (A§3.3; all write, IfM, idem, revertible): `Unblock` — block not planned → `conflict`; `endEarly(user)`; not a placement. `Reblock` — checks the block `Expect` and `TaskExpect`; old planned, unstarted and new date = its date → update in place (overlap checked excluding itself) with one `block:update`; old planned otherwise → `endEarly(rescheduled)` then `createBlock`; old resolved or cancelled → untouched, `createBlock` only (A7/C4); then `place(task)`; any failure leaves everything unchanged; refs block (resulting planned) + task. `SetOutcome` — `worked`/`missed` on a resolved block, or on today's planned block once started → `setBlock{Status, Source: user, Slipped: missed}`; unstarted planned or cancelled → `conflict` ("use unblock"); `auto`: date < today → `resolveByDerivation`; date = today → `setBlock{Status: planned, Slipped: false}`; already planned → `NoChange`.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestBLK04`, `TestBLK10`, `TestBLK27`, `TestBLK28`, `TestBLK30` (Unblock and Reblock variants; the Defer variant is Task 27), `TestBLK31`, `TestBLK32`, `TestBLK33`, `TestBLK35` … `TestBLK47` (`TestBLK41` covers the `cancelled` and `worked` variants; the `done` variant needs `Complete` and is Task 28's `TestBLK41Done`), `TestBLK49`, `TestBLK50`, `TestRCP07`, `TestDUE20`. Example:

```go
func TestBLK27(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A", testutil.Bottom())
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-28 10:00").Unblock(b)
	if got := s.BlockState(b); got != "{cancelled, –, 1, user}" {
		t.Fatalf("B1 = %s", got)
	}
	if bv := s.BlockView(b); !bv.SlippedAt.Valid || !bv.SlippedAt.V.Equal(testutil.Local(t, "10-28 10:00")) {
		t.Fatalf("slipped_at = %v", bv.SlippedAt)
	}
	if r := s.Reasons(id); !slices.Equal(r, []domain.TriageReason{domain.ReasonSlipped}) {
		t.Fatalf("reasons = %v", r)
	}
	if l, st := s.Slips(id); l != 1 || st != 1 || s.Task(id).View != domain.ViewTriage {
		t.Fatalf("L/S = %d/%d", l, st)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'BLK(04|10|2[78]|3[0-9]|4[0-9]|50)|RCP07|DUE20' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): Unblock, Reblock and recap outcomes with the slip table" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 25: Entry edits — `UpdateEntry`, `RelinkEntry`, `DeleteEntry`, `UndeleteEntry`

**Files:**
- Modify: `internal/service/entries.go`, `internal/testutil/scenario.go`
- Create: `internal/service/entry_edit_test.go`

**Interfaces:**
- Consumes: Task 21 `AddEntry` field rules, `Scenario.Log`; Task 16a `addPair`, `recordEntryUpdate`; Task 16b `loadEntry`, `loadTask`; Task 23 `recompute`; Task 10 `UpdateEntry`.
- Produces:

```go
type UpdateEntryIn struct {
	ID       domain.EntryID
	Expect   domain.Expect
	Title    *string
	Notes    *string
	WorkDate *civil.Date
	At       *sql.Null[civil.Minute] // non-nil invalid Null clears
}
type RelinkEntryIn struct{ ID domain.EntryID; Expect domain.Expect; TaskID sql.Null[domain.TaskID] }
type EntryRef struct{ ID domain.EntryID; Expect domain.Expect }
func (s *Service) UpdateEntry(ctx context.Context, m Meta, in UpdateEntryIn) (*OpResult, error)   // destroy
func (s *Service) RelinkEntry(ctx context.Context, m Meta, in RelinkEntryIn) (*OpResult, error)   // write
func (s *Service) DeleteEntry(ctx context.Context, m Meta, in EntryRef) (*OpResult, error)        // destroy
func (s *Service) UndeleteEntry(ctx context.Context, m Meta, in EntryRef) (*OpResult, error)      // destroy
// testutil:
func (s *Scenario) EditEntry(id domain.EntryID, mods ...func(*service.UpdateEntryIn))
func (s *Scenario) Relink(id domain.EntryID, task domain.TaskID) // 0 = standalone
func (s *Scenario) DeleteEntry(id domain.EntryID)
func (s *Scenario) UndeleteEntry(id domain.EntryID)
```

Rules (A§3.4; all IfM, idem, revertible): every write `addPair`s the before **and** after `(task, work_date)` (a standalone side contributes nothing); `complete` entries: only `Notes` editable (Title/WorkDate/At → `validation`, D32) and relinking → `validation`; same field validation as `AddEntry`; relink target missing → `not_found`, tombstoned → `deleted`; `DeleteEntry` of a `complete` entry whose task is done → `conflict` ("use reopen --remove-entry"); deleting a tombstoned entry → `deleted`; `UndeleteEntry` of a live entry → `NoChange`; `entry:update` / `entry:tombstone` / `entry:untombstone`; no field change → `NoChange`. (Purged-entry refusals arrive in Task 37.)

- [ ] **Step 1: Write the failing tests** — catalog rows `TestBLK24`, `TestSLP04`, `TestSLP08`, `TestSLP13`, `TestRCP01`, `TestRCP02`, `TestRCP03`, `TestRCP04`, `TestRCP08`, `TestRCP09`, and `TestEntryEditValidation` (future work date; `At` in the future today; missing → `not_found`; tombstoned → `deleted`; relink to a missing task → `not_found`; `AI()` `UpdateEntry` → `forbidden`). Example:

```go
func TestRCP01(t *testing.T) { // relink recomputes both (task, date) pairs in one op
	s := testutil.NewScenario(t).At("10-26 08:00")
	t1, t2 := s.Add("A"), s.Add("B")
	b1, b2 := s.Block(t1, "10-26 09:00-10:00"), s.Block(t2, "10-26 11:00-12:00")
	e := s.Log(t1, "did it", "")
	s.At("10-28 10:00").Reconcile() // B1 worked (#1{continue}), B2 missed (#2{slipped})
	s.Relink(e, t2)
	if s.BlockState(b1) != "{missed, auto, 1, –}" || s.BlockState(b2) != "{worked, auto, 0, –}" {
		t.Fatalf("B1 %s B2 %s", s.BlockState(b1), s.BlockState(b2))
	}
	/* #1{slipped}, #2{continue}; every event of the relink shares s.Last.OpID */
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'BLK24|SLP0[48]|SLP13|RCP0[1-489]|EntryEdit' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): entry edit, relink, delete and undelete with recompute" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 26: Focus, interruptions, `AddTask{now}`, reconcile step 5

**Files:**
- Create: `internal/service/focus.go`, `internal/service/focus_test.go`
- Modify: `internal/service/tasks.go`, `internal/service/reconcile.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 19 `place`, `rankTop`; Task 16a `record`, `markDirty`, `reconcileSteps`; Task 16b `loadTask`; Task 22 `spanInstants`; Task 9 `GetFocus`/`PutFocus`; Task 10 `PlannedOn`, `InsertInterruption`, `InterruptionsOn`.
- Produces:

```go
type SetFocusIn struct{ TaskID sql.Null[domain.TaskID]; Interrupt bool }
func (s *Service) SetFocus(ctx context.Context, m Meta, in SetFocusIn) (*OpResult, error) // write, idem, revertible, no IfM (D3)
func (o *op) setFocus(task sql.Null[domain.TaskID]) error // focus:set {"task_id","date"}; date = today (NULL when clearing); set_at = now
func (o *op) clearFocusIf(task domain.TaskID) error       // used by Defer, Complete, Drop, Delete
func (o *op) currentBlock() (sql.Null[domain.TimeBlock], error) // the non-deleted planned block whose [start, end) instants contain now
func (o *op) interrupt(to domain.TaskID, viaAddNow, explicit bool) error // A§14 D35
// testutil:
func Now() func(*service.AddTaskIn)
func (s *Scenario) SetFocus(task domain.TaskID, interrupt bool) // 0 = clear
func (s *Scenario) Focus() domain.Focus
func (s *Scenario) Interruptions(date string) []domain.Interruption
```

Rules: `SetFocus` — null → clear (already clear → `NoChange`); missing → `not_found`; tombstoned → `deleted`; not open or deferred → `conflict` (C2); same task already focused today and nothing to record → `NoChange`. Interruption (D35): `AddTask{now}` → from the current block's task and block when a current block exists, else from the previous focus when set, else none; `SetFocus(X)` → current block of a task ≠ X: from that task and block; current block of X itself: none; no current block and `Interrupt` with a previous focus ≠ X: from that focus (block NULL); else none. Row: `at = now`, `date = today`, `op_id = rec.ID`; event `interruption:insert` (TaskID = to). `AddTask` How `now`: owner only, `rankTop`, `place`, `setFocus`, `interrupt(viaAddNow)`. Reconcile step 5 (and its `need`): focus set and (`focus.date ≠ today` or its task not open, tombstoned, or deferred) → `setFocus(null)`.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI07`, `TestFOC01`, `TestFOC02`, `TestINT01`, `TestINT02`, `TestINT03`, `TestINT04`, `TestINT05`, `TestINT07`, `TestINT10`, `TestINT11`, `TestINT12`, `TestINT13`, `TestREC03`, `TestREC04`, plus `TestSetFocusNoChange`. Example:

```go
func TestINT01(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 08:00")
	t42 := s.Add("Set up keycloak")
	b1 := s.Block(t42, "10-28 09:00-11:00")
	s.At("10-28 09:05").SetFocus(t42, false)
	s.At("10-28 10:20")
	t60 := s.Add("Restart stuck pg replica", testutil.Now())
	if f := s.Focus(); f.TaskID.V != t60 || f.Date.V != testutil.D("10-28") {
		t.Fatalf("focus = %+v", f)
	}
	in := s.Interruptions("10-28")
	if len(in) != 1 || in[0].FromTaskID.V != t42 || in[0].FromBlockID.V != b1 || in[0].ToTaskID != t60 ||
		!in[0].At.Equal(testutil.Local(t, "10-28 10:20")) || in[0].OpID != s.Last.OpID {
		t.Fatalf("interruptions = %+v", in)
	}
	/* B1 still {planned, –, 0, –}; #60 reasons empty, top of backlog; effects include interrupted */
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI07|FOC0[12]|INT|REC0[34]|SetFocus' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): focus, interruptions, add --now and midnight focus clearing" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 27: `Defer` (incl. `none`), `AddTask{defer}`, surfacing, `MarkManual` on deferred tasks

**Files:**
- Modify: `internal/service/place.go`, `internal/service/tasks.go`, `internal/service/reconcile.go`, `internal/testutil/scenario.go`
- Create: `internal/service/defer_test.go`

**Interfaces:**
- Consumes: Task 20 `checkDeferDue`; Task 24 `endEarly`, `slipOnCancel`, `started`; Task 26 `clearFocusIf`; Task 19 `place`; Task 16a `reconcileSteps`, `recordTaskUpdate`; Task 16b `MarkManual`, `addReason`, `duePhase`.
- Produces:

```go
type DeferIn struct{ ID domain.TaskID; Expect domain.Expect; Until *domain.Moment } // nil = `defer ID none` (A24)
func (s *Service) Defer(ctx context.Context, m Meta, in DeferIn) (*OpResult, error) // write, IfM, idem, revertible, placement
func (o *op) closeForDefer(t domain.Task) error // planned blocks: started → resolveByDerivation; unstarted → cancelled/deferred, Slipped = slipOnCancel
// reconcile step 1 (surface) + need; MarkManual clears a defer (D4); AddTask How "defer"
// testutil:
func DeferTo(m string) func(*service.AddTaskIn)
func (s *Scenario) Defer(id domain.TaskID, moment string) // "" → nil
```

Rules (A§3.2, D§5.7): set — task not open → `conflict`; minute 0–1439; due phase `overdue` → `validation` "change the due date first" (checked first, DUE-09); `DeferMoment ≤ now` → `validation`; `checkDeferDue(until, current due)`; then `closeForDefer`, `place` (clears reasons and the old defer), write `defer_date`/`defer_min`, `clearFocusIf`; rank never changes. `nil` — only `place` (clears the defer); never touches blocks or focus. `AddTask{defer}` — same validation (against `in.Due`), `place`, then the defer. Surface (D§5.8 step 1): open, non-deleted tasks with `defer_date ≤ today` and `DeferMoment ≤ now` → clear `defer_*` (`task:update`) and `addReason(surfaced)`; it runs before step 2 so a just-surfaced task can also get `due` (DUE-12). `MarkManual` on a deferred task also clears `defer_*` in the same op (D4).

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI05`, `TestTRI13`, `TestTRI26`, `TestTRI27`, `TestTRI28`, `TestBLK09`, `TestBLK29`, `TestBLK30Defer` (BLK-30's Defer variant on a fresh block → `slipped` 0), `TestSLP07`, `TestDUE04`, `TestDUE05`, `TestDUE06`, `TestDUE07`, `TestDUE08`, `TestDUE09`, `TestDUE12`, `TestDUE17`, `TestDUE21`, `TestDAT05`, `TestREC02`, `TestLIF11`. Example:

```go
func TestTRI27(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("11-02"))
	rank := s.Task(id).Rank
	s.Defer(id, "") // tkr defer 1 none
	tv := s.Task(id)
	if tv.DeferDate.Valid || tv.View != domain.ViewBacklog || tv.Rank != rank || s.Last.OpID.IsZero() {
		t.Fatalf("task = %+v", tv)
	}
	if k := s.Op(s.Last.OpID).Kind; k != domain.OpTaskDefer {
		t.Fatalf("kind = %s", k)
	}
	/* second fixture: non-deferred #2{new} with B1 planned tomorrow → Defer(#2, "") → #2{} PLACED, B1 untouched */
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI(05|13|2[678])|BLK09|BLK29|BLK30Defer|SLP07|DUE(0[4-9]|12|17|21)|DAT05|REC02|LIF11' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): Defer and defer none, surfacing, and manual triage of deferred tasks" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 28: `Complete` and `Reopen`

**Files:**
- Create: `internal/service/lifecycle_ops.go`, `internal/service/complete_test.go`
- Modify: `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 19 `place`; Task 23 `resolveByDerivation`, `setBlock`; Task 24 `started`; Task 26 `clearFocusIf`; Task 16a `addPair`, `markInserted`, `recordTaskUpdate`, `recordEntryUpdate`; Task 16b `loadTask`, `addReason`; Task 10 `CompleteEntries`, `PlannedOfTask`, `BlocksOfTask`; Task 4 `ValidateTitle`/`ValidateNotes`.
- Produces:

```go
type CompleteIn struct {
	ID       domain.TaskID
	Expect   domain.Expect
	As       *string    // retitle (its own task:update, D§5.9)
	Date     civil.Date // zero = today; never future
	LogTitle string     // "" = final task title
	LogNotes string
}
type ReopenIn struct{ ID domain.TaskID; Expect domain.Expect; RemoveEntry bool }
func (s *Service) Complete(ctx context.Context, m Meta, in CompleteIn) (*OpResult, error) // write, IfM, idem, revertible, placement
func (s *Service) Reopen(ctx context.Context, m Meta, in ReopenIn) (*OpResult, error)     // write (+destroy with RemoveEntry), IfM, idem, revertible
func (o *op) closeForDone(t domain.Task, d civil.Date) error                             // D§5.6 "done --date D"
// testutil:
func (s *Scenario) Done(id domain.TaskID, mods ...func(*service.CompleteIn))
func (s *Scenario) Reopen(id domain.TaskID, removeEntry bool)
```

`Complete` order: status not open → `conflict` (D10); `Date > today` → `validation`; `As` through `ValidateTitle` → `task:update(title)`; `task:update(status = done, completed_at = now, completed_date = D)`; `entry:insert` kind `complete` (`addPair(task, D)`); `closeForDone(D)`; `place`; `clearFocusIf`; refs task + entry. `closeForDone(D)`: planned blocks after D → `cancelled/task_closed` (no slip); planned blocks on D that have started (all of them when D < today) → `resolveByDerivation` (→ `done`), unstarted ones → `cancelled/task_closed`; `auto` `missed` blocks after D → `cancelled/task_closed` with `Slipped: false`; `worked`/`done` and user-set blocks untouched; `auto` blocks on D re-derive through recompute. `Reopen` (A§3.2, R4-9): from done or dropped, else `conflict`; `task:update(status open, completed_* NULL, due_ack none, due_ack_date NULL)`; the completion entry — the non-deleted `complete` entry with `reopened = 0`, else the newest (`ORDER BY id DESC`) purged one with `reopened = 0` — gets `entry:update(reopened = 1)`; with `RemoveEntry` also `entry:tombstone` unless already tombstoned (`addPair`); `addReason(reopened)`; not a placement.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI15`, `TestBLK22`, `TestSLP09`, `TestSLP16` (also the warning `logged against a done task`), `TestLIF01` … `TestLIF06`, `TestLIF12` … `TestLIF15`, `TestLIF17` … `TestLIF20`, `TestIDM07`, `TestREC11`, `TestBLK41Done` (BLK-41's `done` variant: a block resolved `done` by `Complete` on its day, task reopened, then `Reblock(B1, 10-29, …)` → new planned block, B1 untouched, PLACED). Example:

```go
func TestLIF05(t *testing.T) { // backdated completion
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A")
	mon, tue, wed := s.Block(id, "10-26 09:00-10:00"), s.Block(id, "10-27 09:00-10:00"), s.Block(id, "10-28 09:00-10:00")
	thu, fri := s.Block(id, "10-29 14:00-16:00"), s.Block(id, "10-30 09:00-10:00")
	s.At("10-27 08:00").Log(id, "worked tue", "10-27")
	s.At("10-29 10:00").Reconcile() // Mon missed, Tue worked, Wed missed
	s.Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-26") })
	for b, want := range map[domain.BlockID]string{mon: "{done, auto, 0, –}", tue: "{worked, auto, 0, –}",
		wed: "{cancelled, –, 0, task_closed}", thu: "{cancelled, –, 0, task_closed}", fri: "{cancelled, –, 0, task_closed}"} {
		if got := s.BlockState(b); got != want {
			t.Fatalf("block %d = %s, want %s", b, got, want)
		}
	}
	/* completed_date 10-26; C1 work_date 10-26; reasons empty; L/S 0/0 */
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI15|BLK22|SLP09|SLP16|LIF(0[1-6]|1[2-57-9]|20)|IDM07|REC11' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above (AIDEV-NOTE in `closeForDone`: status is written before blocks close so hooks see a non-open task).

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): Complete with lifecycle effects and Reopen" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 29: `Drop`, `Delete`, `Undelete`, `UpdateTask`

**Files:**
- Modify: `internal/service/lifecycle_ops.go`, `internal/service/tasks.go`, `internal/testutil/scenario.go`
- Create: `internal/service/drop_delete_test.go`, `internal/service/update_test.go`

**Interfaces:**
- Consumes: Task 19 `place`; Task 23 `resolveByDerivation`, `setBlock`; Task 24 `started`; Task 26 `clearFocusIf`; Task 16a `recordTaskUpdate`; Task 16b `loadTask`, `addReason`, `clearReasons`; Task 9 `TaskTags`/`SetTaskTags`; Task 4 `ValidateTitle`/`ValidateNotes`/`ValidateName`.
- Produces:

```go
type TaskRef struct{ ID domain.TaskID; Expect domain.Expect }
type UpdateTaskIn struct {
	ID                     domain.TaskID
	Expect                 domain.Expect
	Title, Notes, Project  *string          // nil = unchanged; Project "" clears
	Priority               *domain.Priority // PriorityNone clears
	TagsAdd, TagsRemove    []string
}
func (s *Service) Drop(ctx context.Context, m Meta, in TaskRef) (*OpResult, error)       // destroy, IfM, placement
func (s *Service) Delete(ctx context.Context, m Meta, in TaskRef) (*OpResult, error)     // destroy, IfM
func (s *Service) Undelete(ctx context.Context, m Meta, in TaskRef) (*OpResult, error)   // write, IfM (A18)
func (s *Service) UpdateTask(ctx context.Context, m Meta, in UpdateTaskIn) (*OpResult, error) // write, IfM
func (o *op) closeForDropOrDelete(t domain.Task) error // started planned → resolveByDerivation; unstarted → cancelled/task_closed
// testutil:
func (s *Scenario) Drop(id domain.TaskID); Delete(id domain.TaskID); Undelete(id domain.TaskID)
func (s *Scenario) Update(id domain.TaskID, mods ...func(*service.UpdateTaskIn))
```

Rules (A§3.2, all idem and revertible): `Drop` — open only (D10) else `conflict`; status written first; `closeForDropOrDelete`; `place`; `clearFocusIf`. `Delete` — already tombstoned → `deleted`; `task:tombstone` first; `closeForDropOrDelete`; `clearReasons`; `clearFocusIf`; not a placement. `Undelete` — live task → `NoChange`; `task:untombstone`; open → `addReason(manual)` and clear a future defer (`task:update`); not open → no reason (D30); blocks and focus untouched. `UpdateTask` — tombstoned → `deleted`; title/notes via `textsafe`, project `ValidateName(_, 100)`, priority `Valid()`, tags `ValidateName(_, 64)` case-insensitive (adding an existing tag in any casing or removing an absent one changes nothing); one `task:update` for field changes, one `task_tags:set` (sorted tags before/after) when the set changes (the version bumps once); nothing changed → `NoChange`.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI16`, `TestTRI21`, `TestTRI23`, `TestTRI29`, `TestBLK08`, `TestBLK34`, `TestSLP10`, `TestSLP11`, `TestLIF07` … `TestLIF10`, `TestLIF16`, `TestIDM01`, `TestFOC03`, `TestREC05`; `TestPlaceRefDeleted` (TRI-04 and TRI-12 `deleted` variants: `AddTask` `After(deleted)` and `Place` `before deleted` → `deleted`); `TestUndeleteLiveTaskNoChange`; Review Focus 5 `TestUpdateTaskTagsAndProjects` (`TagsAdd ["Infra"]` then another task `["infra"]` → one tag row; `TagsRemove ["nope"]` alone → `NoChange`; tag `"a b"`, project `"infra keycloak"`, a 101-char project → `validation` with the field named, never `conflict`; a tag change bumps the version, R-i); and Review Focus 1:

```go
func TestTextInputsEveryOp(t *testing.T) {
	bad := map[string]string{"bidi": "a\u202eb", "c1": "a\u0085b", "utf8": "\xff", "blank": "  \t "}
	for name, v := range bad {
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("ok")
			e := s.Log(id, "ok", "")
			ops := map[string]func() error{
				"AddTask.title": func() error { _, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: v}); return err },
				"UpdateTask.title": /* … */, "Complete.As": /* … */, "Complete.LogTitle": /* … */,
				"AddEntry.title": /* … */, "UpdateEntry.title": /* … */,
			}
			for op, f := range ops {
				if err := f(); !isCode(err, domain.CodeValidation) {
					t.Fatalf("%s(%q) = %v, want validation", op, v, err)
				}
			}
		})
	}
	/* notes fields (AddTask, UpdateTask, Complete.LogNotes, AddEntry, UpdateEntry): bidi/C1/invalid UTF-8 → validation;
	   "a\r\nb" stored as "a\nb"; 65 536-byte notes accepted, 65 537 → validation; a 500-rune "é" title accepted */
}
```
(`isCode` is a local `errors.As` helper; fill the elided map entries the same way.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI(16|2[139])|BLK08|BLK34|SLP1[01]|LIF(0[7-9]|1[06])|IDM01|FOC03|REC05|PlaceRefDeleted|Undelete|UpdateTask|TextInputs' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): Drop, Delete, Undelete and UpdateTask" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 30: `Revert`

**Files:**
- Create: `internal/service/revert.go`, `internal/service/revert_test.go`
- Modify: `internal/service/op.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 16a runner (`run` steps 1, 7, 11, 14), `record`; Task 17 `checkInvariants`; Task 19 `moveTask`; Task 23 `recompute`; Task 26 focus rule (I6); Task 11 `GetOp`, `EventsOfOp`, `EventsAfter`, `ActiveReversal`, `SetOpState`, `ApplyColumns`; Task 23 `setBlock{Restore, Image}`, `afterUnslip`, `afterUnwork`.
- Produces:

```go
type RevertIn struct{ OpID domain.OpID }
func (s *Service) Revert(ctx context.Context, m Meta, in RevertIn) (*OpResult, error) // forward op op.revert, via "revert", revertible, pushed on the undo stack
func (o *op) revertTx(target domain.OpRecord) error // AIDEV-NOTE: conflict + net-zero rule; derived events skipped
// testutil:
func (s *Scenario) Revert(op domain.OpID) *service.OpResult
func (s *Scenario) StateDigest() string // live user state: tasks (backlog order, not rank values), reason sets, tags,
	// entries, blocks (status, source, cancel, slipped, span), focus, interruptions; no versions, *_at, *_seq or reason since/seq
```

Runner changes (A§4.3): for `classRevert`/`classUndo`/`classRedo` step 1 checks only `ownerOnly`/`notWeb`; **step 7** (after the idempotency lookup): `Revert` loads the op (`not_found`); then `Actor.Scopes.Has(target.Scopes)` and a non-owner actor must be the target's actor → `forbidden`; the ops row carries `via` and `reverts_op_id` in the same INSERT and `scopes = target.Scopes`; step 14 pushes a successful `Revert` on the actor's undo stack and clears its redo stack like a forward op; a revert/undo/redo with no domain events still commits with its ops row (R2-4). `revertTx` = A§4.6 steps 1–7 exactly, plus at step 6: R4-6 — for each task whose block the revert restored out of `slipped = 1` or out of `worked`, call `afterUnslip`/`afterUnwork`; P-2 — when the triage delta removed a `slipped`/`continue` row the target had added, re-evaluate it from the post-revert state and re-add it (`since` = now, `seq` = this op) if another block still justifies it (`slipped`: a block with `slipped = 1 AND slipped_seq > COALESCE(last_placed_seq, 0)`; `continue`: task open and a `worked` block with `resolved_seq > COALESCE(last_placed_seq, 0)` and no planned block on a later date) — helper `func (o *op) reasonJustified(task domain.TaskID, r domain.TriageReason) (bool, error)`. Apply map (step 4): insert → `ApplyColumns(deleted_at = now)` with a `*:tombstone` event (block via `setBlock{Restore}`), triage add → `store.RemoveReason`, triage remove → `store.AddReason` with the original `since`/`seq`, update → `ApplyColumns(Before)` with a `*:update` event, tombstone ↔ untombstone, `task_tags:set` → `SetTaskTags(Before.tags)`, move → rank right after `Before.after_id` (top when null), block events → `setBlock{Restore: true, Image: Before}`; every applied event is recorded for `o` (non-derived) and marks its entity dirty.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestSLP05`, `TestUND06`, `TestUND09`, `TestUND10`, `TestUND11`, `TestUND12`, `TestUND14`, `TestUND21`, `TestUND23`, `TestUND24`; `TestRevertConflictsWithSystemEvent` (UND-07's shape through `Revert`: `Block(#1, Mon)` on Mon, the Tue 00:00 reconcile resolves it, `Revert(block op)` → `revert_conflict` naming the `system` op; UND-07 itself calls `Undo` and is Task 31's), `TestRevertAlreadyReverted` (second `Revert` → `revert_conflict` "already reverted by owner"); `TestRevertRunsRemovalChecks` (R4-6: B1 10-28 13:00–14:00 and B2 10-27 13:00–14:00 both created 10-27 08:00; reconcile at 10-28 00:00 slips B2 (`#1{slipped}`); at 10:00 X = `Unblock(B1)` slips B1 without a triage event; Y = `AddEntry(#1, 10-27)` unslips B2 (reason kept for B1, `continue` added); `Revert(X)` → B1 `{planned, –, 0, –}` and `slipped` removed, `continue` kept); `TestRevertKeepsJustifiedReason` (P-2: B1, B2 today 13:00–14:00 and 15:00–16:00 created 09:00; at 10:00 U1 = `Unblock(B1)` adds `slipped`, U2 = `Unblock(B2)` slips B2 without a triage event; `Revert(U1)` → B1 `{planned, –, 0, –}`, `#1{slipped}` kept for B2, L/S 1/1); and:

```go
func TestRevertEveryOpKind(t *testing.T) {
	for _, tc := range []struct {
		kind  domain.OpKind
		setup func(s *testutil.Scenario) // builds state
		op    func(s *testutil.Scenario) // performs exactly one op of kind
	}{ /* task.add, task.update, task.due, task.place, task.accept_new, task.defer, task.manual, task.complete,
	      task.reopen, task.drop, task.delete, task.undelete, block.create, block.reblock, block.unblock,
	      block.outcome, entry.add, entry.update, entry.relink, entry.delete, entry.undelete, focus.set, op.revert */ } {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := testutil.NewScenario(t)
			tc.setup(s)
			before := s.StateDigest()
			tc.op(s)
			s.Revert(s.Last.OpID)
			if after := s.StateDigest(); after != before {
				t.Fatalf("state after revert differs:\n%s\n---\n%s", before, after)
			}
			// restored reason rows keep their original seq; reasons re-derived by the revert's recompute
			// (SLP-05 style) carry the revert's seq — asserted per case in the catalog tests, not here
		})
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'SLP05|UND(0[69]|1[0124]|2[134])|Revert' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per A§4.6 and the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): per-op revert with conflict detection and derived re-computation" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 31: `Undo` and `Redo`

**Files:**
- Create: `internal/service/undo.go`, `internal/service/undo_test.go`
- Modify: `internal/service/op.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 30 `revertTx` and runner classes; Task 11 stack functions.
- Produces:

```go
func (s *Service) Undo(ctx context.Context, m Meta) (*OpResult, error) // op.undo; scopes of the popped op; never pushed
func (s *Service) Redo(ctx context.Context, m Meta) (*OpResult, error) // op.redo; scopes of the popped op; never pushed
type undoRowKey struct{ Actor string; Stack store.Stack; Seq int64 }
type committedError struct{ Err *domain.Error; StackRow undoRowKey } // A§4.3
// testutil:
func (s *Scenario) Undo() *service.OpResult
func (s *Scenario) Redo() *service.OpResult
func (s *Scenario) Stack(actor string, which store.Stack) []domain.OpID
```

Rules (A§4.6, A§4.3 steps 7, 14, 17): the top of the actor's stack is selected in step 7 (empty → `nothing_to_undo`/`nothing_to_redo`, rolled back, nothing stored). `Undo`: top T not revertible → `committedError{not_revertible}`; `T.state = reverted` → `committedError{revert_conflict "already reverted by <actor of the active reversal>"}`; else `revertTx(T)` with `via = "undo"`; success moves T to the redo stack. `Redo`: T must be reverted with an active reversal R, else `committedError{revert_conflict}`; T or R not revertible → `committedError{not_revertible}`; else `revertTx(R)` with `via = "redo"`; success moves T back to the undo stack. A `revert_conflict`/`conflict` from `revertTx` leaves both stacks unchanged. `committedError` path: roll back the savepoint, delete the named stack row, store the idempotency row (status 409, `error_json` = code, detail, conflicts), `COMMIT`, return the error — no ops row, no event; a retry with the same key replays the 409 (Task 18 replay) without touching the next entry. Owner channels share the `owner` stacks.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestUND01` … `TestUND05`, `TestUND07`, `TestUND08`, `TestUND15` … `TestUND20`, `TestUND22`, `TestUND25`, `TestUND27`, `TestRCP05`, `TestREC10`, `TestINT09`, `TestSCP09`; `TestUndoStaleGeneration` (R3-3: `Undo` with another `Gen` → `generation_mismatch`, stacks unchanged); `TestUndoDepthTrim` (`UndoDepth` 3 via `SvcOpt` → newest three). Example:

```go
func TestUND20(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	ai := s.As(testutil.AI())
	ai.Log(id, "older", "") // Y
	ai.Log(id, "newer", "") // X, top of the mcp undo stack
	s.Revert(ai.Last.OpID)  // the owner reverts X
	k := "undo-key-0123456789"
	for i := 0; i < 2; i++ { // "already reverted by owner"; the retry replays the stored 409 and leaves Y alone
		_, err := ai.Svc.Undo(ai.Ctx(), ai.MetaWith(k, "{}"))
		testutil.RequireCode(t, err, domain.CodeRevertConflict)
	}
	if st := s.Stack("token:ai", store.StackUndo); len(st) != 1 {
		t.Fatalf("mcp undo stack = %v", st)
	}
	ai.Undo() // a new key reverts Y
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'UND|RCP05|REC10|INT09|SCP09|Undo' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above (AIDEV-NOTE on `committedError`).

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): per-actor undo and redo with committed pops" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 32: Read views — clock, today, status, upcoming, day, lists, tags, settings, banners

**Files:**
- Modify: `internal/service/views.go`
- Create: `internal/service/views_test.go`

**Interfaces:**
- Consumes: Task 16a `read`, `writeTx`, `Page`; Task 16b `taskView`, `entryView`, `blockView`, `duePhase`; Task 26 `currentBlock` rule; Task 9 `ListTasks`, `TagCounts`, `GetSetting`/`SetSetting`; Task 10 `ListEntries`, `BlocksBetween`, `InterruptionsOn`; Task 11 `CountAIOps`; Task 5 `domain.EncodeCursor`/`DecodeCursor`; Task 14 `backup.Ledger.Health`/`Pending`.
- Produces:

```go
type ClockView struct{ Now time.Time; Today civil.Date; Zone string; WeekStart time.Weekday; DueSoonDays int; Generation domain.Generation }
type WhoAmIView struct{ Actor domain.Actor; Generation domain.Generation }
type Banner struct{ Kind, Message string } // backup_failed, backup_stale, ai_changes, purge_incomplete, purge_ledger_damaged, tz_changed
type BackupHealth struct{ LastSuccessAt sql.Null[time.Time]; LastError string; Healthy bool }
type TodayView struct {
	Clock         ClockView
	Focus         *domain.TaskView
	CurrentBlock  *domain.BlockView
	Blocks        []domain.BlockView
	TriageCount   int
	TriageTop     []domain.TaskView // first 5 in triage order
	Overdue       []domain.TaskView
	DueSoon       []domain.TaskView // DueSoon flag, not deferred, not overdue
	Entries       []domain.EntryView
	Interruptions []domain.Interruption
	AIChanges     int
	Backup        BackupHealth
	Banners       []Banner
}
type UpcomingView struct{ Deferred, DueSoon []domain.TaskView }
type StatusTask struct{ ID domain.TaskID; Title string }
type StatusBlock struct{ ID domain.BlockID; TaskID domain.TaskID; Title string; End civil.EndMinute }
type StatusView struct{ Focus *StatusTask; CurrentBlock *StatusBlock; Triage, Due, AI int; BackupWarn, PurgeWarning bool }
type DayView struct{ Date civil.Date; Blocks []domain.BlockView; Entries, Unlinked []domain.EntryView; Interruptions []domain.Interruption }
type ListTasksIn struct{ View domain.View; Tag, Project string; Limit int; Cursor string }
type ListEntriesIn struct{ From, To civil.Date; TaskID *sql.Null[domain.TaskID]; IncludeDeleted bool; Limit int; Cursor string }
type ListBlocksIn struct{ From, To civil.Date; TaskID sql.Null[domain.TaskID] }
type TagCount struct{ Name string; Count int }
type SettingsView struct{ KeepDaily, KeepWeekly int }
func (s *Service) Clock(ctx context.Context, a domain.Actor) (ClockView, error)   // any authenticated actor
func (s *Service) WhoAmI(ctx context.Context, a domain.Actor) (WhoAmIView, error) // any
func (s *Service) Today(ctx context.Context, a domain.Actor) (TodayView, error)   // read; reconcile first
func (s *Service) Upcoming(ctx context.Context, a domain.Actor) (UpcomingView, error)
func (s *Service) Status(ctx context.Context, a domain.Actor) (StatusView, error)
func (s *Service) Day(ctx context.Context, a domain.Actor, d civil.Date) (DayView, error)
func (s *Service) ListTasks(ctx context.Context, a domain.Actor, in ListTasksIn) (Page[domain.TaskView], error)
func (s *Service) ListEntries(ctx context.Context, a domain.Actor, in ListEntriesIn) (Page[domain.EntryView], error)
func (s *Service) ListBlocks(ctx context.Context, a domain.Actor, in ListBlocksIn) ([]domain.BlockView, error)
func (s *Service) ListTags(ctx context.Context, a domain.Actor) ([]TagCount, error)
func (s *Service) Settings(ctx context.Context, a domain.Actor) (SettingsView, error)
func (s *Service) SyncTimezone(ctx context.Context, name string) (previous string, changed bool, err error)
	// A§4.10 step 9: compares/updates settings.tz_last_seen (writeTx, no event); when changed, Today/banners carry tz_changed for the process lifetime
func (s *Service) aiUnreviewed(ctx context.Context, q store.Q) (int, error) // store.CountAIOps after ai_reviewed_through
```

Rules: A§3.7 (scopes, reconcile-first column, orders, `From`/`To` required with span ≤ 366 days unless a task is given). Paging: `Limit` 0 → 100; < 0 or > 500 → `validation`. Every cursor is validated against its list's complete schema **before** any query, else `malformed`: keyset lists need exactly the Task 9/10 key tuple with the right types and ranges — triage `[rank 1–8, seq ≥ 1]` (encoded from `int(tv.Reasons[0].Reason.Rank())`, `tv.Reasons[0].Seq`), backlog `[int64]` (signed — top placement goes below zero), upcoming `[ISO date, minute 0–1439, int64]`, done/dropped/deleted `[ms ≥ 0]`, all `[]`, entries `[ISO date, 0|1, minute 0–1439, ms ≥ 0]` — plus `id ≥ 1` and no offset; offset lists (search) need `o ≥ 0` and no keys; `json.Number` keys must parse as integers in range (no floats, no objects, no strings where integers belong). `NextCursor` = encoded keys of the last item when a full page was returned. `Unlinked` = standalone entries of the day. Backup health: `Healthy` = `last_backup_at` within 48 h and `last_backup_error` empty; never backed up → unhealthy. Banners: `backup_failed` (error set), `backup_stale` (unhealthy without an error), `ai_changes` (unreviewed > 0), `purge_incomplete` (ledger `Pending` non-empty), `purge_ledger_damaged` (`Health().Damaged`), `tz_changed`. `Status.Due` = open tasks whose due phase is `due` or `overdue`; `PurgeWarning` = either purge banner.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI20` (reason rows inserted with chosen seqs through `Exec` — the one place this plan allows SQL fixtures for reasons), `TestDUE18`, `TestDUE19`, `TestREC06` (two `Today` calls at one instant: equal results, no ops row), `TestREC12` (`ListTasks{triage}` at 10-29 00:00 writes the midnight reconcile as `system`, then returns the post-reconcile view), `TestDAT14` (service half: `Clock` → now 2026-10-28T16:00Z, today 10-28, zone `America/Denver`, Monday, 3, the generation), `TestINT08` (`Day(10-28).Interruptions` and `Today.Interruptions` list INT-01's row; tombstoned rows hidden), `TestTodayView`, `TestStatusView`, `TestBanners` (each kind), `TestListTasksFilters` (Review Focus 5 through the service), `TestTriagePaging` (five triage tasks across three reasons, `Limit 2` → three pages in TRI-20 order with no duplicates or gaps), `TestBacklogPagingNegativeRanks` (ranks are signed: three `Add(…, Top())` tasks get ranks 0, −65536, −131072; `Limit 1` pages through them and the older positive-rank tasks in backlog order), `TestListBlocks`, `TestListTags`, `TestSettingsView`, `TestSyncTimezone`, and Review Focus 4:

```go
func TestPagingInputs(t *testing.T) {
	s := testutil.NewScenario(t)
	for i := 0; i < 3; i++ {
		s.Add(fmt.Sprint(i), testutil.Bottom())
	}
	list := func(in service.ListTasksIn) (service.Page[domain.TaskView], error) {
		return s.Svc.ListTasks(s.Ctx(), testutil.Owner(), in)
	}
	p, err := list(service.ListTasksIn{View: domain.ViewBacklog, Limit: 2})
	if err != nil || len(p.Items) != 2 || p.NextCursor == "" {
		t.Fatalf("page 1 = %+v, %v", p, err)
	}
	p2, _ := list(service.ListTasksIn{View: domain.ViewBacklog, Limit: 2, Cursor: p.NextCursor})
	/* p2 has the third task and NextCursor ""; Limit -1 and 501 → validation; Cursor "garbage" → malformed;
	   the backlog cursor reused for view triage (key count differs) → malformed; hostile cursors built with
	   domain.EncodeCursor — keys [{}], [1.5], ["x"], [9223372036854775808] (int64 overflow) for backlog, triage rank 9, an offset cursor on a keyset list,
	   id 0 — all → malformed, never internal; ListEntries without From/To → validation; From > To → validation;
	   a 367-day span → validation */
	_ = p2
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI20|DUE1[89]|REC06|REC12|DAT14|INT08|Today|Status|Banner|List|Paging|Settings|SyncTimezone' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): today, status, upcoming, day and list views with paging and banners" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 33: History, ops, AI changes and their review

**Files:**
- Create: `internal/service/history.go`, `internal/service/aichanges.go`, `internal/service/aichanges_test.go`

**Interfaces:**
- Consumes: Task 16a `read`, `run`, `effectsFromEvents`, `Page`; Task 32 cursor validation, `aiUnreviewed`; Task 11 `TaskEvents`, `ListOps`, `CountAIOps`, `OpTaskIDs`.
- Produces:

```go
type OpSummary struct {
	Seq        domain.OpSeq
	OpID       domain.OpID
	Kind       domain.OpKind
	Actor      string
	Channel    domain.Channel
	At         time.Time
	State      domain.OpState
	Revertible bool
	Via        string
	TaskIDs    []domain.TaskID
	Effects    []Effect
}
type EventView struct {
	ID            domain.EventID
	Op            OpSummary // Effects nil
	Entity        domain.EntityKind
	EntityID      int64
	Action        domain.Action
	Before, After json.RawMessage
}
type OpDetail struct{ Op OpSummary; Events []EventView }
type AIChanges struct{ ReviewedThrough domain.OpSeq; Unreviewed int; Ops []OpSummary; NextCursor string }
type HistoryIn struct{ TaskID domain.TaskID; Limit int; Cursor string }
type ListOpsIn struct{ Actor string; SinceSeq domain.OpSeq; Limit int; Cursor string }
type ListAIChangesIn struct{ IncludeReviewed bool; Limit int; Cursor string }
type MarkAIReviewedIn struct{ Through domain.OpSeq }
func (s *Service) History(ctx context.Context, a domain.Actor, in HistoryIn) (Page[EventView], error)        // read; task_id = ID, newest first
func (s *Service) ListOps(ctx context.Context, a domain.Actor, in ListOpsIn) (Page[OpSummary], error)         // read; newest first
func (s *Service) GetOp(ctx context.Context, a domain.Actor, id domain.OpID) (OpDetail, error)                // read; not_found
func (s *Service) ListAIChanges(ctx context.Context, a domain.Actor, in ListAIChangesIn) (AIChanges, error)   // read
func (s *Service) MarkAIReviewed(ctx context.Context, m Meta, in MarkAIReviewedIn) (*OpResult, error)
```

Rules (A§3.6, A§14 D14, R-a): AI changes = ops with `actor LIKE 'token:%'` plus `settings.retention_lowered`, newest first; without `IncludeReviewed` only `seq > ai_reviewed_through`; `Unreviewed` counts those. `MarkAIReviewed`: scope write, **owner only** (`ownerOnly`), idem, kind `ai.review`, not revertible; `Through ≥ o.rec.Seq` → `validation` (the bound excludes this op's own seq); `setting:update` `ai_reviewed_through = max(current, Through)` (event `{"key","value"}`); unchanged → `NoChange`. Cursors are keyed by event id (history) and seq (ops).

- [ ] **Step 1: Write the failing tests** — catalog rows `TestTRI25`, `TestSCP11`; `TestHistory` (a task's events across ops incl. derived ones, newest first, two pages with `Limit 2`), `TestGetOp` (events in id order, `TaskIDs`, effects), `TestListOps` (actor filter, `SinceSeq`), `TestMarkAIReviewedBound` (`Through` = the latest committed seq → ok and `Unreviewed` 0; `Through` = latest + 1 → `validation`; a lower `Through` later → `NoChange`; `AI()` → `forbidden`; the review op itself is not revertible).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'TRI25|SCP11|History|GetOp|ListOps|MarkAIReviewed' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): task history, op listing and AI-changes review" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 34: Work report and full-text search

**Files:**
- Create: `internal/service/report.go`, `internal/service/search.go`, `internal/service/report_test.go`, `internal/service/search_test.go`
- Modify: `internal/service/export_test.go`

**Interfaces:**
- Consumes: Task 16a `read`, `Page`; Task 16b `taskView`; Task 32 `ListTasks`/`Upcoming` (todo and upcoming sections) and cursor validation; Task 10 `ReportEntries`; Task 12 `store.Search`.
- Produces:

```go
type ReportIn struct{ From, To civil.Date; Tag, Project string; Todo, Upcoming bool }
type ReportLine struct {
	EntryID  domain.EntryID
	Kind     domain.EntryKind
	Title    string
	Notes    string
	TaskID   sql.Null[domain.TaskID]
	Time     sql.Null[civil.Minute]
	Reopened bool
}
type ReportDay struct{ Date civil.Date; Lines []ReportLine }
type ReportTask struct{ Title string; Status domain.TaskStatus; Deleted bool }
type ReportTodo struct{ Triage, Backlog []domain.TaskView }
type Report struct {
	From, To     civil.Date
	Tag, Project string
	Days         []ReportDay                       // ascending; days without lines omitted
	Tasks        map[domain.TaskID]ReportTask      // every task a line references: current title
	Todo         *ReportTodo                       // only with Todo
	Upcoming     []domain.TaskView                 // only with Upcoming: deferred (defer order), then due-soon not already listed
}
type SearchIn struct{ Query, Kind string; Limit int; Cursor string } // Kind task|entry|all ("" = all)
type SearchHit struct {
	Kind     string
	ID       int64
	TaskID   sql.Null[domain.TaskID]
	Title    string
	Snippet  string
	Status   domain.TaskStatus
	WorkDate sql.Null[civil.Date]
}
func (s *Service) Report(ctx context.Context, a domain.Actor, in ReportIn) (Report, error)        // read; reconcile first iff Todo || Upcoming
func (s *Service) Search(ctx context.Context, a domain.Actor, in SearchIn) (Page[SearchHit], error) // read; offset cursor {"v":1,"o":N}
func ftsQuery(user string) (string, error)                                                           // A§5.3
// export_test.go: func FTSQueryForTest(user string) (string, error)
```

Rules: `From`/`To` zero, `From > To` or a span > 366 days → `validation`; tombstoned entries are excluded (`store.ReportEntries`, ruling P10) while live entries of a deleted or purged task stay, annotated with that task's current title (`[purged]` after a purge); tag/project filters per A§3.7 (standalone entries excluded); line order within a day: timed first by `occurred_min`, then `created_at`, then id (D§14). `ftsQuery` (A§5.3): split on whitespace; each term double-quoted with `"` doubled; trailing `*`s stripped and one `*` emitted after the closing quote only if the stem is non-empty; a bare `*` term dropped; terms joined by spaces; nothing left → `validation`.

- [ ] **Step 1: Write the failing tests** — catalog §13 fixture built with clock travel (Fri 10-23: entries created 09:00 and 15:00, the standalone one at 15:00 with `--at 11:00`; Mon 10-26 entries in catalog order, `Complete(#42)`, `Complete(#57, As …)`; Tue 10-27 `Reopen(#42)`); catalog rows `TestRPT01` (service half: line order and fields), `TestRPT02`, `TestRPT05`, `TestRPT06` (incl. `-P inf` and `-P infra.key` match nothing, `-t Infra` = `-t infra`), `TestRPT08`, `TestRPT09` (shape: days ascending, `Tasks` entries, `Todo`/`Upcoming` nil unless asked), `TestRPT10`; `TestReportBounds`; `TestSearch` (task and entry hits; kind filter; tombstoned rows never hit; offset paging; a title hit ranks above a notes-only hit); `FuzzFTSQuery` (A§13.3: any input → either `validation` or a query that `store.Search` accepts on a temp store); and:

```go
func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"foo bar":  `"foo" "bar"`,
		"fo*":      `"fo"*`,
		"fo**":     `"fo"*`,
		`a"b`:      `"a""b"`,
		"x * y":    `"x" "y"`,
		"NEAR(a b": `"NEAR(a" "b"`,
	} {
		if got, err := service.FTSQueryForTest(in); err != nil || got != want {
			t.Fatalf("ftsQuery(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "*", "** *"} {
		if _, err := service.FTSQueryForTest(in); err == nil {
			t.Fatalf("ftsQuery(%q) accepted", in)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'RPT|Report|Search|FTS' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): work report and full-text search" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 35a: Token authentication, token admin, settings, password change and reset

**Files:**
- Create: `internal/service/{tokens.go, settings.go, password.go, usage.go}`, `internal/service/tokens_test.go`, `internal/service/password_test.go`

**Interfaces:**
- Consumes: Task 13 `auth.ParseToken`, `auth.FormatToken`, `auth.NewSecret`, `auth.HashSecret`, `auth.HashPassword`, `auth.ValidateNewPassword`, `auth.OwnerTokenName`, `(*auth.PasswordGate).Verify`; Task 11 `store.InsertToken`/`GetToken`/`ListTokens`/`RevokeToken`/`RevokeAllTokens`, `store.DeleteAllSessions`; Task 9 `store.GetAuthState`/`PutAuthState`, `store.GetSetting`/`SetSetting`; Task 32 `SettingsView`; Task 33 `ListAIChanges`; Task 16a `run`.
- Produces:

```go
type CreateTokenIn struct{ Name string; Scopes domain.Scopes; ExpiresAt sql.Null[time.Time]; MutationsPerHour int }
type TokenCreated struct{ Token domain.Token; Secret string } // Secret shown once
type RevokeTokenIn struct{ ID domain.TokenID }
type UpdateSettingsIn struct{ KeepDaily, KeepWeekly *int; Password string }
type ChangePasswordIn struct{ Current, New string }
type ResetPasswordIn struct{ NewPassword string; OwnerSecret [32]byte }
func (s *Service) AuthenticateToken(ctx context.Context, bearer string, verifiedSocket bool, ch domain.Channel, remoteKey string) (domain.Actor, error)
func (s *Service) CreateToken(ctx context.Context, m Meta, in CreateTokenIn) (TokenCreated, error)       // admin, notWeb, idempotency-exempt, token.create
func (s *Service) ListTokens(ctx context.Context, a domain.Actor) ([]domain.Token, error)                // admin, not web; SecretHash zeroed
func (s *Service) RevokeToken(ctx context.Context, m Meta, in RevokeTokenIn) (domain.Token, error)       // admin, notWeb, idem, token.revoke
func (s *Service) UpdateSettings(ctx context.Context, m Meta, in UpdateSettingsIn) (SettingsView, error) // admin, exempt
func (s *Service) ChangePassword(ctx context.Context, m Meta, in ChangePasswordIn) error                 // admin, ActorOwner only, exempt, auth.password
func (s *Service) ResetPasswordOffline(ctx context.Context, in ResetPasswordIn) (domain.TokenID, error) // A§12.8 step 3; returns the new owner token id
// usage.go (package-internal; Task 35b adds sessions and the flush):
type usage struct{ mu sync.Mutex; tokens map[domain.TokenID]time.Time; sessions map[[32]byte]time.Time }
func (s *Service) noteTokenUse(id domain.TokenID, at time.Time)
```

Rules: `AuthenticateToken` (A§7.1, A§7.2): unparsable → `unauthenticated`; unknown id → a dummy `ConstantTimeCompare`, `unauthenticated`; hash mismatch, revoked, `expires_at ≤ now` → `unauthenticated`; owner token without `verifiedSocket` → `unauthenticated`; owner with `ChanMCP` → `forbidden` (D33); owner → `Actor{ActorOwner, TokenID, AllScopes, ch, 0, remoteKey}`; token → `Actor{ActorToken, TokenID, Name, Scopes, ch, MutationsPerHour, remoteKey}`; success → `noteTokenUse(id, now)`. `CreateToken`: `ValidateName(_, 64)`, `owner.` prefix → `validation`, scopes 0 → `validation`, `ExpiresAt ≤ now` → `validation`, `MutationsPerHour < 0` → `validation`, duplicate name → `conflict`; `token:insert` after `{"name","scopes","expires_at"}` (never secrets); not revertible. `RevokeToken`: missing → `not_found`; owner → `conflict` (D29); already revoked → `NoChange`. `UpdateSettings`: below 3/1 → `validation`; lowering either value needs `Password` (`""` → `password_required`; gate with `actor.RemoteKey` → `password_invalid`) and records kind `settings.retention_lowered`, else `settings.update`; one `setting:update` per changed key; nothing changed → the current view, no op. `ChangePassword`: gate-verify `Current`, `ValidateNewPassword`, new hash and `password_changed_at`, delete all sessions, `auth:update {"what":"password_changed"}`. `ResetPasswordOffline`: one system op through `run` (actor `system`, channel `system`, kind `auth.password`, not revertible, no idempotency): validate and hash; `auth_state` (hash, `password_changed_at`, `web_login_disabled = 0`); `RevokeAllTokens` (`token:revoke` each); delete all sessions; insert the owner token (`auth.OwnerTokenName(now)`, `HashSecret(OwnerSecret)`, all scopes) with `token:insert`; `auth:update`. AIDEV-NOTE: owner token only with `verifiedSocket`; password bytes never logged or stored outside `auth_state`.

- [ ] **Step 1: Write the failing tests** — catalog rows `TestSCP04` (service half: owner token from `ResetPasswordOffline` with a known secret → `verifiedSocket=false` → `unauthenticated`, `true` → owner actor), `TestSCP07` (`ChanWeb` owner: `CreateToken`/`RevokeToken`/`ListTokens` → `forbidden`; lowering retention without password → `password_required`), `TestSCP12` (service half: expired, revoked → `unauthenticated`), `TestSCP13`; `TestCreateToken` (secret parses with `auth.ParseToken` and authenticates; the `token:insert` event JSON holds no secret; name rules), `TestRevokeToken`, `TestUpdateSettings` (raise → `settings.update`; lower with the password → `settings.retention_lowered`, listed by `ListAIChanges`; wrong password → `password_invalid`; 2 → `validation`), `TestChangePassword` (token actor → `forbidden`; wrong current → `password_invalid`; sessions deleted; `auth:update` content-free), `TestResetPasswordOffline` (all tokens revoked, sessions gone, one new owner row, `web_login_disabled` 0, events `token:revoke`…, `token:insert`, `auth:update`). Example:

```go
func TestSCP13(t *testing.T) {
	s := testutil.NewScenario(t)
	var secret [32]byte
	copy(secret[:], "0123456789abcdef0123456789abcdef")
	id, err := s.Svc.ResetPasswordOffline(s.Ctx(), service.ResetPasswordIn{NewPassword: testutil.Password, OwnerSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Svc.AuthenticateToken(s.Ctx(), auth.FormatToken(id, secret), true, domain.ChanMCP, "socket")
	testutil.RequireCode(t, err, domain.CodeForbidden)
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'SCP(04|07|12|13)|CreateToken|RevokeToken|UpdateSettings|ChangePassword|ResetPassword' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): token auth and admin, retention settings, password change and offline reset" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 35b: Web sessions, usage flushing and session expiry

**Files:**
- Create: `internal/service/sessions.go`, `internal/service/sessions_test.go`
- Modify: `internal/service/usage.go`, `internal/service/lifecycle.go`

**Interfaces:**
- Consumes: Task 13 `auth.NewSessionID`, `auth.ParseSessionCookie`, `auth.HashSessionID`, `auth.NewCSRFSecret`, `auth.CSRFToken`, `(*auth.PasswordGate).Verify`; Task 11 `store.InsertSession`/`GetSession`/`DeleteSession`/`TouchSessions`/`TouchTokens`/`DeleteExpiredSessions`; Task 18 `BeginShutdown`, `Drain`, `enter`; Task 35a `usage`, `noteTokenUse`.
- Produces:

```go
type LoginIn struct{ RemoteKey, Password, OldCookie string }
type Session struct{ CookieValue, CSRFToken string; ExpiresAt time.Time }
type WebAuth struct{ Actor domain.Actor; CSRFToken string }
const (SessionIdle = 7 * 24 * time.Hour; SessionMax = 30 * 24 * time.Hour)
func (s *Service) Login(ctx context.Context, in LoginIn) (Session, error)
func (s *Service) Logout(ctx context.Context, cookie string) error
func (s *Service) Authenticate(ctx context.Context, cookie string) (WebAuth, error)
func (s *Service) FlushUsage(ctx context.Context) error           // writes coalesced last_used_at / last_seen_at; allowed after Drain (final maintenance)
func (s *Service) ExpireSessions(ctx context.Context) (int, error) // expired (SessionMax) or idle (SessionIdle) sessions
func (s *Service) maintTx(ctx context.Context, fn func(tx *sql.Tx, now time.Time) error) error // writer tx that skips enter(): final maintenance only
```

Rules: `Login` (`writeTx`, no ops row): `web_login_disabled` → `forbidden`; `""` → `password_required`; gate (key `RemoteKey`) → `password_invalid`; delete the `OldCookie` session if present; new session (32-byte id stored hashed, CSRF secret, `expires_at = now + SessionMax`, `last_seen_at = now`, remote). `Authenticate`: bad cookie or unknown → `unauthenticated`; `expires_at ≤ now` → `unauthenticated`; last seen = max(stored `last_seen_at`, the in-memory value) and `last seen ≤ now − SessionIdle` → `unauthenticated`; success records `now` as the session's last seen in `usage` (so activity renews the idle window); returns owner/`ChanWeb`/`AllScopes` with `RemoteKey` = the session's remote and `auth.CSRFToken(secret)`. `FlushUsage` swaps the maps out under the mutex and writes `TouchTokens` + `TouchSessions` in one `maintTx`; it never calls `enter()`, so the A§4.10 shutdown order `BeginShutdown → Drain → FlushUsage → CheckpointTruncate` works (P12). `ExpireSessions`: `writeTx` + `DeleteExpiredSessions(now, SessionIdle)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSessionActivityRenewsIdle(t *testing.T) { // P12: activity keeps a session alive past 7 days, never past 30
	s := testutil.NewScenario(t)
	sess, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: "127.0.0.1", Password: testutil.Password})
	if err != nil {
		t.Fatal(err)
	}
	for day := 3; day <= 27; day += 3 { // authenticate every 3 days, flushing in between
		s.Clock.Advance(72 * time.Hour)
		if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
			t.Fatal(err)
		}
	}
	s.Clock.Advance(72 * time.Hour) // day 30: absolute cap
	_, err = s.Svc.Authenticate(s.Ctx(), sess.CookieValue)
	testutil.RequireCode(t, err, domain.CodeUnauthenticated)
}
```

Also: `TestLoginAndSessions` (wrong password → `password_invalid`; `""` → `password_required`; `web_login_disabled` → `forbidden`; `CSRFToken` = `auth.CSRFToken` of the stored secret; idle 7 d without activity → `unauthenticated`; `Logout` deletes; `OldCookie` session deleted on login), `TestFlushUsageAfterDrain` (record token and session use; `BeginShutdown`; `Drain`; `FlushUsage` succeeds and `last_used_at`/`last_seen_at` are written), `TestExpireSessions`.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'Session|Login|FlushUsage|ExpireSessions' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): web sessions with renewing idle window, usage flush and expiry" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 36: Backups, export, system ops

**Files:**
- Create: `internal/service/{backup.go, export.go, system.go}`, `internal/service/backup_test.go`, `internal/service/export_svc_test.go`

**Interfaces:**
- Consumes: Task 15 `(*backup.Manager).Snapshot`/`Rotate`, `backup.List`; Task 12 `store.ExportTable`, `store.ExportFile`, `ExportTables`, `ExportSettings`, `store.Sequences`; Task 18 `enter`, `stopCtx`, `BeginShutdown`, `Drain`; Task 32 `BackupHealth`, banner rule; Task 35b `maintTx`; Task 35a/35b ops for `TestIDM04`.
- Produces:

```go
type BackupStatusView struct {
	Dir       string
	Snapshots []backup.SnapshotInfo
	BackupHealth
	Warning   string
}
func (s *Service) RunBackup(ctx context.Context, a domain.Actor, kind backup.Kind) (backup.SnapshotInfo, error) // admin, or the system actor (scheduler); idempotency-exempt
func (s *Service) BackupStatus(ctx context.Context, a domain.Actor) (BackupStatusView, error)                  // admin
func (s *Service) Export(ctx context.Context, a domain.Actor, w io.Writer) error                                // admin; one reader tx; A§12.4
func (s *Service) RecordSystemOp(ctx context.Context, kind domain.OpKind, after json.RawMessage) (*OpResult, error)
	// system.restore | system.import only (else validation): one system op (noReconcile) with one content-free setting:update event (after = the JSON object)
const LongOpBudget = 10 * time.Minute // A§4.11: RunBackup and Export
// Export output contract: Export checks its context between rows, but cannot interrupt a Write already blocked in w.
// A caller whose writer can block must make Write return once shutdown begins (Stopping() closes, or the caller's
// own signal — Plan B's handler cancels its ctx and sets an immediate write deadline); a Write error is returned as is.
// export_test.go: func SetBackupHook(s *Service, fn func(ctx context.Context) error) // runs after the attempt is recorded, before Snapshot
```

Shutdown contract (review ruling P12): `RunBackup` and `Export` call `enter()` (after `BeginShutdown` → `busy`), stay registered until they return, and run under a context derived from the caller's ctx, `LongOpBudget` and `stopCtx()` — so `BeginShutdown` cancels them mid-operation and `Drain` waits for them to unwind (the export's reader transaction is closed, the backup's temp file deleted). Rules: `RunBackup` (A§12.1 step 5): nil `Backups` → `internal`; record `last_backup_attempt_at`; `Snapshot(kind)`; success → `last_backup_at` (unix ms as a decimal string), `last_backup_error = ''`, then `Rotate` with `backup_keep_daily`/`backup_keep_weekly`; failure (incl. cancellation) → `last_backup_error` = the error text, older snapshots kept, the error returned. Its settings writes use `maintTx` (no events), so they still land while shutdown is in progress. `BackupStatus`: `List(dir)` + health (Task 32 rule) + `Warning` = the banner message, if any. `Export`: header (`format`, `version` 1, `schema_version`, `exported_at` RFC 3339 ms in the server zone, `db_generation`), `sequences`, the allowlisted `settings`, every `ExportTables` table — streamed with `json.Encoder`, decodable into `store.ExportFile`; never `auth_state`, `api_tokens`, `web_sessions`, `idempotency`, `schema_migrations`.

- [ ] **Step 1: Write the failing tests** — catalog row `TestIDM04` (slog captured into a buffer via `SvcOpt`; `Login`, `ChangePassword`, `UpdateSettings` (lowering, with password), `CreateToken`, `RunBackup` called with `Meta.Idem` set → no `idempotency` row; the test password appears in no `ops`/`events` JSON, no idempotency row and no log line); `TestRunBackup` (snapshot file exists, settings updated, no events, rotation applied); `TestRunBackupFailure` (unwritable dir → error; `last_backup_error` set; `BackupStatus.Healthy` false, `Warning` non-empty); `TestBackupStatusHealthy` (≤ 48 h); `TestExport`:

```go
func TestExport(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Log(id, "did", "")
	var buf bytes.Buffer
	if err := s.Svc.Export(s.Ctx(), testutil.Owner(), &buf); err != nil {
		t.Fatal(err)
	}
	var f store.ExportFile
	if err := json.Unmarshal(buf.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Format != "taskronin-export" || f.Version != 1 || f.SchemaVersion != 1 || f.Sequences["tasks"] != 1 ||
		len(f.Tables["tasks"]) != 1 || len(f.Tables["log_entries"]) != 1 || f.Settings["backup_keep_daily"] != "7" ||
		f.ExportedAt != "2026-10-28T10:00:00.000-06:00" || f.DBGeneration != string(s.Svc.Generation()) {
		t.Fatalf("export = %+v", f)
	}
	for _, table := range []string{"auth_state", "api_tokens", "web_sessions", "idempotency", "schema_migrations"} {
		if _, ok := f.Tables[table]; ok {
			t.Fatalf("export contains table %s", table)
		}
	}
	for _, secret := range []string{"password_hash", "secret_hash", "idem_hmac_key", "$argon2id$"} {
		if bytes.Contains(buf.Bytes(), []byte(secret)) {
			t.Fatalf("export contains %s", secret)
		}
	}
}
```
plus `TestRecordSystemOp` (ops row `system.restore`, actor `system`, not revertible, one event; `task.add` → `validation`), `TestExportStopsOnShutdown` (`Export` into a test writer that honours the output contract: its first `Write` blocks until `Stopping()` closes, then returns an error; `BeginShutdown` → `Export` returns an error and `Drain` returns, each awaited with a 5 s test timeout so a regression fails instead of hanging; a new `Export` → `busy`), `TestRunBackupStopsOnShutdown` (`SetBackupHook` blocks until its ctx is done; `BeginShutdown` → `RunBackup` returns an error, no `.tkr-tmp-*` left in the backup dir, `last_backup_error` set, `Drain` returns).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'IDM04|Backup|Export|RecordSystemOp|StopsOnShutdown' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): backups with health, streamed export and system op records" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 37: Purge ledger replay, `PurgeLive`, purged-entity rules

**Files:**
- Create: `internal/service/purge.go`, `internal/service/purge_test.go`
- Modify: `internal/service/lifecycle_ops.go` (Undelete), `internal/service/entries.go` (UpdateEntry, RelinkEntry, UndeleteEntry), `internal/service/export_test.go`, `internal/testutil/scenario.go`

**Interfaces:**
- Consumes: Task 14 `backup.Ledger`/`LedgerRecord`; Task 12 `store.ScrubEntities`, `RaiseSequences`; Task 16a `run`; Task 16b `loadTask`, `loadEntry`, `clearReasons`; Task 23 `setBlock`; Task 26 `setFocus`; Task 28 `Reopen`; Task 29 `Undelete`; Task 25 entry edits; Task 31 `Undo` (UND-13/26).
- Produces:

```go
type ReplayMode uint8
const (ReplayLive ReplayMode = iota + 1; ReplayHistorical)
func (s *Service) ReplayLedger(ctx context.Context, recs []backup.LedgerRecord, mode ReplayMode) (*OpResult, error)
	// one system op system.ledger_replay; historical → opSpec{noReconcile: true, structuralOnly: true} (R2-2)
func (s *Service) ReapplyLedger(ctx context.Context) error // ReplayLedger(s.ledger.Records(), ReplayLive); never appends lines
type PurgeLiveIn struct{ Target string; ID int64; WithEntries bool } // Target "task" | "entry"
type PurgeLiveResult struct{ TaskIDs, EntryIDs, LinkedNotIncluded []int64; Changed bool }
func (s *Service) PurgeLive(ctx context.Context, m Meta, in PurgeLiveIn) (PurgeLiveResult, error) // offline only (S4); purge.task|purge.entry
// export_test.go: func SetPurgeAfterAppendHook(s *Service, fn func() error) // runs after the pending Append, before ScrubEntities
// testutil:
func (s *Scenario) SeqFloor(table string, next int64) // store.RaiseSequences so the next row id is `next` (catalog ids like #42)
```

Rules (A§12.0, A§12.6 step 5; AIDEV-NOTE: ledger-first order in `PurgeLive`): `ReplayLedger` over the union of the records' ids (missing ids skipped): a task not yet tombstoned gets delete semantics first — its planned blocks `setBlock{cancelled, task_closed, Slipped: false}` (no derivation, no slip), its reason rows removed, focus cleared if it points at it, `task:tombstone` — then `store.ScrubEntities`; when anything changed, one content-free purge event (`entity` = task if the set has a task else entry, `entity_id` = its first id, `after = {"ids":[task ids…, entry ids…]}`); nothing changed → no events → no ops row (R2-12). Scrubbed entries add no recompute pairs (blocks keep their resolved outcome). `PurgeLive` through `run` (actor from `Meta`, scope `destroy`, not revertible, no idempotency): task target — missing → `not_found`; not tombstoned → `conflict` "delete it first"; set = the task + all its `complete` entries (+ every linked entry with `WithEntries`); `LinkedNotIncluded` = linked entries outside the set. Entry target — missing → `not_found`; not tombstoned → `conflict` "delete it first", and when its task is tombstoned the detail says `use tkr purge task <id> --with-entries`; set = the entry. If a ledger record already covers exactly the set and every row is purged → `NoChange`, `Changed = false`, no append. Else `s.ledger.Append` the `pending` record (`V` 1, random 128-bit hex `Rec`, `At` = now ms) inside the writer transaction, the test hook, `ScrubEntities`, the purge event. Purged entities (R3-4): `Undelete` of a purged task, and `UpdateEntry`/`RelinkEntry`/`UndeleteEntry` of a purged entry → `conflict` ("purged"); `Reopen` still flags a purged completion entry (Task 28 rule).

- [ ] **Step 1: Write the failing tests** — catalog §14 fixture via `SeqFloor("tasks", 42)` and `SeqFloor("log_entries", 100)`: catalog rows `TestPRG04`, `TestPRG05` (service half: result, one pending ledger line `{tasks:[42], entries:[101]}`, op `purge.task` by owner on `cli`, not revertible, event `{"ids":[42,101]}`), `TestPRG06`, `TestPRG07`, `TestPRG09` (hook fails after the append → error, DB unchanged, ledger pending; `ReapplyLedger` then scrubs as `system.ledger_replay`), `TestPRG10` (historical replay on a second scenario's store holding the open #42 with `B9` planned 10-27 and `#42{new}`: B9 `{cancelled, –, 0, task_closed}`, reasons gone, scrubbed; another past planned block stays `planned`; `CheckStructural` passes), `TestPRG11` (replay after the scrub committed → no ops row), `TestPRG16`, `TestPRG17`, `TestPRG19` (ruling P10: after PRG-06 every #42 entry is tombstoned, so the report has **no** #42 lines; `GetTask(42)` returns `title = "[purged]"`, `purged_at` set, `view = deleted`; search `keycloak` finds nothing), `TestRPT07` (ruling P10: #7 deleted — its live entry is listed with its current title; #42 purged without `--with-entries` — its completion entry is gone from the report, its live work entries remain with `Tasks[42].Title == "[purged]"`), `TestUND13`, `TestUND26`; `TestI4TwoPurgedCompletions`; `TestPurgedEntitiesImmutable` (R3-4); and R3-1:

```go
func TestSequentialPurgesLeaveNoSecret(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("plain")
	s.Done(id, func(in *service.CompleteIn) { in.As = ptr("hunter2-secret") })
	s.Reopen(id, true) // remove-entry tombstones the completion entry
	purge(t, s, "entry", completionEntryID(t, s, id), false)
	s.Delete(id)
	purge(t, s, "task", int64(id), true)
	var n int
	s.Store.Reader.QueryRow(`SELECT COUNT(*) FROM events WHERE before_json LIKE '%hunter2%' OR after_json LIKE '%hunter2%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d events still hold the secret", n)
	}
}
```
(`ptr`, `purge` and `completionEntryID` are helpers local to the test file.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/service -run 'PRG|RPT07|UND1[3]|UND26|I4Two|Purged|SequentialPurges' -race -count=1` → FAIL.

- [ ] **Step 3: Implement** per the rules above.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/service -race -count=1` → PASS; `make check` → exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/service internal/testutil
git commit -m "feat(service): ledger-first PurgeLive, live and historical ledger replay" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 38: Catalog coverage guard, scope matrix, final verification

**Files:**
- Create: `internal/service/catalog_coverage_test.go`, `internal/service/scopes_test.go`

**Interfaces:**
- Consumes: every earlier task.
- Produces: the guard; Plans B and C name their scenario tests `Test<PFX><NN>_<suffix>` and remove their IDs from `notPlanA` in their final tasks (ruling P5).

- [ ] **Step 1: Write the tests**

```go
// IDs whose tests live in other plans (catalog §17-aligned list in this plan's "Catalog coverage").
var notPlanA = map[string]string{
	"TRI-17": "C", "FOC-04": "B", "FOC-05": "B", "FOC-06": "B", "FOC-07": "B", "FOC-08": "B", "FOC-09": "B",
	"FOC-10": "B", "FOC-11": "B", "FOC-12": "B", "INT-06": "B", "IDM-09": "B", "SCP-05": "B", "SCP-06": "B",
	"RPT-03": "B", "RPT-04": "B", "PRG-01": "B", "PRG-02": "B", "PRG-03": "B", "PRG-12": "B", "PRG-18": "B",
	"PRG-20": "B", "PRG-21": "B", "PRG-22": "B",
}

func TestCatalogCoverage(t *testing.T) {
	ids := catalogIDs(t, "../../docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md") // ^\| ([A-Z]{3}-\d{2}) \|
	if len(ids) != 281 {
		t.Fatalf("catalog has %d scenario ids, want 281", len(ids))
	}
	tests := testFuncNames(t, "../..") // module root: every *_test.go under internal/ and cmd/;
	// matches `^func Test([A-Z]{3})(\d{2})(_\w+)?\(` and records "<PFX><NN>"
	for _, id := range ids {
		if _, other := notPlanA[id]; other {
			continue
		}
		if !tests[strings.ReplaceAll(id, "-", "")] {
			t.Errorf("no test for %s", id)
		}
	}
}
```

`scopes_test.go`: catalog rows `TestSCP01` (token `ro` = read: `AddTask`, `AddEntry`, `Place` → `forbidden`; `Today` ok), `TestSCP02` (`AI()`: `Drop`, `Delete`, `DeleteEntry`, `UpdateEntry`, `Reopen{RemoveEntry}` → `forbidden`; `Complete`, `Defer`, `Block`, `Place`, `UpdateTask`, `Undelete`, `AddEntry`, `AddTask` without placement → allowed), `TestSCP08` (write-only token: `Place` allowed; `GetTask` → `forbidden`); and `TestScopeMatrix` (ruling P11) — one row per mutating method with its required scopes from A§7.3 and an `ownerOnly`/`notWeb` flag, run for three actor shapes: (1) a token holding all scopes but one required bit → `forbidden`; (2) a token holding exactly the required bits → not `forbidden`, except owner-only methods (`AddTask` with a placement, `MarkAIReviewed`, `ChangePassword`) → `forbidden` regardless of scopes; (3) the owner on `ChanWeb` → not `forbidden`, except token management (`CreateToken`, `RevokeToken`, `ListTokens`) → `forbidden`. Expected outcomes are explicit table columns, not derived from the scope bits.

- [ ] **Step 2: Run** — `go test ./internal/service -run 'CatalogCoverage|SCP0[128]|ScopeMatrix' -race -count=1` → PASS (every earlier task owns its IDs; a failure names the missing test — add it to the owning task's test file).

- [ ] **Step 3: Final verification** — `make check` → exit 0; `make fuzz FUZZTIME=10s` → every target runs without a failure; `grep -rn 'AIDEV-NOTE' internal | wc -l` lists the anchors required by A§1.4 that exist in Plan A's packages (`run`, `reconcileTx`, `setBlock`, `revertTx` incl. derived-event skipping, `civil.Zone.Instant`, `store.ScrubEntities`, `PurgeLive` ledger-first order, `backup.OpenLedger`, the FTS triggers and views (in the migration), the pragma hook, the seq-based acknowledgement comparisons in `setBlock`/`taskView`).

- [ ] **Step 4: Commit**

```bash
git add internal/service
git commit -m "test(service): catalog coverage guard and scope matrix" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Planner decisions & open items

Engineering calls the architecture left open (each marked in its task). None needs the user.

1. **Plan boundary.** Plan A owns `internal/fsutil` and the parts of `internal/backup` that `service` imports at compile time — `ledger.go`, `snapshot.go`, `verify.go`, `rotate.go` — because `service → backup` is an A§1.3 edge; Plan B owns `stage.go`, `swap.go`, `inventory.go`, `CleanupTemps` and every offline command. (Tasks 14, 15)
2. **Store API.** All store function names and signatures not fixed by A§4–5 (Tasks 7–12), incl. `store.OpenStaging` (always `mode=rwc`, copies only), `(*Store).CheckpointTruncate`, `store.BootstrapDB` (init-time rows, shared by `offline.Init` and `testutil`), `ErrNotFound`, `ApplyColumns` (allowlisted revert writes), `EventsAfter` with a triage-reason key, and the `ListTasks`/`ListEntries` keyset key tuples. Names adopted from Plan B's draft where it guessed: `OpenStaging`, `CheckpointTruncate`, `BeginShutdown`, `Drain`, `ExpireIdempotency`, `ExpireSessions`, `SyncTimezone`, `ReplayLive`/`ReplayHistorical`, `testutil.Password`.
3. **Pragma hook strictness.** The connection hook rejects any `sqlite` connection whose DSN lacks the common parameters, so nothing in the process can open the DB without `store.DSN`. (Task 7)
4. **`MapErr` table.** busy/locked → `busy` (RetryAfter 1 s); `taskronin:` RAISE → `conflict`; UNIQUE/PK/FK → `conflict`; CHECK/NOT NULL → `validation`. (Task 7)
5. **Migration freeze.** A test pins each embedded migration's SHA-256; `applied_at` comes from SQLite's clock so `store` never reads the Go clock. (Task 8)
6. **Cursor codec in `domain`** (`EncodeCursor`/`DecodeCursor`, A§6.3 format), so `service` can emit `NextCursor` strings without importing `api`; garbage → `malformed`. `Page[T]{Items; NextCursor string}` lives in `service`. Limit 0 → 100, < 0 or > 500 → `validation`. (Tasks 5, 32)
7. **`civil` details.** Years 1000–9999; `LoadZone` accepts `UTC` and names containing `/` only. (Tasks 2, 3)
8. **`datespec` ranges** `Week`/`LastWeek`/`Month` in `range.go` (A§1.2 lists the file, not its API). (Task 6)
9. **`domain` additions:** `NewGeneration`, `OpID.IsZero`, `TriageReason.Rank`, `Valid()` on every enum, `Moment`/`BlockSpan` `String`, `ValidationError`, typed `OpKind` constants. (Task 5)
10. **`auth` additions:** `OwnerTokenName`, session/CSRF helpers; `VerifyPassword` honours the encoded argon2 parameters within bounds (m ≤ 1 GiB, t ≤ 10, p ≤ 4) so tests can use cheap hashes; the gate reserves on the injected clock. (Task 13)
11. **Ledger:** a missing file is an empty healthy ledger; `Append` computes `Sum`; valid records cached in memory; added `Health()` and `Pending()`. (Task 14)
12. **Snapshots:** rotation buckets are UTC days and ISO weeks (names carry UTC stamps); `List` parses names only (`SchemaVersion` 0); `Manager` is used as a struct literal. (Task 15)
13. **Event payloads use storage representation** (ms, ISO dates, integer minutes, 0/1), keyed by column; `version`/`*_at` bookkeeping columns excluded; rank changes only as `task:move`; `Event.TaskID` rules for focus/interruptions. (Task 16a)
14. **Effects mapping** from events (table in Task 16a) and **result refs** → `Task`/`Tasks`/`Entry`/`Block`.
15. **Service input/output names:** `XIn` structs, `TaskRef`/`EntryRef`/`BlockRef`, `Undo(ctx, m)`/`Redo(ctx, m)`, admin ops returning their natural types (`TokenCreated`, `domain.Token`, `SettingsView`, `error`, `backup.SnapshotInfo`), view structs mirroring A§6.5 with domain types. (Tasks 16a–37)
16. **Options defaults:** `SlipGrace` 0 → 15 m, `UndoDepth` 0 → 1000; `DueSoonDays` used as given (0 is a valid setting). (Task 16a)
17. **Every placement clears a defer**, implemented once in `o.place` (catalog §0; now A§3.1's placement legend, P-3), so `Complete`/`Drop`/`AcceptNew`/`Reblock` on a deferred task clear it too. (Task 19)
18. **Rank of the first task** = `Gap` (65 536). (Task 16b)
19. **`AddPlacement` shape check** (TRI-09): fields not belonging to `How` → `validation`. (Task 19)
20. **Idempotency rows older than 7 days are ignored at lookup** (and replaced), so IDM-05 holds even before the hourly expiry runs. (Task 18)
21. **`AcceptNew` order** = ascending `new` seq (A§3.2); identical to item order in TRI-18. (Task 19)
22. **No-op answers:** `Undelete` of a live task, `UndeleteEntry` of a live entry, `SetOutcome{auto}` on a planned block, `SetFocus` to the current focus, `RevokeToken` of a revoked token, `MarkAIReviewed` with a lower bound → `NoChange`. (Tasks 24–26, 29, 33, 35a)
23. **`SetDue`** allowed on any non-deleted task; **`Defer` of a non-open task** → `conflict`. (Tasks 20, 27)
24. **`SetOutcome` on a `done` block** is allowed (it is resolved), giving a user `worked`/`missed`. (Task 24)
25. **View `all`** = every non-deleted task by id; `deleted` by `deleted_at` desc; `Today.DueSoon`/`Upcoming.DueSoon` exclude overdue tasks (they are listed as overdue); never backed up → unhealthy, `backup_stale`. (Task 32)
26. **`tz_changed` banner** is set in memory by `SyncTimezone` for the process lifetime. (Task 32)
27. **Login/Logout** use a plain writer transaction (`writeTx`) without an ops row — sessions are auth bookkeeping; `FlushUsage` uses `maintTx` (decision 44). (Task 35b)
28. **`ResetPasswordOffline` and `RecordSystemOp`** live in `service` (they must run through `run`); Plan B's offline commands call them. (Tasks 35a, 36)
29. **`RunBackup`** accepts the system actor (scheduler) besides `admin`; settings bookkeeping without events. (Task 36)
30. **Purge event payload** `{"ids":[task ids…, entry ids…]}` (catalog PRG-05); `ScrubEntities` marks ops purged when they have a `task`/`task_tags`/`triage` event on a listed task or an `entry` event on a listed entry; scrubbed entries add no recompute pairs. (Tasks 12, 37)
31. **Search:** plain snippets (empty markers); `all` merges both tables by bm25. (Tasks 12, 34)
32. **Report `Upcoming`** = deferred tasks then due-soon tasks not already listed. (Task 34)
33. **`blockChange.Image`** added to A§4.5's struct so `setBlock{Restore}` has the before-image to write. (Task 23)
34. **`testutil` signatures:** `Meta(a, gen)` (A§13.1 predates the generation header, R3-3); `Store` cleanup runs `CheckStructural`, `Service` cleanup runs `Reconcile` then `CheckAll`; `StoreNoCheck`; the argon2 hash of `Password` is computed once per test binary. (Tasks 16a, 17)
35. **Archtest TUI launchers:** the `…` in A§1.3's `cli` exceptions is a map the plan adding another TUI launcher file extends. Archtest checks non-test imports only, so `_test.go` files (e.g. Plan C's teatest imports) are unconstrained. (Task 1)
36. **`civil.Zone.Location()`** added so `api` can render server-zone offsets without a second `LoadLocation`. (Task 3)
37. **ETag codec in `domain`** (`FormatETag`/`ParseETag`), because `web` may not import `api`; `api/etag.go` (Plan B) can delegate. (Task 5)
38. **Restore/import data steps are Plan B's.** Plan A supplies the primitives (`PutAuthState`, `InsertToken`, `RevokeAllTokens`, `RaiseSequences`, `SetSetting`, `DeleteAllSessions`, `ClearIdempotency`, `BootstrapDB`) and `RecordSystemOp` for the `system.restore`/`system.import` op record; the row-level restore step 4 and import step 4a–c routines are new store files in Plan B (`store/restore.go`, `store/importrows.go`); restore clears idempotency rows with `store.ClearIdempotency` (ruling P7). (Tasks 9, 11, 36)
39. **Reports exclude tombstoned entries** (ruling P10), so PRG-19 and RPT-07 expect no lines for purged (tombstoned) entries; live entries of a deleted or purged task stay, annotated with its current title. Deviates from the catalog text of PRG-19/RPT-07. (Tasks 34, 37)
40. **Rows created by an op keep version 1** (`o.markInserted`), even when the same op places, tags or focuses them. (Task 16a)
41. **An undo/redo/revert with no domain events has `Effects = []`**; `reverted` leads the list only when at least one event exists (UND-25). (Task 16a)
42. **The reconcile framework (`Reconcile`, `reconcileTx`, `reconcileSteps`) lives in Task 16a** with no steps, so every later task and harness cleanup can call it; Tasks 20, 23, 26, 27 register the D§5.8 steps. (Tasks 16a, 20)
43. **Cursor schemas are validated per list before any query** (types, ranges, keyset vs offset form); triage cursors encode `[Rank(), seq]`. (Tasks 9, 32)
44. **Final maintenance path:** `maintTx` skips `enter()` so `FlushUsage` and backup bookkeeping work after `BeginShutdown`/`Drain`; `Authenticate` records session activity, flushed by `FlushUsage`, renewing the 7-day idle window under the 30-day cap. (Tasks 18, 35b, 36)
45. **Long operations:** `RunBackup` and `Export` register as in-flight work with `LongOpBudget` (10 min) and observe `stopCtx()`, which `BeginShutdown` cancels. (Tasks 18, 36)
46. **DST from real transitions:** `Zone.Instant` takes candidate offsets from the zone periods around the instant (`ZoneBounds`), tested in Denver and Pacific/Auckland. (Task 3)
47. **Task splits** (ruling P3): 16 → 16a (runner, events, effects, reconcile framework, harness) and 16b (`AddTask`/`MarkManual`/`GetTask`, views); 35 → 35a (tokens, settings, passwords) and 35b (web sessions, usage flush, expiry). TRI-22 moved from Task 18 to 19 and BLK-41's `done` variant to Task 28 (ruling P2). (Tasks 16a–b, 18, 19, 24, 28, 35a–b)

Open items for the architect: none. The three raised earlier are resolved in A§14.10 — P-2 (revert keeps a reason another block still justifies; Task 30), P-3 (every placement clears a pending defer; decision 17), P-4 (`testutil.Meta(a, gen)`; decision 34).
