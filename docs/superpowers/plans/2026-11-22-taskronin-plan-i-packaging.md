# Plan I: Packaging and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A pushed `vX.Y.Z` tag publishes Debian packages, linux amd64/arm64 binary tarballs, a source tarball, checksums and provenance; the repository is a Nix flake NixOS can install; CI checks every push.

**Architecture:** GoReleaser builds every release artifact from one config, fed by a script that generates completions and license notices. A flake wraps `buildGoModule` with a thin NixOS module and a VM test. `tkr init` learns to write a Nix profile path instead of a `/nix/store` path into its systemd units. A CI workflow, a two-job release workflow and a Dependabot file follow `linsomniac/ipsak`, with the release split so the job holding write and signing rights runs no project code.

**Tech Stack:** GoReleaser v2.20.0 (nfpm for `.deb`), Nix flakes (`buildGoModule`, `testers.runNixOSTest`), GitHub Actions, POSIX sh, Docker for the package smoke test.

**Spec:** `docs/superpowers/specs/2026-11-22-packaging-design.md` (cited as P§N). Read it before any task; it holds the exact file names, package contents, pins and wording.

**Reference project:** `/home/sean/aix/ipsak` (read only). Its `.goreleaser.yaml`, `scripts/release-assets.sh`, `flake.nix`, `nix/*.nix` and `.github/` are the model. Copy its shape; do not copy what Taskronin does not need (macOS, Windows, rpm, `setcap`, the security wrapper, the single-job release).

**Already committed by the controller before Task 1:** `LICENSE` (CC0-1.0), `packaging/go-LICENSE`, and the module rename to `github.com/linsomniac/taskronin`.

## Global Constraints

- Linux only. The Go toolchain is the one in `go.mod`; do not edit `go.mod` or `go.sum` and add no Go dependency.
- `make check` must pass at the end of every task.
- A§1.3 import rules hold (`internal/archtest`). `internal/offline` may import `internal/systemd`.
- Grep for `AIDEV-` anchors in every directory you touch before editing. Never remove an `AIDEV-NOTE`; update one when you change the code it describes. Add an `AIDEV-NOTE` where a file is security relevant or easy to break.
- Never edit a migration file. Never commit a secret.
- Never push, never create or delete a git tag in this repository, and never run `goreleaser release` without `--snapshot` here.
- Never write under `/tmp` and never touch `~/.config/taskronin`, `~/.local/share/taskronin` or `~/.local/state/taskronin`. Local experiments use `$HOME/.cache/tkr-<name>/` and are deleted afterwards. Package tests run in a container; module tests run in a VM.
- Never leave an untracked, unignored file in the work tree while building: Go stamps `vcs.modified` into the binary and the bytes change.
- Shell scripts are POSIX `sh` with `set -eu`, mode 0755, and clean under `shellcheck` (`nix run nixpkgs#shellcheck -- FILE` when it is not installed). No command whose failure matters may sit on the left of a pipe.
- File names, package paths, modes, action SHAs and tool versions are exactly those in P§4 and P§8.
- A Nix flake only sees files git knows about: `git add` new files before `nix build`.
- When you shorten a command's output, keep its exit status: write to a log file and `tail` the file, never `cmd | tail`.
- If a tool you need (docker, nix, KVM, network) is blocked in your sandbox, do the rest, say exactly which verification you could not run, and report `DONE_WITH_CONCERNS`. Do not claim a check you did not run.
- Commit trailer: `Co-Authored-By: OpenAI Codex <noreply@openai.com>`.

## Review Focus

1. **A profile entry that links to a different `tkr` than the one running.** `tkr init` must not write it into the units; only an entry that resolves to the running executable counts, and only entries from the fixed P§6 list are considered. (Task 1 tests.)
2. **A vacuous smoke or VM test.** `tkr version` always prints a `server` line, `server unreachable` when the server is down. Tests must require the server's version, and fail if the server never started, if `tkr add` failed, or if the installed binary reports `dev`. (Task 3 and Task 4 tests.)
3. **Globs and versions that disagree.** A snapshot or prerelease version contains `-`; the Debian version inside the package uses `~`. The smoke script must find exactly one `.deb` and one tarball for the container's architecture, and take the version from `tkr version`, never from a file name. (Task 3 test.)
4. **Non-reproducible or silently incomplete assets.** Two builds of one clean commit give identical checksums; the license notice generator fails loudly when `go list` fails or a module has no license file. (Task 3 tests.)
5. **A workflow that leaks write access.** Only `release.yml`'s `publish` job has write or OIDC permissions and it runs no project code; no expression reaches a shell; checkout never persists its token. (Task 5 checks with `actionlint` and `zizmor`.)

---

