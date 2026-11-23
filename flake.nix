{
  description = "Taskronin task manager";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = "unstable-${builtins.substring 0 8 (self.lastModifiedDate or "19700101")}-${
        self.shortRev or self.dirtyShortRev or "unknown"
      }";
      commit = self.shortRev or self.dirtyShortRev or "";
    in
    {
      packages = forAllSystems (pkgs: rec {
        taskronin = pkgs.callPackage ./nix/package.nix { inherit version commit; };
        default = taskronin;
      });

      overlays.default = final: prev: {
        taskronin = final.callPackage ./nix/package.nix { inherit version commit; };
      };

      nixosModules.default = import ./nix/module.nix { inherit self; };

      checks = forAllSystems (pkgs: {
        taskronin = self.packages.${pkgs.stdenv.hostPlatform.system}.taskronin;
        nixos-module = import ./nix/test.nix {
          inherit pkgs;
          module = self.nixosModules.default;
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
