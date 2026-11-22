# Taskronin

Taskronin is a single-user task manager built around triage (new, slipped and
due work must be consciously cleared), daily focus (time blocks, a "now working
on" focus, a morning plan and evening recap loop) and a work log linked to
tasks. It ships as one Go binary, `tkr`, over a single SQLite database.

New here? The [quickstart](docs/quickstart.md) shows a five-minute tour.

## Why not Taskwarrior?

Taskronin exists because Taskwarrior failed its author in two specific ways: it
was *out of sight* (nothing pulled him back to the plan during the day) and
*interrupts won* (new work displaced what he had planned, silently). It is not
a Taskwarrior replacement in general; it fixes those two things and the
problems next to them.

- **It pulls you back during the day.** A bare `tkr` dashboard, a
  `tkr status --short` line for your prompt or tmux, a single focus task, and
  a morning plan and evening recap loop. Taskwarrior only speaks when you run
  it.
- **Interruptions are recorded instead of winning.** `tkr add` lands in triage
  without touching your plan or focus. `tkr add … --now` records what
  displaced what, and the displaced block shows up at recap as a slip instead
  of vanishing. Taskwarrior has no notion of planned versus happened.
- **Triage is an inbox you must clear, not an urgency score.** New, slipped,
  surfaced and overdue work enters one list, each item saying why it is there,
  and leaves only by an explicit decision: order, defer, block, finish or
  drop. A computed score that reorders the list behind your back is a
  deliberate anti-goal.
- **Time blocks have outcomes.** Blocks are marked worked, partial, missed or
  cancelled; missed blocks come back for re-triage the same day and slips are
  counted per task. Taskwarrior's `scheduled` is a timestamp with no
  follow-through.
- **The work log is first class.** `tkr log` entries attach to a task or
  stand alone, and `tkr report --week` rolls them into a dated "what I did"
  report. Taskwarrior gets partway there with annotations plus Timewarrior.
- **Undo is a real audit trail.** Every write is an operation with an ID, an
  actor and an idempotency key. `undo`, `redo`, `revert OPID` and `history`
  all work from that record, and undo says in words what it put back.
- **AI assistance with guardrails.** The built-in MCP server lets an assistant
  read, add, log, complete, defer and edit, never delete. AI-created tasks
  always land in triage, writes are rate limited, and `tkr ai-changes` lists
  every change made by a non-owner token for review or one-command revert.
- **Four front doors, one database.** CLI, TUI, a phone-friendly web UI and
  MCP all talk to one local server. Taskwarrior is CLI only; its TUIs, web
  UIs and sync come from separate projects.
- **Built to be trusted.** Verified snapshots with restore, portable JSON
  export and import, stale-view detection instead of silent overwrites, and a
  loopback-only web UI with password login, CSRF and a strict CSP.

Where Taskwarrior still wins, so you can decide with open eyes:

| Taskwarrior has | Taskronin's position |
|---|---|
| Recurring tasks | Not in v1 |
| Multi-device sync | Single workstation only in v1 |
| Filter and report language, UDAs, hooks | Tags, an optional project, priority and search; no query language |
| Ecosystem (vit, Timewarrior, Bugwarrior, taskwarrior-tui) | None |
| Years of hardening and documentation | New, and built for one user first |

Choose Taskronin if your problem was never a lack of a powerful task database
but that the plan kept losing to the day.

## Install

Taskronin needs Go (see `go.mod` for the toolchain) and runs on Linux.

```
make install                     # builds and installs ~/.local/bin/tkr
tkr init
```

`make install PREFIX=/some/dir` installs to `/some/dir/bin/tkr` instead. Make
sure that directory is on your `PATH`, and run `tkr init` from the installed
copy: the systemd units it writes point at the binary that ran it.

`tkr init` asks for an owner password (8 to 1024 bytes), creates the private
data, state and config directories, the database, the `owner` token
(`~/.local/state/taskronin/owner.token`) and, unless you pass `--no-systemd`,
installs and enables two systemd user units:

- `taskronin.socket` listens on `$XDG_RUNTIME_DIR/taskronin/sock`;
- `taskronin.service` runs `tkr serve` and is started on first use.

Useful `init` flags: `--timezone Area/City` (default: `/etc/localtime`),
`--mcp-token`, `--password-stdin`, `-y`, and `--units-only` to reinstall the
units. Check the service with `systemctl --user status taskronin.socket`.

## Daily use

```
tkr add "Set up keycloak postgres cluster"        # captured into triage
tkr add "Restart stuck pg replica" --now          # skips triage, sets focus
tkr add "Run ansible against dev" --top           # skips triage, top of backlog
tkr plan                                          # TUI in plan mode (morning)
tkr block 42 13:00-15:00                          # time block today
tkr now 42                                        # set focus
tkr log 42 "Added Ansible role for postgres"      # log work on a task
tkr log -N "Answered Bob's DNS question"          # log with no task
tkr log 42 "Rebuilt CI runner" --date yesterday   # backdated
tkr done 42                                       # complete and log it
tkr done 57 --as "Better title"                   # complete and retitle
tkr recap                                         # TUI in recap mode (evening)
tkr today                                         # dashboard
tkr ls                                            # triage, then backlog
tkr report --week                                 # work report (text, md, json)
tkr undo                                          # undo your last operation
```

`tkr --help` lists every command and `tkr COMMAND --help` its flags. All
commands accept `--json`, `--socket PATH` and `--token-file PATH`.

If a command's outcome is unknown (the connection dropped after the request was
sent), the request is saved and `tkr pending` lists it; `tkr pending retry`
resends it with its original idempotency key and `tkr pending discard` drops it.

