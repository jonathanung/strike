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
        version = "0-unstable-${builtins.substring 0 8 (self.lastModifiedDate or "19700101")}";
        commit = self.shortRev or self.dirtyShortRev or "unknown";
        strike = pkgs.buildGoModule {
          pname = "strike";
          inherit version;

          src = self;
          proxyVendor = true;
          vendorHash = "sha256-ax5mSaryrwb+vSoqm6+Brl6RnA/2WZm+z+eEdxubhtQ=";

          subPackages = [ "cmd/strike" ];
          preBuild = ''
            go generate ./internal/frontend/tui/app
          '';
          ldflags = [
            "-s"
            "-w"
            "-X github.com/jonathanung/strike-cli/internal/version.Version=${version}"
            "-X github.com/jonathanung/strike-cli/internal/version.Commit=${commit}"
          ];

          meta = {
            description = "Agentic coding TUI";
            homepage = "https://strike.jonathanung.ca/";
            license = pkgs.lib.licenses.asl20;
            mainProgram = "strike";
          };
        };
      in
      {
        packages = {
          inherit strike;
          default = strike;
        };

        apps = {
          strike = flake-utils.lib.mkApp { drv = strike; };
          default = flake-utils.lib.mkApp { drv = strike; };
        };

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