### Task 1: `tkr init` writes a stable unit binary path (P§6)

**Files:**
- Create: `internal/systemd/binary.go`, `internal/systemd/binary_test.go`
- Modify: `internal/systemd/units.go` (export the path rule), `internal/offline/init.go`, `internal/offline/init_test.go`
- Modify: `docs/superpowers/specs/2026-10-25-taskronin-architecture.md` (the `{{.Binary}}` bullet in §8.5, near line 2611)

**Interfaces:**
- Produces, in `internal/systemd`:
  ```go
  // NixStoreDir is the directory whose contents can vanish after an upgrade.
  const NixStoreDir = "/nix/store"

  // ErrStoreBinary reports an executable in the Nix store that no Nix profile links to.
  var ErrStoreBinary = errors.New("binary is in the Nix store")

  // CheckUnitPath reports whether path may be written into a unit file
  // (the rule RenderUnits already applies); field names the path in the error.
  func CheckUnitPath(field, path string) error

  // NixProfileBins returns the P§6 profile bin directories in order.
  // Empty home, stateHome or user drop the entries that need them.
  func NixProfileBins(home, stateHome, user string) []string

  // StableBinary returns the path systemd units should execute for exe (P§6).
  func StableBinary(exe string, profileBins []string) (string, error)
  ```
  `StableBinary` delegates to an unexported `stableBinary(exe string, profileBins []string, storeDir string)` so tests can use a temporary store directory. `RenderUnits` keeps its behaviour and error text and calls `CheckUnitPath`.
- Produces, in `internal/offline`: `InitOptions.ProfileBins []string` ("Nix profile bin directories searched for a link to the binary; nil means the standard ones for this user").
- Task 4's VM test relies on: a store-path `tkr` that `/run/current-system/sw/bin/tkr` links to gets `ExecStart=/run/current-system/sw/bin/tkr serve`; a store-path `tkr` in no profile makes `tkr init` exit with the validation exit code, print the P§6 message, and create no config, database or unit file.

**Semantics of `NixProfileBins(home, stateHome, user)`:** exactly the five directories of P§6 in that order; `stateHome` empty means `home/.local/state`; entries needing an empty value are omitted.

**Semantics of `stableBinary`:**
- `exe` not inside `storeDir` → return `exe` unchanged, never touching the filesystem. "Inside" is by path component after `filepath.Clean`: `/nix/store-old/x` is not inside `/nix/store`.
- Otherwise resolve `exe` with `filepath.EvalSymlinks`; for each directory in `profileBins`, in order: the candidate is `filepath.Join(dir, filepath.Base(exe))`; skip it if it fails `CheckUnitPath`, if it is itself inside `storeDir`, or if its `EvalSymlinks` result differs from the resolved `exe` (including when it does not exist). Return the first survivor as written (cleaned, **not** resolved).
- No survivor, or `exe` itself cannot be resolved → `ErrStoreBinary`.

**Semantics in `offline.Init`:**
- When units will be written (`o.UnitsOnly || !o.NoSystemd`), resolve the unit binary once, immediately after the context check and before the `UnitsOnly` branch, `e.begin`, any prompt and any write. The effective executable is `o.Binary`, else `os.Executable()`; it must be absolute (existing rule and message). The profile directories are `o.ProfileBins`, else `NixProfileBins` for the current user (home directory, `XDG_STATE_HOME`, login name; a failed user lookup just drops the per-user entry).
- On `ErrStoreBinary` return a `domain.CodeValidation` error whose message is exactly the P§6 text with the executable path substituted; with `o.UnitsOnly` use the variant without the `--no-systemd` clause.
- Apply `CheckUnitPath("Binary", path)` to the resolved path at the same point and return its error.
- `installInitUnits` receives the resolved path; it no longer calls `os.Executable()` itself.

- [ ] **Step 1: Write the failing unit tests** in `internal/systemd/binary_test.go`, table-driven over a `t.TempDir()` tree with a fake store directory, fake profile directories and symlinks:

  | Case | Expect |
  |---|---|
  | exe outside the store, a profile links to it | exe unchanged |
  | exe in store, second profile dir has a symlink to it | that symlink path |
  | exe in store, link reached through a symlinked directory (profile → store dir) | the profile path, unresolved |
  | two matching profile dirs | the first |
  | profile `tkr` resolves to a **different** store file | skipped; `ErrStoreBinary` if nothing else matches |
  | profile dir that is itself inside the store | skipped |
  | profile path containing a space | skipped |
  | link with another base name | not matched |
  | empty or nil profile list | `ErrStoreBinary` |
  | exe in store but missing on disk | `ErrStoreBinary` |
  | exe in `<store>-old/…` | exe unchanged |
  | after a match, the profile link is repointed | the earlier return value is still the profile path (it was never resolved) |

  Assert errors with `errors.Is(err, ErrStoreBinary)`. Add `TestNixProfileBins` for the order, the `stateHome` default and the omissions.

