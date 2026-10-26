# Taskronin

Taskronin is a single-user task manager built around triage (new, slipped and
due work must be consciously cleared), daily focus (time blocks, a "now working
on" focus, a morning plan and evening recap loop) and a work log linked to
tasks. It ships as one Go binary, `tkr`, over a single SQLite database.

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
