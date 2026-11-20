{
  description = "incus-compose development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      perSystem = { pkgs, ... }: {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gotestsum
            just
            golangci-lint
            jq
            podman
            nix
            curl
            git
            shellcheck
            prettier
          ];

          env = {
            CGO_ENABLED = "1";
            GOTOOLCHAIN = "go1.27.4";
          };
        };
      };
    };
}