### Status line

```
# bash
PS1='$(tkr status --short) '"$PS1"
# zsh
setopt prompt_subst; RPROMPT='$(tkr status --short)'
# tmux
set -g status-right '#(tkr status --short --ascii)'
```

`tkr status --short` is built to be prompt-safe: it prints nothing and exits 0
when the server is down or slow.

## Web UI

The web UI is enabled by default. On single-user hosts, `tkr init` records
consent for plain loopback HTTP, so the UI starts automatically with the server.
The default settings in `~/.config/taskronin/server.toml` after that consent are:

```toml
[web]
enabled = true
listen = "127.0.0.1:7878"
allow_plain_loopback = true
```

Set `enabled = false` in `[web]` to turn the UI off. If its port is busy or the
web handler cannot start, the server logs a warning and continues serving the
unix-socket API without the web UI.

After changing settings, restart the server (`systemctl --user restart
taskronin.service` when using systemd). Open <http://127.0.0.1:7878/ui/>, and log in
with the owner password set by `tkr init`. The UI shares tasks with the CLI and
TUI. Remote access is not supported in v1.

## MCP

Create a dedicated token for the MCP client and keep the secret out of the
terminal:

```
tkr token create mcp --mcp --write-file
```

`--mcp` gives the read, capture and write scopes at 60 mutations per hour;
`--write-file` writes the secret to `~/.local/state/taskronin/tokens/mcp.token`
(0600). Use `--expires` to bound its life. Then add `tkr mcp` to the client's
MCP configuration:

```
{"command": "tkr", "args": ["mcp"]}
```

`tkr mcp` speaks MCP over stdio. It refuses the owner token and needs the `mcp`
token (use `--token-file` to point at another one). Changes made through
non-owner tokens can be reviewed with `tkr ai-changes` and reverted with
`tkr ai-changes --revert OPID` (or `tkr revert OPID` for any operation); the
8-character ID they print is enough unless it is ambiguous.

## Backups

The server takes verified snapshots on a schedule into
`~/.local/share/taskronin/backups`. `tkr backup` takes one now, `tkr backup
--list` lists them, and `tkr export -o FILE` writes a portable JSON export.
Lowering retention (`tkr settings set`) asks for the owner password.

Snapshots live on the same host as the database, so copy them elsewhere:

```
restic -r sftp:host:/srv/restic backup ~/.local/share/taskronin/backups
rsync -a --chmod=D0700,F0600 ~/.local/share/taskronin/backups/ host:taskronin-backups/
```

## Offline administration

These commands work on the database directly and refuse to run while the
server is up. Stop both units first, and start them again afterwards:

```
systemctl --user stop taskronin.socket taskronin.service
...
systemctl --user start taskronin.socket
```

| Command | Purpose |
|---|---|
| `tkr migrate [--status]` | apply schema migrations, or show the schema status |
| `tkr restore SNAPSHOT [-y]` | restore a snapshot (name from `tkr backup --list`, or a path); the old database is kept as `<db>.pre-restore-<time>/` |
| `tkr restore --resume` / `--rollback` | finish or undo an interrupted restore or import |
| `tkr import FILE [-y]` | load a `tkr export` file into a freshly initialized, empty database |
| `tkr passwd --reset [--password-stdin]` | set a new owner password; revokes every token |
| `tkr purge task\|entry ID [--with-entries]` | permanently scrub a deleted task or entry from the database; `--repair-ledger` repairs a damaged purge ledger |
| `tkr scrub-backups [--check] [--resume]` | remove purged content from every backup; `--check` rewrites no backup (it still finishes an interrupted rewrite and applies the purge ledger to the live database) and exits 8 if any set still holds purged content |

`purge` and `scrub-backups` need the owner password typed on a terminal. Exports
and log lines made before a purge are out of its reach.

The purge ledger (`~/.local/share/taskronin/purge-ledger`) belongs to the
database in the same data directory: every restore, import and server start
re-applies it by task and entry ID. Only restore snapshots and import exports
that came from this instance, and never copy a ledger between instances. `tkr
init` refuses to start over a leftover ledger; to begin again from scratch,
move the old data directory aside rather than deleting just the database.

## Security model

- Processes running as you are trusted. They can run `tkr` (which reads
  `owner.token`), read the database and backups, stop the server or delete
  files. MCP scopes constrain only MCP-only clients.
- Within the API, protection is recoverability: every operation is revertible,
  deletes are tombstones, and backups are verified. The only irrecoverable
  operations, purge and backup scrubbing, require your current password on a
  terminal.
- Against raw file access nothing in-process can help. Keep off-host backups.
- Other local users, malicious web pages, a stolen token file (tokens are
  scoped, revocable and can expire), prompt injection through task text, and
  terminal escape sequences in stored text are all in scope.
- All files are private (0600, directories 0700). Never sync or commit
  `owner.token` (or anything under the `tokens` directory): the owner token is
  accepted only over the local socket, but it is the key to your data.

## Development

- Run `make check` before every commit (fmt-check, vet, lint, race tests,
  govulncheck, `go mod verify`, asset hashes).
- `make fuzz` runs every `Fuzz*` target (`FUZZTIME=30s` by default).
- Grep for `AIDEV-` anchors before editing a package, and never delete an
  `AIDEV-NOTE`.
- Migrations are never edited once committed.
- Layering is enforced by `internal/archtest`; see architecture section 1.3.

Specs:

- [Design](docs/superpowers/specs/2026-10-25-taskronin-design.md)
- [Architecture](docs/superpowers/specs/2026-10-25-taskronin-architecture.md)
- [Behavior catalog](docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md)
