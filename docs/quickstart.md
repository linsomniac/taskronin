# Taskronin quickstart

**Your tasks, your focus, your work log: one fast binary, one SQLite file, zero cloud.**
Taskronin makes you consciously *triage* new and slipping work, keeps one task in focus,
and turns what you did into a report, from the CLI, a TUI, a local web UI or your AI assistant.

## Install (Linux, about 1 minute)

```
$ make install && tkr init          # sets a password, creates the DB, starts on demand via systemd
```

## Capture now, decide later

```
$ tkr add "Set up keycloak postgres cluster"
#1 'Set up keycloak postgres cluster' added to triage (new)
$ tkr add "Restart stuck pg replica" --now
#2 'Restart stuck pg replica' added, focus → #2
$ tkr add "Write Q4 capacity report" --due friday
#3 'Write Q4 capacity report' added to triage (new)
$ tkr add "Renew TLS certs" --defer mon
#4 'Renew TLS certs' added, deferred to Mon 2026-11-23
```

## Triage, schedule, log as you go

```
$ tkr block 1 "tomorrow 09:00-11:00"
b1 tomorrow 09:00–11:00 #1 'Set up keycloak postgres cluster'
$ tkr log 2 "Replica caught up after WAL replay"
e1 logged today on #2 'Restart stuck pg replica'
$ tkr log -N "Answered DNS question from Bob"     # interruptions count too
e2 logged today (no task)
$ tkr done 2
✓ #2 'Restart stuck pg replica' done · logged today
focus cleared
one done today
$ tkr report --week                              # text, Markdown or JSON
Work report Mon 2026-11-16 – Sun 2026-11-22

Fri 2026-11-20
  - Replica caught up after WAL replay  [#2 Restart stuck pg replica]
  - Answered DNS question from Bob
  ✓ Restart stuck pg replica            [#2]
```

Made a mistake? `tkr undo` reverses your last operation and tells you exactly what it put back.

## Four ways in, one source of truth

- **TUI:** `tkr tui` (tabs: Today · Triage · Backlog · Upcoming · Log), `tkr plan` each morning, `tkr recap` each evening.
- **Web:** <http://127.0.0.1:7878/ui/>, password login, loopback only, strict CSP, CSRF-protected.
- **Prompt:** `tkr status --short` puts `▶ #2 Restart stuck pg replica · triage 1` in your shell or tmux.
- **AI assistant (MCP):** `tkr token create mcp --mcp --write-file`, then add `{"command": "tkr", "args": ["mcp"]}`.

## Let the AI help, and stay in charge

```
$ tkr ai-changes
op 4e3ba7ab #12 task.add by token:mcp at 2026-11-20 13:31:54-07:00
  #5 created
  #5 needs triage (new)
op 5d33a595 #11 entry.add by token:mcp at 2026-11-20 13:31:54-07:00
  e4 logged
$ tkr revert 5d33                                # a unique prefix is enough
reverted log (op 5d33a595)
e4 deleted
```

AI-created tasks always land in triage, writes are rate-limited, and every change is
reviewable and revertible.

## Built to be trusted

- **Your data stays local.** Everything is in one local SQLite file, with verified daily snapshots and `tkr export` to portable JSON.
- **No double writes.** Every write carries an idempotency key, so retries and double-clicks never double-apply.
- **Stale views get caught.** A page or screen changed elsewhere gets a "changed elsewhere — reload" warning instead of a silent overwrite.