- [ ] **Step 2: Run them and see them fail**

  Run: `go test ./internal/systemd -run 'TestStableBinary|TestNixProfileBins' -count=1`
  Expected: FAIL, `undefined: stableBinary`.

- [ ] **Step 3: Implement `internal/systemd/binary.go`** and export `CheckUnitPath` from `units.go`. Add an `AIDEV-NOTE` citing P§6 and stating two things: the returned path is deliberately unresolved, and `PATH` is deliberately not consulted.

- [ ] **Step 4: Write the failing `Init` tests** in `internal/offline/init_test.go`. Each passes `ProfileBins:` an empty temp directory, a `ReadPassword` that fails the test if called and a `Systemctl` that fails the test if called:
  - `TestInitRefusesStoreBinaryOutsideProfiles`: `Binary: "/nix/store/0000-taskronin/bin/tkr"`. Expect a validation error equal to the P§6 message for that path; afterwards the database, the server config, the owner token and both unit files do not exist.
  - `TestInitUnitsOnlyRefusesStoreBinary`: same binary, `UnitsOnly: true`, on an initialized instance. The message has no `--no-systemd`; the existing unit files are byte-identical afterwards.
  - `TestInitUnitsOnlyNoSystemdStillRefusesStoreBinary`: `UnitsOnly: true, NoSystemd: true` is refused the same way.
  - `TestInitNoSystemdIgnoresStoreBinary`: `NoSystemd: true` alone succeeds and writes no unit.
  - `TestInitRefusesUnsafeBinaryEarly`: `Binary: "/opt/my tools/tkr"` fails with the existing unsafe-path error before the database exists.

- [ ] **Step 5: Run them and see them fail**, then implement the `Init` changes.

  Run: `go test ./internal/offline -run 'TestInit.*(StoreBinary|UnsafeBinary)' -count=1`
  Expected before: FAIL. After: PASS.

- [ ] **Step 6: Amend the architecture bullet** to: "`{{.Binary}}` = `os.Executable()` resolved at init, or, when that is inside `/nix/store`, the first Nix profile entry that links to it (packaging design P§6); …" keeping the rest of the bullet.

- [ ] **Step 7: Verify and commit**

  Run: `make check`
  Expected: every package `ok`, 0 lint issues.

  ```bash
  git add internal/systemd/binary.go internal/systemd/binary_test.go internal/systemd/units.go internal/offline/init.go internal/offline/init_test.go docs/superpowers/specs/2026-10-25-taskronin-architecture.md
  git commit -m "feat(init): write a Nix profile path, not a store path, into the units"
  ```

---

### Task 2: Tests that do not depend on this workstation (P§8.4)

**Files:**
- Modify: test files only, as the failures require. Known: `internal/systemd/systemd_test.go` (`TestUnitsVerify`).
- Do not change product code. If a failure can only be fixed in product code, stop and report `NEEDS_CONTEXT`.

**Interfaces:**
- Consumes: the controller's runner simulation, `<workspace>/runner-sim.sh`, and its failure list in the controller notes for this task.
- Produces for Task 5: `go vet ./...` and `go test -race -count=1 -timeout=30m ./...` pass for UID 1001 in `/home/runner/work/taskronin/taskronin` with `XDG_RUNTIME_DIR` unset and `systemd-analyze` installed.

**Known defect:** `TestUnitsVerify` runs `systemd-analyze --user verify`, which fails with "Failed to initialize manager" when `XDG_RUNTIME_DIR` is unset. Give the command its own environment: the process environment plus `XDG_RUNTIME_DIR` set to a private directory the test creates.

**Rules for every fix:** a test must create what it needs (directories, runtime dir, sockets) under its own temp root; it must not read `/home/sean`, `/run/user/1000`, the caller's `XDG_*` variables or the caller's systemd session; it must not be skipped to make it pass; socket paths must stay within 107 bytes when the checkout is at `/home/runner/work/taskronin/taskronin`.

- [ ] **Step 1: Reproduce each listed failure** with the command the controller notes give (or, if docker is blocked, by running the named test with `env -u XDG_RUNTIME_DIR`).

- [ ] **Step 2: Fix the tests**, one commit-sized change per cause, each with an `AIDEV-NOTE` naming the runner assumption it removes.

- [ ] **Step 3: Verify**

  Run: `env -u XDG_RUNTIME_DIR go test -race -count=1 ./internal/systemd ./internal/config` and then `make check`
  Expected: `ok`; all packages `ok`.

