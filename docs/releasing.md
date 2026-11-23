# Releasing Taskronin

## One-time setup

We recommend protecting `v*` tags with a repository ruleset so only the owner
can create them.

## Prepare the release

Start from a clean `main`. To write your own release notes, commit them in
`docs/release-notes/<version>.md` before tagging. Omit the leading `v` from
the filename. Otherwise the release lists commit subjects since the previous
tag, or from the whole history for the first release. GoReleaser skips
subjects beginning with `Merge `.

Wait for CI to pass on the exact commit you intend to tag, including the arm64
and Nix jobs. The release workflow tests amd64 only. The dry run needs
GoReleaser v2.20.0 (the version pinned in the workflows), Docker and Nix.

```sh
make snapshot
make package-smoke
nix flake check
```

Run `make vuln` just before tagging because the release build checks the live
vulnerability database.

## Tag and publish

Use a new version. A suffix such as `-rc1` marks a GitHub prerelease.

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

The release workflow's read-only build job validates the tag, runs
`make check`, builds that tag with GoReleaser and runs `make package-smoke`.
It checks the six filenames against the tag and uploads the files and selected
notes as one workflow artifact. For example, tag `v0.1.0` produces these files.

```text
taskronin_0.1.0_linux_amd64.tar.gz
taskronin_0.1.0_linux_arm64.tar.gz
taskronin_0.1.0_amd64.deb
taskronin_0.1.0_arm64.deb
taskronin_0.1.0_source.tar.gz
checksums.txt
```

The publish job downloads the artifact, verifies the SHA-256 checksums and
attests the files listed in `checksums.txt`. It then creates the GitHub
release with all six files and appends verification instructions to the notes.
Only this job has write and signing permissions. It runs no project code.

Re-run a failed workflow from the Actions page. If the publish job failed
after it began creating the release, delete the leftover draft release first.
The build artifact is kept for seven days. After it expires, choose
“Re-run all jobs” to rebuild the same bytes.

**Never reuse a version.** Never delete or move the tag. The Go module proxy
may already have recorded it. If the source needs a fix, commit it and release
the next patch version.

## Update dependencies

After changing Go dependencies, set `vendorHash = lib.fakeHash;` in
`nix/package.nix` and build to get the new hash.

```sh
nix build
```

Replace `lib.fakeHash` with the quoted hash Nix reports as `got` in the
mismatch error. Rebuild to verify it, then regenerate `THIRD_PARTY_LICENSES.txt`
with the Go toolchain named in `go.mod`.

```sh
nix build
make third-party
```

To update the nixpkgs revision in `flake.lock`, run the following and commit
the lockfile once the checks pass.

```sh
nix flake update
nix flake check
```
