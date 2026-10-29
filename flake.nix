{
  description = "Multi-agent orchestration system for Claude Code with persistent work tracking";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    beads.url = "github:gastownhall/beads";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      beads,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
        beadsPkg = beads.packages.${system}.default;
      in
      {
        packages = {
          gt = pkgs.buildGoModule {
            pname = "gt";
            version = "1.0.0";
            src = ./.;
            vendorHash = "sha256-1ZzA/crpwA2DleEyo+iIr2rQ+U+rRxNDneIBBWe++bo=";

            ldflags = [
              "-X github.com/gastownhall/gastown/internal/cmd.Build=nix"
              "-X github.com/steveyegge/gastown/internal/cmd.BuiltProperly=1"
            ];

            subPackages = [ "cmd/gtb" ];

            # The code shells out to `gt` by name; provide it alongside `gtb`.
            postInstall = ''
              ln -s gtb $out/bin/gt
            '';

            meta = with pkgs.lib; {
              description = "Multi-agent orchestration system for Claude Code with persistent work tracking";
              homepage = "https://github.com/gastownhall/gastown";
              license = licenses.mit;
              mainProgram = "gtb";
            };
          };
          default = self.packages.${system}.gt;
        };

        apps = {
          gt = flake-utils.lib.mkApp {
            drv = self.packages.${system}.gt;
          };
          default = self.apps.${system}.gt;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = [
            beadsPkg
            pkgs.go_1_26
            pkgs.gopls
            pkgs.gotools
            pkgs.go-tools
          ];
        };
      }
    );
}