- [ ] **Step 4: Commit**

  ```bash
  git add -u internal
  git commit -m "test: remove assumptions about the developer's workstation"
  ```

---

### Task 3: License notices, release assets, GoReleaser config, Debian package, smoke test (P§4, P§5)

**Files:**
- Create: `scripts/release-assets.sh`, `scripts/package-smoke.sh`, `.goreleaser.yaml`, `packaging/licenses/htmx/LICENSE`, `THIRD_PARTY_LICENSES.txt`
- Create if needed: `packaging/licenses/<module path>/LICENSE` for any linked module that ships no license file (today every module ships one)
- Modify: `Makefile`, `.gitignore`

**Interfaces:**
- Consumes: `tkr completion bash|zsh|fish`; `internal/buildinfo.Version` and `.Commit`; `packaging/go-LICENSE`; `LICENSE`.
- Produces for Tasks 4–6:
  - `THIRD_PARTY_LICENSES.txt` committed at the repository root
  - `make release-assets` → runs `scripts/release-assets.sh`
  - `make third-party` → runs the script and copies `build/THIRD_PARTY_LICENSES.txt` over the committed file
  - `make third-party-check` → runs the script and fails with a unified diff unless the two files are equal; added to the `check` target's prerequisites
  - `make snapshot` → `goreleaser release --snapshot --clean`
  - `make package-smoke` → runs `scripts/package-smoke.sh` in a `debian:stable` container with `dist/` mounted read-only at `/dist` and the script mounted read-only; exit status is the script's
  - `dist/` after `make snapshot` holds the six files of P§4 (with the snapshot version in place of `0.1.0`)

**`scripts/release-assets.sh`:** model on ipsak's. Outputs are exactly those in P§5. Differences from ipsak:
- no PowerShell completion; the module path and every text mention say `taskronin`;
- modules come from `go list -deps` over `./cmd/tkr` with `CGO_ENABLED=0 GOOS=linux`, once for `GOARCH=amd64` and once for `GOARCH=arm64`, each as its own checked command writing to a file under `build/`, then merged with `LC_ALL=C sort -u`;
- after the modules, add the htmx notice headed `htmx 2.0.11 (embedded web asset)` from `packaging/licenses/htmx/LICENSE`. Fetch that text from the htmx repository at tag `v2.0.11` (it is the Zero-Clause BSD license) and record the source URL in a comment in the script;
- the opening lines of the notices file say Taskronin is dedicated to the public domain under CC0-1.0.

**`.goreleaser.yaml`:** `version: 2`, `project_name: taskronin`, `before.hooks: [./scripts/release-assets.sh]`.
- `builds`: one build, `main: ./cmd/tkr`, `binary: tkr`, `CGO_ENABLED=0`, `-trimpath`, `goos: [linux]`, `goarch: [amd64, arm64]`, `mod_timestamp: "{{ .CommitTimestamp }}"`, ldflags `-s -w -X {{ .ModulePath }}/internal/buildinfo.Version={{ .Version }} -X {{ .ModulePath }}/internal/buildinfo.Commit={{ .ShortCommit }}`.
- `archives`: `tar.gz`, `name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`, contents per P§4 (`docs/quickstart.md` with `strip_parent: true`; the committed root `THIRD_PARTY_LICENSES.txt`; `build/completions/*` as `completions/`), owner root and mtime `{{ .CommitDate }}` on the binary (`builds_info`) and on every file (`info`).
- `source`: enabled, `name_template: "{{ .ProjectName }}_{{ .Version }}_source"`, `prefix_template: "{{ .ProjectName }}-{{ .Version }}/"`.
- `checksum.name_template: checksums.txt`.
- `nfpms`: one package per P§4 and P§3.3, `formats: [deb]`, `file_name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Arch }}"` (no `~` in the file name, P§4), `bindir: /usr/bin`, `mtime: "{{ .CommitDate }}"`, no `scripts`, no `dependencies`.
- `changelog`: ascending, exclude `^Merge `. No `release:` block and no `GITHUB_TOKEN` use: the workflow publishes (P§8.2).
- Start the file with an `AIDEV-NOTE` saying: the release workflow's `build` job runs it with `--skip=publish`; CI's `packages` job builds a snapshot from it; and any untracked, unignored file in the work tree changes the binary's bytes.

