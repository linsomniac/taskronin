# Taskronin packaging and release design (P)

Date: 2026-11-22. Sections are cited as P§N.

## 1. Intent

Sean's request: "lets start packaging this, I'd like github actions, similar
to my other projects, which produce debian packages, the source and some binary
tar file packages for linux arm and amd64, and packaging for nixos."

Outcome: pushing a `vX.Y.Z` tag publishes a GitHub release with Debian
packages, binary tarballs for linux amd64 and arm64, a source tarball,
checksums and signed build provenance. The repository is a Nix flake that
NixOS can install from. Every push and pull request runs the full check suite.

The reference for "similar to my other projects" is `linsomniac/ipsak`, Sean's
newest Go project: GoReleaser driven by a tag, a CI job that builds a snapshot
and installs the package in a container, a flake with a NixOS module and a VM
test, SHA-pinned actions, Dependabot for actions.

## 2. Sean's decisions (2026-11-22)

1. **License: CC0-1.0**, as in ipsak, apt-cacher-ultra and statshed-cli.
2. **NixOS units: `tkr init` uses the profile path** (P§6). The hardened units
   stay defined once, in Go. The NixOS module only installs the package.
3. **Module path: `github.com/linsomniac/taskronin`**, renamed before the
   first release.

Items 2 and 3 supersede design §19 items 2 ("no Nix packaging") and 1 ("keep
`taskronin`"), which were made while the repository was private and local.

## 3. Controller calls

These are Claude's calls, not Sean's. Each is cheap to reverse.

1. Architectures are linux/amd64 and linux/arm64. No 32-bit ARM, although
   ipsak ships armv7: GitHub has no 32-bit ARM runner, so the test suite
   could only run under slow emulation, and a database application should
   not ship where it is untested. **Open for Sean:** if "linux arm" meant
   armv7 as well, it is one `goarch` entry plus an emulated test job.
   No macOS or Windows: Taskronin is Linux only (design §18.14).
2. No rpm. Sean did not ask for one; nfpm makes it a small addition later.
3. Maintainer and contact fields use the identity of this repository's
   commits: `Sean Reifschneider <sean@realgo.com>`.
4. The Debian package ships no systemd units and no maintainer scripts.
   `/usr/bin/tkr` is a stable path, so `tkr init` writes working per-user
   units as it does today. A root-run package script must not restart a
   user's server.
5. The Nix package does not run the Go test suite (`doCheck = false`). The
   suite takes about two and a half minutes and every NixOS user would pay
   for it on every update. CI runs it on both architectures, and the flake's
   VM test exercises the built binary end to end.
6. No man pages. `tkr --help` and shell completions are the reference.
7. The release version comes from the tag. Nix builds report
   `unstable-<commit date>-<short revision>`, because flakes cannot see tags.
   A `go install` build reports `dev`; the README says so.
8. Dependabot updates GitHub Actions only. A `go.sum` change also needs a new
   `vendorHash` in `nix/package.nix` and a regenerated
   `THIRD_PARTY_LICENSES.txt`, which Dependabot cannot produce.
9. Tags are `v` plus a semantic version (`v0.1.0`). Sean creates and pushes
   them; nothing in this work pushes. A version is never reused: the Go
   module proxy may have recorded the first tag.
10. The release workflow departs from ipsak's single job (P§8.2): the job
    that holds write and signing rights runs no project code. This costs
    about forty lines of YAML and closes the gap where test or build code
    runs with a release token.

## 4. Release artifacts

For a tag `v0.1.0`:

| Artifact | File name |
|---|---|
| Binary tarball, amd64 | `taskronin_0.1.0_linux_amd64.tar.gz` |
| Binary tarball, arm64 | `taskronin_0.1.0_linux_arm64.tar.gz` |
| Debian package, amd64 | `taskronin_0.1.0_amd64.deb` |
| Debian package, arm64 | `taskronin_0.1.0_arm64.deb` |
| Source tarball | `taskronin_0.1.0_source.tar.gz` (expands to `taskronin-0.1.0/`) |
| Checksums | `checksums.txt` (SHA-256 of every file above) |

File names use the tag's version text unchanged, including for a prerelease
(`taskronin_0.1.0-rc1_amd64.deb`). Inside the package the Debian version is
`0.1.0~rc1`, so it sorts before the final release. The file name avoids `~`
because GitHub rewrites it in asset names, which would break `checksums.txt`.

The binary is static (`CGO_ENABLED=0`, `-trimpath`, `-s -w`) and carries
`buildinfo.Version` (the tag without `v`) and `buildinfo.Commit` (the short
commit). File times inside archives and packages are the commit time, and
owners are root, so rebuilding a clean checkout of a tag gives the same bytes.
Go records whether the work tree was modified, so any untracked, unignored
file changes the binary.

**Binary tarball contents** (flat, no top directory, as in ipsak):
`tkr`, `LICENSE`, `README.md`, `quickstart.md`, `THIRD_PARTY_LICENSES.txt`,
`completions/tkr.bash`, `completions/_tkr`, `completions/tkr.fish`.

**Debian package** `taskronin`, section `utils`, priority `optional`, no
dependencies, no maintainer scripts:

| Path | Mode |
|---|---|
| `/usr/bin/tkr` | 0755 |
| `/usr/share/bash-completion/completions/tkr` | 0644 |
| `/usr/share/zsh/vendor-completions/_tkr` | 0644 |
| `/usr/share/fish/vendor_completions.d/tkr.fish` | 0644 |
| `/usr/share/doc/taskronin/copyright` | 0644 |
| `/usr/share/doc/taskronin/THIRD_PARTY_LICENSES.txt` | 0644 |
| `/usr/share/doc/taskronin/README.md` | 0644 |
| `/usr/share/doc/taskronin/quickstart.md` | 0644 |

Description: `Task manager built around triage, daily focus and a work log`
followed by a short paragraph taken from the README's first paragraph.

## 5. License notices and generated assets

The static binary contains the Go standard library and every dependency
module, and their BSD, MIT and Apache licenses require shipping their notices
with binaries.

`THIRD_PARTY_LICENSES.txt` is **committed** at the repository root, so the
tarballs, the Debian package and the Nix package all ship the same reviewed
file. It holds the license texts of the Go standard library (committed copy
at `packaging/go-LICENSE`, because Nix's Go omits `GOROOT/LICENSE`), of every
module linked into `./cmd/tkr` for linux/amd64 or linux/arm64 with
`CGO_ENABLED=0`, and of the embedded htmx file (committed copy at
`packaging/licenses/htmx/LICENSE`).

`scripts/release-assets.sh` writes into `build/` (ignored by git):

- `build/THIRD_PARTY_LICENSES.txt`, regenerated from the module cache. A
  module with no license file fails the script; the fix is a committed copy
  of the upstream text at `packaging/licenses/<module path>/LICENSE`.
- `build/completions/tkr.bash`, `_tkr`, `tkr.fish`, from `tkr completion`.
- `build/copyright`: Debian machine-readable copyright, CC0-1.0, built from
  `LICENSE`, with a comment pointing at `THIRD_PARTY_LICENSES.txt`.

`make check` gains `third-party-check`: it runs the script and fails unless
`build/THIRD_PARTY_LICENSES.txt` equals the committed file. `make third-party`
copies the regenerated file over the committed one after a dependency change.

The script must be deterministic (`LC_ALL=C` for the whole script, so glob
and sort order do not depend on the shell or locale; no timestamps) and must
not hide a failed command inside a pipeline: every `go list` runs as its own
checked command writing to a file.

## 6. `tkr init` and the unit binary path

Today the units' `ExecStart` is `os.Executable()`. From a Nix-built `tkr`
that is a `/nix/store/<hash>-taskronin-…/bin/tkr` path, which stops existing
after an upgrade and garbage collection, so the server would silently stop
starting.

New rule, applied whenever init will write units (`tkr init` without
`--no-systemd`, and `tkr init --units-only` with or without it):

1. If the executable is not inside `/nix/store/`, nothing changes.
2. Otherwise init looks for the executable's base name in the Nix profile
   `bin` directories, in the order NixOS puts them on `PATH`:

   1. `$HOME/.nix-profile/bin`
   2. `$XDG_STATE_HOME/nix/profile/bin` (default `$HOME/.local/state/…`)
   3. `/etc/profiles/per-user/<login name>/bin`
   4. `/nix/var/nix/profiles/default/bin`
   5. `/run/current-system/sw/bin`

   The first entry that resolves, following symlinks, to the running
   executable becomes `ExecStart`, written as the profile path, not the
   resolved one. These paths follow upgrades. The list is fixed on purpose:
   `PATH` is not consulted, so a directory another account can write to, a
   `result/bin` link or a numbered profile generation can never be chosen.
3. If no entry matches (`nix run`, `nix shell`, or a profile that holds a
   different build), init refuses before it prompts or writes anything, with
   the exit code of a validation error:

   ```
   tkr is running from the Nix store (/nix/store/…/bin/tkr) and is not installed in a Nix profile, so systemd units would break when that store path is collected. Install tkr into a profile (on NixOS: programs.taskronin.enable = true), or pass --no-systemd.
   ```

   With `--units-only` the last clause is omitted (the sentence ends after
   "…enable = true).").

The chosen path, store or not, is checked against the unit templates' path
rule (no whitespace, control characters, `%`, quotes or backslash) at the
same early point, so an unusable path is refused before the database exists.
A profile entry that fails the rule is skipped.

`--no-systemd` without `--units-only` skips all of this. The store test is a
lexical prefix test on `/nix/store/`; it does not consult `NIX_STORE_DIR`.

`nix run` succeeds when the same build is also installed in a profile.
When one build is in two profiles the earlier entry wins; after moving `tkr`
between profiles, `tkr init --units-only` rewrites the units. A `tkr` kept
somewhere the list does not cover (another profile manager, a hand-made
link) is refused; the documented way out is a standard profile or a copy
outside the store. An explicit override flag is **open for Sean**.

On a single-user Nix install `/nix/var/nix/profiles/default` belongs to the
account that installed Nix, as does the whole store, so that entry adds no
new trust.
After an upgrade the running server keeps the old binary (Nix does not
collect a running program) until the service restarts; then it runs the new
one. This matches the Debian package's behaviour.

## 7. Nix

`flake.nix`, modelled on ipsak's:

- Systems: `x86_64-linux`, `aarch64-linux`.
- `packages.<system>.taskronin` and `.default`; `overlays.default`;
  `nixosModules.default`; `checks.<system>.taskronin` (the package) and
  `checks.<system>.nixos-module` (the VM test); `formatter` = `nixfmt`.
- Input: `nixpkgs` at `nixos-unstable`, pinned by a committed `flake.lock`.

`nix/package.nix`: `buildGoModule`, `pname = "taskronin"`, source limited
with `lib.fileset` to `go.mod`, `go.sum`, `cmd`, `internal`, `LICENSE` and
`THIRD_PARTY_LICENSES.txt`, a pinned `vendorHash`, `CGO_ENABLED = 0`, the
same `-s -w -X …buildinfo.Version/Commit` flags, `subPackages = [ "cmd/tkr" ]`,
`doCheck = false` (P§3.5), shell completions for bash, zsh and fish via
`installShellFiles`, `LICENSE` and `THIRD_PARTY_LICENSES.txt` installed in
`$out/share/doc/taskronin`, `meta.license = lib.licenses.cc0`,
`meta.mainProgram = "tkr"`, `meta.platforms = lib.platforms.linux`.

`nix/module.nix`: `programs.taskronin.enable` and `programs.taskronin.package`.
Enabling adds the package to `environment.systemPackages`. Nothing else: no
units, no wrappers.

`nix/test.nix`, a NixOS VM test with the module enabled, `time.timeZone =
"America/Denver"` and two ordinary users, proving P§6 and that the hardened
units work on NixOS. Every user command runs with that user's
`XDG_RUNTIME_DIR`.

1. As `alice`, in a lingering user session, `tkr init` (no `--timezone`)
   succeeds. The server config names `America/Denver`. The installed
   `taskronin.service` has `ExecStart=/run/current-system/sw/bin/tkr serve`
   and contains no `/nix/store` path. `taskronin.socket` is active and
   `taskronin.service` is not yet active.
2. `tkr add` then `tkr ls` round-trips a task; afterwards the service is
   active (socket activation started the hardened unit), and `tkr version`
   prints a `server` line carrying the same version as the client line, not
   `server unreachable`.
3. As `bob`, running the store path directly, `tkr init` exits with the
   validation exit code, prints the P§6 message, and leaves no unit file, no
   config and no data directory. (`bob` has the system profile too, so this
   uses a second build of the package that is not in any profile.)

The Go toolchain in a Nix build is nixpkgs' `go`, not the `toolchain` line in
`go.mod`; a Nix-built binary gets standard-library fixes when `flake.lock`
is updated. It needs a nixpkgs with Go 1.26 or newer.

## 8. GitHub Actions

All workflows: `permissions: contents: read` at the top, extra permissions
only on the job that needs them; every action pinned to a full commit SHA
with the version in a trailing comment; `persist-credentials: false` on
checkout; no `${{ … }}` expression inside a `run:` script (pass values
through `env:`); `concurrency` per ref; `actionlint` and `zizmor` clean.

Pinned versions (SHAs verified against their tags on 2026-11-22):

| Action | Version | SHA |
|---|---|---|
| `actions/checkout` | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `actions/setup-go` | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| `golangci/golangci-lint-action` | v9.3.0 | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` |
| `goreleaser/goreleaser-action` | v7.3.0 | `a4f02ab5355a589de60d917a659ce617ed19b3de` |
| `actions/upload-artifact` | v7.0.2 | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| `actions/download-artifact` | v8.0.2 | `9000827ccba6bdab643e8b6fd33ac0654aef8333` |
| `actions/attest` | v4.2.2 | `1e69f48acb82d1966a394da916b4c1698aa569d6` |
| `cachix/install-nix-action` | v31.11.2 | `448cde739abec1eaf0646249e3d25bb8d9bc7702` |

Tool versions: GoReleaser `v2.20.0`, golangci-lint `v2.15.0` (the versions
used locally). Go comes from `go-version-file: go.mod`, so CI and releases
build with the pinned toolchain.

### 8.1 `.github/workflows/ci.yml` (push and pull_request)

| Job | Runner | Does |
|---|---|---|
| `check` | `ubuntu-latest` | installs golangci-lint (action, `install-only: true`), runs `make check` |
| `test-arm64` | `ubuntu-24.04-arm` | `go vet ./...` and `go test -race -count=1 -timeout=30m ./...` |
| `packages` | matrix: `ubuntu-latest`, `ubuntu-24.04-arm` | `goreleaser check`; `goreleaser release --snapshot --clean`; `make package-smoke` |
| `nix` | `ubuntu-latest` | enables KVM, installs Nix, runs `nix flake check --print-build-logs` |
| `nix-arm64` | `ubuntu-24.04-arm` | installs Nix, `nix build .#taskronin`, runs `./result/bin/tkr version` (no VM test: the ARM runners have no KVM) |

`scripts/package-smoke.sh` runs in a `debian:stable` container (not `-slim`,
which discards `/usr/share/doc`) with `dist/` mounted read-only. As root it
verifies `checksums.txt`, installs the `.deb` for the container's
architecture and checks the P§4 paths and modes. Then, as an unprivileged
user with no systemd and a private runtime directory under its home, it runs
`tkr init --no-systemd`, starts `tkr serve`, waits until `tkr version`
reports the server's version, adds a task, lists it, and stops the server.
It fails if the installed version is `dev`, if the server never answers, or
if a glob matches other than exactly one file.

### 8.2 `.github/workflows/release.yml` (tags `v*`)

Two jobs. Nothing is public until the last step of the second.

**`build`** (`contents: read` only; it runs project code):
checkout with full history; refuse a tag that is not
`vMAJOR.MINOR.PATCH` with an optional `-prerelease` suffix; Go with no
module cache; golangci-lint; `make check`;
`goreleaser release --clean --skip=publish` with
`GORELEASER_CURRENT_TAG` set to the pushed tag; `make package-smoke`; fail
unless the six P§4 files exist under the names the pushed tag implies;
choose the release notes; upload the six files and the notes as one
workflow artifact.

GoReleaser picks its version from git, and when two tags sit on one commit
(`v0.2.0-rc1` and `v0.2.0`) it can pick the other one. The explicit tag and
the name check make the build match the tag that was pushed.

Release notes are `docs/release-notes/<version>.md` when that file exists
(as in apt-cacher-ultra), otherwise GoReleaser's list of commit subjects
since the previous tag.

**`publish`** (`needs: build`; `contents: write`, `id-token: write`,
`attestations: write`; it checks out nothing and runs no project code):
download the artifact; `sha256sum -c checksums.txt`; `actions/attest` with
`subject-checksums`; then `gh release create "$TAG" --verify-tag` with the
six files passed by their exact names, the notes plus a verification footer,
and `--prerelease` when the tag has a suffix.

A failed run is re-run from the Actions page; it publishes nothing partial.

### 8.3 `.github/dependabot.yml`

GitHub Actions only, monthly, grouped into one pull request, seven-day
cooldown (P§3.8).

### 8.4 Tests must not depend on this workstation

The suite has only ever run on Sean's machine. Before the workflows are
written it must pass in a container laid out like a GitHub runner: the
Ubuntu release behind `ubuntu-latest` (26.04 as of 2026-11; the `-arm` jobs
name 24.04) with systemd installed, an ordinary user with UID 1001, the
checkout at `/home/runner/work/taskronin/taskronin`, and no
`XDG_RUNTIME_DIR`. It passes on both 24.04 and 26.04. Tests that
assume `/home/sean`, UID 1000, a session runtime directory or a short
checkout path are fixed in the tests, not skipped.

## 9. Documentation

- `README.md`: the Install section covers the Debian package, the binary
  tarball, NixOS (flake input, module, `tkr init`), `go install` (which
  reports version `dev`), building from source, and verifying a download
  (`gh attestation verify FILE --repo linsomniac/taskronin`, or
  `sha256sum -c --ignore-missing checksums.txt`). A new "Upgrading"
  paragraph says the running server keeps the old binary until
  `systemctl --user restart taskronin.service`, and that when a new version
  changes the database schema the server takes a verified snapshot and then
  migrates on start. The Development section links this document.
- `docs/releasing.md`: how to cut a release (CI green on the commit, dry run
  with `make snapshot` and `make package-smoke`, tag, push the tag, what
  appears), optional hand-written notes in `docs/release-notes/<version>.md`,
  that a version is never reused, how to re-run a failed release,
  how to update `vendorHash` and `THIRD_PARTY_LICENSES.txt` after a
  dependency change, how to update `flake.lock`, and a recommendation to
  protect `v*` tags with a repository ruleset.
- `LICENSE` at the repository root.
- Architecture §8.5 gains the P§6 rule where it says `{{.Binary}}` is
  `os.Executable()`.

## 10. What cannot be verified before Sean pushes

The workflows run only on GitHub. Locally the work is verified with
`make check`, the runner-like container of P§8.4, `goreleaser check`, a
snapshot release, a real (unpublished) release build in a scratch clone with
a scratch tag, the amd64 package smoke test in Docker, `nix flake check`
including the VM test, and `actionlint` and `zizmor` over the workflow files.
The arm64 runner jobs, the artifact hand-off between the two release jobs,
the attestation step and the release upload first run on GitHub. The release
run tests amd64 only, so the commit to be tagged must already be green in CI,
including the arm64 and Nix jobs.

## 11. Non-goals

rpm, Homebrew, an apt repository, submission to nixpkgs or Debian, a
home-manager module, package signing beyond GitHub attestations, man pages,
restarting the server from a package script, a changelog file (release notes
come from commit subjects, as in ipsak), 32-bit ARM (P§3.1).
