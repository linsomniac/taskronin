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
#4 'Write Q4 capacity report' added to triage (new)
$ tkr add "Renew TLS certs" --defer mon
#5 'Renew TLS certs' added, deferred to Mon 2026-11-16
```

## Triage, schedule, log as you go

```
$ tkr block 1 "tomorrow 09:00-11:00"
b1 Mon 2026-11-16 09:00–11:00 #1 'Set up keycloak postgres cluster'
$ tkr log 2 "Replica caught up after WAL replay"
e1 logged Sun 2026-11-15 on #2 'Restart stuck pg replica'
$ tkr log -N "Answered DNS question from Bob"     # interruptions count too
$ tkr done 2
#2 'Restart stuck pg replica' completed; ✓ logged Sun 2026-11-15
$ tkr report --week                              # text, Markdown or JSON
Sun 2026-11-15
  - Replica caught up after WAL replay  [#2 Restart stuck pg replica]
  - Answered DNS question from Bob
  ✓ Restart stuck pg replica            [#2]
```

Made a mistake? `tkr undo` reverses your last operation and tells you exactly what it put back.

## Four ways in, one source of truth

- **TUI:** `tkr tui` (tabs: Today · Triage · Backlog · Upcoming · Log), `tkr plan` each morning, `tkr recap` each evening.
- **Web:** <http://127.0.0.1:7878/ui/>, password login, loopback only, strict CSP, CSRF-protected.
- **Prompt:** `tkr status --short` puts `▶ #2 Restart stuck pg replica · triage 2` in your shell or tmux.
- **AI assistant (MCP):** `tkr token create mcp --mcp --write-file`, then add `{"command": "tkr", "args": ["mcp"]}`.

## Let the AI help, and stay in charge

```
$ tkr ai-changes
op 98625f8b #15 task.add by token:mcp at 2026-11-15 21:54:30-07:00
  #6 created
op 5d33c98f #14 entry.add by token:mcp at 2026-11-15 21:54:30-07:00
  e5 logged
$ tkr revert 5d33                                # a unique prefix is enough
reverted op 5d33c98f
e5 deleted
```

AI-created tasks always land in triage, writes are rate-limited, and every change is
reviewable and revertible.

## Built to be trusted

- **Your data stays local.** Everything is in one local SQLite file, with verified daily snapshots and `tkr export` to portable JSON.
- **No double writes.** Every write carries an idempotency key, so retries and double-clicks never double-apply.
- **Stale views get caught.** A page or screen changed elsewhere gets a "changed elsewhere — reload" warning instead of a silent overwrite.