**`scripts/package-smoke.sh`** (runs inside a Debian container; argument 1 is the dist directory, default `/dist`). Root part:
1. Architecture from `dpkg --print-architecture`. Find `taskronin_*_<arch>.deb`, `taskronin_*_linux_<arch>.tar.gz` and `taskronin_*_source.tar.gz`; fail unless each glob matches exactly one file.
2. `sha256sum -c checksums.txt` in the dist directory.
3. `apt-get install -y --no-install-recommends` the `.deb`.
4. Assert every path of the P§4 Debian table exists with the stated mode and owner `root:root`, and that `dpkg-query` reports package `taskronin` with an empty `Depends`.
5. `version` is the second field of the first line of `tkr version`; that line must match `^tkr [0-9]` and must not contain `dev`.
6. The binary tarball lists exactly the eight P§4 entries (sorted comparison), and its extracted `tkr version` first line equals the installed one.
7. The source tarball has the single top directory `taskronin-<version>/` and contains `go.mod`, `LICENSE` and `cmd/tkr/main.go`.
8. Create user `smoke` with a home directory and re-execute the script as that user with a clean environment: `setpriv --reuid=smoke --regid=smoke --init-groups env -i HOME=/home/smoke PATH=/usr/bin:/bin sh "$0" --as-user`.

User part (`--as-user`):
1. `XDG_RUNTIME_DIR="$HOME/run"`, created mode 0700 (Taskronin refuses a world-writable ancestor, so not under `/tmp`).
2. `tkr init --no-systemd --password-stdin --timezone America/Denver -y` with the password on stdin.
3. `tkr serve &` directly, so `$!` is the server. A trap kills it on every exit path.
4. Poll `tkr version` for up to 20 seconds until its `server` line equals `server <client version>`; `server unreachable` after 20 seconds is a failure.
5. `tkr add "package smoke"`, then `tkr ls` must print `package smoke`.
6. Send SIGTERM, `wait` for the server and require exit status 0.
Any failure exits non-zero with a one-line reason on stderr; success ends with `package smoke: ok`.

- [ ] **Step 1: Write `scripts/release-assets.sh`; run it twice and compare**

  Run: `scripts/release-assets.sh && cp -r build "$HOME/.cache/tkr-assets-first" && scripts/release-assets.sh && diff -r build "$HOME/.cache/tkr-assets-first"; rm -rf "$HOME/.cache/tkr-assets-first"`
  Expected: no diff output. `build/` holds `completions/tkr.bash`, `completions/_tkr`, `completions/tkr.fish`, `THIRD_PARTY_LICENSES.txt`, `copyright` (and the two module list files). `grep -c '^htmx 2.0.11' build/THIRD_PARTY_LICENSES.txt` prints `1`.

  Prove Review Focus 4: run the script with `GOFLAGS=-mod=bogus` in the environment → it exits non-zero and writes no notices file that looks complete. Record the output in the report.

- [ ] **Step 2: Commit the notices file and wire the Makefile**: `make third-party`, add `third-party-check` to `check`, add `release-assets`, `snapshot`, `package-smoke`; add `/build/`, `/dist/`, `/result` to `.gitignore`.

  Run: `make third-party-check`
  Expected: exit 0. Then append a line to `THIRD_PARTY_LICENSES.txt`, see `make third-party-check` fail with a diff, and restore the file.

- [ ] **Step 3: Write `.goreleaser.yaml`**

  Run: `goreleaser check`
  Expected: `1 configuration file(s) validated`, no deprecation notices.

