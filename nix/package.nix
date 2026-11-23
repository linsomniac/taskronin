{
  lib,
  buildGoModule,
  installShellFiles,
  stdenv,
  version ? "dev",
  commit ? "",
}:

buildGoModule {
  pname = "taskronin";
  inherit version;

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
      ../LICENSE
      ../THIRD_PARTY_LICENSES.txt
    ];
  };

  # AIDEV-NOTE: vendorHash pins the modules in go.sum. After any dependency
  # change, set it to lib.fakeHash, run `nix build`, and copy the hash from
  # the mismatch error. CI's nix job fails until it matches.
  vendorHash = "sha256-8Ardy2QW1QnfDqm65jL7qxApEe5gaqyZ6fK8QTIXufs=";

  # AIDEV-NOTE: Keep bin/tkr unwrapped. Init matches the running executable's
  # basename and resolved path against profile entries when writing user units.
  subPackages = [ "cmd/tkr" ];
  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X github.com/linsomniac/taskronin/internal/buildinfo.Version=${version}"
    "-X github.com/linsomniac/taskronin/internal/buildinfo.Commit=${commit}"
  ];

  # AIDEV-NOTE: CI runs the Go suite on both architectures and the VM test
  # exercises this binary. Avoid rerunning the suite on every user update.
  doCheck = false;

  nativeBuildInputs = [ installShellFiles ];
  postInstall = ''
    install -Dm644 -t "$out/share/doc/taskronin" LICENSE THIRD_PARTY_LICENSES.txt
  ''
  + lib.optionalString (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
    for shell in bash zsh fish; do
      "$out/bin/tkr" completion "$shell" > "tkr.$shell"
      installShellCompletion --cmd tkr "--$shell" "tkr.$shell"
    done
  '';

  meta = {
    description = "Taskronin task manager";
    homepage = "https://github.com/linsomniac/taskronin";
    license = lib.licenses.cc0;
    mainProgram = "tkr";
    platforms = lib.platforms.linux;
  };
}
