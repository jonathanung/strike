{
  description = "strike-cli isolated dev environment (Go 1.26.2+ per go.mod)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          name = "strike-cli";

          packages = [
            pkgs.go
            pkgs.gnumake
            pkgs.git
            pkgs.gopls
          ];

          shellHook = ''
            export GOROOT="${pkgs.go}/share/go"
            export GOPATH="$PWD/.nix-go/gopath"
            export GOCACHE="$PWD/.nix-go/gocache"
            export GOENV="$PWD/.nix-go/env"
            export GOTOOLCHAIN=local
            export PATH="$GOPATH/bin:$PATH"
            mkdir -p "$GOPATH" "$GOCACHE"
            echo "strike-cli dev shell: $(go version)"
          '';
        };
      });
}
