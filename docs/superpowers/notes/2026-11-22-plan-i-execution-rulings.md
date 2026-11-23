# Plan I (packaging and release) — execution rulings

Plan: `docs/superpowers/plans/2026-11-22-taskronin-plan-i-packaging.md`.
Design: `docs/superpowers/specs/2026-11-22-packaging-design.md` (P).
Executed 2026-11-22 with Codex implementers and Claude reviewers (Opus for
the `tkr init` change, the release scripts, Nix, the workflows, the
documentation and the whole-branch review; Sonnet for the test-only task and
two scoped re-reviews). Reference project: `linsomniac/ipsak`.

## Sean's decisions

- License CC0-1.0; `tkr init` writes the Nix profile path into its units;
  module renamed to `github.com/linsomniac/taskronin` (all 2026-11-22).
- "Let's cut a 0.1.0 release and publish the packages": Claude pushes `main`,
  waits for green CI, then pushes `v0.1.0` directly, with no release
  candidate. This permission covers the 0.1.0 release only.

## Rulings

- R-I1: architectures are amd64 and arm64. 32-bit ARM is out because CI
  cannot test it natively. Open for Sean (P§3.1).
- R-I2: P§6 uses a fixed list of five Nix profile directories and never
  consults `PATH`. Both design reviewers showed that a `PATH` search could
  pick a directory another account controls, a `result/bin` link or a
  numbered generation. A `tkr` kept anywhere else is refused; an override
  flag is open for Sean.
- R-I3: the rule applies whenever units are written, including
  `--units-only --no-systemd`, and the binary path is checked against the
  unit path rule before the database exists.
- R-I4: the release workflow has two jobs, unlike ipsak. `build` runs
  project code with read-only rights; `publish` holds the write and signing
  rights, checks out nothing, attests, then creates the release. Nothing is
  public before every check and the attestation have passed.
- R-I5: the build pins GoReleaser to the pushed tag
  (`GORELEASER_CURRENT_TAG`) and checks the six artifact names against it.
  With two tags on one commit GoReleaser otherwise built the other one.
- R-I6: `THIRD_PARTY_LICENSES.txt` is committed and `make check` fails when
  it is stale, so tarballs, the Debian package and the Nix package ship one
  reviewed file. ipsak's Nix package ships no notices.
- R-I7: archive entries carry explicit modes. Without them the tarball bytes
  followed the builder's umask.
- R-I8: the Debian file name carries the tag's version text unchanged;
  GitHub rewrites `~` in asset names, which would break `checksums.txt`.
- R-I9: the smoke test runs in `debian:stable`, not `-slim`, which discards
  `/usr/share/doc`.
- R-I10: the Nix package skips the Go suite and stays unwrapped (the init
  rule matches the executable's base name).
- R-I11: `ubuntu-latest` is kept, as in ipsak. It is Ubuntu 26.04 today; the
  suite passes in 24.04 and 26.04 containers laid out like a runner.
- R-I12: release notes come from `docs/release-notes/<version>.md` when it
  exists (as in apt-cacher-ultra). `0.1.0.md` was written by Claude.
- R-I13: stricter attestation flags are not shown in the README; it points
  at `gh attestation verify --help`.
- R-I14: the ARM test job runs without the race detector, and the amd64 race
  runs use `GOFLAGS=-p=2`. The first two CI runs on GitHub failed only in the
  ARM race job: once on a two-second test wait (fixed in the tests), then with
  "writer is busy" in two password tests, because Argon2 under `-race` on
  that runner exceeds the service's fixed 15-second operation budget. Race
  detection does not depend on the architecture and stays on amd64.

## Review outcomes

Design: two rounds (Opus and Codex, then a scoped Opus re-review). Round one
said "fix first" with three Critical findings (the `PATH` search, the
Debian slim image, uncommitted prerequisites); round two found the sibling
tag defect (R-I5). All were fixed in the design before any task started.

Tasks: no Critical findings. Important findings, all fixed: a relative
`HOME` or `XDG_STATE_HOME` could put a relative `ExecStart` into the unit
(Task 1); tarball bytes depended on the umask (Task 3). One test,
`TestUnitsVerify`, depended on the developer's login session and was fixed
(Task 2). Tasks 4, 5 and 6 were approved on first review.

Whole-branch review: "ship", six Minor findings. One fix wave applied them
together with the Minor findings carried from the tasks.

## Release 0.1.0 (2026-11-23)

Claude pushed `main` and, once CI was green on `95b725c`, the tag `v0.1.0`.
CI needed three runs: the first two failed only in the arm64 race job
(R-I14). The release workflow passed on its first tag run (build 18.5
minutes, publish 8 seconds).

Checked from outside afterwards: all six assets present; `sha256sum -c
checksums.txt` passes; `gh attestation verify` passes for all five artifacts
(signer `release.yml` at `refs/tags/v0.1.0`); the published `.deb` and
tarball pass `scripts/package-smoke.sh` in `debian:stable`; a rebuild on the
workstation reproduces both tarballs and both `.deb` files byte for byte
(the source tarball has identical contents and different gzip bytes);
`go install …/cmd/tkr@v0.1.0` works and reports `dev`;
`nix build github:linsomniac/taskronin/v0.1.0#taskronin` builds. The arm64
`.deb` was smoke-tested on GitHub's ARM runner in CI, not from the published
release.

Dependabot opened its first pull request (a patch bump of the Nix installer
action) minutes after the workflows landed; it is Sean's to merge.

## Left for Sean

- Whether "linux arm" meant 32-bit ARM as well (R-I1).
- An explicit `tkr init` override for a `tkr` kept outside the standard Nix
  profiles (R-I2).
- Protect `v*` tags with a repository ruleset (`docs/releasing.md`).
- A `go install` build reports version `dev`; reading the module version
  from the build information would fix that.
- The release run tests amd64 only. An arm64 smoke job in `release.yml`
  would remove the reliance on CI being green for the tagged commit.
- Password tests under the race detector run close to the 15-second
  operation budget on shared runners (R-I14). A cheaper Argon2 setting for
  tests, or a budget the tests can raise for password operations, would
  remove the remaining risk of a false failure on amd64.
- The source tarball and the repository include `description`,
  `initial_prompt` and `docs/superpowers`; they hold no secrets.