- [ ] **Step 4: Commit, then build a snapshot from the clean tree and inspect it**

  Commit first (see Step 7's file list, minus the smoke script if it is not written yet), because an untracked file changes the build.

  Run: `make snapshot && ls dist/*.tar.gz dist/*.deb dist/checksums.txt`
  Expected: two binary tarballs, one source tarball, two `.deb` files, `checksums.txt`; the `.deb` names contain no `~`. `tar -tzf` on the amd64 tarball lists exactly the eight entries of P§4. `go version -m` on `dist/*/tkr` shows `vcs.modified=false`.

- [ ] **Step 5: Write `scripts/package-smoke.sh` and prove it can fail**

  Run: `make package-smoke`
  Expected: exit 0, final line `package smoke: ok`.

  Then prove Review Focus 2 and 3 using a copy of `dist/` under `$HOME/.cache/tkr-smoke-neg/` (run the script against the copy with `docker run` directly; delete the copy afterwards): (a) a second `.deb` matching the glob → fails naming the ambiguous glob; (b) a `dist` built from a temporary config without the `-X …buildinfo.Version` flag → fails on `dev`; (c) with the server start line temporarily replaced by `false &` → fails at the 20-second wait, does not hang. Record the three outputs in the report.

- [ ] **Step 6: Reproducibility, from a clean committed tree**

  Run: `make snapshot && cp dist/checksums.txt "$HOME/.cache/tkr-checksums-first" && make snapshot && diff dist/checksums.txt "$HOME/.cache/tkr-checksums-first"; rm -f "$HOME/.cache/tkr-checksums-first"`
  Expected: no diff.

- [ ] **Step 7: Verify and commit**

  Run: `shellcheck scripts/release-assets.sh scripts/package-smoke.sh && make check`
  Expected: no findings; all packages `ok`.

  ```bash
  git add scripts/release-assets.sh scripts/package-smoke.sh .goreleaser.yaml packaging THIRD_PARTY_LICENSES.txt Makefile .gitignore
  git commit -m "build: GoReleaser config, Debian package, license notices and smoke test"
  ```

---

### Task 4: Nix flake, package, NixOS module and VM test (P§7)

**Files:**
- Create: `flake.nix`, `flake.lock`, `nix/package.nix`, `nix/module.nix`, `nix/test.nix`

**Interfaces:**
- Consumes: Task 1's behaviour (see its Interfaces block); `tkr completion`; the committed `LICENSE` and `THIRD_PARTY_LICENSES.txt`.
- Produces for Task 5: `nix flake check --print-build-logs` builds the package and runs the VM test; `nix build .#taskronin` works on both systems. For Task 6: flake outputs `packages.<system>.default`, `overlays.default`, `nixosModules.default`, option `programs.taskronin.enable`.

**`nix/package.nix`** arguments: `lib, buildGoModule, installShellFiles, stdenv, version ? "dev", commit ? ""`. Everything else per P§7. The `-X` paths are `github.com/linsomniac/taskronin/internal/buildinfo.Version` and `.Commit`. Carry ipsak's `AIDEV-NOTE` on `vendorHash` (how to refresh it), and add one on `doCheck = false` giving the P§3.5 reason.

**`flake.nix`**: `version` as in ipsak (`unstable-<date>-<rev>`); `commit = self.shortRev or self.dirtyShortRev or ""`. The package and the overlay both pass `version` and `commit`. No `apps`, no `devShells`.

**`nix/module.nix`**: options and config exactly as P§7.

**`nix/test.nix`** (`pkgs.testers.runNixOSTest`): one machine importing the module with `programs.taskronin.enable = true`, `time.timeZone = "America/Denver"`, users `alice` (uid 1000) and `bob` (uid 1001), both `isNormalUser`. A second build of the package that is in no profile (`loose`, for example the package with a different `version`) is put in the VM's store with `virtualisation.additionalPaths`. Every user command runs in a login shell with `XDG_RUNTIME_DIR=/run/user/<uid>`. Script:
1. `loginctl enable-linger alice`; wait for `user@1000.service`.
2. As alice: `tkr init --password-stdin -y` with a password on stdin and **without** `--timezone`. The server config under `~alice/.config/taskronin` contains `America/Denver`.
3. `~alice/.config/systemd/user/taskronin.service` contains the line `ExecStart=/run/current-system/sw/bin/tkr serve` and does not contain `/nix/store`.
4. As alice: `systemctl --user is-active taskronin.socket` is `active`, and `taskronin.service` is **not** active yet.
5. As alice: `tkr add "vm smoke"`; `tkr ls` prints `vm smoke`; `taskronin.service` is now `active`; the `server` line of `tkr version` equals `server ` plus the client's version.
6. As alice, running the package's own store path directly: `<pkg>/bin/tkr init --units-only` succeeds and the unit still says `/run/current-system/sw/bin/tkr` (the same build is in a profile).
7. `loginctl enable-linger bob`; as bob: `<loose>/bin/tkr init --password-stdin -y` exits with the validation exit code (read it from the CLI's exit code table; assert the exact number); its output contains the P§6 message for that path; `~bob/.config/systemd/user/taskronin.service`, `~bob/.config/taskronin` and `~bob/.local/share/taskronin` do not exist.

- [ ] **Step 1: Write `nix/package.nix` and `flake.nix`**, `git add` them, set `vendorHash = lib.fakeHash`.

  Run: `nix build .#taskronin > "$HOME/.cache/tkr-nix.log" 2>&1; echo "exit=$?"; tail -20 "$HOME/.cache/tkr-nix.log"`
  Expected: non-zero exit with a hash mismatch naming the real hash. Put it in `vendorHash`.

- [ ] **Step 2: Build and inspect**

  Run: `nix build .#taskronin && ./result/bin/tkr version && ls result/share/bash-completion/completions result/share/zsh/site-functions result/share/fish/vendor_completions.d result/share/doc/taskronin`
  Expected: first line `tkr unstable-<date>-<rev> (<rev>)`; one completion file in each directory; `LICENSE` and `THIRD_PARTY_LICENSES.txt` in the doc directory. `flake.lock` now exists; `git add flake.lock`.

- [ ] **Step 3: Write `nix/module.nix` and `nix/test.nix`**, `git add` them, wire `nixosModules.default`, `checks` and `formatter` into the flake.

  Run: `nix flake check --print-build-logs > "$HOME/.cache/tkr-nix.log" 2>&1; echo "exit=$?"; tail -30 "$HOME/.cache/tkr-nix.log"`
  Expected: `exit=0`. Then prove the test can fail: temporarily change the expected `ExecStart` line in `nix/test.nix` to a wrong path, see `exit=1` on that assertion, and restore it. Record both outputs in the report.

- [ ] **Step 4: Format, verify and commit**

  Run: `nix fmt -- flake.nix nix/*.nix && nix flake check && make check; rm -f "$HOME/.cache/tkr-nix.log"`
  Expected: the formatter leaves no diff on a second run; checks pass; all packages `ok`.

  ```bash
  git add flake.nix flake.lock nix
  git commit -m "build(nix): flake with package, NixOS module and VM test"
  ```

---

### Task 5: GitHub Actions and Dependabot (P§8)

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/dependabot.yml`

**Interfaces:**
- Consumes: `make check`, `make snapshot`, `make package-smoke` (Task 3); `nix flake check`, `nix build .#taskronin` (Task 4).

Write the three files exactly to P§8: the job table of P§8.1, the two jobs of P§8.2, the Dependabot rules of P§8.3, the pins and tool versions of the P§8 table, and the hardening rules in P§8's first paragraph. Model the YAML style on ipsak's `.github/workflows/go.yml` and `.github/dependabot.yml`.

Details P§8 leaves to this task:
- `ci.yml` `check` job: `golangci/golangci-lint-action` with `version: v2.15.0` and `install-only: true`, then `run: make check`.
- `ci.yml` `packages` job: `strategy.fail-fast: false`, matrix over the two runners, checkout with `fetch-depth: 0`, setup-go with `cache: false`, GoReleaser action twice (`args: check`, then `args: release --snapshot --clean`, `version: v2.20.0`), then `run: make package-smoke`.
- `ci.yml` `nix` job: the KVM udev step and `cachix/install-nix-action` configuration from ipsak's `go.yml`. `nix-arm64`: install Nix, `nix build .#taskronin --print-build-logs`, `./result/bin/tkr version`.
- `release.yml`: `on.push.tags: ["v*"]` only; `concurrency` with `cancel-in-progress: false`.
  - `build`: checkout with `fetch-depth: 0`; a step that takes `TAG: ${{ github.ref_name }}` through `env:` and exits 1 unless it matches `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`; setup-go with `go-version-file: go.mod` and `cache: false`; golangci-lint as in CI; `make check`; GoReleaser `args: release --clean --skip=publish`; `make package-smoke`; one `actions/upload-artifact` step named `release` with `if-no-files-found: error`, `retention-days: 7`, and exactly these paths: `dist/*.tar.gz`, `dist/*.deb`, `dist/checksums.txt`, `dist/CHANGELOG.md`.
  - `publish`: `needs: build`; job permissions `contents: write`, `id-token: write`, `attestations: write`; no checkout; `actions/download-artifact` into `dist`; `sha256sum -c checksums.txt` in `dist`; `actions/attest` with `subject-checksums: dist/checksums.txt`; a final step with `env:` `GH_TOKEN: ${{ github.token }}`, `GH_REPO: ${{ github.repository }}`, `TAG: ${{ github.ref_name }}` that appends the footer ``Verify a download with `gh attestation verify <file> --repo linsomniac/taskronin`, or compare it with `checksums.txt`.`` to the changelog and runs `gh release create "$TAG" --verify-tag --title "$TAG" --notes-file …` with the six P§4 files, adding `--prerelease` when `$TAG` contains `-`.
- Each workflow starts with an `AIDEV-NOTE` comment saying what triggers it and that actions are pinned to SHAs that Dependabot updates together with the version comment. `release.yml` adds one on `publish`: it holds the write and signing rights, so it must never check out or execute repository code.

- [ ] **Step 1: Write the three files.**

- [ ] **Step 2: Lint them**

  Run: `nix run nixpkgs#actionlint -- .github/workflows/*.yml && nix run nixpkgs#zizmor -- --offline .github/`
  Expected: `actionlint` prints nothing; `zizmor` reports no findings. `zizmor`'s template-injection audit is the check for expressions inside `run:` blocks, including multi-line ones. Fix findings in the workflow; do not suppress one without an inline reason. If `nix run` is unavailable, say so in the report.

- [ ] **Step 3: Check the pins and permissions mechanically**

  Run: `grep -nE 'uses: ' .github/workflows/*.yml | grep -vE '@[0-9a-f]{40} # v[0-9]' ; grep -c 'persist-credentials: false' .github/workflows/*.yml ; grep -nE '(write)$' .github/workflows/*.yml`
  Expected: the first grep prints nothing; the count equals the number of checkout steps in each file; the only `write` lines are the three in `release.yml`'s `publish` job. Every `uses:` SHA and version equals the P§8 table.

- [ ] **Step 4: Verify and commit**

  Run: `make check`

  ```bash
  git add .github
  git commit -m "ci: check, package and Nix jobs; two-job tag release; Dependabot for actions"
  ```

---

### Task 6: Documentation (P§9)

**Files:**
- Modify: `README.md` (Install section, new Upgrading subsection, Development section), `docs/quickstart.md` (the install line only)
- Create: `docs/releasing.md`

**Interfaces:**
- Consumes: artifact names (P§4), the P§6 rule, flake outputs (Task 4), Make targets (Task 3), workflow behaviour (Task 5).

**README Install section** replaces the current one and keeps its facts about `tkr init` (password length, what it creates, the two units, the `init` flags, `systemctl --user status taskronin.socket`). Order:
1. One sentence: releases at `https://github.com/linsomniac/taskronin/releases` carry a `.deb` and a static binary tarball for amd64 and arm64, plus the source.
2. **Debian and Ubuntu:** `sudo apt install ./taskronin_*_amd64.deb`, then `tkr init`.
3. **NixOS:** the flake input (`taskronin.url = "github:linsomniac/taskronin";`), `imports = [ inputs.taskronin.nixosModules.default ];`, `programs.taskronin.enable = true;`, then `tkr init`. Do not show `inputs.nixpkgs.follows`; say in one sentence that the flake pins its own nixpkgs and that following yours needs Go 1.26 or newer. Explain P§6 in user terms: the units point at the profile's `tkr`, so they keep working across upgrades; `tkr init` refuses to write units from a `tkr` that is in no profile (`nix run`, `nix shell`). Mention `packages.<system>.default` and `overlays.default`, and that Nix builds report a version made from the commit date and revision.
4. **Other Linux systems:** unpack the tarball, put `tkr` on `PATH`, run `tkr init`; completions are in `completions/`.
5. **From source:** `go install github.com/linsomniac/taskronin/cmd/tkr@latest` (such a build reports version `dev`), or `make install` (keep the existing `PREFIX` paragraph). Keep the sentence that units point at the binary that ran `tkr init`, and that `tkr init --units-only` rewrites them after the binary moves.
6. **Verify a download:** `gh attestation verify taskronin_*_amd64.deb --repo linsomniac/taskronin`, or `sha256sum -c --ignore-missing checksums.txt`.

**Upgrading** (a short subsection after Install): installing a new version replaces the binary; the running server keeps the old one until `systemctl --user restart taskronin.service`; when the new version changes the database schema, the server takes a verified snapshot and then migrates on start; an older binary refuses a database migrated by a newer one; `tkr version` shows both versions.

**Development section:** add the license line ("Taskronin is dedicated to the public domain under CC0-1.0; see `LICENSE`. `THIRD_PARTY_LICENSES.txt` lists the licenses of the code compiled into the binary.") and links to `docs/releasing.md` and the packaging design.

**`docs/releasing.md`**, short and imperative: prerequisites (clean `main`, CI green on the commit to be tagged); dry run (`make snapshot`, `make package-smoke`, `nix flake check`); cut the release (`git tag vX.Y.Z`, `git push origin vX.Y.Z`); what the release workflow does and publishes (P§4, P§8.2), and that a `-rc1` style suffix marks a prerelease; **never reuse a version** (the Go module proxy may have recorded the tag: a failed run is re-run from the Actions page, a source fix gets the next patch version); after a dependency change (`vendorHash` refresh procedure from `nix/package.nix`, then `make third-party`); updating `flake.lock` (`nix flake update`, then `nix flake check`); a recommendation to protect `v*` tags with a repository ruleset so only Sean can create them.

Wording rules: plain sentences, no marketing; commands in code blocks; say only what the earlier tasks actually built (check the real file names in `dist/` and the real flake outputs before writing them).

- [ ] **Step 1: Write the README changes, the quickstart line and `docs/releasing.md`.**

- [ ] **Step 2: Check every command and path against the tree**

  Run: `grep -n 'taskronin_\|nixosModules\|programs.taskronin\|make snapshot\|make package-smoke\|make third-party\|releasing.md\|packaging-design' README.md docs/releasing.md docs/quickstart.md`
  Expected: every artifact name matches P§4, every Make target exists in the `Makefile`, every linked file exists.

- [ ] **Step 3: Verify and commit**

  Run: `make check`

  ```bash
  git add README.md docs/quickstart.md docs/releasing.md
  git commit -m "docs: install from packages and Nix, upgrading, releasing"
  ```
