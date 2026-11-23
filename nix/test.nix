{ pkgs, module }:
pkgs.testers.runNixOSTest (
  { nodes, ... }:
  let
    pkg = nodes.machine.programs.taskronin.package;
    loose = pkg.override { version = "loose"; };
  in
  {
    name = "taskronin";
    nodes.machine = {
      imports = [ module ];
      programs.taskronin.enable = true;
      time.timeZone = "America/Denver";
      users.users = {
        alice = {
          isNormalUser = true;
          uid = 1000;
        };
        bob = {
          isNormalUser = true;
          uid = 1001;
        };
      };
      # AIDEV-NOTE: Bob also sees the system profile. Keep his distinct build
      # only in the store so init must reject it before writing any files.
      virtualisation.additionalPaths = [ loose ];
    };

    testScript = ''
      import shlex
      import tomllib

      assert "${loose}" != "${pkg}", "Bob's build must be outside the system profile"

      def user_command(user: str, command: str) -> str:
          login_command = f"export XDG_RUNTIME_DIR=/run/user/$(id -u); {command}"
          return f"su - {user} -c {shlex.quote(login_command)}"

      def alice(command: str) -> str:
          return machine.succeed(user_command("alice", command)).strip()

      def check_unit() -> None:
          unit = alice("cat ~/.config/systemd/user/taskronin.service")
          assert "ExecStart=/run/current-system/sw/bin/tkr serve" in unit.splitlines(), unit
          assert "/nix/store" not in unit, unit

      machine.wait_for_unit("multi-user.target")
      machine.succeed("loginctl enable-linger alice")
      machine.wait_for_unit("user@1000.service")
      alice("tkr init --password-stdin -y <<< 'vm smoke password'")
      config = tomllib.loads(alice("cat ~/.config/taskronin/server.toml"))
      assert config["timezone"] == "America/Denver", config["timezone"]
      check_unit()
      assert alice("systemctl --user is-active taskronin.socket") == "active"
      assert alice("systemctl --user show -p ActiveState --value taskronin.service") == "inactive"

      alice('tkr add "vm smoke"')
      tasks = alice("tkr ls")
      assert "vm smoke" in tasks, tasks
      assert alice("systemctl --user is-active taskronin.service") == "active"
      client, server = alice("tkr version").splitlines()
      program, version, _ = client.split()
      assert program == "tkr" and version.startswith("unstable-"), client
      assert server == f"server {version}", server

      alice("${pkg}/bin/tkr init --units-only")
      check_unit()

      machine.succeed("loginctl enable-linger bob")
      machine.wait_for_unit("user@1001.service")
      status, output = machine.execute(user_command(
          "bob", "${loose}/bin/tkr init --password-stdin -y <<< 'vm smoke password' 2>&1"
      ))
      assert status == 1, (status, output)
      message = (
          "tkr is running from the Nix store (${loose}/bin/tkr) and is not installed in a Nix profile, "
          "so systemd units would break when that store path is collected. Install tkr into a profile "
          "(on NixOS: programs.taskronin.enable = true), or pass --no-systemd."
      )
      assert message in output, output
      for path in ["~/.config/systemd/user/taskronin.service", "~/.config/taskronin", "~/.local/share/taskronin"]:
          machine.succeed(user_command("bob", f"test ! -e {path}"))
    '';
  }
)
