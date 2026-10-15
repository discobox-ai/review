{
  description = "review development environment";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { nixpkgs, ... }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      forSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      # Everything `go tool task <target>` needs that is not a Go program.
      # Go tools (task, golangci-lint, repocheck, generators) are pinned by
      # go.mod `tool` directives and deliberately absent here: two pins drift.
      devShells = forSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        rec {
          default = pkgs.mkShell {
            packages = [
              pkgs.go
              pkgs.git
              pkgs.shellcheck
              pkgs.actionlint
            ];
            # go.mod's go directive picks the toolchain; nixpkgs' go only
            # bootstraps it.
            GOTOOLCHAIN = "auto";
          };

          # What `go tool task screenshots` needs beyond the default shell:
          # freeze, ImageMagick, and a fontconfig that can find JetBrains Mono.
          # freeze comes from here rather than a go.mod tool directive: its
          # charmbracelet/x dependencies are older than the ones this module
          # builds with, and one module has one version of each.
          screenshots = pkgs.mkShell {
            inputsFrom = [ default ];
            packages = [
              pkgs.charm-freeze
              pkgs.imagemagick
              pkgs.fontconfig
            ];
            FONTCONFIG_FILE = pkgs.makeFontsConf { fontDirectories = [ pkgs.jetbrains-mono ]; };
          };
        }
      );

      formatter = forSystems (system: nixpkgs.legacyPackages.${system}.nixfmt);
    };
}
