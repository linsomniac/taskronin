{ self }:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.taskronin;
in
{
  options.programs.taskronin = {
    enable = lib.mkEnableOption "Taskronin";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "taskronin.packages.\${pkgs.stdenv.hostPlatform.system}.default";
      description = "The Taskronin package to install.";
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ];
  };
}
